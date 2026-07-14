package telegram

import (
	"context"
	"errors"
	"testing"

	"forest/go-api/internal/config"
	"forest/go-api/internal/queue"
)

type captureTelegramQueue struct {
	queueNames []string
	runNow     bool
}

func TestDirectNotifierSendsSynchronouslyWithoutQueue(t *testing.T) {
	jobs := &captureTelegramQueue{}
	svc := NewService(config.Config{TelegramBotEnable: true, TelegramBotToken: "bot-token"}, nil).WithQueueRuntime(jobs)
	svc.resolveRecipients = func(context.Context, bool) ([]int64, error) {
		return []int64{11, 22}, nil
	}
	var sent []int64
	svc.sendMessage = func(_ context.Context, chatID int64, text string) error {
		sent = append(sent, chatID)
		if text != "dns alert" {
			t.Fatalf("text = %q", text)
		}
		return nil
	}

	if err := svc.DirectNotifier().NotifyAdmins(context.Background(), "dns alert", true); err != nil {
		t.Fatalf("direct notify: %v", err)
	}
	if len(jobs.queueNames) != 0 {
		t.Fatalf("direct notifier used async queue: %#v", jobs.queueNames)
	}
	if len(sent) != 2 || sent[0] != 11 || sent[1] != 22 {
		t.Fatalf("direct sends = %#v", sent)
	}
}

func TestDirectNotifierReturnsUnavailableWhenTelegramDisabled(t *testing.T) {
	svc := NewService(config.Config{}, nil)
	err := svc.DirectNotifier().NotifyAdmins(context.Background(), "dns alert", true)
	if !errors.Is(err, ErrDirectNotifierUnavailable) {
		t.Fatalf("error = %v, want ErrDirectNotifierUnavailable", err)
	}
}

func TestDirectNotifierReturnsActualSendFailure(t *testing.T) {
	wantErr := errors.New("telegram send failed")
	svc := NewService(config.Config{TelegramBotEnable: true, TelegramBotToken: "bot-token"}, nil)
	svc.resolveRecipients = func(context.Context, bool) ([]int64, error) {
		return []int64{11}, nil
	}
	svc.sendMessage = func(context.Context, int64, string) error { return wantErr }

	err := svc.DirectNotifier().NotifyAdmins(context.Background(), "dns alert", true)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestDirectNotifierTreatsNoRecipientsAsDelivered(t *testing.T) {
	svc := NewService(config.Config{TelegramBotEnable: true, TelegramBotToken: "bot-token"}, nil)
	svc.resolveRecipients = func(context.Context, bool) ([]int64, error) { return nil, nil }
	if err := svc.DirectNotifier().NotifyAdmins(context.Background(), "dns alert", true); err != nil {
		t.Fatalf("no-recipient direct notify: %v", err)
	}
}

func (c *captureTelegramQueue) Enqueue(queueName, jobName string, fn queue.JobFunc) error {
	c.queueNames = append(c.queueNames, queueName)
	if c.runNow {
		return fn(context.Background())
	}
	return nil
}

func (c *captureTelegramQueue) Snapshot() queue.Snapshot {
	return queue.Snapshot{}
}

func TestServiceNotifyAdminsEnqueuesTelegramJobs(t *testing.T) {
	svc := NewService(config.Config{TelegramBotEnable: true, TelegramBotToken: "bot-token"}, nil).WithQueueRuntime(&captureTelegramQueue{runNow: true})
	svc.resolveRecipients = func(context.Context, bool) ([]int64, error) {
		return []int64{11, 22}, nil
	}

	var sent []int64
	svc.sendMessage = func(_ context.Context, chatID int64, text string) error {
		sent = append(sent, chatID)
		if text == "" {
			t.Fatal("expected telegram text")
		}
		return nil
	}

	if err := svc.NotifyAdmins(context.Background(), "ticket created", true); err != nil {
		t.Fatalf("notify admins: %v", err)
	}

	q := svc.jobs.(*captureTelegramQueue)
	if len(q.queueNames) != 2 || q.queueNames[0] != "send_telegram" || q.queueNames[1] != "send_telegram" {
		t.Fatalf("unexpected queue names: %#v", q.queueNames)
	}
	if len(sent) != 2 || sent[0] != 11 || sent[1] != 22 {
		t.Fatalf("unexpected sent ids: %#v", sent)
	}
}

func TestServiceNotifyAdminsSkipsWhenBotDisabled(t *testing.T) {
	svc := NewService(config.Config{}, nil).WithQueueRuntime(&captureTelegramQueue{runNow: true})
	called := false
	svc.resolveRecipients = func(context.Context, bool) ([]int64, error) {
		called = true
		return []int64{11}, nil
	}

	if err := svc.NotifyAdmins(context.Background(), "ignored", true); err != nil {
		t.Fatalf("notify admins: %v", err)
	}

	if called {
		t.Fatal("expected recipient lookup to be skipped when telegram is disabled")
	}
}
