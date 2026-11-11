package newapibridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAdminConversationQueryValidationAndLiteralFilter(t *testing.T) {
	q, err := normalizeAdminConversationQuery(AdminConversationQuery{})
	if err != nil || q.Current != 1 || q.PageSize != 20 {
		t.Fatalf("defaults=%+v error=%v", q, err)
	}
	for name, input := range map[string]AdminConversationQuery{
		"negative page": {Current: -1}, "large page": {Current: 100001},
		"negative size": {PageSize: -1}, "large size": {PageSize: 101},
		"negative user": {UserID: -1}, "negative start": {StartAt: -1}, "negative end": {EndAt: -1},
		"empty window": {StartAt: 100, EndAt: 100}, "reverse window": {StartAt: 101, EndAt: 100},
		"bad email": {Email: "not-an-email"}, "email display name": {Email: "Someone <user@example.test>"},
		"long email":   {Email: strings.Repeat("a", 250) + "@a.test"},
		"unknown mode": {Mode: "any"}, "long keyword": {Keyword: strings.Repeat("字", 101)},
		"control keyword": {Keyword: "line\nnext"}, "invalid UTF8": {Keyword: string([]byte{0xff})},
		"long model": {Model: strings.Repeat("m", 201)}, "model whitespace": {Model: " model "},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeAdminConversationQuery(input); !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted %+v: %v", input, err)
			}
		})
	}
	needle := `%' OR 1=1 --_\`
	query, args := adminConversationWhere(AdminConversationQuery{UserID: 7, Email: "person@example.test", Keyword: needle, Mode: "character", Model: "model-a", StartAt: 10, EndAt: 20})
	if strings.Contains(query, needle) || len(args) != 7 || args[2] != needle || strings.Count(query, " AND ") != 7 {
		t.Fatalf("unsafe/uncombined filter: %q %#v", query, args)
	}
}

type adminNoUpstreamTransport struct{ calls atomic.Int64 }

func (tr *adminNoUpstreamTransport) RoundTrip(*http.Request) (*http.Response, error) {
	tr.calls.Add(1)
	return nil, errors.New("admin chat viewing must not contact upstream")
}
func adminChatFixture(t *testing.T) *Service {
	t.Helper()
	s := chatFixture(t)
	_, err := s.db.Exec(`ALTER TABLE v2_user ADD COLUMN email TEXT NOT NULL DEFAULT '',ADD COLUMN is_admin BIGINT NOT NULL DEFAULT 0,ADD COLUMN is_staff BIGINT NOT NULL DEFAULT 0,ADD COLUMN password TEXT NOT NULL DEFAULT 'ACCOUNT_PASSWORD_SECRET',ADD COLUMN token TEXT NOT NULL DEFAULT 'ACCOUNT_TOKEN_SECRET';
