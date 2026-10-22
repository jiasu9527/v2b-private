package admin

import (
	"context"
	"errors"
	"strings"
	"time"

	"forest/go-api/internal/queue"
)

// MigrationMailSettings exposes only the existing bulk-mail pacing and whether
// SMTP is configured, never the SMTP connection credentials.
func (s *DBService) MigrationMailSettings() (intervalSeconds int64, configured bool, err error) {
	if s == nil {
		return 0, false, ErrUnavailable
	}
	cfg, err := s.loadBulkMailConfig()
	if err != nil {
		return 0, false, errors.New("读取邮件设置失败")
	}
	return cfg.bulkIntervalSeconds, cfg.host != "", nil
}

// PrepareMigrationMail captures the same configuration used by ordinary bulk
// mail. It neither queues a job nor sends mail; the migration service owns job
// persistence and dispatch. onResult is called once per attempted recipient.
func (s *DBService) PrepareMigrationMail(ctx context.Context, emails []string, subject, content string, onResult func(context.Context, bool) error) (intervalSeconds int64, run queue.JobFunc, err error) {
	if s == nil {
		return 0, nil, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	subject = strings.TrimSpace(subject)
	content = strings.TrimSpace(content)
	if subject == "" || content == "" || strings.ContainsAny(subject, "\r\n") {
		return 0, nil, errors.New("邮件主题或内容无效")
	}
	emailList := normalizeUserMailRecipients(emails)
	if len(emailList) == 0 {
		return 0, nil, errors.New("没有可发送的迁移用户")
	}
	cfg, err := s.loadBulkMailConfig()
	if err != nil {
		return 0, nil, errors.New("读取邮件设置失败")
	}
	if cfg.host == "" {
		return 0, nil, errors.New("邮件服务未配置")
	}
	batch := s.userMailBatch(emailList, subject, content, cfg, onResult)
	return cfg.bulkIntervalSeconds, func(jobCtx context.Context) error {
		err := batch(jobCtx)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, context.Canceled):
			return context.Canceled
		case errors.Is(err, context.DeadlineExceeded):
			return context.DeadlineExceeded
		default:
			// Detailed SMTP errors remain in the existing mail log only.
			return errors.New("迁移通知邮件未全部发送完成")
		}
	}, nil
}

func normalizeUserMailRecipients(emails []string) []string {
	emailList := make([]string, 0, len(emails))
	for _, email := range emails {
		if email = strings.TrimSpace(email); email != "" {
			emailList = append(emailList, email)
		}
	}
	return emailList
}

// userMailBatch is shared by user-manager bulk mail and migration notices so
// both use the notify template, mail log, and email_bulk_interval setting.
func (s *DBService) userMailBatch(emails []string, subject, content string, cfg bulkMailConfig, onResult func(context.Context, bool) error) queue.JobFunc {
	return func(jobCtx context.Context) error {
		var batchErr error
		for idx, email := range emails {
			if err := jobCtx.Err(); err != nil {
				return err
			}
			var sendErr error
			if cfg.host == "" {
				sendErr = errors.New("邮件服务未配置")
			} else {
				renderedBody := renderAdminMailBody(cfg, "notify", content, nil)
				sendErr = s.adminMailSender()(cfg.host, int(cfg.port), cfg.encryption, cfg.username, cfg.password, cfg.from, cfg.fromName, email, subject, renderedBody)
			}
			_ = s.insertMailLog(jobCtx, email, subject, sendErr)
			if onResult != nil {
				if err := onResult(jobCtx, sendErr == nil); err != nil {
					return err
				}
			}
			if sendErr != nil && batchErr == nil {
				batchErr = sendErr
			}
			if idx < len(emails)-1 && cfg.bulkIntervalSeconds > 0 {
				if err := s.sleepContext(jobCtx, time.Duration(cfg.bulkIntervalSeconds)*time.Second); err != nil {
					return err
				}
			}
		}
		return batchErr
	}
}
