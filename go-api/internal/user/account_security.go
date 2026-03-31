package user

import (
	"context"
	"crypto/md5"
	crand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func (s *DBService) TelegramBotInfo(ctx context.Context) (map[string]any, error) {
	token := strings.TrimSpace(s.currentConfig().TelegramBotToken)
	if token == "" {
		return nil, errors.New("Request failed")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.telegram.org/bot"+token+"/getMe", nil)
	if err != nil {
		return nil, errors.New("Request failed")
	}

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, errors.New("Request failed")
	}
	defer resp.Body.Close()

	var payload struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, errors.New("Request failed")
	}
	if !payload.OK {
		message := strings.TrimSpace(payload.Description)
		if message == "" {
			return nil, errors.New("Request failed")
		}
		return nil, errors.New("来自TG的错误：" + message)
	}

	return map[string]any{"username": payload.Result.Username}, nil
}

func (s *DBService) UnbindTelegram(ctx context.Context, userID int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}

	exists, err := s.userExists(ctx, userID)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, ErrNotFound
	}

	result, err := s.db.ExecContext(ctx, `UPDATE v2_user
SET telegram_id = NULL, updated_at = $2
WHERE id = $1`, userID, time.Now().Unix())
	if err != nil {
		return false, errors.New("Unbind telegram failed")
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, errors.New("Unbind telegram failed")
	}
	return true, nil
}

func (s *DBService) ResetSecurity(ctx context.Context, userID int64) (string, error) {
	if s.db == nil {
		return "", ErrUnavailable
	}

	exists, err := s.userExists(ctx, userID)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", ErrNotFound
	}

	uuid, err := userRandomUUID()
	if err != nil {
		return "", errors.New("Reset failed")
	}
	token, err := userRandomMD5Token()
	if err != nil {
		return "", errors.New("Reset failed")
	}

	result, err := s.db.ExecContext(ctx, `UPDATE v2_user
SET uuid = $2, token = $3, updated_at = $4
WHERE id = $1`, userID, uuid, token, time.Now().Unix())
	if err != nil {
		return "", errors.New("Reset failed")
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return "", errors.New("Reset failed")
	}

	subscribeURL, err := s.buildSubscribeURL(ctx, userID, token)
	if err != nil {
		return "", err
	}
	return subscribeURL, nil
}

func (s *DBService) UpdateProfile(ctx context.Context, userID int64, req ProfileUpdateRequest) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}

	exists, err := s.userExists(ctx, userID)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, ErrNotFound
	}

	assignments := make([]string, 0, 4)
	args := make([]any, 0, 5)
	args = append(args, userID)

	if req.AutoRenewal != nil {
		assignments = append(assignments, fmt.Sprintf("auto_renewal = $%d", len(args)+1))
		args = append(args, *req.AutoRenewal)
	}
	if req.RemindExpire != nil {
		assignments = append(assignments, fmt.Sprintf("remind_expire = $%d", len(args)+1))
		args = append(args, *req.RemindExpire)
	}
	if req.RemindTraffic != nil {
		assignments = append(assignments, fmt.Sprintf("remind_traffic = $%d", len(args)+1))
		args = append(args, *req.RemindTraffic)
	}

	if len(assignments) == 0 {
		return true, nil
	}

	assignments = append(assignments, fmt.Sprintf("updated_at = $%d", len(args)+1))
	args = append(args, time.Now().Unix())
	args = append(args, userID)

	result, err := s.db.ExecContext(ctx, `UPDATE v2_user SET `+strings.Join(assignments, ", ")+` WHERE id = $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return false, errors.New("Save failed")
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, errors.New("Save failed")
	}
	return true, nil
}

func (s *DBService) ChangePassword(ctx context.Context, userID int64, oldPassword, newPassword string) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}

	var (
		hash string
		algo sql.NullString
		salt sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `SELECT password, password_algo, password_salt
FROM v2_user
WHERE id = $1
LIMIT 1`, userID).Scan(&hash, &algo, &salt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, fmt.Errorf("query user password: %w", err)
	}

	if !verifyUserPassword(algo, salt, oldPassword, hash) {
		return false, errors.New("The old password is wrong")
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return false, errors.New("Save failed")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("Save failed")
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE v2_user
SET password = $1, password_algo = NULL, password_salt = NULL, updated_at = $2
WHERE id = $3`, string(hashedPassword), now, userID); err != nil {
		return false, errors.New("Save failed")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM v2_auth_session WHERE user_id = $1`, userID); err != nil {
		return false, errors.New("Save failed")
	}
	if err := tx.Commit(); err != nil {
		return false, errors.New("Save failed")
	}
	if s.authCache != nil {
		s.authCache.InvalidateUser(userID)
	}
	return true, nil
}

func (s *DBService) userExists(ctx context.Context, userID int64) (bool, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_user WHERE id = $1)`, userID).Scan(&exists); err != nil {
		return false, fmt.Errorf("query user exists: %w", err)
	}
	return exists, nil
}

func verifyUserPassword(algo sql.NullString, salt sql.NullString, password, hash string) bool {
	switch strings.ToLower(strings.TrimSpace(algo.String)) {
	case "md5":
		sum := md5.Sum([]byte(password))
		return hex.EncodeToString(sum[:]) == hash
	case "sha256":
		sum := sha256.Sum256([]byte(password))
		return hex.EncodeToString(sum[:]) == hash
	case "md5salt":
		sum := md5.Sum([]byte(password + salt.String))
		return hex.EncodeToString(sum[:]) == hash
	default:
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
	}
}

func userRandomUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := crand.Read(b); err != nil {
		return "", fmt.Errorf("generate uuid bytes: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	hexStr := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexStr[0:8], hexStr[8:12], hexStr[12:16], hexStr[16:20], hexStr[20:32]), nil
}

func userRandomMD5Token() (string, error) {
	seed := make([]byte, 32)
	if _, err := crand.Read(seed); err != nil {
		return "", fmt.Errorf("generate token bytes: %w", err)
	}
	sum := md5.Sum([]byte(fmt.Sprintf("%s-%d", hex.EncodeToString(seed), time.Now().UnixNano())))
	return hex.EncodeToString(sum[:]), nil
}
