package main

import (
	"context"
	"errors"
	"testing"
)

type fakeDNSFailoverSchemaInitializer struct {
	called bool
	err    error
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