UPDATE v2_user SET email='one@example.test' WHERE id=1;
UPDATE v2_user SET email='two@example.test' WHERE id=2;
INSERT INTO v2_user(id,balance,email,is_admin,is_staff,banned) VALUES (99,0,'admin@example.test',1,0,0),(98,0,'staff@example.test',0,1,0),(97,0,'banned-admin@example.test',1,0,1);`)
	if err != nil {
		t.Fatal(err)
	}
	transport := &adminNoUpstreamTransport{}
	s.client = &http.Client{Transport: transport}
	t.Cleanup(func() {
		if transport.calls.Load() != 0 {
			t.Errorf("admin read made %d upstream requests", transport.calls.Load())
		}
	})
	return s
}
func adminSeedChat(t *testing.T, s *Service, owner int64, title, mode, model string, created, updated int64) int64 {
	t.Helper()
	var id int64
	if err := s.db.QueryRow(`INSERT INTO v2_newapi_chat(user_id,title,mode,model,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, owner, title, mode, model, created, updated).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAdminChatPostgresFilterIsolationDeletedUserAndSafeProjection(t *testing.T) {
	s := adminChatFixture(t)
	ctx := context.Background()
	literal := `literal %_' OR 1=1 --\ title`
	wanted := adminSeedChat(t, s, 1, literal, "character", "model-a", 100, 500)
	adminSeedChat(t, s, 2, literal, "character", "model-a", 100, 501)
	adminSeedChat(t, s, 1, "another title", "chat", "model-a", 100, 502)
	adminSeedChat(t, s, 1, literal, "character", "model-b", 100, 503)
	adminSeedChat(t, s, 1, literal, "character", "model-a", 200, 504)
	deleted := adminSeedChat(t, s, 2, "deleted account remains readable", "chat", "model-a", 150, 505)
	if _, err := s.db.Exec(`UPDATE v2_newapi_chat SET character_id=123,character_snapshot=$1 WHERE id=$2`, `{"name":"Frozen role","description":"snapshot body","extensions":{"password":"CARD_EXTENSION_SECRET"}}`, wanted); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO v2_newapi_chat_message(conversation_id,user_id,role,content,status,created_at,updated_at) VALUES($1,1,'user','MESSAGE_BODY_SECRET','complete',101,101),($1,1,'assistant','ok','complete',102,102),($1,2,'user','wrong-owner message','complete',103,103)`, wanted); err != nil {
		t.Fatal(err)
	}
	q := AdminConversationQuery{Current: 1, PageSize: 10, UserID: 1, Email: "one@example.test", Keyword: `%'`, Mode: "character", Model: "model-a", StartAt: 100, EndAt: 200}
	page, err := s.AdminListConversations(ctx, 99, q)
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("literal absent keyword matched: %+v %v", page, err)
	}
	q.Keyword = literal
	page, err = s.AdminListConversations(ctx, 99, q)
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != wanted {
		t.Fatalf("AND filters lost/isolation: %+v %v", page, err)
	}
	item := page.Items[0]
	if item.MessageCount != 2 || item.LastMessageAt != 102 || item.CharacterName != "Frozen role" || item.UserDeleted || item.UserEmail != "one@example.test" {
		t.Fatalf("unexpected projection: %+v", item)
	}
	raw, _ := json.Marshal(page)
	for _, forbidden := range []string{"MESSAGE_BODY_SECRET", "ACCOUNT_PASSWORD_SECRET", "ACCOUNT_TOKEN_SECRET", "CARD_EXTENSION_SECRET", "active_request", "request_hash", "persona"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("list leaked %q: %s", forbidden, raw)
		}
	}
	q.Email = "two@example.test"
	page, err = s.AdminListConversations(ctx, 99, q)
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("user/email must combine with AND: %+v %v", page, err)
	}
	if _, err = s.db.Exec(`DELETE FROM v2_user WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	page, err = s.AdminListConversations(ctx, 99, AdminConversationQuery{UserID: 2, Keyword: "deleted account"})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || !page.Items[0].UserDeleted || page.Items[0].UserEmail != "" || page.Items[0].ID != deleted {
		t.Fatalf("deleted user fallback: %+v %v", page, err)
	}
	detail, err := s.AdminGetConversation(ctx, 99, deleted)
	if err != nil || !detail.UserDeleted || detail.UserID != 2 || detail.UserEmail != "" || detail.CharacterSnapshot != nil {
		t.Fatalf("deleted user detail: %+v %v", detail, err)
	}
	page, err = s.AdminListConversations(ctx, 99, AdminConversationQuery{Current: 100, PageSize: 1})
	if err != nil || page.Total != 6 || len(page.Items) != 0 {
		t.Fatalf("empty page total: %+v %v", page, err)
	}
}

func TestAdminChatPostgresMessagePaginationFrozenSnapshotAndNoRecovery(t *testing.T) {
	s := adminChatFixture(t)
	ctx := context.Background()
	id := adminSeedChat(t, s, 1, "history", "character", "model-a", 100, 100)
	card := `{"name":"Frozen character","description":"Old description","personality":"Patient","scenario":"Old setting","system_prompt":"Role context","extensions":{"token":"TOP_LEVEL_CARD_SECRET"},"character_book":{"name":"Lore","extensions":{"token":"BOOK_SECRET"},"entries":[{"content":"Remembered lore","keys":["place"],"enabled":true,"extensions":{"password":"ENTRY_SECRET"}}]},"password":"UNKNOWN_JSON_SECRET"}`
	if _, err := s.db.Exec(`UPDATE v2_newapi_chat SET character_id=123,character_snapshot=$1,persona='adult reader',active_request='stale-request',active_until=1 WHERE id=$2`, card, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO v2_newapi_chat_request(user_id,client_request_id,request_hash,conversation_id,user_message_id,assistant_message_id,status,created_at,updated_at) VALUES(1,'stale-request','REQUEST_HASH_SECRET',$1,0,0,'streaming',1,1)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO v2_newapi_chat_message(conversation_id,user_id,role,content,status,client_request_id,created_at,updated_at)
SELECT $1,1,CASE WHEN n%2=0 THEN 'assistant' ELSE 'user' END,'message-'||n,CASE WHEN n=237 THEN 'streaming' ELSE 'complete' END,'stale-request',100+n,100+n FROM generate_series(1,237) n`, id); err != nil {
		t.Fatal(err)
	}
	detail, err := s.AdminGetConversation(ctx, 99, id)
	if err != nil || len(detail.Messages) != 100 || !detail.HasMore || detail.Messages[0].Content != "message-138" || detail.Messages[99].Content != "message-237" || detail.Messages[99].Status != "streaming" {
		t.Fatalf("latest 100 ascending: count=%d more=%v error=%v", len(detail.Messages), detail.HasMore, err)
	}
	if detail.CharacterSnapshot == nil || detail.CharacterSnapshot.Name != "Frozen character" || detail.CharacterSnapshot.CharacterBook.Entries[0].Content != "Remembered lore" || detail.Conversation.Persona != "adult reader" {
		t.Fatalf("missing frozen context: %+v", detail)
	}
	raw, _ := json.Marshal(detail)
	for _, secret := range []string{"TOP_LEVEL_CARD_SECRET", "BOOK_SECRET", "ENTRY_SECRET", "UNKNOWN_JSON_SECRET", "ACCOUNT_PASSWORD_SECRET", "ACCOUNT_TOKEN_SECRET", "REQUEST_HASH_SECRET", "active_until"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("detail leaked %q", secret)
		}
	}
	seen := map[int64]bool{}
	for _, m := range detail.Messages {
		seen[m.ID] = true
	}
	cursor := detail.NextBeforeID
	for {
		page, err := s.AdminGetConversationMessages(ctx, 99, id, cursor, 60)
		if err != nil {
			t.Fatal(err)
		}
		for i, m := range page.Items {
			if seen[m.ID] || m.ID >= cursor || (i > 0 && page.Items[i-1].ID >= m.ID) {
				t.Fatalf("duplicate/out-of-order page: %+v", page)
			}
			seen[m.ID] = true
		}
		if len(page.Items) > 0 && page.NextBeforeID != page.Items[0].ID {
			t.Fatalf("cursor must be earliest returned ID: %+v", page)
		}
		if !page.HasMore {
			break
		}
		cursor = page.NextBeforeID
	}
	if len(seen) != 237 {
		t.Fatalf("lost messages: %d", len(seen))
	}
	var active, requestState, lastState string
	var until int64
	if err = s.db.QueryRow(`SELECT active_request,active_until FROM v2_newapi_chat WHERE id=$1`, id).Scan(&active, &until); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT status FROM v2_newapi_chat_request WHERE conversation_id=$1`, id).Scan(&requestState); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT status FROM v2_newapi_chat_message WHERE conversation_id=$1 ORDER BY id DESC LIMIT 1`, id).Scan(&lastState); err != nil {
		t.Fatal(err)
	}
	if active != "stale-request" || until != 1 || requestState != "streaming" || lastState != "streaming" {
		t.Fatalf("read recovered/mutated state: %s %d %s %s", active, until, requestState, lastState)
	}
}

