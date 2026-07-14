package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWriteJSONConfigFileAtomicallyReplacesCompletePrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "admin.json")
	first := &phpConfigFile{
		order: []string{"first"},
		values: map[string]phpConfigValue{
			"first": {kind: phpConfigScalar, scalar: "one"},
		},
	}
	if err := writeJSONConfigFile(path, first); err != nil {
		t.Fatalf("write first config: %v", err)
	}
	firstInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat first config: %v", err)
	}

	second := &phpConfigFile{
		order: []string{"second"},
		values: map[string]phpConfigValue{
			"second": {kind: phpConfigScalar, scalar: "two"},
		},
	}
	if err := writeJSONConfigFile(path, second); err != nil {
		t.Fatalf("write second config: %v", err)
	}
	secondInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat second config: %v", err)
	}
	if os.SameFile(firstInfo, secondInfo) {
		t.Fatal("admin config rewrite reused the destination inode; want temp-file rename")
	}
	if secondInfo.Mode().Perm() != 0o600 {
		t.Fatalf("admin config mode = %o, want 0600", secondInfo.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read second config: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode complete JSON: %v; raw=%q", err, raw)
	}
	if len(decoded) != 1 || decoded["second"] != "two" {
		t.Fatalf("unexpected atomically written config: %#v", decoded)
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".admin.json.tmp-*"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(temps) != 0 {
		t.Fatalf("atomic write left temp files: %#v", temps)
	}
}

func TestWriteJSONConfigFileCleansTempAfterRenameFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "admin.json")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("create destination directory: %v", err)
	}
	cfg := &phpConfigFile{
		order: []string{"key"},
		values: map[string]phpConfigValue{
			"key": {kind: phpConfigScalar, scalar: "value"},
		},
	}
	if err := writeJSONConfigFile(path, cfg); err == nil {
		t.Fatal("writeJSONConfigFile should fail when destination is a directory")
	}
	temps, err := filepath.Glob(filepath.Join(dir, ".admin.json.tmp-*"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(temps) != 0 {
		t.Fatalf("failed atomic write left temp files: %#v", temps)
	}
}

func TestCreateAdminConfigStoreIfMissingDoesNotOverwriteExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "admin.json")
	existing := &phpConfigFile{
		order: []string{"online"},
		values: map[string]phpConfigValue{
			"online": {kind: phpConfigScalar, scalar: "saved"},
		},
	}
	if err := writeJSONConfigFile(path, existing); err != nil {
		t.Fatalf("write existing config: %v", err)
	}
	migrated := &phpConfigFile{
		order: []string{"legacy"},
		values: map[string]phpConfigValue{
			"legacy": {kind: phpConfigScalar, scalar: "stale"},
		},
	}
	created, err := createAdminConfigStoreIfMissing(path, migrated)
	if err != nil {
		t.Fatalf("create missing config: %v", err)
	}
	if created {
		t.Fatal("existing online config was reported as migrated")
	}
	loaded, err := loadJSONConfigFile(path)
	if err != nil {
		t.Fatalf("load config after skipped migration: %v", err)
	}
	if loaded.stringValue("online", "") != "saved" || loaded.stringValue("legacy", "") != "" {
		t.Fatalf("migration overwrote existing online config: %#v", loaded.values)
	}
}

