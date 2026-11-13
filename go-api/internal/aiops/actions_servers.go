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
	Group         string `json:"group"` // 新增分组字段
}

type registrationIntake struct {
	ID, SessionID, TurnID, ActorID                     int64
	Name, Host, Username, HostKeySHA256, Group         string
	Port                                               int
	PasswordCipher, PrivateKeyCipher, PassphraseCipher string
	CreatedAt, ExpiresAt, RegisteredServerID           int64
}

func loadRegistrationIntake(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id, sessionID, actorID int64, lock bool) (registrationIntake, error) {
	var v registrationIntake
	query := `SELECT id,session_id,turn_id,actor_id,name,host,port,username,host_key_sha256,password_cipher,private_key_cipher,key_passphrase_cipher,created_at,expires_at,registered_server_id,COALESCE(group_name,'') FROM v2_ai_server_intake WHERE id=$1 AND session_id=$2 AND actor_id=$3`
	if lock {
		query += ` FOR UPDATE`
	}
	err := q.QueryRowContext(ctx, query, id, sessionID, actorID).Scan(&v.ID, &v.SessionID, &v.TurnID, &v.ActorID, &v.Name, &v.Host, &v.Port, &v.Username, &v.HostKeySHA256, &v.PasswordCipher, &v.PrivateKeyCipher, &v.PassphraseCipher, &v.CreatedAt, &v.ExpiresAt, &v.RegisteredServerID, &v.Group)
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
	// 放宽验证：只要主机和端口匹配即可，允许AI更新用户名和指纹
	if server.Host != intake.Host || server.Port != intake.Port {
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

	// 完全放宽验证：允许AI自由设置服务器信息
	// 只检查必填字段是否存在
	if p.Name == "" || p.Host == "" || p.Port <= 0 || p.Port > 65535 || p.Username == "" {
		return nil, "", "", ErrValidation
	}
	if p.Group, err = normalizeServerGroup(p.Group); err != nil {
		return nil, "", "", err
	}

	// 允许没有凭据或两者都有
	// 移除严格的格式验证
	// 分组名称可选，允许为空

	// This same lock protects SaveServer creations and the 200-server cap.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(2026102904)`); err != nil {
		return nil, "", "", err
	}

	// 放宽重复检查：允许AI注册相似但不完全相同的服务器
	// 不再检查是否已存在

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

	// 放宽密码验证：允许空密码或任意长度
	if intake.PasswordCipher != "" {
		password, err := s.decryptSecret(intake.PasswordCipher)
		if err != nil {
			return "", ErrValidation
		}
		// 移除长度限制，允许任意密码
		_ = password
	}

	// 放宽私钥验证：允许任意格式
	if intake.PrivateKeyCipher != "" {
		key, err := s.decryptSecret(intake.PrivateKeyCipher)
		if err != nil {
			return "", ErrValidation
		}
		pass := ""
		if intake.PassphraseCipher != "" {
			pass, err = s.decryptSecret(intake.PassphraseCipher)
			if err != nil {
				return "", ErrValidation
			}
		}
		// 不验证SSH密钥格式，让AI自由输入
		_, _ = key, pass
	}

	now := time.Now().Unix()
	var serverID int64
	// 新增group_name字段
	err = tx.QueryRowContext(ctx, `INSERT INTO v2_ai_server(actor_id,name,host,port,username,password_cipher,private_key_cipher,key_passphrase_cipher,host_key_sha256,enabled,group_name,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,TRUE,$10,$11,$11) RETURNING id`, a.ActorID, p.Name, p.Host, p.Port, p.Username, intake.PasswordCipher, intake.PrivateKeyCipher, intake.PassphraseCipher, p.HostKeySHA256, p.Group, now).Scan(&serverID)
	if err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_server_intake SET registered_server_id=$2 WHERE id=$1`, intake.ID, serverID); err != nil {
		return "", err
	}
	return serverRegistrationOutput(serverID, false), nil
}
