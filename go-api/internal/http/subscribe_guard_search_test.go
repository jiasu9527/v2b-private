package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/cliententry"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

func guardSearchHTTPTestSnapshot(ids ...int64) *admin.SubscribeGuardSearchSnapshot {
	s := &admin.SubscribeGuardSearchSnapshot{Users: map[int64]admin.SubscribeGuardSearchUser{}, UserIDsByToken: map[string]int64{}, Assignments: map[int64]map[int64]int64{}, EvaluatedAt: 10000}
	for _, id := range ids {
		email := fmt.Sprintf("user%d@example.com", id)
		s.Users[id] = admin.SubscribeGuardSearchUser{ID: id, Email: email, Public: map[string]any{"id": id, "email": email}}
		s.UserIDsByToken[fmt.Sprintf("token-%d", id)] = id
	}
	return s
}
func guardSearchHTTPTestPolicy(id int64, ua string) admin.ClientEntryUserPolicyRecord {
	p := admin.ClientEntryUserPolicyRecord{ID: id, Name: "rule", Sort: id, Enabled: 1, Mode: "standard", Action: "override", Members: []admin.ClientEntryGroupMemberRecord{{ServerType: "vmess", ServerID: id}}}
	if ua != "" {
		raw, _ := json.Marshal(ua)
		p.Conditions = []cliententry.Condition{{Field: "ua", Operator: "contains_any", Values: []json.RawMessage{raw}}}
	}
	return p
}
func guardSearchHTTPTestQuery() subscribeGuardSearchQuery {
	return subscribeGuardSearchQuery{AccountMatch: "contains", UAMatch: "contains", Result: "all", Sort: "last_at", Current: 1, PageSize: 20}
}
func guardSearchRows(t *testing.T, r map[string]any) []map[string]any {
	t.Helper()
	return r["data"].([]map[string]any)
}
func TestSubscribeGuardSearchANDUsesSameEventAndAllUAs(t *testing.T) {
	s := guardSearchHTTPTestSnapshot(1, 2)
	s.Policies = []admin.ClientEntryUserPolicyRecord{guardSearchHTTPTestPolicy(9, "Forest")}
	events := []subscribeGuardEvent{{UserID: 1, Time: 100, UA: "Forest/1"}, {UserID: 1, Time: 200, UA: "Clash/1"}, {UserID: 2, Time: 200, UA: "Forest/1", Blocked: true}}
	q := guardSearchHTTPTestQuery()
	q.PolicyID = 9
	q.StartAt = 150
	q.EndAt = 250
	q.UA = "clash"
	result, err := aggregateSubscribeGuardSearch(context.Background(), q, events, s)
	if err != nil {
		t.Fatal(err)
	}
	if result["total"].(int64) != 0 {
		t.Fatal("combined events from different UAs")
	}
	q.StartAt = 50
	q.UA = "forest"
	q.Result = "allowed"
	result, err = aggregateSubscribeGuardSearch(context.Background(), q, events, s)
	if err != nil {
		t.Fatal(err)
	}
	rows := guardSearchRows(t, result)
	if len(rows) != 1 || rows[0]["user_id"] != int64(1) || rows[0]["count"] != int64(1) || rows[0]["last_at"] != int64(100) {
		t.Fatalf("not the actual matching old UA: %#v", result)
	}
}
func TestSubscribeGuardSearchFiltersBeforePagination(t *testing.T) {
	s := guardSearchHTTPTestSnapshot()
	s.Policies = []admin.ClientEntryUserPolicyRecord{guardSearchHTTPTestPolicy(9, "Forest")}
	events := []subscribeGuardEvent{}
	for id := int64(1); id <= 70; id++ {
		u := guardSearchHTTPTestSnapshot(id)
		s.Users[id] = u.Users[id]
		ua := "Clash"
		if id <= 30 {
			ua = "Forest"
		}
		events = append(events, subscribeGuardEvent{UserID: id, Time: 100 + id, UA: ua})
	}
	q := guardSearchHTTPTestQuery()
	q.PolicyID = 9
	q.Current = 2
	result, err := aggregateSubscribeGuardSearch(context.Background(), q, events, s)
	if err != nil {
		t.Fatal(err)
	}
	rows := guardSearchRows(t, result)
	if result["total"] != int64(30) || len(rows) != 10 || rows[0]["user_id"] != int64(10) {
		t.Fatalf("pagination applied before rule: %#v", result)
	}
}
func TestSubscribeGuardSearchOnlyAccountOrPolicyIncludesNoEvents(t *testing.T) {
	s := guardSearchHTTPTestSnapshot(1, 2, 3)
	s.Policies = []admin.ClientEntryUserPolicyRecord{guardSearchHTTPTestPolicy(9, "Forest")}
	events := []subscribeGuardEvent{{UserID: 1, Time: 100, UA: "Other"}}
	q := guardSearchHTTPTestQuery()
	q.Account = "example.com"
	result, err := aggregateSubscribeGuardSearch(context.Background(), q, events, s)
	if err != nil {
		t.Fatal(err)
	}
	rows := guardSearchRows(t, result)
	if len(rows) != 3 || rows[1]["count"] != int64(0) || rows[1]["entry_policy_uncertain"] != true || result["uncertain_no_event_users"] != int64(2) {
		t.Fatalf("no-event account mismatch %#v", result)
	}
	q.PolicyID = 9
	result, err = aggregateSubscribeGuardSearch(context.Background(), q, events, s)
	if err != nil {
		t.Fatal(err)
	}
	if result["total"] != int64(0) {
		t.Fatal("unknown UA or observed nonmatching UA should not match a rule")
	}
	s.Policies = []admin.ClientEntryUserPolicyRecord{guardSearchHTTPTestPolicy(9, "")}
	result, _ = aggregateSubscribeGuardSearch(context.Background(), q, events, s)
	if result["total"] != int64(3) {
		t.Fatal("deterministic no-log rule membership missing")
	}
	for _, change := range []func(*subscribeGuardSearchQuery){func(q *subscribeGuardSearchQuery) { q.UA = "Forest" }, func(q *subscribeGuardSearchQuery) { q.StartAt = 500 }, func(q *subscribeGuardSearchQuery) { q.Result = "blocked" }} {
		testQ := q
		change(&testQ)
		r, _ := aggregateSubscribeGuardSearch(context.Background(), testQ, events, s)
		if r["total"] != int64(0) {
			t.Fatalf("event predicate must exclude no-log users %#v", r)
		}
	}
	q = guardSearchHTTPTestQuery()
	r, _ := aggregateSubscribeGuardSearch(context.Background(), q, nil, s)
	if r["total"] != int64(0) || len(guardSearchRows(t, r)) != 0 {
		t.Fatal("empty query enumerated all users")
	}
}
func TestSubscribeGuardSearchBoundariesUAModesAndParsing(t *testing.T) {
	q := guardSearchHTTPTestQuery()
	q.StartAt = 100
	q.EndAt = 200
	q.UA = "FoReSt"
	q.UAMatch = "equals"
	for _, test := range []struct {
		event subscribeGuardEvent
		want  bool
	}{{subscribeGuardEvent{Time: 100, UA: "forest"}, true}, {subscribeGuardEvent{Time: 200, UA: "forest"}, false}, {subscribeGuardEvent{Time: 99, UA: "forest"}, false}, {subscribeGuardEvent{Time: 150, UA: "forest/1"}, false}} {
		if q.matchesEvent(test.event) != test.want {
			t.Fatalf("boundary %#v", test)
		}
	}
	q.UAMatch = "not_contains"
	if q.matchesEvent(subscribeGuardEvent{Time: 150, UA: "Forest/1"}) || !q.matchesEvent(subscribeGuardEvent{Time: 150, UA: "Clash"}) {
		t.Fatal("not contains")
	}
	for _, raw := range []string{"account_match=bad", "ua_match=bad", "result=downloaded", "sort=bad", "start_at=-1", "start_at=200&end_at=100", "group_id=2", "current=0", "page_size=101", "account=0", "account=99999999999999999999"} {
		v, _ := url.ParseQuery(raw)
		if _, err := parseSubscribeGuardSearchQuery(v); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	parsed, err := parseSubscribeGuardSearchQuery(url.Values{"account": {"0001"}})
	if err != nil || parsed.Account != "1" || parsed.PageSize != 20 {
		t.Fatalf("parsed %#v err %v", parsed, err)
	}
	s := guardSearchHTTPTestSnapshot(1)
	if !subscribeGuardSearchAccountMatches(parsed, s.Users[1]) {
		t.Fatal("leading zero ID mismatch")
	}
}
func TestSubscribeGuardSearchCanonicalTokensAndUnresolved(t *testing.T) {
	s := guardSearchHTTPTestSnapshot(1)
	events := []subscribeGuardEvent{{Time: 10, Token: "transient", CanonicalToken: "token-1", UA: "Forest"}, {Time: 11, UserID: 999, UA: "Forest"}, {Time: 12, Token: "invalid", UA: "Forest"}}
	q := guardSearchHTTPTestQuery()
	r, err := aggregateSubscribeGuardSearch(context.Background(), q, events, s)
	if err != nil {
		t.Fatal(err)
	}
	if r["matched_events"] != int64(1) || r["unresolved_events"] != int64(2) {
		t.Fatalf("identity attribution %#v", r)
	}
	q.Account = "user1"
	r, _ = aggregateSubscribeGuardSearch(context.Background(), q, events, s)
	if r["unresolved_events"] != int64(0) {
		t.Fatal("out-of-account canonical tokens counted as unresolved")
	}
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "token-1") || strings.Contains(string(raw), "transient") {
		t.Fatal("token leak")
	}
}
func TestSubscribeGuardSearchLegacyPeekCachedAndDoesNotConsume(t *testing.T) {
	service := &subscribeGuardRankUserService{fakeUserService: &fakeUserService{}, resolvedByToken: map[string]int64{"legacy": 1}}
	events := []subscribeGuardEvent{{Token: "legacy"}, {Token: "legacy"}, {CanonicalToken: "canonical", Token: "temporary"}}
	ids, tokens, err := subscribeGuardSearchEventIdentities(context.Background(), events, service)
	if err != nil || len(ids) != 1 || ids[0] != 1 || len(tokens) != 2 || service.resolveCalls["legacy"] != 1 || service.resolveCalls["canonical"] != 0 || events[1].UserID != 1 {
		t.Fatalf("ids %v tokens %v calls %v err %v", ids, tokens, service.resolveCalls, err)
	}
	events = make([]subscribeGuardEvent, subscribeGuardSearchMaxLegacyTokens+1)
	for i := range events {
		events[i].Token = fmt.Sprintf("legacy-%d", i)
	}
	_, _, err = subscribeGuardSearchEventIdentities(context.Background(), events, service)
	if !errors.Is(err, errSubscribeGuardSearchLimit) {
		t.Fatalf("expected explicit legacy resource limit %v", err)
	}
}
func TestSubscribeGuardSearchAggregationSortRecentCapAndSplitValidation(t *testing.T) {
	s := guardSearchHTTPTestSnapshot(1, 2)
	q := guardSearchHTTPTestQuery()
	q.Sort = "count"
	events := []subscribeGuardEvent{{UserID: 2, Time: 1000, UA: "Other"}}
	for i := 0; i < 30; i++ {
		events = append(events, subscribeGuardEvent{UserID: 1, Time: int64(i), IP: fmt.Sprint(i % 2), UA: fmt.Sprint(i % 3), Blocked: i%2 == 0})
	}
	r, _ := aggregateSubscribeGuardSearch(context.Background(), q, events, s)
	rows := guardSearchRows(t, r)
	first := rows[0]
	if first["user_id"] != int64(1) || len(first["recent"].([]subscribeGuardUserEvent)) != 20 || first["ip_count"] != int64(2) || first["ua_count"] != int64(3) || first["allowed"] != int64(15) || first["blocked"] != int64(15) {
		t.Fatalf("aggregate %#v", first)
	}
	s.Policies = []admin.ClientEntryUserPolicyRecord{{ID: 9, SplitGroups: []admin.ClientEntryUserPolicySplitGroupRecord{{ID: 4, IsLeaf: true}, {ID: 5, IsLeaf: false}}}}
	q.PolicyID = 9
	q.GroupID = 4
	if err := validateSubscribeGuardSearchPolicy(q, s); err != nil {
		t.Fatal(err)
	}
	for _, groupID := range []int64{5, 99} {
		q.GroupID = groupID
		if validateSubscribeGuardSearchPolicy(q, s) == nil {
			t.Fatal("invalid child accepted")
		}
	}
}

