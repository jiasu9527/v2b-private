package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func relayConfigRequest() ConfigSaveRequest {
	return ConfigSaveRequest{Enabled: true, BaseURL: "https://fixture.example.test/v1", Models: append([]string{}, DefaultModels...)}
}

func TestRelayConfigPostgresSaveAndIsolation(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx := context.Background()
	on, off, timeout := true, false, 45
	relays := []RelaySaveRequest{
		{Name: "备用一", BaseURL: "https://one.example.test", APIKey: "key-one-fixture", Enabled: true, Models: []string{DefaultModels[0]}},
		{Name: "备用二", BaseURL: "https://two.example.test", APIKey: "key-two-fixture", Enabled: true},
		{Name: "停用", BaseURL: "https://disabled.example.test", Enabled: false},
	}
	req := relayConfigRequest()
	req.FallbackRelays, req.FailoverEnabled, req.FailoverTimeoutSeconds = &relays, &on, &timeout
	saved, err := f.s.SaveConfig(ctx, f.admin, req)
	if err != nil || len(saved.FallbackRelays) != 3 || !saved.FailoverEnabled || saved.FailoverTimeoutSeconds != 45 {
		t.Fatal("save fallback config", saved, err)
	}
	snapshot := func() string {
		t.Helper()
		var raw string
		if e := f.db.QueryRowContext(ctx, `SELECT row_to_json(c)::TEXT FROM v2_ai_config c WHERE id=1`).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		return raw
	}
	before := snapshot()
	if strings.Contains(before, "key-one-fixture") || strings.Contains(before, "key-two-fixture") {
		t.Fatal("plaintext fallback key persisted")
	}
	_, primaryCipher, stored, err := f.s.loadConfigState(ctx)
	if err != nil || len(stored) != 3 {
		t.Fatal("load stored config", err)
	}
	for i, want := range []string{"key-one-fixture", "key-two-fixture"} {
		plain, e := f.s.decryptSecret(stored[i].APIKeyCipher)
		if e != nil || plain != want {
			t.Fatal("encrypted fallback mismatch", i, e)
		}
	}
	adminView, err := f.s.GetConfig(ctx, f.admin)
	if err != nil || adminView.FallbackRelays[0].Name != "备用一" {
		t.Fatal("admin config projection", err)
	}
	publicRaw, _ := json.Marshal(adminView)
	for _, secret := range []string{primaryCipher, stored[0].APIKeyCipher, "key-one-fixture", "api_key_cipher"} {
		if strings.Contains(string(publicRaw), secret) {
			t.Fatal("admin response exposed credential")
		}
	}
	staffView, err := f.s.GetConfig(ctx, f.staff)
	staffRaw, _ := json.Marshal(staffView)
	if err != nil || len(staffView.FallbackRelays) != 0 || staffView.BaseURL != "" || !staffView.FailoverEnabled || strings.Contains(string(staffRaw), "备用一") || strings.Contains(string(staffRaw), "one.example.test") {
		t.Fatal("staff relay details exposed", staffView, err)
	}
	if _, err = f.s.SaveConfig(ctx, f.staff, req); !errors.Is(err, ErrForbidden) || snapshot() != before {
		t.Fatal("staff changed fallback configuration", err)
	}

	// Omitting newly introduced fields is compatible with existing clients.
	retained, err := f.s.SaveConfig(ctx, f.admin, relayConfigRequest())
	if err != nil || len(retained.FallbackRelays) != 3 || retained.FallbackRelays[0].ID != saved.FallbackRelays[0].ID || !retained.FailoverEnabled || retained.FailoverTimeoutSeconds != 45 {
		t.Fatal("omitted fields were reset", retained, err)
	}
	for _, model := range []string{DefaultModels[0], DefaultModels[1]} {
		_, candidates, e := f.s.loadProviderCandidates(ctx, model)
		want := 2
		if model == DefaultModels[0] {
			want = 3
		}
		if e != nil || len(candidates) != want || candidates[0].ID != "primary" || candidates[len(candidates)-1].ID != saved.FallbackRelays[1].ID {
			t.Fatal("candidate model/order mismatch", model, candidates, e)
		}
	}
	for i := range relays {
		relays[i].ID, relays[i].APIKey = saved.FallbackRelays[i].ID, ""
	}
	relays[0], relays[1] = relays[1], relays[0]
	relays[0].Name = "更名后仍保留原密钥"
	req.FallbackRelays = &relays
	if _, err = f.s.SaveConfig(ctx, f.admin, req); err != nil {
		t.Fatal(err)
	}
	_, _, reordered, err := f.s.loadConfigState(ctx)
	if err != nil || reordered[0].APIKeyCipher != stored[1].APIKeyCipher || reordered[1].APIKeyCipher != stored[0].APIKeyCipher {
		t.Fatal("reordering swapped credentials", err)
	}

	for _, name := range []string{"changed address", "unknown id", "missing primary", "timeout low", "timeout high", "enabled key cleared", "removed supported model"} {
		t.Run(name, func(t *testing.T) {
			bad := req
			copyRelays := append([]RelaySaveRequest{}, relays...)
			bad.FallbackRelays = &copyRelays
			switch name {
			case "changed address":
				copyRelays[0].BaseURL = "https://untrusted.example.test"
			case "unknown id":
				copyRelays[0].ID = "relay_unknown"
			case "missing primary":
				bad.ClearAPIKey = true
			case "timeout low":
				n := 9
				bad.FailoverTimeoutSeconds = &n
			case "timeout high":
				n := 91
				bad.FailoverTimeoutSeconds = &n
			case "enabled key cleared":
				copyRelays[0].ClearAPIKey = true
			case "removed supported model":
				bad.Models, bad.FallbackRelays = DefaultModels[1:], nil
			}
			state := snapshot()
			if _, e := f.s.SaveConfig(ctx, f.admin, bad); !errors.Is(e, ErrValidation) || snapshot() != state {
				t.Fatal("invalid save did not roll back atomically", e)
			}
		})
	}
	relays[0].BaseURL, relays[0].APIKey = "https://replacement.example.test", "replacement-key"
	if _, err = f.s.SaveConfig(ctx, f.admin, req); err != nil {
		t.Fatal("replacing key and endpoint", err)
	}
	_, _, replaced, err := f.s.loadConfigState(ctx)
	plain, decryptErr := f.s.decryptSecret(replaced[0].APIKeyCipher)
	if err != nil || decryptErr != nil || plain != "replacement-key" {
		t.Fatal("replacement key not stored", err, decryptErr)
	}
	relays[0].APIKey, relays[0].Enabled, relays[0].ClearAPIKey = "", false, true
	if _, err = f.s.SaveConfig(ctx, f.admin, req); err != nil {
		t.Fatal("explicit fallback key clear", err)
	}
	_, _, cleared, err := f.s.loadConfigState(ctx)
	if err != nil || cleared[0].APIKeyCipher != "" {
		t.Fatal("fallback key not cleared", err)
	}
	disabled := relayConfigRequest()
	disabled.FailoverEnabled = &off
	if _, err = f.s.SaveConfig(ctx, f.admin, disabled); err != nil {
		t.Fatal(err)
	}
	_, candidates, err := f.s.loadProviderCandidates(ctx, DefaultModels[0])
	if err != nil || len(candidates) != 1 || candidates[0].APIKeyCipher != primaryCipher {
		t.Fatal("disabled failover still adds backup candidates", err)
	}
	removed := []RelaySaveRequest{}
	disabled.FallbackRelays = &removed
	deleted, err := f.s.SaveConfig(ctx, f.admin, disabled)
	if err != nil || len(deleted.FallbackRelays) != 0 {
		t.Fatal("explicit list clear", err)
	}
	state := snapshot()
	if _, err = f.s.SaveConfig(ctx, f.admin, req); !errors.Is(err, ErrValidation) || state != snapshot() {
		t.Fatal("deleted endpoint ID reused old credentials", err)
	}
}

