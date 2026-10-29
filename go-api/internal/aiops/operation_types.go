package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
)

type Server struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Host             string `json:"host"`
	Port             int    `json:"port"`
	Username         string `json:"username"`
	HostKeySHA256    string `json:"host_key_sha256"`
	Enabled          bool   `json:"enabled"`
	Version          int64  `json:"version"`
	HasPassword      bool   `json:"has_password"`
	HasPrivateKey    bool   `json:"has_private_key"`
	HasKeyPassphrase bool   `json:"has_key_passphrase"`
	CreatedAt        int64  `json:"created_at"`
	UpdatedAt        int64  `json:"updated_at"`
}
type ServerSaveRequest struct {
	ID                 int64  `json:"id"`
	Version            int64  `json:"version"`
	Name               string `json:"name"`
	Host               string `json:"host"`
	Port               int    `json:"port"`
	Username           string `json:"username"`
	HostKeySHA256      string `json:"host_key_sha256"`
	Enabled            bool   `json:"enabled"`
	Password           string `json:"password"`
	PrivateKey         string `json:"private_key"`
	KeyPassphrase      string `json:"key_passphrase"`
	ClearPassword      bool   `json:"clear_password"`
	ClearPrivateKey    bool   `json:"clear_private_key"`
	ClearKeyPassphrase bool   `json:"clear_key_passphrase"`
}
type HostKeyRequest struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}
type HostKeyResult struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Fingerprint string `json:"fingerprint"`
	KeyType     string `json:"key_type"`
}
type Action struct {
	ID          int64           `json:"id"`
	SessionID   int64           `json:"session_id"`
	ActorID     int64           `json:"actor_id"`
	ApprovedBy  int64           `json:"approved_by"`
	RejectedBy  int64           `json:"rejected_by"`
	ToolCallID  string          `json:"tool_call_id"`
	Kind        string          `json:"kind"`
	TargetID    int64           `json:"target_id"`
	TargetType  string          `json:"target_type"`
	ServerLabel string          `json:"server_label"`
	Payload     json.RawMessage `json:"payload"`
	Reason      string          `json:"reason"`
	Status      string          `json:"status"`
	ExpiresAt   int64           `json:"expires_at"`
	ClaimedAt   int64           `json:"claimed_at"`
	CompletedAt int64           `json:"completed_at"`
	Output      string          `json:"output"`
	Error       string          `json:"error"`
	CreatedAt   int64           `json:"created_at"`
	UpdatedAt   int64           `json:"updated_at"`
}

const actionColumns = `id,session_id,actor_id,approved_by,rejected_by,tool_call_id,kind,target_id,target_type,server_label,payload,reason,status,expires_at,claimed_at,completed_at,output,error,created_at,updated_at`

func ensureActionSchema(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS v2_ai_server(id BIGSERIAL PRIMARY KEY,actor_id BIGINT NOT NULL REFERENCES v2_user(id),name TEXT NOT NULL,host TEXT NOT NULL,port INTEGER NOT NULL CHECK(port BETWEEN 1 AND 65535),username TEXT NOT NULL,password_cipher TEXT NOT NULL DEFAULT '',private_key_cipher TEXT NOT NULL DEFAULT '',key_passphrase_cipher TEXT NOT NULL DEFAULT '',host_key_sha256 TEXT NOT NULL,enabled BOOLEAN NOT NULL DEFAULT TRUE,version BIGINT NOT NULL DEFAULT 1,created_at BIGINT NOT NULL,updated_at BIGINT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS v2_ai_action(id BIGSERIAL PRIMARY KEY,session_id BIGINT NOT NULL REFERENCES v2_ai_session(id) ON DELETE CASCADE,actor_id BIGINT NOT NULL REFERENCES v2_user(id),tool_call_id TEXT NOT NULL,kind TEXT NOT NULL,target_id BIGINT NOT NULL DEFAULT 0,target_type TEXT NOT NULL DEFAULT '',server_label TEXT NOT NULL DEFAULT '',payload JSONB NOT NULL,snapshot_hash TEXT NOT NULL,request_hash TEXT NOT NULL,reason TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'pending',expires_at BIGINT NOT NULL,claimed_at BIGINT NOT NULL DEFAULT 0,completed_at BIGINT NOT NULL DEFAULT 0,output TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '',created_at BIGINT NOT NULL,updated_at BIGINT NOT NULL,UNIQUE(session_id,tool_call_id))`,
		`ALTER TABLE v2_ai_action ADD COLUMN IF NOT EXISTS approved_by BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE v2_ai_action ADD COLUMN IF NOT EXISTS rejected_by BIGINT NOT NULL DEFAULT 0`,
		`CREATE INDEX IF NOT EXISTS idx_ai_action_session ON v2_ai_action(session_id,id)`,
		`CREATE INDEX IF NOT EXISTS idx_ai_action_status ON v2_ai_action(status,claimed_at)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
