package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	platformpostgres "forest/go-api/internal/platform/postgres"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAIActionsPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("dedicated disposable FOREST_AIOPS_TEST_DSN required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = platformpostgres.EnsureClientEntrySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = platformpostgres.EnsureDNSFailoverSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	panel := admin.NewDBService(config.Config{}, db)
	s := NewService(config.Config{AppKey: "actions-fixture-key"}, db, panel)
	defer s.Close()
	if err = s.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var actor, session int64
	token := fmt.Sprint(time.Now().UnixNano())
	if err = db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at)VALUES($1,'unused','test',$2,1,1,1)RETURNING id`, token+"@example.test", token).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_user WHERE id=$1`, actor)
	if _, err = s.SaveConfig(ctx, actor, ConfigSaveRequest{Enabled: true, BaseURL: "https://fixture.example.test/v1", Models: DefaultModels, APIKey: "actions-test-key"}); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_ai_config WHERE updated_by=$1`, actor)
	if err = db.QueryRow(`INSERT INTO v2_ai_session(title,model,created_by,created_at,updated_at)VALUES('actions','fixture',$1,1,1)RETURNING id`, actor).Scan(&session); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_ai_session WHERE id=$1`, session)
	makeAction := func(call, kind string, id int64, typ string, payload any) Action {
		t.Helper()
		raw, _ := json.Marshal(payload)
		a, err := s.proposeAction(ctx, actor, session, call, actionSpec{Kind: kind, TargetID: id, TargetType: typ, Payload: raw, Reason: "test human approval"})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	t.Run("entry preserves UA and compare-and-swap", func(t *testing.T) {
		var id int64
		conditions := `[{"field":"ua","operator":"contains_any","values":["Forest"]}]`
		if err = db.QueryRow(`INSERT INTO v2_client_entry_user_policy(name,entry_host,conditions,created_at,updated_at)VALUES('ai-action','old.example.test',$1,1,1)RETURNING id`, conditions).Scan(&id); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(`DELETE FROM v2_client_entry_user_policy WHERE id=$1`, id)
		a := makeAction("entry-one", "entry_host", id, "policy", entryPayload{Kind: "policy", TargetID: id, Host: "new.example.test"})
		var host string
		db.QueryRow(`SELECT entry_host FROM v2_client_entry_user_policy WHERE id=$1`, id).Scan(&host)
		if host != "old.example.test" {
			t.Fatal("proposal executed")
		}
		done, err := s.ApproveAction(ctx, actor, session, a.ID)
		if err != nil || done.Status != "succeeded" || done.ApprovedBy != actor {
			t.Fatalf("approve %+v %v", done, err)
		}
		var raw string
		db.QueryRow(`SELECT entry_host,conditions FROM v2_client_entry_user_policy WHERE id=$1`, id).Scan(&host, &raw)
		if host != "new.example.test" || raw != conditions {
			t.Fatal(host, raw)
		}
		if _, err = s.ApproveAction(ctx, actor, session, a.ID); !errors.Is(err, ErrConflict) {
			t.Fatal("replay", err)
		}
		a = makeAction("entry-two", "entry_host", id, "policy", entryPayload{Kind: "policy", TargetID: id, Host: "third.example.test"})
		db.Exec(`UPDATE v2_client_entry_user_policy SET remarks='concurrent edit' WHERE id=$1`, id)
		if _, err = s.ApproveAction(ctx, actor, session, a.ID); !errors.Is(err, ErrConflict) {
			t.Fatal("stale applied", err)
		}
	})
	t.Run("node patch is narrow and typed", func(t *testing.T) {
		var id int64
		if err = db.QueryRow(`INSERT INTO v2_server_vmess(group_id,name,host,port,server_port,rate,network,"show",created_at,updated_at)VALUES('[]','ai-node','old.example.test','54101',54101,'1','tcp',1,1,1)RETURNING id`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(`DELETE FROM v2_server_vmess WHERE id=$1`, id)
		host, port, show := "new.example.test", 54102, 0
		a := makeAction("node-one", "node_change", id, "vmess", nodePayload{NodeType: "vmess", NodeID: id, Changes: nodePatch{Host: &host, Port: &port, Show: &show}})
		if _, err = s.ApproveAction(ctx, actor, session, a.ID); err != nil {
			t.Fatal(err)
		}
		var gotHost, gotPort string
		var gotServerPort, gotShow int
		if err = db.QueryRow(`SELECT host,port,server_port,"show" FROM v2_server_vmess WHERE id=$1`, id).Scan(&gotHost, &gotPort, &gotServerPort, &gotShow); err != nil || gotHost != host || gotPort != "54102" || gotServerPort != 54101 || gotShow != 0 {
			t.Fatalf("%s %s %d %d %v", gotHost, gotPort, gotServerPort, gotShow, err)
		}
	})
	t.Run("disabled approvals expiry and role revocation", func(t *testing.T) {
		var id int64
		if err := db.QueryRow(`INSERT INTO v2_client_entry_user_policy(name,entry_host,created_at,updated_at)VALUES('approval-safety','old.example.test',1,1)RETURNING id`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(`DELETE FROM v2_client_entry_user_policy WHERE id=$1`, id)
		a := makeAction("entry-disabled", "entry_host", id, "policy", entryPayload{Kind: "policy", TargetID: id, Host: "new.example.test"})
		if _, err := db.Exec(`UPDATE v2_ai_config SET enabled=FALSE WHERE id=1`); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(`UPDATE v2_ai_config SET enabled=TRUE WHERE id=1`)
		if _, err := s.ApproveAction(ctx, actor, session, a.ID); !errors.Is(err, ErrUnavailable) {
			t.Fatal("disabled AI approved", err)
		}
		rejected, err := s.RejectAction(ctx, actor, session, a.ID)
		if err != nil || rejected.Status != "rejected" || rejected.RejectedBy != actor || rejected.ApprovedBy != 0 {
			t.Fatalf("reject while disabled %+v %v", rejected, err)
		}
		db.Exec(`UPDATE v2_ai_config SET enabled=TRUE WHERE id=1`)
		a = makeAction("entry-expired", "entry_host", id, "policy", entryPayload{Kind: "policy", TargetID: id, Host: "new.example.test"})
		db.Exec(`UPDATE v2_ai_action SET expires_at=$2 WHERE id=$1`, a.ID, time.Now().Unix()-1)
		if _, err := s.ApproveAction(ctx, actor, session, a.ID); !errors.Is(err, ErrConflict) {
			t.Fatal("expired AI approved", err)
		}
		a = makeAction("entry-revoked", "entry_host", id, "policy", entryPayload{Kind: "policy", TargetID: id, Host: "new.example.test"})
		db.Exec(`UPDATE v2_user SET is_admin=0 WHERE id=$1`, actor)
		_, err = s.ApproveAction(ctx, actor, session, a.ID)
		db.Exec(`UPDATE v2_user SET is_admin=1 WHERE id=$1`, actor)
		if !errors.Is(err, ErrForbidden) {
			t.Fatal("revoked admin approved", err)
		}
		var host string
		if err = db.QueryRow(`SELECT entry_host FROM v2_client_entry_user_policy WHERE id=$1`, id).Scan(&host); err != nil || host != "old.example.test" {
			t.Fatal("rejected operations changed host", host, err)
		}
		// Simulate a process restart with a stale claim, then verify maintenance
		// repairs it without depending on visiting the action list first.
		db.Exec(`UPDATE v2_ai_action SET status='running',claimed_at=$2 WHERE id=$1`, a.ID, time.Now().Unix()-181)
		if err := s.rejectBusySSHServer(ctx, 0); err != nil {
			t.Fatal(err)
		}
		var status string
		if err := db.QueryRow(`SELECT status FROM v2_ai_action WHERE id=$1`, a.ID).Scan(&status); err != nil || status != "unknown" {
			t.Fatal("stale run not reconciled", status, err)
		}
	})
	t.Run("SSH encryption observation approval and once-only", func(t *testing.T) {
		host, port, pin, auth, execs := testSSHHost(t, false)
		observed, err := s.ProbeHostKey(ctx, actor, HostKeyRequest{Host: host, Port: port})
		if err != nil || observed.Fingerprint != pin || auth.Load() != 0 {
			t.Fatalf("probe %+v %v auth%d", observed, err, auth.Load())
		}
		saved, err := s.SaveServer(ctx, actor, ServerSaveRequest{Name: "fixture SSH", Host: host, Port: port, Username: "root", HostKeySHA256: pin, Password: "secret-test", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		defer db.Exec(`DELETE FROM v2_ai_server WHERE id=$1`, saved.ID)
		var cipher string
		db.QueryRow(`SELECT password_cipher FROM v2_ai_server WHERE id=$1`, saved.ID).Scan(&cipher)
		if cipher == "secret-test" || cipher == "" {
			t.Fatal("not encrypted")
		}
		list, err := s.ListServers(ctx, actor)
		encoded, _ := json.Marshal(list)
		if err != nil || strings.Contains(string(encoded), "secret-test") || strings.Contains(string(encoded), cipher) {
			t.Fatal("secret in projection")
		}
		a := makeAction("ssh-one", "ssh_command", saved.ID, "", sshPayload{ServerID: saved.ID, Command: "echo EXACT-command"})
		if execs.Load() != 0 {
			t.Fatal("proposal executed SSH")
		}
		var wg sync.WaitGroup
		success := make(chan error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _, e := s.ApproveAction(ctx, actor, session, a.ID); success <- e }()
		}
		wg.Wait()
		close(success)
		oks := 0
		for e := range success {
			if e == nil {
				oks++
			} else if !errors.Is(e, ErrConflict) && !errors.Is(e, ErrBusy) {
				t.Fatal(e)
			}
		}
		if oks != 1 {
			t.Fatal("two approvals", oks)
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			var status string
			db.QueryRow(`SELECT status FROM v2_ai_action WHERE id=$1`, a.ID).Scan(&status)
			if status == "succeeded" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if execs.Load() != 1 {
			t.Fatal("execution count", execs.Load())
		}
		a = makeAction("ssh-stale", "ssh_command", saved.ID, "", sshPayload{ServerID: saved.ID, Command: "echo stale"})
		_, err = s.SaveServer(ctx, actor, ServerSaveRequest{ID: saved.ID, Version: saved.Version, Name: "renamed", Host: host, Port: port, Username: "root", HostKeySHA256: pin, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.ApproveAction(ctx, actor, session, a.ID); !errors.Is(err, ErrConflict) {
			t.Fatal("stale server action", err)
		}
		_, err = s.SaveServer(ctx, actor, ServerSaveRequest{ID: saved.ID, Version: saved.Version + 1, Name: "changed-host", Host: "192.0.2.99", Port: port, Username: "root", HostKeySHA256: pin, Enabled: true})
		if !errors.Is(err, ErrValidation) {
			t.Fatal("old credential forwarded to new host", err)
		}
		// Deletion invalidates an already displayed card rather than reconnecting
		// using the payload snapshot or silently selecting another server.
		a = makeAction("ssh-deleted", "ssh_command", saved.ID, "", sshPayload{ServerID: saved.ID, Command: "echo deleted"})
		if err = s.DeleteServer(ctx, actor, saved.ID, saved.Version+1); err != nil {
			t.Fatal(err)
		}
		if _, err = s.ApproveAction(ctx, actor, session, a.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("deleted server used", err)
		}
	})
	t.Run("running SSH rejects concurrent server maintenance promptly", func(t *testing.T) {
		host, port, pin, _, execs := testSSHHost(t, true)
		saved, err := s.SaveServer(ctx, actor, ServerSaveRequest{Name: "slow SSH", Host: host, Port: port, Username: "root", HostKeySHA256: pin, Password: "secret-test", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		defer db.Exec(`DELETE FROM v2_ai_server WHERE id=$1`, saved.ID)
		a := makeAction("ssh-running", "ssh_command", saved.ID, "", sshPayload{ServerID: saved.ID, Command: "echo delayed"})
		second := makeAction("ssh-next", "ssh_command", saved.ID, "", sshPayload{ServerID: saved.ID, Command: "echo next"})
		var otherSession int64
		if err = db.QueryRow(`INSERT INTO v2_ai_session(title,model,created_by,created_at,updated_at)VALUES('other','fixture',$1,1,1)RETURNING id`, actor).Scan(&otherSession); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(`DELETE FROM v2_ai_session WHERE id=$1`, otherSession)
		if _, err = s.ApproveAction(ctx, actor, session, a.ID); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(time.Second)
		for execs.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if execs.Load() != 1 {
			t.Fatal("SSH did not start")
		}
		shortCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		if err = s.DeleteServer(shortCtx, actor, saved.ID, saved.Version); !errors.Is(err, ErrBusy) {
			t.Fatal("delete should immediately be busy", err)
		}
		if _, err = s.SaveServer(shortCtx, actor, ServerSaveRequest{ID: saved.ID, Version: saved.Version, Name: "rename", Host: host, Port: port, Username: "root", HostKeySHA256: pin, Enabled: true}); !errors.Is(err, ErrBusy) {
			t.Fatal("edit should immediately be busy", err)
		}
		if err = s.ClearSessions(shortCtx, actor); !errors.Is(err, ErrBusy) {
			t.Fatal("clear should immediately be busy", err)
		}
		if err = s.ResetData(shortCtx, actor); !errors.Is(err, ErrBusy) {
			t.Fatal("reset should immediately be busy", err)
		}
		if _, err = s.ApproveAction(shortCtx, actor, session, second.ID); !errors.Is(err, ErrBusy) {
			t.Fatal("second action should immediately be busy", err)
		}
		if err = s.DeleteSession(shortCtx, actor, otherSession); !errors.Is(err, ErrBusy) {
			t.Fatal("unrelated session delete should not wait for long SSH", err)
		}
		deadline = time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			var status string
			db.QueryRow(`SELECT status FROM v2_ai_action WHERE id=$1`, a.ID).Scan(&status)
			if status == "succeeded" {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("SSH action did not finish")
	})
	t.Run("server registration cap keeps every record manageable", func(t *testing.T) {
		_, _, pin, _, _ := testSSHHost(t, false)
		if _, err := db.Exec(`INSERT INTO v2_ai_server(actor_id,name,host,port,username,host_key_sha256,password_cipher,created_at,updated_at) SELECT $1,'cap-fixture-'||n,'192.0.2.1',22,'root',$2,'fixture',1,1 FROM generate_series(1,200) n`, actor, pin); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(`DELETE FROM v2_ai_server WHERE actor_id=$1 AND name LIKE 'cap-fixture-%'`, actor)
		if _, err := s.SaveServer(ctx, actor, ServerSaveRequest{Name: "over-cap", Host: "192.0.2.2", Port: 22, Username: "root", HostKeySHA256: pin, Password: "x", Enabled: true}); !errors.Is(err, ErrValidation) {
			t.Fatal("201st server accepted", err)
		}
	})
	t.Run("monitor proposal snapshots visible targets and invalidates changes", func(t *testing.T) {
		var id, monitor, probe int64
		if err := db.QueryRow(`INSERT INTO v2_client_entry_user_policy(name,entry_host,created_at,updated_at)VALUES('monitor-cas','old.example.test',1,1)RETURNING id`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(`DELETE FROM v2_client_entry_user_policy WHERE id=$1`, id)
		if err := db.QueryRow(`INSERT INTO v2_client_entry_monitor(policy_id,created_at,updated_at)VALUES($1,1,1)RETURNING id`, id).Scan(&monitor); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(`DELETE FROM v2_client_entry_monitor WHERE id=$1`, monitor)
		if _, err := db.Exec(`INSERT INTO v2_client_entry_monitor_target(monitor_id,source_key,host,port,auto_split_enabled,created_at,updated_at)VALUES($1,$2,'old.example.test',54101,1,1,1)`, monitor, fmt.Sprintf("policy:%d", id)); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(`DELETE FROM v2_client_entry_monitor_target WHERE monitor_id=$1`, monitor)
		if err := db.QueryRow(`INSERT INTO v2_dns_probe(name,token_hash,created_at,updated_at)VALUES('test-probe',$1,1,1)RETURNING id`, hashOperation(token)).Scan(&probe); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(`DELETE FROM v2_dns_probe WHERE id=$1`, probe)
		a := makeAction("monitor-snapshot", "monitor_run", 0, "", monitorPayload{PolicyIDs: []int64{id}})
		var p monitorPayload
		if json.Unmarshal(a.Payload, &p) != nil || len(p.Targets) != 1 || p.Targets[0].Host != "old.example.test" || !p.Targets[0].AutoSplitEnabled {
			t.Fatal("missing visible target", string(a.Payload))
		}
		if _, err := db.Exec(`UPDATE v2_client_entry_monitor_target SET port=54102 WHERE monitor_id=$1`, monitor); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ApproveAction(ctx, actor, session, a.ID); !errors.Is(err, ErrConflict) {
			t.Fatal("changed monitor target was approved", err)
		}
	})
}
