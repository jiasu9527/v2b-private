package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// This fixture uses sshBatchDBFixture's per-test PostgreSQL schema. It never
// mutates registered servers or users outside the disposable test database.
func TestProposeServerGroupToolPostgres(t *testing.T) {
	db, s := sshBatchDBFixture(t)
	ctx := context.Background()
	if _, err := s.SaveConfig(ctx, 1, ConfigSaveRequest{Enabled: true, BaseURL: "https://fixture.example.test/v1", APIKey: "fixture-relay", Models: DefaultModels}); err != nil {
		t.Fatal(err)
	}
	session, _ := sshBatchSessionTurn(t, db, 2)
	cipher, err := s.encryptSecret("private-fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	const pin = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := db.QueryRow(`INSERT INTO v2_ai_server(actor_id,name,host,port,username,password_cipher,host_key_sha256,created_at,updated_at)
		VALUES(1,'fixture server','192.0.2.75',22,'root',$1,$2,1,1) RETURNING id`, cipher, pin).Scan(&id); err != nil {
		t.Fatal(err)
	}
	server, err := scanServer(db.QueryRow(`SELECT `+serverColumns+` FROM v2_ai_server WHERE id=$1`, id))
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(actor, sessionID int64, callID string, args any) (ToolOutcome, error) {
		raw, _ := json.Marshal(args)
		return s.callTool(ctx, actor, sessionID, ToolCall{ID: callID, Function: ToolFunction{Name: "propose_server_group", Arguments: string(raw)}})
	}
	args := map[string]any{"server_id": id, "version": server.Version, "group": "  亚洲线路  ", "reason": "fixture: classify registered server"}
	out, err := invoke(2, session, "server-group-first", args)
	if err != nil || !out.Pending || out.ActionID <= 0 {
		t.Fatalf("tool did not create reviewable action: %+v %v", out, err)
	}
	var before string
	if err = db.QueryRow(`SELECT group_name FROM v2_ai_server WHERE id=$1`, id).Scan(&before); err != nil || before != "" {
		t.Fatalf("proposal mutated server before approval: %q %v", before, err)
	}
	if strings.Contains(out.Content, "private-fixture-password") || strings.Contains(out.Content, cipher) {
		t.Fatal("tool result exposed a credential")
	}
	action, err := scanAction(db.QueryRow(`SELECT `+actionColumns+` FROM v2_ai_action WHERE id=$1`, out.ActionID))
	if err != nil || action.Kind != "server_group_set" || action.TargetID != id || action.TargetType != "server" || strings.Contains(string(action.Payload), cipher) || strings.Contains(string(action.Payload), "private-fixture-password") {
		t.Fatalf("bad action payload/target: %+v %v", action, err)
	}
	done, err := s.ApproveAction(ctx, 2, session, action.ID)
	if err != nil || done.Status != "succeeded" {
		t.Fatalf("approved group update failed: %+v %v", done, err)
	}
	var group, savedCipher string
	var version int64
	if err = db.QueryRow(`SELECT group_name,version,password_cipher FROM v2_ai_server WHERE id=$1`, id).Scan(&group, &version, &savedCipher); err != nil || group != "亚洲线路" || version != server.Version+1 || savedCipher != cipher {
		t.Fatalf("group update changed identity/credentials: group=%q version=%d cipher_preserved=%v err=%v", group, version, savedCipher == cipher, err)
	}
	if _, err = s.ApproveAction(ctx, 2, session, action.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("approved action replay was accepted: %v", err)
	}
	if _, err = invoke(2, session, "server-group-stale", args); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale server version was accepted: %v", err)
	}
	clear := map[string]any{"server_id": id, "version": version, "group": "", "reason": "fixture: remove grouping"}
	out, err = invoke(2, session, "server-group-clear", clear)
	if err != nil || !out.Pending {
		t.Fatalf("clear group proposal failed: %+v %v", out, err)
	}
	if _, err = s.ApproveAction(ctx, 2, session, out.ActionID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT group_name FROM v2_ai_server WHERE id=$1`, id).Scan(&group); err != nil || group != "" {
		t.Fatalf("group was not cleared: %q %v", group, err)
	}
	var latestVersion int64
	if err = db.QueryRow(`SELECT version FROM v2_ai_server WHERE id=$1`, id).Scan(&latestVersion); err != nil {
		t.Fatal(err)
	}
	concurrent := map[string]any{"server_id": id, "version": latestVersion, "group": "香港线路", "reason": "fixture: check concurrent edit"}
	out, err = invoke(2, session, "server-group-concurrent", concurrent)
	if err != nil || !out.Pending {
		t.Fatalf("concurrent proposal failed: %+v %v", out, err)
	}
	if _, err = s.SetServerGroup(ctx, 1, id, latestVersion, "管理员修改"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApproveAction(ctx, 2, session, out.ActionID); !errors.Is(err, ErrConflict) {
		t.Fatalf("approval overwrote concurrent group edit: %v", err)
	}
	if err = db.QueryRow(`SELECT group_name FROM v2_ai_server WHERE id=$1`, id).Scan(&group); err != nil || group != "管理员修改" {
		t.Fatalf("concurrent group edit overwritten: %q %v", group, err)
	}
}

func TestProposeServerGroupToolRejectsBadRequestsPostgres(t *testing.T) {
	db, s := sshBatchDBFixture(t)
	ctx := context.Background()
	session, _ := sshBatchSessionTurn(t, db, 2)
	outsiderSession, _ := sshBatchSessionTurn(t, db, 4)
	var id int64
	if err := db.QueryRow(`INSERT INTO v2_ai_server(actor_id,name,host,port,username,host_key_sha256,created_at,updated_at)
		VALUES(1,'fixture server','192.0.2.76',22,'root','SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA',1,1) RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for index, tc := range []struct {
		name      string
		actor     int64
		sessionID int64
		args      map[string]any
		want      error
	}{
		{"unknown argument", 2, session, map[string]any{"server_id": id, "version": 1, "group": "test", "reason": "fixture", "password": "not-allowed"}, ErrValidation},
		{"missing version", 2, session, map[string]any{"server_id": id, "group": "test", "reason": "fixture"}, ErrValidation},
		{"missing group", 2, session, map[string]any{"server_id": id, "version": 1, "reason": "fixture"}, ErrValidation},
		{"missing reason", 2, session, map[string]any{"server_id": id, "version": 1, "group": "test"}, ErrValidation},
		{"long group", 2, session, map[string]any{"server_id": id, "version": 1, "group": strings.Repeat("界", 101), "reason": "fixture"}, ErrValidation},
		{"missing target", 2, session, map[string]any{"server_id": id + 1000, "version": 1, "group": "test", "reason": "fixture"}, ErrNotFound},
		{"nonoperator", 4, outsiderSession, map[string]any{"server_id": id, "version": 1, "group": "test", "reason": "fixture"}, ErrForbidden},
	} {
		raw, _ := json.Marshal(tc.args)
		_, err := s.callTool(ctx, tc.actor, tc.sessionID, ToolCall{ID: fmt.Sprintf("group-bad-%d", index), Function: ToolFunction{Name: "propose_server_group", Arguments: string(raw)}})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM v2_ai_action`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid group requests created actions: count=%d err=%v", count, err)
	}
}
