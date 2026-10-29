package admin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	cfgpkg "forest/go-api/internal/config"
)

func TestAITicketSettingCompareAndSwapPreservesOtherConfig(t *testing.T) {
	root := t.TempDir()
	writeAdminJSONFixture(t, root, map[string]any{"ticket_status": 0, "email_password": "fixture-secret", "unrelated": 123})
	old := adminProjectRoot
	adminProjectRoot = root
	defer func() { adminProjectRoot = old }()
	s := &DBService{}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, status := range []int64{1, 2} {
		wg.Add(1)
		go func(v int64) { defer wg.Done(); errs <- s.CompareAndSetAITicketStatus(context.Background(), 0, v) }(status)
	}
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, ErrAITicketConfigConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("concurrent CAS success=%d conflict=%d", success, conflict)
	}
	cfg, err := loadAdminConfigStore(adminConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.int64Value("ticket_status", 0) == 0 || cfg.stringValue("email_password", "") != "fixture-secret" || cfg.int64Value("unrelated", 0) != 123 {
		t.Fatal("config lost")
	}
	if err = s.CompareAndSetAITicketStatus(context.Background(), 0, 2); !errors.Is(err, ErrAITicketConfigConflict) {
		t.Fatal("stale config overwritten", err)
	}
	if err = s.CompareAndSetAITicketStatus(context.Background(), 1, 9); err == nil {
		t.Fatal("unknown ticket mode accepted")
	}
}

func TestAITicketSettingReloadsRuntime(t *testing.T) {
	root := t.TempDir()
	writeAdminJSONFixture(t, root, map[string]any{"ticket_status": 0})
	oldRoot := adminProjectRoot
	adminProjectRoot = root
	defer func() { adminProjectRoot = oldRoot }()
	if err := os.MkdirAll(filepath.Join(root, "go-api"), 0700); err != nil {
		t.Fatal(err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWD)
	if err = os.Chdir(filepath.Join(root, "go-api")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TICKET_STATUS", "")
	cfg := cfgpkg.Load()
	runtime := cfgpkg.NewRuntimeState(cfg)
	s := (&DBService{cfg: cfg}).WithRuntimeConfig(runtime)
	if runtime.CurrentConfig().TicketStatus != 0 {
		t.Fatal("fixture initial state")
	}
	if err = s.CompareAndSetAITicketStatus(context.Background(), 0, 2); err != nil {
		t.Fatal(err)
	}
	if runtime.CurrentConfig().TicketStatus != 2 {
		t.Fatal("runtime config did not reload")
	}
}
