package aiops

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"
)

// Explicit public projection: neither plaintext nor encrypted credentials can
// accidentally be marshalled into chat history or a tool result.
type sshIntakePublic struct {
	IntakeID           int64  `json:"intake_id"`
	ItemIndex          int    `json:"item_index"`
	Name               string `json:"name"`
	Host               string `json:"host"`
	Port               int    `json:"port"`
	Username           string `json:"username"`
	HostKeySHA256      string `json:"host_key_sha256,omitempty"`
	HasPassword        bool   `json:"has_password"`
	HasPrivateKey      bool   `json:"has_private_key"`
	HasKeyPassphrase   bool   `json:"has_key_passphrase"`
	ExpiresAt          int64  `json:"expires_at"`
	RegisteredServerID int64  `json:"registered_server_id,omitempty"`
	State              string `json:"state,omitempty"`
}

func sshIntakesToolDefinition() ToolDefinition {
	raw, _ := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{
		"turn_id": map[string]any{"type": "integer", "minimum": 1, "description": "批量暂存摘要中的turn_id。首次省略查询本人在当前会话最近一批；翻页必须传返回的turn_id"},
		"offset":  map[string]any{"type": "integer", "minimum": 0, "maximum": maxSSHIntakeBatchSize},
		"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": maxSSHIntakeBatchSize},
	}, "required": []string{}, "additionalProperties": false})
	return ToolDefinition{Type: "function", Function: ToolFunctionDefinition{Name: "ssh_intakes_list", Description: "分页读取本人在当前会话提交的SSH批量暂存资料和登记进度；不返回任何凭据或密文，不连接服务器。默认最近一批；按返回turn_id和next_offset继续，核对total防止漏登记。registered已登记；pending待审核；expired需重新提交；unregistered尚未登记。", Parameters: raw}}
}

func (s *Service) callSSHIntakesList(ctx context.Context, actorID, sessionID int64, call ToolCall) (ToolOutcome, error) {
	var args struct {
		TurnID int64 `json:"turn_id"`
		Offset int   `json:"offset"`
		Limit  int   `json:"limit"`
	}
	d := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	d.DisallowUnknownFields()
	if d.Decode(&args) != nil || d.Decode(new(any)) != io.EOF || args.TurnID < 0 || args.Offset < 0 || args.Offset > maxSSHIntakeBatchSize || args.Limit < 0 || args.Limit > maxSSHIntakeBatchSize {
		return ToolOutcome{}, ErrValidation
	}
	if args.Limit == 0 {
		args.Limit = 20
	}
	if err := s.ensureSchema(ctx); err != nil {
		return ToolOutcome{}, err
	}
	if args.TurnID == 0 {
		if args.Offset != 0 {
			// Never page silently into a newly submitted batch.
			return ToolOutcome{}, ErrValidation
		}
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(turn_id),0) FROM v2_ai_server_intake WHERE session_id=$1 AND actor_id=$2`, sessionID, actorID).Scan(&args.TurnID); err != nil {
			return ToolOutcome{}, err
		}
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_server_intake WHERE session_id=$1 AND actor_id=$2 AND turn_id=$3`, sessionID, actorID, args.TurnID).Scan(&total); err != nil {
		return ToolOutcome{}, err
	}
	now := time.Now().Unix()
	rows, err := s.db.QueryContext(ctx, `SELECT i.id,i.item_index,i.name,i.host,i.port,i.username,i.host_key_sha256,i.password_cipher<>'',i.private_key_cipher<>'',i.key_passphrase_cipher<>'',i.expires_at,i.registered_server_id,
		CASE WHEN i.registered_server_id>0 THEN 'registered' WHEN i.expires_at<=$6 THEN 'expired'
		WHEN EXISTS(SELECT 1 FROM v2_ai_action a WHERE a.session_id=i.session_id AND a.actor_id=i.actor_id AND a.kind='server_register' AND a.target_id=i.id AND a.status='pending' AND a.expires_at>$6) THEN 'pending' ELSE 'unregistered' END
		FROM v2_ai_server_intake i WHERE i.session_id=$1 AND i.actor_id=$2 AND i.turn_id=$3 ORDER BY i.item_index LIMIT $4 OFFSET $5`, sessionID, actorID, args.TurnID, args.Limit, args.Offset, now)
	if err != nil {
		return ToolOutcome{}, err
	}
	defer rows.Close()
	items := make([]sshIntakePublic, 0, args.Limit)
	size := 0
	for rows.Next() {
		var item sshIntakePublic
		if err := rows.Scan(&item.IntakeID, &item.ItemIndex, &item.Name, &item.Host, &item.Port, &item.Username, &item.HostKeySHA256, &item.HasPassword, &item.HasPrivateKey, &item.HasKeyPassphrase, &item.ExpiresAt, &item.RegisteredServerID, &item.State); err != nil {
			return ToolOutcome{}, err
		}
		raw, err := json.Marshal(item)
		if err != nil {
			return ToolOutcome{}, err
		}
		// Leave room for the envelope below the chat loop's 32 KB output cap.
		if len(items) > 0 && size+len(raw) > 24000 {
			break
		}
		size += len(raw)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ToolOutcome{}, err
	}
	truncated := args.Offset+len(items) < total
	var next any
	if truncated {
		next = args.Offset + len(items)
	}
	raw, err := json.Marshal(map[string]any{"turn_id": args.TurnID, "total": total, "items": items, "offset": args.Offset, "limit": args.Limit, "next_offset": next, "truncated": truncated})
	return ToolOutcome{Content: string(raw)}, err
}