func TestAdminChatPostgresForbiddenActorsFreshRevocationAndInvalidPaging(t *testing.T) {
	s := adminChatFixture(t)
	ctx := context.Background()
	id := adminSeedChat(t, s, 1, "private", "chat", "fixture-model", 1, 1)
	for _, actor := range []int64{0, -1, 1, 2, 98, 97, 999} {
		if _, err := s.AdminListConversations(ctx, actor, AdminConversationQuery{}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("actor %d list accepted: %v", actor, err)
		}
		if _, err := s.AdminGetConversation(ctx, actor, id); !errors.Is(err, ErrForbidden) {
			t.Fatalf("actor %d detail accepted: %v", actor, err)
		}
		if _, err := s.AdminGetConversationMessages(ctx, actor, id, 0, 100); !errors.Is(err, ErrForbidden) {
			t.Fatalf("actor %d messages accepted: %v", actor, err)
		}
	}
	if _, err := s.AdminGetConversation(ctx, 99, id+1000); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing detail: %v", err)
	}
	if _, err := s.AdminGetConversationMessages(ctx, 99, id+1000, 0, 100); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing messages: %v", err)
	}
	for _, invalid := range [][2]int64{{-1, 100}, {0, 0}, {0, 201}, {0, -1}} {
		if _, err := s.AdminGetConversationMessages(ctx, 99, id, invalid[0], int(invalid[1])); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid paging %v accepted: %v", invalid, err)
		}
	}
	if _, err := s.AdminGetConversation(ctx, 99, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE v2_user SET is_admin=0 WHERE id=99`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdminGetConversation(ctx, 99, id); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked admin accepted: %v", err)
	}
}

func TestAdminChatPostgresReadSnapshotAcrossConcurrentInsert(t *testing.T) {
	s := adminChatFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id := adminSeedChat(t, s, 1, "before snapshot", "chat", "model-a", 1, 1)
	blocker, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err = blocker.ExecContext(ctx, `LOCK TABLE v2_newapi_chat IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		page AdminConversationPage
		err  error
	}
	results := make(chan result, 1)
	go func() {
		page, err := s.AdminListConversations(ctx, 99, AdminConversationQuery{})
		results <- result{page, err}
	}()
	// The actor read establishes the snapshot before COUNT waits on the table
	// lock. Commit an insert while COUNT is blocked, then release the lock.
	for {
		var waiting bool
		err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation='v2_newapi_chat'::regclass AND mode='AccessShareLock' AND NOT granted)`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case out := <-results:
			t.Fatalf("list finished before blocking: %+v %v", out.page, out.err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err = blocker.ExecContext(ctx, `INSERT INTO v2_newapi_chat(user_id,title,mode,model,created_at,updated_at) VALUES(1,'after snapshot','chat','model-a',2,2)`); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	out := <-results
	if out.err != nil || out.page.Total != 1 || len(out.page.Items) != 1 || out.page.Items[0].ID != id {
		t.Fatalf("mixed snapshot: %+v %v", out.page, out.err)
	}
	page, err := s.AdminListConversations(ctx, 99, AdminConversationQuery{})
	if err != nil || page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("new request missing commit: %+v %v", page, err)
	}
}

func TestAdminConversationWhereParameterOrder(t *testing.T) {
	where, args := adminConversationWhere(AdminConversationQuery{UserID: 3, Email: "a@example.test", Keyword: "leaf", Mode: "chat", Model: "model-a", StartAt: 11, EndAt: 22})
	want := []any{int64(3), "a@example.test", "leaf", "chat", "model-a", int64(11), int64(22)}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args=%#v", args)
	}
	for i := range args {
		if !strings.Contains(where, fmt.Sprintf("$%d", i+1)) {
			t.Fatalf("missing placeholder %d: %s", i+1, where)
		}
	}
}

func TestAdminChatPostgresDeletedConversationUnavailable(t *testing.T) {
	s := adminChatFixture(t)
	ctx := context.Background()
	id := adminSeedChat(t, s, 1, "remove this conversation", "chat", "model-a", 1, 1)
	if _, err := s.db.Exec(`INSERT INTO v2_newapi_chat_message(conversation_id,user_id,role,content,status,created_at,updated_at) VALUES($1,1,'user','body must not be retained','complete',1,1)`, id); err != nil {
		t.Fatal(err)
	}
	// Exercise the unchanged user deletion path; admin viewing must not create
	// archives, retain bodies, or turn its receipt into a restoration endpoint.
	if err := s.DeleteConversation(ctx, 1, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdminGetConversation(ctx, 99, id); !errors.Is(err, ErrForbidden) {
		t.Fatalf("deleted conversation exposed: %v", err)
	}
	if _, err := s.AdminGetConversationMessages(ctx, 99, id, 0, 100); !errors.Is(err, ErrForbidden) {
		t.Fatalf("deleted messages exposed: %v", err)
	}
	page, err := s.AdminListConversations(ctx, 99, AdminConversationQuery{})
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("deleted conversation in list: %+v %v", page, err)
	}
	var messages int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM v2_newapi_chat_message WHERE conversation_id=$1`, id).Scan(&messages); err != nil || messages != 0 {
		t.Fatalf("deleted bodies remain: count=%d error=%v", messages, err)
	}
}
