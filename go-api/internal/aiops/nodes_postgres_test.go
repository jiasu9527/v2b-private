package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	platformpostgres "forest/go-api/internal/platform/postgres"
)

func TestAINodeUpdatePostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_AIOPS_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err = platformpostgres.EnsureClientEntrySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	panel := admin.NewDBService(config.Config{}, db)
	s := NewService(config.Config{AppKey: "node-fixture-key"}, db, panel)
	defer s.Close()
	if err = s.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	marker := fmt.Sprint(time.Now().UnixNano())
	var actor, session, node, group int64
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at)VALUES($1,'unused','fixture',$2,1,1,1) RETURNING id`, "node-"+marker+"@example.test", marker).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_user WHERE id=$1`, actor)
	if _, err = s.SaveConfig(ctx, actor, ConfigSaveRequest{Enabled: true, BaseURL: "https://fixture.example.test/v1", APIKey: "fixture", Models: DefaultModels}); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_ai_config WHERE updated_by=$1`, actor)
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_ai_session(title,model,created_by,created_at,updated_at)VALUES('node test','fixture',$1,1,1) RETURNING id`, actor).Scan(&session); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_ai_session WHERE id=$1`, session)
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_server_group(name,created_at,updated_at)VALUES('node-fixture',1,1) RETURNING id`).Scan(&group); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_server_group WHERE id=$1`, group)
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_server_vless(group_id,name,host,port,server_port,tls,tls_settings,network,network_settings,rate,created_at,updated_at)VALUES($1,'node-fixture','192.0.2.1',443,443,1,$2,'tcp',$3,'1',1,1) RETURNING id`, fmt.Sprintf("[%d]", group), `{"server_name":"old.test","private_key":"never-expose-node-secret","dns_env":"SECRET=keep"}`, `{"path":"/old","headers":{"Authorization":"keep-auth","Host":"old.test"}}`).Scan(&node); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_server_vless WHERE id=$1`, node)
	read, err := s.callTool(ctx, actor, session, ToolCall{ID: "node-read", Function: ToolFunction{Name: "node_detail", Arguments: fmt.Sprintf(`{"node_type":"vless","node_id":%d}`, node)}})
	if err != nil || strings.Contains(read.Content, "never-expose") || strings.Contains(read.Content, "SECRET=keep") || strings.Contains(read.Content, "keep-auth") {
		t.Fatal("detail secret", err, read.Content)
	}
	propose := func(callID, changes string) Action {
		t.Helper()
		out, e := s.callTool(ctx, actor, session, ToolCall{ID: callID, Function: ToolFunction{Name: "propose_node_update", Arguments: fmt.Sprintf(`{"node_type":"vless","node_id":%d,"changes":%s,"reason":"fixture connection update"}`, node, changes)}})
		if e != nil || !out.Pending {
			t.Fatal("proposal", e, out)
		}
		var summary struct {
			ID int64 `json:"action_id"`
		}
		if json.Unmarshal([]byte(out.Content), &summary) != nil {
			t.Fatal(out.Content)
		}
		actions, e := s.ListActions(ctx, actor, session)
		if e != nil {
			t.Fatal(e)
		}
		for _, a := range actions {
			if a.ID == summary.ID {
				return a
			}
		}
		t.Fatal("action absent")
		return Action{}
	}
	first := propose("node-update", fmt.Sprintf(`{"port":54101,"server_port":55101,"group_id":[%d],"tls_settings":{"server_name":"new.test"},"network_settings":{"path":"/new","headers":{"Host":"new.test"}}}`, group))
	if strings.Contains(string(first.Payload), "never-expose") || strings.Contains(string(first.Payload), "keep-auth") || strings.Contains(string(first.Payload), "SECRET=keep") {
		t.Fatal("approval contains private config")
	}
	var port, backend int
	var tls, network string
	if err = db.QueryRowContext(ctx, `SELECT port,server_port,tls_settings,network_settings FROM v2_server_vless WHERE id=$1`, node).Scan(&port, &backend, &tls, &network); err != nil || port != 443 {
		t.Fatal("changed before approval", err)
	}
	done, err := s.ApproveAction(ctx, actor, session, first.ID)
	if err != nil || done.Status != "succeeded" {
		t.Fatal("approve", err, done)
	}
	if err = db.QueryRowContext(ctx, `SELECT port,server_port,tls_settings,network_settings FROM v2_server_vless WHERE id=$1`, node).Scan(&port, &backend, &tls, &network); err != nil || port != 54101 || backend != 55101 || !strings.Contains(tls, "new.test") || !strings.Contains(tls, "never-expose-node-secret") || !strings.Contains(network, "keep-auth") || !strings.Contains(network, "/new") {
		t.Fatal("bad update", err, port, backend)
	}
	if _, err = s.ApproveAction(ctx, actor, session, first.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate approved", err)
	}
	stale := propose("node-stale", `{"port":54102}`)
	if _, err = db.ExecContext(ctx, `UPDATE v2_server_vless SET host='192.0.2.2' WHERE id=$1`, node); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApproveAction(ctx, actor, session, stale.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("stale approved", err)
	}
	for i, changes := range []string{`{"tls_settings":{"private_key":"bad"}}`, `{"server_port":70000}`, `{"group_id":[2147483647]}`} {
		_, err = s.callTool(ctx, actor, session, ToolCall{ID: fmt.Sprint("bad", i), Function: ToolFunction{Name: "propose_node_update", Arguments: fmt.Sprintf(`{"node_type":"vless","node_id":%d,"changes":%s,"reason":"invalid"}`, node, changes)}})
		if err == nil {
			t.Fatal("invalid accepted", changes)
		}
	}
}
