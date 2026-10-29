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

func TestAIEntryRuleEditPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_AIOPS_TEST_DSN")
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
	s := NewService(config.Config{AppKey: "entry-rule-fixture"}, db, admin.NewDBService(config.Config{}, db))
	defer s.Close()
	if err = s.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var actor, session, node, policy, split, leaf, collection int64
	marker := fmt.Sprint(time.Now().UnixNano())
	if err = db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at)VALUES($1,'unused','fixture',$2,1,1,1) RETURNING id`, "entry-rule-"+marker+"@example.test", marker).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_user WHERE id=$1`, actor)
	if _, err = s.SaveConfig(ctx, actor, ConfigSaveRequest{Enabled: true, BaseURL: "https://fixture.example.test/v1", APIKey: "fixture", Models: DefaultModels}); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_ai_config WHERE updated_by=$1`, actor)
	if err = db.QueryRow(`INSERT INTO v2_ai_session(title,model,created_by,created_at,updated_at)VALUES('entry fixture','fixture',$1,1,1)RETURNING id`, actor).Scan(&session); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_ai_session WHERE id=$1`, session)
	if err = db.QueryRow(`INSERT INTO v2_server_vmess(group_id,name,host,port,server_port,rate,network,created_at,updated_at)VALUES('[]','entry fixture','192.0.2.10','443',443,'1','tcp',1,1)RETURNING id`).Scan(&node); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_server_vmess WHERE id=$1`, node)
	values := []string{}
	for i := 0; i < 83; i++ {
		values = append(values, fmt.Sprintf("fixture-%d@example.test", i))
	}
	valuesRaw, _ := json.Marshal(values)
	conditions := `[{"field":"email","operator":"in","values":` + string(valuesRaw) + `},{"field":"ua","operator":"contains_any","values":["Forest"]}]`
	secret := `["vmess://MUST_NOT_EXPOSE_EXTRA_NODE"]`
	if err = db.QueryRow(`INSERT INTO v2_client_entry_user_policy(name,entry_host,conditions,extra_nodes,created_at,updated_at)VALUES('fixture','old.example.test',$1,$2,1,1)RETURNING id`, conditions, secret).Scan(&policy); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_client_entry_user_policy WHERE id=$1`, policy)
	defer db.Exec(`DELETE FROM v2_client_entry_user_policy_member WHERE policy_id=$1`, policy)
	if _, err = db.Exec(`INSERT INTO v2_client_entry_user_policy_member(policy_id,server_type,server_id,sort,created_at,updated_at)VALUES($1,'vmess',$2,10,1,1)`, policy, node); err != nil {
		t.Fatal(err)
	}
	detail := func(kind string, id int64, section string, offset int) string {
		t.Helper()
		args, _ := json.Marshal(map[string]any{"kind": kind, "target_id": id, "section": section, "offset": offset})
		out, e := s.callEntryRuleTool(ctx, actor, session, ToolCall{Function: ToolFunction{Name: "entry_rule_detail", Arguments: string(args)}})
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(out.Content, "MUST_NOT_EXPOSE") {
			t.Fatal("secret projection")
		}
		return out.Content
	}
	revision := func(kind string, id int64) string {
		var out struct {
			Rule struct {
				Revision string `json:"revision"`
			} `json:"rule"`
		}
		json.Unmarshal([]byte(detail(kind, id, "summary", 0)), &out)
		return out.Rule.Revision
	}
	propose := func(call, kind string, id int64, changes string) (Action, error) {
		raw := json.RawMessage(changes)
		var parsed map[string]json.RawMessage
		json.Unmarshal(raw, &parsed)
		payload, _ := json.Marshal(entryRuleUpdatePayload{Kind: kind, TargetID: id, Revision: revision(kind, id), Changes: parsed})
		return s.proposeAction(ctx, actor, session, call, actionSpec{Kind: "entry_rule_update", TargetID: id, TargetType: kind, Payload: payload, Reason: "fixture"})
	}
	if got := detail("policy", policy, "conditions", 0); !strings.Contains(got, `"values_truncated":true`) || !strings.Contains(got, `"values_count":83`) {
		t.Fatal("large condition must explicitly identify truncated values", got)
	}
	if got := detail("policy", policy, "condition_values", 20); !strings.Contains(got, "fixture-20@example.test") || !strings.Contains(got, `"next_offset":40`) {
		t.Fatal("condition value paging", got)
	}
	a, err := propose("rename", "policy", policy, `{"name":"renamed","entry_host":"new.example.test","sort":5}`)
	if err != nil {
		t.Fatal(err)
	}
	var name, actualConditions, extra string
	db.QueryRow(`SELECT name,conditions,extra_nodes FROM v2_client_entry_user_policy WHERE id=$1`, policy).Scan(&name, &actualConditions, &extra)
	if name != "fixture" {
		t.Fatal("unapproved change")
	}
	if _, err = s.ApproveAction(ctx, actor, session, a.ID); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT name,conditions,extra_nodes FROM v2_client_entry_user_policy WHERE id=$1`, policy).Scan(&name, &actualConditions, &extra)
	if name != "renamed" || actualConditions != conditions || extra != secret {
		t.Fatal("omitted fields were lost", name, actualConditions, extra)
	}
	if _, err = s.ApproveAction(ctx, actor, session, a.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("replay", err)
	}
	stale, err := propose("stale", "policy", policy, `{"remarks":"old draft"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE v2_client_entry_user_policy SET conditions='[]' WHERE id=$1`, policy); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApproveAction(ctx, actor, session, stale.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("stale rules must conflict", err)
	}
	changed, err := propose("conditions", "policy", policy, fmt.Sprintf(`{"conditions":[{"field":"user_id","operator":"between","min":1,"max":1000},{"field":"ua","operator":"contains_any","values":["Forest","Clash"]}],"members":[{"server_type":"vmess","server_id":%d}],"resolve_entry_host":1}`, node))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApproveAction(ctx, actor, session, changed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = propose("invalid", "policy", policy, `{"conditions":[{"field":"group_id","operator":"eq","value":1}]}`); !errors.Is(err, ErrValidation) {
		t.Fatal("unsupported condition", err)
	}
	if _, err = propose("invalidmember", "policy", policy, `{"members":[{"server_type":"vmess","server_id":2147483647}]}`); !errors.Is(err, ErrValidation) {
		t.Fatal("missing node", err)
	}
	hide, err := propose("hide", "policy", policy, `{"action":"hide"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hide.Payload), `"entry_host": ""`) && !strings.Contains(string(hide.Payload), `"entry_host":""`) {
		t.Fatal("implicit clearing absent in approval")
	}
	if _, err = s.ApproveAction(ctx, actor, session, hide.ID); err != nil {
		t.Fatal(err)
	}
	var host string
	var resolve int
	if err = db.QueryRow(`SELECT entry_host,resolve_entry_host FROM v2_client_entry_user_policy WHERE id=$1`, policy).Scan(&host, &resolve); err != nil || host != "" || resolve != 0 {
		t.Fatal("hide did not clear override", host, resolve, err)
	}
	if _, err = db.Exec(`UPDATE v2_client_entry_user_policy SET action='override',entry_host='shared.example.test',resolve_entry_host=1 WHERE id=$1`, policy); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`INSERT INTO v2_client_entry_collection(name,entry_host,resolve_entry_host,created_at,updated_at)VALUES('fixture','shared.example.test',1,1,1)RETURNING id`).Scan(&collection); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_client_entry_collection WHERE id=$1`, collection)
	if _, err = db.Exec(`INSERT INTO v2_client_entry_collection_member(collection_id,policy_id)VALUES($1,$2)`, collection, policy); err != nil {
		t.Fatal(err)
	}
	if _, err = propose("collection-host", "policy", policy, `{"entry_host":"break.example.test"}`); !errors.Is(err, ErrConflict) {
		t.Fatal("collection invariant", err)
	}
	safe, err := propose("collection-name", "policy", policy, `{"name":"allowed rename"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApproveAction(ctx, actor, session, safe.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`INSERT INTO v2_client_entry_user_policy(name,mode,entry_host,conditions,created_at,updated_at)VALUES('split','split','', $1,1,1)RETURNING id`, conditions).Scan(&split); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_client_entry_user_policy WHERE id=$1`, split)
	defer db.Exec(`DELETE FROM v2_client_entry_user_policy_member WHERE policy_id=$1`, split)
	if _, err = db.Exec(`INSERT INTO v2_client_entry_user_policy_member(policy_id,server_type,server_id,sort,created_at,updated_at)VALUES($1,'vmess',$2,10,1,1)`, split, node); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`INSERT INTO v2_client_entry_user_policy_split_group(policy_id,name,path,entry_host,sort,global_sort,created_at,updated_at)VALUES($1,'leaf','0','leaf.example.test',10,10,1,1)RETURNING id`, split).Scan(&leaf); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO v2_client_entry_user_policy_split_assignment(policy_id,user_id,group_id,created_at,updated_at)VALUES($1,$2,$3,1,1)`, split, actor, leaf); err != nil {
		t.Fatal(err)
	}
	if _, err = propose("splitcondition", "split_group", leaf, `{"conditions":[]}`); !errors.Is(err, ErrValidation) {
		t.Fatal("split source condition immutable", err)
	}
	splitAction, err := propose("splitedit", "split_group", leaf, `{"name":"leaf renamed","enabled":0,"sort":4,"resolve_entry_host":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(splitAction.Payload), `"shared_fields": ["enabled"]`) && !strings.Contains(string(splitAction.Payload), `"shared_fields":["enabled"]`) {
		t.Fatal("shared change not called out", string(splitAction.Payload))
	}
	if _, err = s.ApproveAction(ctx, actor, session, splitAction.ID); err != nil {
		t.Fatal(err)
	}
	var count, enabled int
	if err = db.QueryRow(`SELECT enabled,conditions FROM v2_client_entry_user_policy WHERE id=$1`, split).Scan(&enabled, &actualConditions); err != nil || enabled != 0 || actualConditions != conditions {
		t.Fatal("split shared edits/source", err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1`, split).Scan(&count); err != nil || count != 1 {
		t.Fatal("split membership lost", err)
	}
}
