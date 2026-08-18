package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
	"forest/go-api/internal/user"
)

type subscribeGuardRankUserService struct {
	*fakeUserService
	resolvedByToken map[string]int64
	subscribeByID   map[int64]user.Subscribe
	resolveCalls    map[string]int
}

func (s *subscribeGuardRankUserService) ResolveClientUserID(ctx context.Context, token string) (int64, error) {
	return s.PeekClientUserID(ctx, token)
}

func (s *subscribeGuardRankUserService) PeekClientUserID(_ context.Context, token string) (int64, error) {
	if s.resolveCalls == nil {
		s.resolveCalls = make(map[string]int)
	}
	s.resolveCalls[token]++
	if userID, ok := s.resolvedByToken[token]; ok {
		return userID, nil
	}
	return 0, user.ErrClientTokenInvalid
}

func (s *subscribeGuardRankUserService) Subscribe(ctx context.Context, userID int64) (user.Subscribe, error) {
	if subscribe, ok := s.subscribeByID[userID]; ok {
		return subscribe, nil
	}
	return s.fakeUserService.Subscribe(ctx, userID)
}

func TestSubscribeGuardUserRankItemsMergesLegacyEventsBeforeSorting(t *testing.T) {
	resetSubscribeGuardStateForTest()
	publicDir := filepath.Join(t.TempDir(), "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{PublicDir: publicDir, SubscribeGuardLogKeepDays: 7}
	now := time.Now().Unix()
	events := []subscribeGuardEvent{
		{Time: now - 7, UserID: 10, IP: "203.0.113.1", UA: "Clash/1"},
		{Time: now - 6, Token: "old-dynamic-a", CanonicalToken: "canonical-10", IP: "203.0.113.2", UA: "Clash/2"},
		{Time: now - 5, Token: "old-dynamic-b", CanonicalToken: "canonical-10", IP: "203.0.113.3", UA: "Clash/3"},
	}
	for i := 0; i < 4; i++ {
		events = append(events, subscribeGuardEvent{
			Time: now - int64(4-i), UserID: 20, IP: "198.51.100.1", UA: "Clash/4",
		})
	}
	if err := writeSubscribeGuardEvents(cfg, events); err != nil {
		t.Fatal(err)
	}

	userService := &subscribeGuardRankUserService{
		fakeUserService: &fakeUserService{},
		resolvedByToken: map[string]int64{"canonical-10": 10},
	}
	byIP := subscribeGuardUserRankItems(context.Background(), cfg, userService, subscribeGuardUserRankSortIPCount, 20)
	if len(byIP) != 2 || anyInt64(byIP[0]["user_id"]) != 10 {
		t.Fatalf("expected merged user 10 first by distinct IP count, got %#v", byIP)
	}
	if anyInt64(byIP[0]["count"]) != 3 || anyInt64(byIP[0]["ip_count"]) != 3 {
		t.Fatalf("unexpected merged legacy rank row: %#v", byIP[0])
	}
	if userService.resolveCalls["canonical-10"] != 1 {
		t.Fatalf("expected canonical token lookup to be cached, got %d calls", userService.resolveCalls["canonical-10"])
	}

	byCount := subscribeGuardUserRankItems(context.Background(), cfg, userService, subscribeGuardUserRankSortCount, 20)
	if len(byCount) != 2 || anyInt64(byCount[0]["user_id"]) != 20 {
		t.Fatalf("expected user 20 first by request count, got %#v", byCount)
	}
}

func TestSubscribeGuardUserRankItemsCapsLegacyTokenLookups(t *testing.T) {
	resetSubscribeGuardStateForTest()
	publicDir := filepath.Join(t.TempDir(), "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{PublicDir: publicDir, SubscribeGuardLogKeepDays: 7}
	now := time.Now().Unix()
	events := []subscribeGuardEvent{{Time: now, UserID: 7, IP: "203.0.113.7", UA: "Clash/1"}}
	for index := 0; index < subscribeGuardLegacyUserRankResolveLimit*5; index++ {
		events = append(events, subscribeGuardEvent{
			Time:  now,
			Token: fmt.Sprintf("attacker-token-%03d", index),
			IP:    fmt.Sprintf("198.51.100.%d", index%250+1),
			UA:    "curl/8",
		})
	}
	if err := writeSubscribeGuardEvents(cfg, events); err != nil {
		t.Fatal(err)
	}

	userService := &subscribeGuardRankUserService{fakeUserService: &fakeUserService{}}
	items := subscribeGuardUserRankItems(context.Background(), cfg, userService, subscribeGuardUserRankSortIPCount, 20)
	if len(items) != 1 || anyInt64(items[0]["user_id"]) != 7 {
		t.Fatalf("expected direct user rank to remain available, got %#v", items)
	}
	lookupCount := 0
	for _, count := range userService.resolveCalls {
		lookupCount += count
	}
	if lookupCount != subscribeGuardLegacyUserRankResolveLimit {
		t.Fatalf("expected exactly %d bounded legacy lookups, got %d", subscribeGuardLegacyUserRankResolveLimit, lookupCount)
	}
}

func TestSubscribeGuardUserRankItemsUsesStableSecondarySorts(t *testing.T) {
	events := []subscribeGuardEvent{
		{UserID: 30, IP: "203.0.113.1"}, {UserID: 30, IP: "203.0.113.2"}, {UserID: 30, IP: "203.0.113.2"},
		{UserID: 40, IP: "198.51.100.1"}, {UserID: 40, IP: "198.51.100.1"}, {UserID: 40, IP: "198.51.100.1"},
		{UserID: 20, IP: "192.0.2.1"}, {UserID: 20, IP: "192.0.2.2"},
		{UserID: 10, IP: "192.0.2.3"}, {UserID: 10, IP: "192.0.2.4"},
	}

	assertOrder := func(t *testing.T, sortBy string, want ...int64) {
		t.Helper()
		items := subscribeGuardUserRankItemsFromEvents(context.Background(), events, nil, sortBy, 20)
		if len(items) != len(want) {
			t.Fatalf("expected %d rows, got %#v", len(want), items)
		}
		for index, userID := range want {
			if got := anyInt64(items[index]["user_id"]); got != userID {
				t.Fatalf("sort %s index %d: expected user %d, got %d (%#v)", sortBy, index, userID, got, items)
			}
		}
	}

	assertOrder(t, subscribeGuardUserRankSortCount, 30, 40, 10, 20)
	assertOrder(t, subscribeGuardUserRankSortIPCount, 30, 10, 20, 40)
}

func TestSubscribeGuardUserRankItemsLimitsDetailArraysWithoutChangingCounts(t *testing.T) {
	events := make([]subscribeGuardEvent, 0, subscribeGuardUserRankDetailLimit+5)
	for index := 0; index < subscribeGuardUserRankDetailLimit+5; index++ {
		events = append(events, subscribeGuardEvent{
			UserID: 55,
			IP:     fmt.Sprintf("203.0.113.%03d", index),
			UA:     fmt.Sprintf("Client/%03d", index),
		})
	}
	items := subscribeGuardUserRankItemsFromEvents(context.Background(), events, nil, subscribeGuardUserRankSortIPCount, 20)
	if len(items) != 1 {
		t.Fatalf("expected one rank row, got %#v", items)
	}
	row := items[0]
	if anyInt64(row["ip_count"]) != int64(len(events)) || anyInt64(row["ua_count"]) != int64(len(events)) {
		t.Fatalf("expected complete distinct counts, got %#v", row)
	}
	if ips, ok := row["ips"].([]string); !ok || len(ips) != subscribeGuardUserRankDetailLimit {
		t.Fatalf("expected %d IP detail values, got %#v", subscribeGuardUserRankDetailLimit, row["ips"])
	}
	if uas, ok := row["uas"].([]string); !ok || len(uas) != subscribeGuardUserRankDetailLimit {
		t.Fatalf("expected %d UA detail values, got %#v", subscribeGuardUserRankDetailLimit, row["uas"])
	}
}

func TestRouterAdminSubscribeGuardStatsRanksFromAllRetainedEvents(t *testing.T) {
	resetSubscribeGuardStateForTest()
	publicDir := filepath.Join(t.TempDir(), "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		PublicDir:                 publicDir,
		AdminPath:                 "localadmin",
		SubscribeGuardLogKeepDays: 7,
	}
	now := time.Now().Unix()
	events := make([]subscribeGuardEvent, 0, 625)
	for userID := int64(1); userID <= 20; userID++ {
		for requestIndex := 0; requestIndex < 30; requestIndex++ {
			events = append(events, subscribeGuardEvent{
				Time: now, UserID: userID, IP: fmt.Sprintf("192.0.2.%d", userID), UA: "Clash/1",
			})
		}
	}
	// User 99 is outside the request-count top 20, but has by far the most
	// distinct IPs. IP sorting must select it from the full retained log.
	for requestIndex := 0; requestIndex < 25; requestIndex++ {
		events = append(events, subscribeGuardEvent{
			Time: now, UserID: 99, IP: fmt.Sprintf("198.51.100.%d", requestIndex+1), UA: "Clash/1",
		})
	}
	if err := writeSubscribeGuardEvents(cfg, events); err != nil {
		t.Fatal(err)
	}

	userService := &subscribeGuardRankUserService{
		fakeUserService: &fakeUserService{subscribe: user.Subscribe{Email: "rank@example.com"}},
	}
	sessionService := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1, Email: "admin@example.com"}}
	router := NewRouter(cfg, WithSessionService(sessionService), WithUserService(userService))

	assertRank := func(t *testing.T, query string, wantSort string, wantFirstUserID, wantCount, wantIPCount int64) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/subscribe-guard/stats?auth_data=jwt-admin"+query, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var payload struct {
			Data struct {
				UserRankSort string `json:"user_rank_sort"`
				TopUsers     []struct {
					UserID  int64 `json:"user_id"`
					Count   int64 `json:"count"`
					IPCount int64 `json:"ip_count"`
				} `json:"top_subscribe_users"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode stats response: %v", err)
		}
		if payload.Data.UserRankSort != wantSort {
			t.Fatalf("expected sort %q, got %q", wantSort, payload.Data.UserRankSort)
		}
		if len(payload.Data.TopUsers) != subscribeGuardUserRankLimit {
			t.Fatalf("expected %d ranked users, got %d", subscribeGuardUserRankLimit, len(payload.Data.TopUsers))
		}
		first := payload.Data.TopUsers[0]
		if first.UserID != wantFirstUserID || first.Count != wantCount || first.IPCount != wantIPCount {
			t.Fatalf("unexpected first rank row: %#v", first)
		}
	}

	assertRank(t, "", subscribeGuardUserRankSortCount, 1, 30, 1)
	assertRank(t, "&user_rank_sort=ip_count", subscribeGuardUserRankSortIPCount, 99, 25, 25)

	invalidReq := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/subscribe-guard/stats?auth_data=jwt-admin&user_rank_sort=unknown", nil)
	invalidRec := httptest.NewRecorder()
	router.ServeHTTP(invalidRec, invalidReq)
	if invalidRec.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid sort 400, got %d: %s", invalidRec.Code, invalidRec.Body.String())
	}

	postReq := httptest.NewRequest(http.MethodPost, "/api/v1/localadmin/subscribe-guard/stats?auth_data=jwt-admin", nil)
	postRec := httptest.NewRecorder()
	router.ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusMethodNotAllowed || postRec.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("expected POST 405 with Allow GET, got %d Allow=%q: %s", postRec.Code, postRec.Header().Get("Allow"), postRec.Body.String())
	}
}
