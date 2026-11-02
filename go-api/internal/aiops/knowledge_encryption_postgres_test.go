package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestKnowledgePostgresEncryptedBodyAndAuthorization(t *testing.T) {
	f := setupKnowledgeFixture(t)
	ctx := context.Background()
	const secret = "fixture-install-key-2914"
	req := knowledgeFixtureRequest()
	req.Content = "\n# 部署脚本\ncurl https://example.test/install.sh | bash -s -- --key '" + secret + "' --port 54101\n"
	req.Title, req.Summary, req.Pinned = "部署 key="+secret, "key="+secret, true
	entry, err := f.s.SaveKnowledge(ctx, f.admin, req)
	if err != nil || entry.Content != req.Content || !entry.ContainsCredentials {
		t.Fatalf("encrypted save failed: %v", err)
	}
	var index, cipher, title, summary string
	var sensitive bool
	if err = f.db.QueryRow(`SELECT content,content_cipher,title,summary,contains_credentials FROM v2_ai_knowledge WHERE id=$1`, entry.ID).Scan(&index, &cipher, &title, &summary, &sensitive); err != nil {
		t.Fatal(err)
	}
	if !sensitive || !strings.HasPrefix(cipher, "v1:") || strings.Contains(index+cipher+title+summary, secret) || !strings.Contains(index, "--port 54101") {
		t.Fatal("plaintext secret leaked or searchable script missing")
	}
	for _, actor := range []int64{f.admin, f.staff, f.other} {
		got, readErr := f.s.GetKnowledge(ctx, actor, entry.ID)
		if readErr != nil || got.Content != req.Content || !got.ContainsCredentials {
			t.Fatalf("authorized read did not restore body: %v", readErr)
		}
	}
	list, err := f.s.ListKnowledge(ctx, f.staff, KnowledgeListRequest{Query: "54101"})
	if err != nil || list.Total != 1 || list.Data[0].Content != "" || !list.Data[0].ContainsCredentials {
		t.Fatalf("search index failed: %#v %v", list, err)
	}
	list, err = f.s.ListKnowledge(ctx, f.admin, KnowledgeListRequest{Query: secret})
	if err != nil || list.Total != 0 {
		t.Fatal("secret indexed", err)
	}
	messages, err := f.s.refreshKnowledgeContext(ctx, nil)
	if err != nil || len(messages) != 1 || strings.Contains(messages[0].Content, secret) || strings.Contains(messages[0].Content, "--port") || !strings.Contains(messages[0].Content, `"contains_credentials":true`) {
		t.Fatal("sensitive pinned body automatically included", err)
	}
	out, err := f.s.callKnowledgeTool(ctx, f.staff, 0, ToolCall{Function: ToolFunction{Name: "knowledge_get", Arguments: `{"id":` + intString(entry.ID) + `}`}})
	if err != nil || !strings.Contains(out.Content, secret) || !strings.Contains(out.Content, `"contains_credentials":true`) {
		t.Fatal("model on-demand body unavailable", err)
	}

	draft, err := f.s.SaveKnowledge(ctx, f.staff, req)
	if err != nil || draft.Status != "draft" || draft.Pinned || draft.Content != req.Content {
		t.Fatal("staff secret draft failed", err)
	}
	if _, err = f.s.GetKnowledge(ctx, f.other, draft.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign draft exposed", err)
	}
	req.ID, req.ExpectedVersion = draft.ID, &draft.Version
	if _, err = f.s.SaveKnowledge(ctx, f.staff, req); !errors.Is(err, ErrForbidden) {
		t.Fatal("staff edited sensitive draft", err)
	}
	if err = f.s.DeleteKnowledge(ctx, f.staff, draft.ID, draft.Version); !errors.Is(err, ErrForbidden) {
		t.Fatal("staff deleted sensitive draft", err)
	}

	req.ID, req.ExpectedVersion = entry.ID, &entry.Version
	req.Content = "# Updated\nAPI_KEY=fixture-replaced-key"
	updated, err := f.s.SaveKnowledge(ctx, f.admin, req)
	if err != nil || updated.Version != 2 || updated.Content != req.Content || !updated.ContainsCredentials {
		t.Fatal("encrypted update failed", err)
	}
	if _, err = f.s.SaveKnowledge(ctx, f.admin, req); !errors.Is(err, ErrConflict) {
		t.Fatal("version conflict lost", err)
	}
	if _, err = f.db.Exec(`UPDATE v2_ai_knowledge SET content_cipher='v1:broken' WHERE id=$1`, updated.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.GetKnowledge(ctx, f.admin, updated.ID); !errors.Is(err, ErrUnavailable) {
		t.Fatal("corrupt cipher fell back to incomplete plaintext", err)
	}
}

func intString(id int64) string { raw, _ := json.Marshal(id); return string(raw) }

func TestKnowledgePostgresLegacyPlaintextAndEncryptedDedupe(t *testing.T) {
	f := setupKnowledgeFixture(t)
	ctx := context.Background()
	const body = "legacy architecture port 54101"
	var id int64
	if err := f.db.QueryRow(`INSERT INTO v2_ai_knowledge(title,summary,content,category,status,pinned,source_kind,created_by,updated_by,created_at,updated_at)VALUES('legacy','', $1,'architecture','active',true,'manual',$2,$2,1,1) RETURNING id`, body, f.admin).Scan(&id); err != nil {
		t.Fatal(err)
	}
	entry, err := f.s.GetKnowledge(ctx, f.staff, id)
	if err != nil || entry.Content != body || entry.ContainsCredentials {
		t.Fatal("legacy body unavailable", err)
	}
	contextMessages, err := f.s.refreshKnowledgeContext(ctx, nil)
	if err != nil || len(contextMessages) != 1 || !strings.Contains(contextMessages[0].Content, body) {
		t.Fatal("legacy pinned body unavailable", err)
	}
	req := knowledgeFixtureRequest()
	req.ID, req.ExpectedVersion, req.Content = entry.ID, &entry.Version, body
	entry, err = f.s.SaveKnowledge(ctx, f.admin, req)
	if err != nil || entry.Content != body || entry.contentCipher == "" {
		t.Fatal("legacy update not encrypted", err)
	}

	session, turn := knowledgeRunningTurn(t, f, f.admin)
	req.ID, req.ExpectedVersion, req.Content = 0, nil, "TOKEN=fixture-dedupe-key"
	first, err := f.s.SaveKnowledgeFromAI(ctx, f.admin, session, turn, req, "安装说明 key=fixture-source-key")
	if err != nil || strings.Contains(first.SourceNote, "fixture-source-key") {
		t.Fatal("AI source metadata not protected", err)
	}
	again, err := f.s.SaveKnowledgeFromAI(ctx, f.admin, session, turn, req, "安装说明 key=fixture-source-key")
	if err != nil || first.ID != again.ID || again.Content != req.Content || first.contentCipher != again.contentCipher {
		t.Fatal("encrypted create deduplication failed", err)
	}
	oldKey := f.s.cfg.AppKey
	f.s.cfg.AppKey = "incorrect-fixture-key"
	if _, err = f.s.GetKnowledge(ctx, f.admin, first.ID); !errors.Is(err, ErrUnavailable) {
		t.Fatal("wrong app key silently used index", err)
	}
	f.s.cfg.AppKey = oldKey
}