func TestRelayConfigPostgresUpgradeDefaults(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx := context.Background()
	_, originalCipher, err := f.s.loadConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ExecContext(ctx, `ALTER TABLE v2_ai_config DROP COLUMN fallback_relays,DROP COLUMN failover_enabled,DROP COLUMN failover_timeout_seconds`); err != nil {
		t.Fatal(err)
	}
	upgraded := NewService(f.s.cfg, f.db, f.s.panel)
	defer upgraded.Close()
	if err = upgraded.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	cfg, cipher, err := upgraded.loadConfig(ctx)
	if err != nil || cipher != originalCipher || cfg.FailoverEnabled || cfg.FailoverTimeoutSeconds != 30 || len(cfg.FallbackRelays) != 0 || !cfg.Enabled {
		t.Fatal("upgrade changed existing config", cfg, err)
	}
}

func TestRelayConfigPostgresConcurrentSnapshots(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	relays := []RelaySaveRequest{{Name: "初始", BaseURL: "https://initial.example.test", APIKey: "initial-key", Enabled: true}}
	req := relayConfigRequest()
	req.FallbackRelays = &relays
	saved, err := f.s.SaveConfig(ctx, f.admin, req)
	if err != nil {
		t.Fatal(err)
	}
	relayID := saved.FallbackRelays[0].ID
	results := make(chan error, 32)
	start := make(chan struct{})
	var group sync.WaitGroup
	for worker := 0; worker < 12; worker++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			<-start
			label := fmt.Sprintf("worker-%d", i)
			entries := []RelaySaveRequest{{ID: relayID, Name: label, BaseURL: "https://" + label + ".example.test", APIKey: label + "-key", Enabled: true}}
			request := relayConfigRequest()
			request.FallbackRelays = &entries
			if _, e := f.s.SaveConfig(ctx, f.admin, request); e != nil {
				results <- e
			}
		}(worker)
	}
	close(start)
	for i := 0; i < 20; i++ {
		_, _, endpoints, e := f.s.loadConfigState(ctx)
		if e != nil || len(endpoints) != 1 {
			results <- fmt.Errorf("read concurrent snapshot: %v", e)
			continue
		}
		r := endpoints[0]
		plain, e := f.s.decryptSecret(r.APIKeyCipher)
		want := r.Name + "-key"
		if r.Name == "初始" {
			want = "initial-key"
		}
		if e != nil || plain != want {
			results <- fmt.Errorf("relay address and key came from different updates: %v", e)
		}
	}
	group.Wait()
	close(results)
	for e := range results {
		t.Error(e)
	}
}

func TestRelayConfigPostgresTransactionRechecksAdmin(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := f.db.ExecContext(ctx, `UPDATE v2_user SET is_admin=1 WHERE id=$1`, f.other); err != nil {
		t.Fatal(err)
	}
	blocker, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err = blocker.ExecContext(ctx, `SELECT pg_advisory_xact_lock(2026102902)`); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, e := f.s.SaveConfig(ctx, f.other, relayConfigRequest())
		result <- e
	}()
	for {
		var blocked bool
		if err = f.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query='SELECT pg_advisory_xact_lock_shared(2026102902)')`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case err := <-result:
			t.Fatal("save never reached transaction", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err = f.db.ExecContext(ctx, `UPDATE v2_user SET is_admin=0 WHERE id=$1`, f.other); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-result:
		if !errors.Is(err, ErrForbidden) {
			t.Fatal("save accepted revoked admin", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