func TestUpdateAdminConfigStoreSerializesConcurrentDifferentKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "admin.json")
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for index, key := range []string{"alpha", "beta"} {
		index, key := index, key
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- updateAdminConfigStore(path, func(cfg *phpConfigFile) error {
				time.Sleep(10 * time.Millisecond)
				cfg.values[key] = phpConfigValue{kind: phpConfigScalar, scalar: fmt.Sprintf("value-%d", index)}
				cfg.order = appendMissingConfigKeys(cfg.order, []string{key}, cfg.values)
				return nil
			})
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent config update: %v", err)
		}
	}

	cfg, err := loadJSONConfigFile(path)
	if err != nil {
		t.Fatalf("load concurrent config: %v", err)
	}
	if cfg.stringValue("alpha", "") != "value-0" || cfg.stringValue("beta", "") != "value-1" {
		t.Fatalf("concurrent updates lost a key: alpha=%q beta=%q", cfg.stringValue("alpha", ""), cfg.stringValue("beta", ""))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat concurrent config: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("concurrent config mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestWriteJSONConfigFileReadersNeverObservePartialJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "admin.json")
	if err := writeJSONConfigFile(path, &phpConfigFile{
		order: []string{"generation", "payload"},
		values: map[string]phpConfigValue{
			"generation": {kind: phpConfigScalar, scalar: "0"},
			"payload":    {kind: phpConfigScalar, scalar: strings.Repeat("a", 64<<10)},
		},
	}); err != nil {
		t.Fatalf("initialize config: %v", err)
	}

	stop := make(chan struct{})
	readerErr := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				readerErr <- nil
				return
			default:
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				readerErr <- fmt.Errorf("read config during replace: %w", err)
				return
			}
			var decoded map[string]any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				readerErr <- fmt.Errorf("reader observed partial JSON: %w", err)
				return
			}
			if _, ok := decoded["generation"].(string); !ok {
				readerErr <- fmt.Errorf("reader observed incomplete config: %#v", decoded)
				return
			}
			if payload, ok := decoded["payload"].(string); !ok || len(payload) != 64<<10 {
				readerErr <- fmt.Errorf("reader observed incomplete payload")
				return
			}
		}
	}()

	for generation := 1; generation <= 50; generation++ {
		cfg := &phpConfigFile{
			order: []string{"generation", "payload"},
			values: map[string]phpConfigValue{
				"generation": {kind: phpConfigScalar, scalar: fmt.Sprint(generation)},
				"payload":    {kind: phpConfigScalar, scalar: strings.Repeat(string(rune('a'+generation%26)), 64<<10)},
			},
		}
		if err := writeJSONConfigFile(path, cfg); err != nil {
			close(stop)
			<-readerErr
			t.Fatalf("replace config generation %d: %v", generation, err)
		}
	}
	close(stop)
	if err := <-readerErr; err != nil {
		t.Fatal(err)
	}
}

func TestDNSFailoverAdminConfigWritersDoNotLoseConcurrentDifferentKeys(t *testing.T) {
	root := t.TempDir()
	oldRoot := adminProjectRoot
	adminProjectRoot = root
	t.Cleanup(func() { adminProjectRoot = oldRoot })
	if err := writeJSONConfigFile(adminConfigPath(), &phpConfigFile{order: []string{}, values: map[string]phpConfigValue{}}); err != nil {
		t.Fatalf("initialize admin config: %v", err)
	}

	service := &DBService{}
	const saveConfigWriters = 24
	start := make(chan struct{})
	errs := make(chan error, saveConfigWriters+1)
	var wg sync.WaitGroup
	for index := 0; index < saveConfigWriters; index++ {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			key := fmt.Sprintf("concurrent_key_%02d", index)
			_, err := service.SaveConfig(context.Background(), map[string]any{key: fmt.Sprintf("value-%02d", index)})
			errs <- err
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_, err := service.SaveDNSFailoverSettings(context.Background(), DNSFailoverSettingsSaveRequest{ProbeAPIURL: "https://probe.example.com"})
		errs <- err
	}()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent public config writer: %v", err)
		}
	}

	cfg, err := loadJSONConfigFile(adminConfigPath())
	if err != nil {
		t.Fatalf("load concurrently written admin config: %v", err)
	}
	for index := 0; index < saveConfigWriters; index++ {
		key := fmt.Sprintf("concurrent_key_%02d", index)
		want := fmt.Sprintf("value-%02d", index)
		if got := cfg.stringValue(key, ""); got != want {
			t.Fatalf("concurrent SaveConfig lost %s: got %q want %q", key, got, want)
		}
	}
	if got := cfg.stringValue(dnsProbeAPIURLKey, ""); got != "https://probe.example.com" {
		t.Fatalf("concurrent SaveDNSFailoverSettings lost %s: got %q", dnsProbeAPIURLKey, got)
	}
}