func TestSubscribeGuardSearchDiskSnapshotStrictAndRetention(t *testing.T) {
	now := time.Unix(2000000, 0)
	cfg := config.Config{PublicDir: filepath.Join(t.TempDir(), "public"), SubscribeGuardLogKeepDays: 1}
	q := guardSearchHTTPTestQuery()
	events := []subscribeGuardEvent{{Time: now.Unix() - 86401, UserID: 1}, {Time: now.Unix() - 86400, UserID: 2}, {Time: now.Unix(), UserID: 3}}
	if err := writeSubscribeGuardEvents(cfg, events); err != nil {
		t.Fatal(err)
	}
	got, from, to, warnings, err := readSubscribeGuardSearchEvents(context.Background(), cfg, q, now)
	if err != nil || len(got) != 2 || from != now.Unix()-86400 || to != now.Unix() || len(warnings) != 0 {
		t.Fatalf("retention %v %d %d %v %v", got, from, to, warnings, err)
	}
	// A failed or missing disk read must never expose unrelated RAM events.
	subscribeGuardEventState.Lock()
	old := subscribeGuardEventState.events
	subscribeGuardEventState.events = []subscribeGuardEvent{{Time: now.Unix(), UserID: 999}}
	subscribeGuardEventState.Unlock()
	defer func() {
		subscribeGuardEventState.Lock()
		subscribeGuardEventState.events = old
		subscribeGuardEventState.Unlock()
	}()
	path := subscribeGuardLogPath(cfg)
	for _, content := range []string{"{bad-json}\n", strings.Repeat("x", (1<<20)+10)} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, _, _, err := readSubscribeGuardSearchEvents(context.Background(), cfg, q, now); err == nil {
			t.Fatal("corrupt/scanner failure was silently ignored")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	got, _, _, warnings, err = readSubscribeGuardSearchEvents(context.Background(), cfg, q, now)
	if err != nil || len(got) != 0 || len(warnings) == 0 {
		t.Fatal("missing log RAM fallback")
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := readSubscribeGuardSearchEvents(context.Background(), cfg, q, now); err == nil {
		t.Fatal("directory was treated as empty log")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, _, err := readSubscribeGuardSearchEvents(ctx, cfg, q, now); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type guardSearchHTTPAdminService struct {
	*fakeAdminService
	snapshot *admin.SubscribeGuardSearchSnapshot
	request  admin.SubscribeGuardSearchLoadRequest
	calls    int
}

func (s *guardSearchHTTPAdminService) LoadSubscribeGuardSearchSnapshot(_ context.Context, request admin.SubscribeGuardSearchLoadRequest) (*admin.SubscribeGuardSearchSnapshot, error) {
	s.calls++
	s.request = request
	return s.snapshot, nil
}
func TestSubscribeGuardSearchRouterAdminOnlyNoStoreAndResponse(t *testing.T) {
	cfg := config.Config{PublicDir: filepath.Join(t.TempDir(), "public"), AdminPath: "localadmin", SubscribeGuardLogKeepDays: 7, SubscribeGuardEnable: true}
	if err := writeSubscribeGuardEvents(cfg, []subscribeGuardEvent{{Time: time.Now().Unix(), UserID: 1, UA: "Forest", Token: "private"}}); err != nil {
		t.Fatal(err)
	}
	service := &guardSearchHTTPAdminService{fakeAdminService: &fakeAdminService{}, snapshot: guardSearchHTTPTestSnapshot(1)}
	sessions := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	router := NewRouter(cfg, WithSessionService(sessions), WithAdminService(service))
	request := func(query string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/subscribe-guard/search?auth_data=admin"+query, nil))
		return rec
	}
	rec := request("&ua=Forest")
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") || !sessions.lastRequireAdmin || strings.Contains(rec.Body.String(), "private") {
		t.Fatalf("response %d %s %v", rec.Code, rec.Body.String(), rec.Header())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["rule_basis"] != "current" || payload["total"] != float64(1) || payload["warnings"] == nil {
		t.Fatalf("response %#v", payload)
	}
	if rec = request("&policy_id=999"); rec.Code != 400 {
		t.Fatalf("nonexisting policy %d", rec.Code)
	}
	for _, identity := range []*session.Identity{{ID: 2, IsStaff: 1}, {ID: 3}, nil} {
		sessions.user = identity
		before := service.calls
		rec = request("")
		if rec.Code != 403 || service.calls != before {
			t.Fatalf("non-admin queried %d", rec.Code)
		}
	}
	sessions.user = &session.Identity{ID: 1, IsAdmin: 1}
	if err := os.WriteFile(subscribeGuardLogPath(cfg), []byte("bad\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rec = request("")
	if rec.Code != 503 {
		t.Fatalf("disk failure must be explicit %d", rec.Code)
	}
}

func TestSubscribeGuardSearchOversizeDiskAndEvaluationCancellation(t *testing.T) {
	cfg := config.Config{PublicDir: filepath.Join(t.TempDir(), "public")}
	path := subscribeGuardLogPath(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(subscribeGuardSearchMaxLogBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	if _, _, _, _, err := readSubscribeGuardSearchEvents(context.Background(), cfg, guardSearchHTTPTestQuery(), time.Now()); !errors.Is(err, errSubscribeGuardSearchLogSize) {
		t.Fatalf("oversize log should fail before scan %v", err)
	}
	rec := httptest.NewRecorder()
	subscribeGuardSearchError(rec, errSubscribeGuardSearchLogSize)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "512 MiB") {
		t.Fatalf("unhelpful size error %s", rec.Body.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = aggregateSubscribeGuardSearch(ctx, guardSearchHTTPTestQuery(), []subscribeGuardEvent{{UserID: 1}}, guardSearchHTTPTestSnapshot(1))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel lost %v", err)
	}
}
