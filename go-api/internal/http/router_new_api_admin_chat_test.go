package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"forest/go-api/internal/config"
	"forest/go-api/internal/newapibridge"
	"forest/go-api/internal/session"
)

type adminChatHTTPFixture struct {
	newAPIHTTPFixture
	chatCalls                 int
	chatActor, chatID, before int64
	pageSize                  int
	chatQuery                 newapibridge.AdminConversationQuery
	chatErr                   error
}

func (f *adminChatHTTPFixture) AdminListConversations(_ context.Context, actor int64, q newapibridge.AdminConversationQuery) (newapibridge.AdminConversationPage, error) {
	f.chatCalls++
	f.chatActor = actor
	f.chatQuery = q
	return newapibridge.AdminConversationPage{}, f.chatErr
}
func (f *adminChatHTTPFixture) AdminGetConversation(_ context.Context, actor, id int64) (newapibridge.AdminConversationDetail, error) {
	f.chatCalls++
	f.chatActor, f.chatID = actor, id
	return newapibridge.AdminConversationDetail{}, f.chatErr
}
func (f *adminChatHTTPFixture) AdminGetConversationMessages(_ context.Context, actor, id, before int64, size int) (newapibridge.MessagePage, error) {
	f.chatCalls++
	f.chatActor, f.chatID, f.before, f.pageSize = actor, id, before, size
	return newapibridge.MessagePage{Items: []newapibridge.ChatMessage{}}, f.chatErr
}

func adminChatRouter(f *adminChatHTTPFixture, sessions *newAPIFreshSession) http.Handler {
	return NewRouter(config.Config{AdminPath: "review-admin"}, WithSessionService(sessions), WithNewAPIBridge(f))
}

func TestNewAPIAdminChatFreshAdminOnlyAndNoUserRoute(t *testing.T) {
	paths := []string{"conversations", "conversations/detail?id=9", "conversations/messages?id=9"}
	for _, path := range paths {
		for _, identity := range []*session.Identity{nil, {ID: 7}, {ID: 7, IsStaff: 1}, {ID: 7, IsAdmin: 1}} {
			f := &adminChatHTTPFixture{}
			s := &newAPIFreshSession{identity: identity}
			router := adminChatRouter(f, s)
			w := newAPIHTTPCall(t, router, "GET", "/api/v1/review-admin/new-api/"+path, "", "panel-auth")
			if identity == nil || identity.IsAdmin == 0 {
				if w.Code < 400 || f.chatCalls != 0 {
					t.Fatal("non-admin read", path, w.Code, f.chatCalls)
				}
				continue
			}
			if w.Code != 200 || f.chatCalls != 1 || f.chatActor != 7 || s.freshCalls != 1 {
				t.Fatal("admin read", path, w.Code, f.chatCalls, s.freshCalls)
			}
			if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") || w.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Fatal("cacheable private chat", w.Header())
			}
			for _, token := range []string{"", "expired", "nap_valid"} {
				w = newAPIHTTPCall(t, router, "GET", "/api/v1/review-admin/new-api/"+path, "", token)
				if w.Code < 400 || f.chatCalls != 1 {
					t.Fatal("scoped or absent token", token, w.Code)
				}
			}
			s.revoked = true
			w = newAPIHTTPCall(t, router, "GET", "/api/v1/review-admin/new-api/"+path, "", "panel-auth")
			if w.Code < 400 || f.chatCalls != 1 {
				t.Fatal("revoked session", w.Code)
			}
		}
	}
	f := &adminChatHTTPFixture{}
	router := adminChatRouter(f, &newAPIFreshSession{identity: &session.Identity{ID: 7, IsAdmin: 1}})
	for _, path := range []string{"/api/v1/user/new-api/conversations/detail?id=9", "/api/v1/staff/new-api/conversations", "/api/v1/new-api/conversations"} {
		w := newAPIHTTPCall(t, router, "GET", path, "", "panel-auth")
		if w.Code < 400 || f.chatCalls != 0 {
			t.Fatal("alternate route invoked admin", path, w.Code)
		}
	}
}

