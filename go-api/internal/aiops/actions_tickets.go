package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"forest/go-api/internal/admin"
)

type ticketUpdatePayload struct {
	TicketID  int64        `json:"ticket_id"`
	Operation string       `json:"operation"`
	Message   string       `json:"message,omitempty"`
	Before    *aiTicketDTO `json:"before,omitempty"`
}
type ticketSettingsPayload struct {
	Mode         string `json:"mode"`
	Status       int64  `json:"ticket_status"`
	BeforeStatus int64  `json:"before_status"`
	BeforeMode   string `json:"before_mode"`
}

func ticketSettingValue(mode string) (int64, bool) {
	switch mode {
	case "all":
		return 0, true
	case "paid_only":
		return 1, true
	case "disabled":
		return 2, true
	}
	return 0, false
}
func ticketSettingMode(status int64) string {
	switch status {
	case 0:
		return "all"
	case 1:
		return "paid_only"
	case 2:
		return "disabled"
	}
	return "unknown"
}
func validateTicketUpdate(p ticketUpdatePayload) error {
	if p.TicketID <= 0 || !oneOf(p.Operation, "close", "reopen", "reply") {
		return ErrValidation
	}
	if p.Operation == "reply" {
		if strings.TrimSpace(p.Message) == "" || len(p.Message) > 8000 || !utf8.ValidString(p.Message) || strings.ContainsRune(p.Message, 0) {
			return ErrValidation
		}
	} else if p.Message != "" {
		return ErrValidation
	}
	return nil
}
func (s *Service) readTicketSetting(ctx context.Context) (int64, error) {
	if s.panel == nil {
		return 0, ErrUnavailable
	}
	v, err := s.panel.FetchConfig(ctx, "ticket")
	if err != nil {
		return 0, ErrUnavailable
	}
	section, ok := v["ticket"].(map[string]any)
	if !ok {
		return 0, ErrUnavailable
	}
	raw, err := json.Marshal(section["ticket_status"])
	if err != nil {
		return 0, ErrUnavailable
	}
	var status int64
	if string(raw) == "null" || json.Unmarshal(raw, &status) != nil || !oneOfInt(status, 0, 1, 2) {
		return 0, ErrUnavailable
	}
	return status, nil
}
func loadAITicketSnapshot(ctx context.Context, tx *sql.Tx, id int64) (aiTicketDTO, string, error) {
	ticket, err := scanAITicket(tx.QueryRowContext(ctx, `SELECT `+aiTicketColumns+` FROM v2_ticket t LEFT JOIN v2_user u ON u.id=t.user_id WHERE t.id=$1 FOR UPDATE OF t`, id))
	if err != nil {
		return ticket, "", err
	}
	// The ticket row serializes normal reply/close writers. Include message IDs
	// and revisions as well so a new reply invalidates an existing approval card.
	var messages string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_array(id,user_id,updated_at,md5(message)) ORDER BY id),'[]'::JSONB)::TEXT FROM v2_ticket_message WHERE ticket_id=$1`, id).Scan(&messages); err != nil {
		return ticket, "", err
	}
	raw, _ := json.Marshal(ticket)
	return ticket, hashOperation(string(raw) + "\n" + messages), nil
}
func (s *Service) captureTicketAction(ctx context.Context, tx *sql.Tx, spec actionSpec) (json.RawMessage, string, string, error) {
	switch spec.Kind {
	case "ticket_update":
		var p ticketUpdatePayload
		if json.Unmarshal(spec.Payload, &p) != nil || validateTicketUpdate(p) != nil || spec.TargetID != p.TicketID {
			return nil, "", "", ErrValidation
		}
		before, snapshot, err := loadAITicketSnapshot(ctx, tx, p.TicketID)
		if err != nil {
			return nil, "", "", err
		}
		if (p.Operation == "close" && before.Status == 1) || (p.Operation == "reopen" && before.Status == 0) {
			return nil, "", "", ErrConflict
		}
		p.Before = &before
		raw, _ := json.Marshal(p)
		return raw, snapshot, "", nil
	case "ticket_settings":
		var p ticketSettingsPayload
		if json.Unmarshal(spec.Payload, &p) != nil {
			return nil, "", "", ErrValidation
		}
		status, ok := ticketSettingValue(p.Mode)
		if !ok || p.Status != status {
			return nil, "", "", ErrValidation
		}
		before, err := s.readTicketSetting(ctx)
		if err != nil {
			return nil, "", "", err
		}
		if before == status {
			return nil, "", "", ErrConflict
		}
		p.BeforeStatus = before
		p.BeforeMode = ticketSettingMode(before)
		raw, _ := json.Marshal(p)
		return raw, hashOperation(fmt.Sprint(before)), "", nil
	}
	return nil, "", "", ErrValidation
}
func applyTicketAction(ctx context.Context, tx *sql.Tx, a Action) error {
	if a.Kind != "ticket_update" {
		return ErrValidation
	}
	var p ticketUpdatePayload
	if json.Unmarshal(a.Payload, &p) != nil || validateTicketUpdate(p) != nil || a.ApprovedBy <= 0 {
		return ErrValidation
	}
	now := time.Now().Unix()
	status := int64(0)
	if p.Operation == "close" {
		status = 1
	}
	if p.Operation == "reply" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_ticket_message(user_id,ticket_id,message,created_at,updated_at) VALUES($1,$2,$3,$4,$4)`, a.ApprovedBy, p.TicketID, p.Message, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE v2_ticket SET status=0,reply_status=CASE WHEN user_id=$2 THEN 0 ELSE 1 END,updated_at=$3 WHERE id=$1`, p.TicketID, a.ApprovedBy, now)
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE v2_ticket SET status=$2,updated_at=$3 WHERE id=$1`, p.TicketID, status, now)
	return err
}

