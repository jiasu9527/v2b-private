package admin

import (
	"context"
	"strings"
	"testing"

	"forest/go-api/internal/config"
)

func TestNotifyTicketReplyEnqueuesEmailJob(t *testing.T) {
	oldRoot := adminProjectRoot
	adminProjectRoot = t.TempDir()
	defer func() { adminProjectRoot = oldRoot }()

	service := (&DBService{cfg: config.Config{
		AppName:         "Forest",
		AppURL:          "https://forest.example.com",
		MailHost:        "127.0.0.1",
		MailPort:        25,
		MailFromAddress: "noreply@example.com",
	}}).WithQueueRuntime(&captureQueue{runNow: true})

	var (
		sentTo  string
		subject string
		body    string
	)
	service.mailSender = func(host string, port int, encryption, username, password, from, to, mailSubject, mailBody string) error {
		sentTo = to
		subject = mailSubject
		body = mailBody
		return nil
	}

	if err := service.notifyTicketReply(context.Background(), "user@example.com", "Need help", "handled"); err != nil {
		t.Fatalf("notify ticket reply: %v", err)
	}

	q := service.jobs.(*captureQueue)
	if len(q.queueNames) != 1 || q.queueNames[0] != "send_email" {
		t.Fatalf("unexpected queue names: %#v", q.queueNames)
	}
	if sentTo != "user@example.com" {
		t.Fatalf("unexpected recipient: %q", sentTo)
	}
	if !strings.Contains(subject, "您在Forest的工单得到了回复") {
		t.Fatalf("unexpected subject: %q", subject)
	}
	if !strings.Contains(body, "主题：Need help") || !strings.Contains(body, "回复内容：handled") {
		t.Fatalf("unexpected body: %q", body)
	}
}
