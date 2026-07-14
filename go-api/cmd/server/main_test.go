package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"forest/go-api/internal/telegram"
)

type fakeDNSFailoverSchemaInitializer struct {
	called bool
	err    error
}

func TestDNSFailoverNotifierForTelegramUsesDirectAdapter(t *testing.T) {
	service := telegram.NewService(config.Config{TelegramBotEnable: true, TelegramBotToken: "test-token"}, nil)
	var notifier admin.DNSFailoverNotifier = dnsFailoverNotifierForTelegram(service)
	if _, ok := notifier.(*telegram.DirectNotifier); !ok {
		t.Fatalf("notifier type = %T, want *telegram.DirectNotifier", notifier)
	}
}

type fakeDNSFailoverAutomationStarter struct {
	called  bool
	ctx     context.Context
	stopped bool
}

func (starter *fakeDNSFailoverAutomationStarter) StartDNSFailoverAutomation(ctx context.Context) {
	starter.called = true
	starter.ctx = ctx
}

func (starter *fakeDNSFailoverAutomationStarter) StopDNSFailoverAutomation(context.Context) error {
	starter.stopped = true
	return nil
}

func TestValidateServerConfigRejectsInvalidProbeTrustedProxyCIDR(t *testing.T) {
	err := validateServerConfig(config.Config{ProbeTrustedProxyCIDRs: []string{"127.0.0.0/8", "broken"}})
	if err == nil || !strings.Contains(err.Error(), "PROBE_TRUSTED_PROXY_CIDRS") {
		t.Fatalf("error = %v, want trusted proxy CIDR configuration error", err)
	}
}

func (initializer *fakeDNSFailoverSchemaInitializer) InitializeDNSFailoverSchema(context.Context) error {
	initializer.called = true
	return initializer.err
}

func TestInitializeDNSFailoverBeforeServeCallsInitializerAndPropagatesFailure(t *testing.T) {
	wantErr := errors.New("schema unavailable")
	initializer := &fakeDNSFailoverSchemaInitializer{err: wantErr}

	err := initializeDNSFailoverBeforeServe(context.Background(), initializer)

	if !initializer.called {
		t.Fatal("schema initializer was not called")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestStartDNSFailoverAutomationAfterSchemaStartsConfiguredWorker(t *testing.T) {
	ctx := context.Background()
	starter := &fakeDNSFailoverAutomationStarter{}

	startDNSFailoverAutomationAfterSchema(ctx, starter)

	if !starter.called || starter.ctx != ctx {
		t.Fatalf("starter was not called with server context: %#v", starter)
	}
}

func TestStopDNSFailoverAutomationBeforeDependenciesWaitsForWorker(t *testing.T) {
	starter := &fakeDNSFailoverAutomationStarter{}
	if err := stopDNSFailoverAutomationBeforeDependencies(context.Background(), starter); err != nil {
		t.Fatalf("stop DNS failover automation: %v", err)
	}
	if !starter.stopped {
		t.Fatal("DNS failover automation was not stopped")
	}
}
