package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Credentials are deliberately absent from this publicly rendered payload.
type serverRegisterPayload struct {
	IntakeID      int64  `json:"intake_id"`
	Name          string `json:"name"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	HostKeySHA256 string `json:"host_key_sha256"`
	TrustSource   string `json:"trust_source"`
	HasPassword   bool   `json:"has_password"`
	HasPrivateKey bool   `json:"has_private_key"`
}

type registrationIntake struct {
	ID, SessionID, TurnID, ActorID                     int64
	Name, Host, Username, HostKeySHA256                string
	Port                                               int
	PasswordCipher, PrivateKeyCipher, PassphraseCipher string
	CreatedAt, ExpiresAt, RegisteredServerID           int64
}

func loadRegistrationIntake(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id, sessionID, actorID int64, lock bool) (registrationIntake, error) {
	var v registrationIntake
	query := `SELECT id,session_id,turn_id,actor_id,name,host,port,username,host_key_sha256,password_cipher,private_key_cipher,key_passphrase_cipher,created_at,expires_at,registered_server_id FROM v2_ai_server_intake WHERE id=$1 AND session_id=$2 AND actor_id=$3`
	if lock {
		query += ` FOR UPDATE`
	}
	err := q.QueryRowContext(ctx, query, id, sessionID, actorID).Scan(&v.ID, &v.SessionID, &v.TurnID, &v.ActorID, &v.Name, &v.Host, &v.Port, &v.Username, &v.HostKeySHA256, &v.PasswordCipher, &v.PrivateKeyCipher, &v.PassphraseCipher, &v.CreatedAt, &v.ExpiresAt, &v.RegisteredServerID)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return v, err
}

func registrationTargetExists(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, host string, port int, username string) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_ai_server WHERE host=$1 AND port=$2 AND username=$3)`, host, port, username).Scan(&exists)
	return exists, err
}

func (s *Service) registeredIntakeServer(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, intake registrationIntake) (Server, error) {
	server, err := scanServer(q.QueryRowContext(ctx, `SELECT `+serverColumns+` FROM v2_ai_server WHERE id=$1`, intake.RegisteredServerID))
	if err != nil {
		return server, err
	}
	if server.Host != intake.Host || server.Port != intake.Port || server.Username != intake.Username || (intake.HostKeySHA256 != "" && server.HostKeySHA256 != intake.HostKeySHA256) {
		return Server{}, ErrConflict
	}
	return server, nil
}

func (s *Service) captureServerRegisterAction(ctx context.Context, tx *sql.Tx, spec actionSpec) (json.RawMessage, string, string, error) {
	var p serverRegisterPayload
	if json.Unmarshal(spec.Payload, &p) != nil || p.IntakeID <= 0 || spec.TargetID != p.IntakeID || spec.TargetType != "server_intake" || spec.ActorID <= 0 || spec.SessionID <= 0 {
		return nil, "", "", ErrValidation
	}
	intake, err := loadRegistrationIntake(ctx, tx, p.IntakeID, spec.SessionID, spec.ActorID, true)
	if err != nil {
		return nil, "", "", err
	}
	if intake.ExpiresAt <= time.Now().Unix() || intake.RegisteredServerID != 0 {
		return nil, "", "", ErrConflict
	}
	normalized, err := normalizeServer(ServerSaveRequest{Name: p.Name, Host: p.Host, Port: p.Port, Username: p.Username, HostKeySHA256: p.HostKeySHA256})
	if err != nil || normalized.Name != p.Name || normalized.Host != p.Host || normalized.Username != p.Username || normalized.HostKeySHA256 != p.HostKeySHA256 || p.Host != intake.Host || p.Port != intake.Port || p.Username != intake.Username || p.HasPassword != (intake.PasswordCipher != "") || p.HasPrivateKey != (intake.PrivateKeyCipher != "") || (!p.HasPassword && !p.HasPrivateKey) {
		return nil, "", "", ErrValidation
	}
	if (intake.HostKeySHA256 != "" && (p.TrustSource != "provided" || p.HostKeySHA256 != intake.HostKeySHA256)) || (intake.HostKeySHA256 == "" && p.TrustSource != "first_use") {
		return nil, "", "", ErrValidation
	}
	// This same lock protects SaveServer creations and the 200-server cap.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(2026102904)`); err != nil {
		return nil, "", "", err
	}
	if exists, err := registrationTargetExists(ctx, tx, p.Host, p.Port, p.Username); err != nil {
		return nil, "", "", err
	} else if exists {
		return nil, "", "", ErrConflict
	}
	raw, _ := json.Marshal(p)
	// Only the digest is persisted; ciphertext never enters action payloads.
	snapshot, _ := json.Marshal(struct {
		Intake  registrationIntake
		Payload serverRegisterPayload
	}{intake, p})
	return raw, hashOperation(string(snapshot)), p.Name, nil
}

func (s *Service) applyServerRegisterAction(ctx context.Context, tx *sql.Tx, a Action) (string, error) {
	var p serverRegisterPayload
	if json.Unmarshal(a.Payload, &p) != nil {
		return "", ErrValidation
	}
	intake, err := loadRegistrationIntake(ctx, tx, p.IntakeID, a.SessionID, a.ActorID, true)
	if err != nil {
		return "", err
	}
	if intake.ExpiresAt <= time.Now().Unix() || intake.RegisteredServerID != 0 {
		return "", ErrConflict
	}
	// Capture already holds the creation lock and validates the immutable target.
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_server`).Scan(&count); err != nil {
		return "", err
	}
	if count >= 200 {
		return "", ErrValidation
	}
	if intake.PasswordCipher != "" {
		password, err := s.decryptSecret(intake.PasswordCipher)
		if err != nil || password == "" || len(password) > 4096 {
			return "", ErrValidation
		}
	}
	if intake.PrivateKeyCipher != "" {
		key, err := s.decryptSecret(intake.PrivateKeyCipher)
		if err != nil || len(key) > 32768 {
			return "", ErrValidation
		}
		pass := ""
		if intake.PassphraseCipher != "" {
			pass, err = s.decryptSecret(intake.PassphraseCipher)
			if err != nil || len(pass) > 4096 {
				return "", ErrValidation
			}
		}
		if _, err = parseSSHKey(key, pass); err != nil {
			return "", ErrValidation
		}
	}
	now := time.Now().Unix()
	var serverID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO v2_ai_server(actor_id,name,host,port,username,password_cipher,private_key_cipher,key_passphrase_cipher,host_key_sha256,enabled,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,TRUE,$10,$10) RETURNING id`, a.ActorID, p.Name, p.Host, p.Port, p.Username, intake.PasswordCipher, intake.PrivateKeyCipher, intake.PassphraseCipher, p.HostKeySHA256, now).Scan(&serverID)
	if err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_server_intake SET registered_server_id=$2 WHERE id=$1`, intake.ID, serverID); err != nil {
		return "", err
	}
	return serverRegistrationOutput(serverID, false), nil
}
