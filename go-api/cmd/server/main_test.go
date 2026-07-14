package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forest/go-api/internal/config"
)

type fakeDNSFailoverSchemaInitializer struct {
	called bool
	err    error
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
