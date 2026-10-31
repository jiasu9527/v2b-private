package aiops

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const miniTokenLifetime = 8 * time.Hour

type MiniIdentity struct {
	ID        int64  `json:"id"`
	Email     string `json:"email"`
	IsAdmin   int64  `json:"is_admin"`
	IsStaff   int64  `json:"is_staff"`
	ExpiresAt int64  `json:"expires_at"`
}

type MiniAuthResult struct {
	Token     string       `json:"token"`
	ExpiresAt int64        `json:"expires_at"`
	User      MiniIdentity `json:"user"`
}

type miniAuthorization struct {
	MiniIdentity
	bot, fingerprint string
	generation       int64
}

// The bot secret never appears in an HTTP DTO or the ordinary transport API.
func (b *TelegramBridge) miniBot() (string, string, bool) {
	if b == nil {
		return "", "", false
	}
	b.mu.Lock()
	t := b.transport
	b.mu.Unlock()
	provider, ok := t.(interface{ MiniBotToken() string })
	if !ok {
		return "", "", false
	}
	bot, ready := t.Identity()
	secret := provider.MiniBotToken()
	if !ready || bot == "" || secret == "" || !strings.HasPrefix(secret, bot+":") {
		return "", "", false
	}
	return bot, secret, true
}

// Telegram's WebApp HMAC uses HMAC("WebAppData", botToken), not SHA256(token).
// Return only the signed Telegram user id; browser-supplied actor ids are ignored.
func verifyMiniInitData(raw, botToken string, now time.Time) (int64, error) {
	if raw == "" || len(raw) > 16384 || botToken == "" {
		return 0, ErrForbidden
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return 0, ErrForbidden
	}
	var fields []string
	for key, value := range values {
		if key == "" || len(value) != 1 || strings.ContainsAny(key, "\r\n=") {
			return 0, ErrForbidden
		}
		if key != "hash" {
			fields = append(fields, key+"="+value[0])
		}
	}
	provided, err := hex.DecodeString(values.Get("hash"))
	if err != nil || len(provided) != sha256.Size {
		return 0, ErrForbidden
	}
	sort.Strings(fields)
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secret.Write([]byte(botToken))
	check := hmac.New(sha256.New, secret.Sum(nil))
	_, _ = check.Write([]byte(strings.Join(fields, "\n")))
	if !hmac.Equal(check.Sum(nil), provided) {
		return 0, ErrForbidden
	}
	stamp, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil || stamp <= 0 || stamp < now.Unix()-300 || stamp > now.Unix()+30 {
		return 0, ErrForbidden
	}
	var user struct {
		ID    int64 `json:"id"`
		IsBot bool  `json:"is_bot"`
	}
	if err = json.Unmarshal([]byte(values.Get("user")), &user); err != nil || user.ID <= 0 || user.IsBot {
		return 0, ErrForbidden
	}
	return user.ID, nil
}

