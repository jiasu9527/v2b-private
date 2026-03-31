package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (s *DBService) notifyTicketReply(ctx context.Context, email, ticketSubject, replyMessage string) error {
	if s == nil {
		return nil
	}

	email = strings.TrimSpace(email)
	if email == "" {
		return nil
	}

	mailConfig, err := s.loadBulkMailConfig()
	if err != nil {
		return err
	}

	appName := strings.TrimSpace(mailConfig.appName)
	if appName == "" {
		appName = "V2Board"
	}
	subject := fmt.Sprintf("您在%s的工单得到了回复", appName)
	body := buildTicketReplyMailBody(strings.TrimSpace(ticketSubject), strings.TrimSpace(replyMessage), strings.TrimSpace(mailConfig.appURL))
	return s.dispatchTicketReplyMail(ctx, email, subject, body, mailConfig)
}

func buildTicketReplyMailBody(ticketSubject, replyMessage, appURL string) string {
	builder := &strings.Builder{}
	if ticketSubject != "" {
		fmt.Fprintf(builder, "主题：%s\n", ticketSubject)
	}
	if replyMessage != "" {
		fmt.Fprintf(builder, "回复内容：%s\n", replyMessage)
	}
	if appURL != "" {
		fmt.Fprintf(builder, "\n查看站点：%s\n", appURL)
	}
	return strings.TrimSpace(builder.String())
}

func (s *DBService) dispatchTicketReplyMail(ctx context.Context, email, subject, content string, cfg bulkMailConfig) error {
	runJob := func(jobCtx context.Context) error {
		var sendErr error
		if cfg.host == "" {
			sendErr = errors.New("邮件服务未配置")
		} else {
			renderedBody := renderAdminMailBody(cfg, "notify", content, nil)
			sendErr = s.adminMailSender()(cfg.host, int(cfg.port), cfg.encryption, cfg.username, cfg.password, cfg.from, cfg.fromName, email, subject, renderedBody)
		}
		_ = s.insertMailLog(jobCtx, email, subject, sendErr)
		return sendErr
	}

	if s.jobs != nil {
		if err := s.jobs.Enqueue("send_email", "ticket:"+email, runJob); err == nil {
			return nil
		}
	}

	return runJob(ctx)
}

func (s *DBService) loadBulkMailConfig() (bulkMailConfig, error) {
	cfg, err := loadAdminConfigStore(adminConfigPath())
	if err != nil {
		return bulkMailConfig{}, err
	}
	runtimeCfg := s.currentConfig()

	mailConfig := bulkMailConfig{
		host:                strings.TrimSpace(valueToString(cfg.values["email_host"])),
		port:                cfg.int64Value("email_port", runtimeCfg.MailPort),
		username:            strings.TrimSpace(valueToString(cfg.values["email_username"])),
		password:            valueToString(cfg.values["email_password"]),
		encryption:          strings.TrimSpace(valueToString(cfg.values["email_encryption"])),
		from:                strings.TrimSpace(valueToString(cfg.values["email_from_address"])),
		fromName:            strings.TrimSpace(valueToString(cfg.values["email_from_name"])),
		template:            strings.TrimSpace(valueToString(cfg.values["email_template"])),
		appName:             strings.TrimSpace(valueToString(cfg.values["app_name"])),
		appURL:              strings.TrimSpace(valueToString(cfg.values["app_url"])),
		bulkIntervalSeconds: cfg.int64Value("email_bulk_interval", 0),
	}

	if mailConfig.host == "" {
		mailConfig.host = strings.TrimSpace(runtimeCfg.MailHost)
	}
	if mailConfig.port == 0 {
		mailConfig.port = runtimeCfg.MailPort
	}
	if mailConfig.port == 0 {
		mailConfig.port = 25
	}
	if mailConfig.username == "" {
		mailConfig.username = strings.TrimSpace(runtimeCfg.MailUsername)
	}
	if mailConfig.password == "" {
		mailConfig.password = runtimeCfg.MailPassword
	}
	if mailConfig.encryption == "" {
		mailConfig.encryption = strings.TrimSpace(runtimeCfg.MailEncryption)
	}
	if mailConfig.from == "" {
		mailConfig.from = strings.TrimSpace(runtimeCfg.MailFromAddress)
	}
	if mailConfig.from == "" {
		mailConfig.from = "noreply@example.com"
	}
	if mailConfig.appName == "" {
		mailConfig.appName = strings.TrimSpace(runtimeCfg.AppName)
	}
	if mailConfig.appName == "" {
		mailConfig.appName = "V2Board"
	}
	if mailConfig.appURL == "" {
		mailConfig.appURL = strings.TrimSpace(runtimeCfg.AppURL)
	}
	if mailConfig.template == "" {
		mailConfig.template = "default"
	}
	if mailConfig.bulkIntervalSeconds < 0 {
		mailConfig.bulkIntervalSeconds = 0
	}
	if mailConfig.fromName == "" || mailConfig.fromName == "forest" || mailConfig.fromName == "forest-go-api" || mailConfig.fromName == "V2Board" {
		mailConfig.fromName = mailConfig.appName
	}
	return mailConfig, nil
}