// This is a filesystem CAS through the same config store and runtime reload as
// the admin settings page. It cannot share a PostgreSQL transaction, therefore
// the action is claimed first and never retried automatically after interruption.
func (s *Service) executeTicketSettings(ctx context.Context, a Action, snapshot string) (string, string, string) {
	var p ticketSettingsPayload
	if json.Unmarshal(a.Payload, &p) != nil || hashOperation(fmt.Sprint(p.BeforeStatus)) != snapshot {
		return "failed", "", "工单配置快照无效，未修改"
	}
	status, ok := ticketSettingValue(p.Mode)
	if !ok || status != p.Status {
		return "failed", "", "工单设置无效，未修改"
	}
	writer, ok := s.panel.(interface {
		CompareAndSetAITicketStatus(context.Context, int64, int64) error
	})
	if !ok {
		return "failed", "", "工单设置接口不可用，未修改"
	}
	if err := s.requireAdmin(ctx, a.ApprovedBy); err != nil {
		return "failed", "", "管理员权限已变化，未修改"
	}
	if err := writer.CompareAndSetAITicketStatus(ctx, p.BeforeStatus, p.Status); err != nil {
		if errors.Is(err, admin.ErrAITicketConfigConflict) {
			return "failed", "", "全站工单设置已变化，未覆盖；请重新查询并确认"
		}
		return "unknown", "", "工单配置保存结果待核对，请检查当前设置，不要自动重试"
	}
	return "succeeded", "已更新全站工单提交设置：" + p.Mode, ""
}

// Notify only after the reply and action outcome have committed. Notifications
// retain the panel's existing best-effort mail behavior; a mail failure must not
// re-run the reply or duplicate its message.
func (s *Service) afterTicketAction(ctx context.Context, a Action) {
	if a.Kind != "ticket_update" {
		return
	}
	var p ticketUpdatePayload
	if json.Unmarshal(a.Payload, &p) != nil || p.Operation != "reply" {
		return
	}
	if notifier, ok := s.panel.(interface {
		NotifyAITicketReply(context.Context, int64, string) error
	}); ok {
		_ = notifier.NotifyAITicketReply(ctx, p.TicketID, p.Message)
	}
}