func (b *TelegramBridge) AuthenticateMini(ctx context.Context, initData string) (MiniAuthResult, error) {
	var out MiniAuthResult
	bot, secret, ready := b.miniBot()
	if !ready {
		return out, ErrForbidden
	}
	tgID, err := verifyMiniInitData(initData, secret, time.Now())
	if err != nil {
		return out, err
	}
	if err := b.ready(ctx); err != nil {
		return out, err
	}
	tx, err := b.ai.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err = lockTelegramBridge(ctx, tx); err != nil {
		return out, err
	}
	currentBot, currentSecret, ready := b.miniBot()
	if !ready || bot != currentBot || !hmac.Equal([]byte(secret), []byte(currentSecret)) {
		return out, ErrForbidden
	}
	var generation int64
	err = tx.QueryRowContext(ctx, `SELECT u.id,u.email,u.is_admin,u.is_staff,b.generation FROM v2_ai_telegram_binding b JOIN v2_user u ON u.id=b.actor_id JOIN v2_ai_telegram_config c ON c.id=1 WHERE c.enabled AND b.active AND b.bot_id=$1 AND b.telegram_id=$2 AND u.banned=0 AND (u.is_admin=1 OR u.is_staff=1)`, bot, tgID).Scan(&out.User.ID, &out.User.Email, &out.User.IsAdmin, &out.User.IsStaff, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return MiniAuthResult{}, ErrForbidden
	}
	if err != nil {
		return MiniAuthResult{}, err
	}
	random := make([]byte, 32)
	if _, err = rand.Read(random); err != nil {
		return MiniAuthResult{}, err
	}
	out.Token = "tma_" + base64.RawURLEncoding.EncodeToString(random)
	out.ExpiresAt = time.Now().Add(miniTokenLifetime).Unix()
	out.User.ExpiresAt = out.ExpiresAt
	// Bound storage growth while allowing multiple devices. Only opaque hashes
	// are persisted, never initData or a usable bearer credential.
	if _, err = tx.ExecContext(ctx, `DELETE FROM v2_ai_telegram_mini_token WHERE expires_at<=$1 OR (actor_id=$2 AND token_hash NOT IN(SELECT token_hash FROM v2_ai_telegram_mini_token WHERE actor_id=$2 ORDER BY created_at DESC,token_hash DESC LIMIT 15))`, time.Now().Unix(), out.User.ID); err != nil {
		return MiniAuthResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO v2_ai_telegram_mini_token(token_hash,actor_id,bot_id,bot_fingerprint,generation,expires_at,created_at)VALUES($1,$2,$3,$4,$5,$6,$7)`, hashOperation(out.Token), out.User.ID, bot, hashOperation(secret), generation, out.ExpiresAt, time.Now().Unix()); err != nil {
		return MiniAuthResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return MiniAuthResult{}, err
	}
	return out, nil
}

func (b *TelegramBridge) authenticateMiniTokenTx(ctx context.Context, tx *sql.Tx, token string) (miniAuthorization, error) {
	var out miniAuthorization
	if len(token) != 47 || !strings.HasPrefix(token, "tma_") {
		return out, ErrForbidden
	}
	bot, secret, ready := b.miniBot()
	if !ready {
		return out, ErrForbidden
	}
	// Do not hold a user FOR SHARE lock across SendMessage/ApproveAction: those
	// may legitimately modify this operator using a second database connection.
	err := tx.QueryRowContext(ctx, `SELECT u.id,u.email,u.is_admin,u.is_staff,t.expires_at,t.generation FROM v2_ai_telegram_mini_token t JOIN v2_ai_telegram_binding b ON b.actor_id=t.actor_id JOIN v2_user u ON u.id=t.actor_id JOIN v2_ai_telegram_config c ON c.id=1 WHERE t.token_hash=$1 AND t.expires_at>$2 AND t.bot_id=$3 AND t.bot_fingerprint=$4 AND c.enabled AND b.active AND b.bot_id=t.bot_id AND b.generation=t.generation AND u.banned=0 AND (u.is_admin=1 OR u.is_staff=1)`, hashOperation(token), time.Now().Unix(), bot, hashOperation(secret)).Scan(&out.ID, &out.Email, &out.IsAdmin, &out.IsStaff, &out.ExpiresAt, &out.generation)
	if errors.Is(err, sql.ErrNoRows) {
		return miniAuthorization{}, ErrForbidden
	}
	out.bot, out.fingerprint = bot, hashOperation(secret)
	return out, err
}

func (b *TelegramBridge) AuthenticateMiniToken(ctx context.Context, token string) (MiniIdentity, error) {
	if err := b.ready(ctx); err != nil {
		return MiniIdentity{}, err
	}
	tx, err := b.ai.db.BeginTx(ctx, nil)
	if err != nil {
		return MiniIdentity{}, err
	}
	defer tx.Rollback()
	identity, err := b.authenticateMiniTokenTx(ctx, tx, token)
	return identity.MiniIdentity, err
}
