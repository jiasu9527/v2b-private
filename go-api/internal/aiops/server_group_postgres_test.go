package aiops

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNormalizeServerGroup(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		valid       bool
	}{
		{"", "", true},
		{"  东京线路  ", "东京线路", true},
		{strings.Repeat("a", 100), strings.Repeat("a", 100), true},
		{strings.Repeat("界", 101), "", false},
		{"bad\nname", "", false},
		{"bad\x00name", "", false},
	} {
		got, err := normalizeServerGroup(tc.input)
		if (err == nil) != tc.valid || (tc.valid && got != tc.want) {
			t.Fatalf("group %q -> %q, %v", tc.input, got, err)
		}
	}
}

func TestServerGroupMigrationAndCASPostgres(t *testing.T) {
	db, s := sshBatchDBFixture(t)
	ctx := context.Background()
	var id int64
	fingerprint := "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := db.QueryRow(`INSERT INTO v2_ai_server(actor_id,name,host,port,username,host_key_sha256,password_cipher,created_at,updated_at)VALUES(1,'legacy','192.0.2.40',22,'root',$1,'cipher',1,1) RETURNING id`, fingerprint).Scan(&id); err != nil {
		t.Fatal(err)
	}
	// Emulate deployment from the previous schema in this isolated fixture.
	if _, err := db.Exec(`ALTER TABLE v2_ai_server DROP COLUMN group_name; ALTER TABLE v2_ai_server_intake DROP COLUMN group_name`); err != nil {
		t.Fatal(err)
	}
	upgraded := NewService(s.cfg, db, s.panel)
	defer upgraded.Close()
	if err := upgraded.ensureSchema(ctx); err != nil {
		t.Fatal("automatic group migration", err)
	}
	all, err := upgraded.ListServers(ctx, 1)
	if err != nil || len(all) != 1 || all[0].Group != "" || !all[0].HasPassword {
		t.Fatalf("legacy server group/read broken: %+v %v", all, err)
	}
	grouped, err := upgraded.SetServerGroup(ctx, 1, id, all[0].Version, "  亚太  ")
	if err != nil || grouped.Group != "亚太" || grouped.Version != all[0].Version+1 || !grouped.HasPassword {
		t.Fatalf("group mutation did not preserve credentials: %+v %v", grouped, err)
	}
	var cipher, group string
	if err = db.QueryRow(`SELECT password_cipher,group_name FROM v2_ai_server WHERE id=$1`, id).Scan(&cipher, &group); err != nil || cipher != "cipher" || group != "亚太" {
		t.Fatal("group edit changed credentials", err, cipher, group)
	}
	// Older callers omit group when saving unrelated server fields. That must
	// not silently clear a group set through the dedicated endpoint.
	saved, err := upgraded.SaveServer(ctx, 1, ServerSaveRequest{ID: id, Version: grouped.Version, Name: "renamed", Host: "192.0.2.40", Port: 22, Username: "root", HostKeySHA256: fingerprint, Enabled: true})
	if err != nil || saved.Group != "亚太" || saved.Version != grouped.Version+1 {
		t.Fatalf("ordinary edit cleared the group: %+v %v", saved, err)
	}
	if _, err = upgraded.SetServerGroup(ctx, 1, id, all[0].Version, "overwritten"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale group update did not conflict", err)
	}
	if _, err = upgraded.SetServerGroup(ctx, 4, id, saved.Version, "unauthorized"); !errors.Is(err, ErrForbidden) {
		t.Fatal("ordinary user changed server group", err)
	}
	cleared, err := upgraded.SetServerGroup(ctx, 2, id, saved.Version, "")
	if err != nil || cleared.Group != "" || cleared.Version != saved.Version+1 {
		t.Fatal("group clear failed", cleared, err)
	}
	if _, err = upgraded.SetServerGroup(ctx, 1, id+9999, 1, "not found"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing server not classified", err)
	}
}

func TestSSHRegistrationGroupFlowsIntoIntakePostgres(t *testing.T) {
	db, s := sshBatchDBFixture(t)
	ctx := context.Background()
	session, turn := sshBatchSessionTurn(t, db, 1)
	text := "主机: 192.0.2.41\n用户名: root\n密码: group-fixture-secret\n分组:  洛杉矶  "
	_, inputs, err := parseSSHRegistrationSingle(text)
	if err != nil || len(inputs) != 1 || inputs[0].Group != "洛杉矶" {
		t.Fatal("group parser", inputs, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.storeSSHIntakesTx(ctx, tx, 1, session, turn, inputs); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var saved string
	if err = db.QueryRow(`SELECT group_name FROM v2_ai_server_intake WHERE turn_id=$1`, turn).Scan(&saved); err != nil || saved != "洛杉矶" {
		t.Fatal("intake group not persisted", err, saved)
	}
}