func TestNewAPIAdminChatFiltersAndMessageCursor(t *testing.T) {
	f := &adminChatHTTPFixture{}
	router := adminChatRouter(f, &newAPIFreshSession{identity: &session.Identity{ID: 7, IsAdmin: 1}})
	w := newAPIHTTPCall(t, router, "GET", "/api/v1/review-admin/new-api/conversations?current=2&page_size=50&user_id=23&email=user%40example.test&keyword=%E6%81%8B%E7%88%B1&mode=character&model=deepseek-flash&start_at=100&end_at=200", "", "panel-auth")
	if w.Code != 200 || f.chatQuery != (newapibridge.AdminConversationQuery{Current: 2, PageSize: 50, UserID: 23, Email: "user@example.test", Keyword: "恋爱", Mode: "character", Model: "deepseek-flash", StartAt: 100, EndAt: 200}) {
		t.Fatal(w.Code, w.Body.String(), f.chatQuery)
	}
	w = newAPIHTTPCall(t, router, "GET", "/api/v1/review-admin/new-api/conversations/detail?id=27", "", "panel-auth")
	if w.Code != 200 || f.chatID != 27 {
		t.Fatal(w.Code, f.chatID)
	}
	w = newAPIHTTPCall(t, router, "GET", "/api/v1/review-admin/new-api/conversations/messages?id=27&before_id=999&page_size=37", "", "panel-auth")
	if w.Code != 200 || f.chatID != 27 || f.before != 999 || f.pageSize != 37 {
		t.Fatal(w.Code, f.before, f.pageSize)
	}
	w = newAPIHTTPCall(t, router, "GET", "/api/v1/review-admin/new-api/conversations/messages?id=27", "", "panel-auth")
	if w.Code != 200 || f.before != 0 || f.pageSize != 100 {
		t.Fatal(w.Code, f.before, f.pageSize)
	}
}

func TestNewAPIAdminChatRejectsUnknownMalformedAndWriteRequests(t *testing.T) {
	f := &adminChatHTTPFixture{}
	router := adminChatRouter(f, &newAPIFreshSession{identity: &session.Identity{ID: 7, IsAdmin: 1}})
	queries := []string{"user_id=0", "user_id=", "user_id=-1", "current=0", "current=100001", "page_size=101", "page_size=0", "current=1&current=2", "user_id[]=7", "user_id=9223372036854775808", "actor_id=1", "auth_data=x", "deleted=true", "mode=anything", "start_at=0", "start_at=100&end_at=99", "start_at=100&end_at=100", "start_at=1.5", "keyword=%00", "keyword=%ff", "email=" + strings.Repeat("a", 255), "keyword=" + url.QueryEscape(strings.Repeat("中", 101)), "model=" + strings.Repeat("x", 201), "keyword=" + strings.Repeat("x", 101) + "&email=" + strings.Repeat("x", 101)}
	for _, q := range queries {
		w := newAPIHTTPCall(t, router, "GET", "/api/v1/review-admin/new-api/conversations?"+q, "", "panel-auth")
		if w.Code != 400 || f.chatCalls != 0 {
			t.Fatal(q, w.Code, w.Body.String(), f.chatCalls)
		}
	}
	for _, suffix := range []string{"detail", "detail?id=0", "detail?id=3&id=4", "detail?id=3&user_id=7", "detail?id=3&before_id=0", "messages?id=0", "messages?id=3&before_id=-1", "messages?id=3&before_id=", "messages?id=3&page_size=201", "messages?id=3&unknown=1"} {
		w := newAPIHTTPCall(t, router, "GET", "/api/v1/review-admin/new-api/conversations/"+suffix, "", "panel-auth")
		if w.Code != 400 || f.chatCalls != 0 {
			t.Fatal(suffix, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"conversations", "conversations/detail?id=3", "conversations/messages?id=3"} {
		for _, method := range []string{"POST", "DELETE", "PATCH"} {
			w := newAPIHTTPCall(t, router, method, "/api/v1/review-admin/new-api/"+path, `{}`, "panel-auth")
			if w.Code != 405 || w.Header().Get("Allow") != "GET" || f.chatCalls != 0 {
				t.Fatal(method, path, w.Code)
			}
		}
	}
}

func TestNewAPIAdminChatSafeErrors(t *testing.T) {
	f := &adminChatHTTPFixture{}
	router := adminChatRouter(f, &newAPIFreshSession{identity: &session.Identity{ID: 7, IsAdmin: 1}})
	for _, tc := range []struct {
		err    error
		status int
	}{{newapibridge.ErrForbidden, 403}, {newapibridge.ErrInvalid, 400}, {errors.New("PRIVATE_DATABASE_SECRET"), 503}} {
		f.chatErr = tc.err
		w := newAPIHTTPCall(t, router, "GET", "/api/v1/review-admin/new-api/conversations/detail?id=5", "", "panel-auth")
		if w.Code != tc.status || strings.Contains(w.Body.String(), "PRIVATE_DATABASE_SECRET") || strings.Contains(w.Body.String(), "充值") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	// A legacy bridge without the new interface fails closed without a panic.
	s := &newAPIFreshSession{identity: &session.Identity{ID: 7, IsAdmin: 1}}
	legacy := NewRouter(config.Config{AdminPath: "review-admin"}, WithSessionService(s), WithNewAPIBridge(&newAPIHTTPFixture{}))
	w := newAPIHTTPCall(t, legacy, "GET", "/api/v1/review-admin/new-api/conversations", "", "panel-auth")
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestAdminConversationQueryLiteralPercentAndDefaults(t *testing.T) {
	q, err := parseAdminConversationQuery(httptest.NewRequest("GET", "/?keyword=100%25_%27&email=a%40b.test", nil))
	if err != nil || q.Keyword != "100%_'" || q.Current != 1 || q.PageSize != 20 {
		t.Fatal(q, err)
	}
}
