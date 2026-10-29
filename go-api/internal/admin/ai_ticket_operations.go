package admin

import (
	"context"
	"errors"
	"strconv"
)

var ErrAITicketConfigConflict = errors.New("工单提交设置已变化")

// CompareAndSetAITicketStatus uses the same locked, atomic config store as
// SaveConfig. Only this known setting is exposed; no other secret config is
// accepted or returned to the AI tool layer.
func (s *DBService) CompareAndSetAITicketStatus(ctx context.Context, expected, status int64) error {
	if expected < 0 || expected > 2 || status < 0 || status > 2 {
		return errors.New("工单提交设置无效")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err := updateAdminConfigStore(adminConfigPath(), func(cfg *phpConfigFile) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if cfg.int64Value("ticket_status", 0) != expected {
			return ErrAITicketConfigConflict
		}
		if _, exists := cfg.values["ticket_status"]; !exists {
			cfg.order = append(cfg.order, "ticket_status")
		}
		cfg.values["ticket_status"] = phpConfigValue{kind: phpConfigScalar, scalar: strconv.FormatInt(status, 10)}
		return nil
	})
	if err == nil && s.runtime != nil {
		s.runtime.Reload()
	}
	return err
}

func (s *DBService) NotifyAITicketReply(ctx context.Context, id int64, message string) error {
	if s.db == nil {
		return ErrUnavailable
	}
	var email, subject string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(u.email,''),t.subject FROM v2_ticket t LEFT JOIN v2_user u ON u.id=t.user_id WHERE t.id=$1`, id).Scan(&email, &subject); err != nil {
		return err
	}
	return s.notifyTicketReply(ctx, email, subject, message)
}
