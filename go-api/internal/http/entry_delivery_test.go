package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forest/go-api/internal/config"
	"forest/go-api/internal/entryevidence"
	"forest/go-api/internal/session"
	usersvc "forest/go-api/internal/user"
	"github.com/DATA-DOG/go-sqlmock"
)

func entryDeliveryFixture(t *testing.T, filename, content string) config.Config {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "resources", "rules"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "resources", "rules", filename), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return config.Config{PublicDir: filepath.Join(root, "public")}
}
func deliveryCollector() *entryevidence.Collector {
	c := entryevidence.NewCollector("subscription", "Forest")
	c.SetUser(1, "user@example.test")
	c.Observe(entryevidence.Node{ServerType: "vmess", ServerID: 7, PolicyID: 2, GroupID: 3, Source: "split", SourceHost: "entry.example", DeliveredHost: "192.0.2.7", Port: "54101"})
	return c
}
func TestEntryDeliveryBuildersOnlyEmittedNodes(t *testing.T) {
	servers := []map[string]any{{"type": "vmess", "id": 7, "name": "real", "host": "192.0.2.7", "port": 54101, "network": "tcp"}, {"type": "unsupported", "id": 8, "host": "not-output.example", "port": 443}}
	cases := []struct {
		name  string
		build func(*entryevidence.Collector) error
	}{
		{"clash", func(c *entryevidence.Collector) error {
			cfg := entryDeliveryFixture(t, "test.yaml", "proxies: []\nproxy-groups: []\nrules: []\n")
			_, e := buildClashStandardProfile(cfg, "test.yaml", "", "secret-uuid", servers, c)
			return e
		}},
		{"singbox", func(c *entryevidence.Collector) error {
			cfg := entryDeliveryFixture(t, "test.json", `{"outbounds":[{"type":"direct","tag":"direct"}]}`)
			_, e := buildSingBoxProfile(cfg, "test.json", "", "secret-uuid", servers, c)
			return e
		}},
		{"surge", func(c *entryevidence.Collector) error {
			cfg := entryDeliveryFixture(t, "test.conf", "[Proxy]\nDIRECT = direct\n$proxies\n[Proxy Group]\n")
			_, e := buildSurgeProfile(cfg, "test.conf", "", "secret-url", "example", usersvc.Subscribe{}, "secret-uuid", servers, c)
			return e
		}},
		{"general", func(c *entryevidence.Collector) error {
			buildGeneralSubscribePayload("secret-uuid", servers, c)
			return nil
		}},
		{"shadowrocket", func(c *entryevidence.Collector) error {
			buildShadowrocketPayload(usersvc.Subscribe{UUID: "secret-uuid"}, servers, c)
			return nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := deliveryCollector()
			if err := tc.build(c); err != nil {
				t.Fatal(err)
			}
			r, n := c.Finish(200, 100, "")
			if !r.ResponseComplete || r.Coverage != "complete" || len(n) != 1 || n[0].ServerID != 7 || n[0].DeliveredHost != "192.0.2.7" || n[0].Port != "54101" {
				t.Fatalf("record=%+v nodes=%+v", r, n)
			}
		})
	}
}
func TestEntryDeliveryTemplateNodesAreExplicitlyPartial(t *testing.T) {
	cases := []struct {
		name  string
		build func(*entryevidence.Collector) error
	}{
		{"clash", func(c *entryevidence.Collector) error {
			cfg := entryDeliveryFixture(t, "test.yaml", "proxies:\n  - {name: static, type: socks5, server: static.example, port: 1080}\nproxy-groups: []\n")
			_, e := buildClashStandardProfile(cfg, "test.yaml", "", "", nil, c)
			return e
		}},
		{"singbox", func(c *entryevidence.Collector) error {
			cfg := entryDeliveryFixture(t, "test.json", `{"outbounds":[{"type":"socks","server":"static.example","server_port":1080}]}`)
			_, e := buildSingBoxProfile(cfg, "test.json", "", "", nil, c)
			return e
		}},
		{"surge", func(c *entryevidence.Collector) error {
			cfg := entryDeliveryFixture(t, "test.conf", "[Proxy]\nmanual = ss, static.example, 443, password=secret\n$proxies\n")
			_, e := buildSurgeProfile(cfg, "test.conf", "", "", "", usersvc.Subscribe{}, "", nil, c)
			return e
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := deliveryCollector()
			if e := tc.build(c); e != nil {
				t.Fatal(e)
			}
			r, _ := c.Finish(200, 10, "")
			if r.Coverage != "partial" || r.CoverageReason != "template_static_nodes" {
				t.Fatalf("%+v", r)
			}
		})
	}
}
func TestEntryDeliveryForestProviderDoesNotMisattributeRule(t *testing.T) {
	c := deliveryCollector()
	ctx := entryevidence.WithCollector(context.Background(), c)
	proxies := buildForestEntryProviderProxies(ctx, "secret", []map[string]any{{"type": "vmess", "id": 7, "name": "test", "host": "192.0.2.7", "port": 54101, "network": "tcp"}}, usersvc.ClientEntryGroup{ID: 99, UpdatedAt: 123, IPs: []usersvc.ClientEntryGroupIP{{IP: "198.51.100.1"}, {IP: "198.51.100.2"}}, RemoteSSHPassword: "DO-NOT-STORE"}, nil)
	r, nodes := c.Finish(200, 100, "")
	if len(proxies) != 2 || len(nodes) != 2 || !r.ResponseComplete {
		t.Fatalf("%+v %+v", r, nodes)
	}
	for _, n := range nodes {
		if n.Source != "provider" || n.ProviderID != 99 || n.PolicyID != 0 || n.GroupID != 0 || n.CollectionID != 0 || !strings.HasPrefix(n.DeliveredHost, "198.51.100.") {
			t.Fatalf("provider=%+v", n)
		}
	}
}
func TestEntryDeliveryJSONAndStatusInfo(t *testing.T) {
	c := deliveryCollector()
	row := map[string]any{"type": "vmess", "id": 7, "host": "192.0.2.7", "port": 54101, "uuid": "secret"}
	emitEntryJSON(entryevidence.WithCollector(context.Background(), c), []map[string]any{row})
	emitEntryURI(c, cloneSubscribeInfoServer(row, "remaining"))
	r, n := c.Finish(200, 100, "")
	if !r.ResponseComplete || len(n) != 1 {
		t.Fatalf("%+v %+v", r, n)
	}
}

type failingEntryWriter struct {
	*httptest.ResponseRecorder
	short bool
}

func (w failingEntryWriter) Write(body []byte) (int, error) {
	if w.short {
		return len(body) - 1, nil
	}
	return 0, errors.New("secret-token must not be retained")
}
func TestEntryDeliveryWriterFailureAndShortWrite(t *testing.T) {
	for _, short := range []bool{false, true} {
		w := &entryDeliveryWriter{ResponseWriter: failingEntryWriter{httptest.NewRecorder(), short}}
		if _, e := w.Write([]byte("body")); e == nil {
			t.Fatal("failure lost")
		}
		if w.status != 200 || w.writeError == "" || strings.Contains(w.writeError, "secret") {
			t.Fatalf("%+v", w)
		}
		c := deliveryCollector()
		c.Coverage("complete", "")
		r, _ := c.Finish(w.status, w.bytes, w.writeError)
		if r.ResponseComplete {
			t.Fatal("failed write marked completed")
		}
	}
}
func TestBeginEntryDeliveryRecordsAfterCanceledRequestAndSkipsAnonymous(t *testing.T) {
	db, mock, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	store := entryevidence.NewStore(db)
	ctx, cancel := context.WithCancel(entryevidence.WithStore(context.Background(), store))
	cancel()
	r := httptest.NewRequest(http.MethodGet, "/subscribe?token=DO-NOT-STORE", nil).WithContext(ctx)
	w, r, finish := beginEntryDelivery(httptest.NewRecorder(), r, "subscription")
	c := entryevidence.FromContext(r.Context())
	c.SetUser(7, "u@example.test")
	c.Coverage("complete", "server_write_completed")
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO v2_entry_delivery_request").WithArgs(sqlmock.AnyArg(), int64(7), "u@example.test", "", sqlmock.AnyArg(), sqlmock.AnyArg(), "subscription", 200, true, "complete", "server_write_completed", 0, int64(2), "").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	_, _ = w.Write([]byte("ok"))
	finish()
	if e := mock.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
	_, _, finish = beginEntryDelivery(httptest.NewRecorder(), r, "subscription")
	finish()
	if e := mock.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}

func TestEntryDeliveryPortMetadataMatchesProtocolRenderer(t *testing.T) {
	server := map[string]any{"type": "hysteria", "version": 2, "id": 7, "host": "entry.example", "port": 1000, "mport": "2000-2009", "name": "hop"}
	for _, tc := range []struct {
		name, want string
		build      func(*entryevidence.Collector)
	}{
		{"general", "1000", func(c *entryevidence.Collector) { buildGeneralSubscribePayload("secret", []map[string]any{server}, c) }},
		{"surge", "2000", func(c *entryevidence.Collector) {
			cfg := entryDeliveryFixture(t, "ports.conf", "[Proxy]\n$proxies\n")
			_, e := buildSurgeProfile(cfg, "ports.conf", "", "", "", usersvc.Subscribe{}, "secret", []map[string]any{server}, c)
			if e != nil {
				t.Fatal(e)
			}
		}},
		{"clash", "2000-2009", func(c *entryevidence.Collector) {
			cfg := entryDeliveryFixture(t, "ports.yaml", "proxies: []\nproxy-groups: []\n")
			_, e := buildClashStandardProfile(cfg, "ports.yaml", "", "secret", []map[string]any{server}, c)
			if e != nil {
				t.Fatal(e)
			}
		}},
		{"singbox", "2000-2009", func(c *entryevidence.Collector) {
			cfg := entryDeliveryFixture(t, "ports.json", `{"outbounds":[]}`)
			_, e := buildSingBoxProfile(cfg, "ports.json", "", "secret", []map[string]any{server}, c)
			if e != nil {
				t.Fatal(e)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := entryevidence.NewCollector("subscription", "")
			c.Observe(entryevidence.Node{ServerType: "hysteria", ServerID: 7, Source: "default", SourceHost: "entry.example"})
			tc.build(c)
			r, n := c.Finish(200, 10, "")
			if len(n) != 1 || n[0].Port != tc.want || r.Coverage != "complete" {
				t.Fatalf("%+v %+v", r, n)
			}
		})
	}
}
func TestEntryDeliverySurgeTemplateWithoutNodeSlotDoesNotRecordNodes(t *testing.T) {
	c := deliveryCollector()
	cfg := entryDeliveryFixture(t, "empty.conf", "[Proxy]\nDIRECT=direct\n")
	_, err := buildSurgeProfile(cfg, "empty.conf", "", "", "", usersvc.Subscribe{}, "secret", []map[string]any{{"type": "vmess", "id": 7, "host": "entry.example", "port": 443}}, c)
	if err != nil {
		t.Fatal(err)
	}
	r, n := c.Finish(200, 10, "")
	if len(n) != 0 || r.Coverage != "partial" || r.CoverageReason != "template_missing_node_slot" {
		t.Fatalf("%+v %+v", r, n)
	}
}

type evidenceUserService struct{ *fakeUserService }

func (s *evidenceUserService) Servers(ctx context.Context, id int64, ua string) ([]map[string]any, error) {
	c := entryevidence.FromContext(ctx)
	c.SetUser(id, "route@example.test")
	for _, row := range s.servers {
		c.Observe(entryevidence.Node{ServerType: serverString(row, "type"), ServerID: serverInt64(row, "id"), PolicyID: 5, GroupID: 9, Source: "split", SourceHost: "entry.example", DeliveredHost: serverString(row, "host"), Port: serverString(row, "port")})
	}
	return s.fakeUserService.Servers(ctx, id, ua)
}
func expectEvidenceRequest(mock sqlmock.Sqlmock, transport string, status int, complete bool, coverage, reason string, count int) {
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO v2_entry_delivery_request").WithArgs(sqlmock.AnyArg(), int64(10), "route@example.test", "Forest/1.0", sqlmock.AnyArg(), sqlmock.AnyArg(), transport, status, complete, coverage, reason, count, sqlmock.AnyArg(), "").WillReturnResult(sqlmock.NewResult(1, 1))
	if count > 0 {
		mock.ExpectExec("INSERT INTO v2_entry_delivery_node").WithArgs(sqlmock.AnyArg(), 0, "vmess", int64(7), int64(5), int64(9), "", "entry.example", "192.0.2.7", "54101", false, int64(0), int64(0), "split").WillReturnResult(sqlmock.NewResult(1, 1))
	}
	mock.ExpectCommit()
}
func TestEntryDeliveryRouterSubscriptionAndNotModified(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := &evidenceUserService{&fakeUserService{resolvedClientUserID: 10, subscribe: usersvc.Subscribe{UUID: "NEVER-LOG-UUID"}, servers: []map[string]any{{"type": "vmess", "id": 7, "host": "192.0.2.7", "port": 54101, "name": "real", "network": "tcp", "cache_key": "test"}}}}
	sessionService := &fakeSessionService{user: &session.Identity{ID: 10}}
	router := NewRouter(config.Config{AppName: "evidence"}, WithUserService(service), WithSessionService(sessionService), WithEntryEvidenceStore(entryevidence.NewStore(db)))
	request := func(path, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("User-Agent", "Forest/1.0")
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		return rec
	}
	expectEvidenceRequest(mock, "subscription", 200, true, "complete", "server_write_completed", 1)
	rec := request("/api/v1/client/subscribe?token=NEVER-LOG-TOKEN", "")
	if rec.Code != 200 {
		t.Fatalf("subscribe %d %s", rec.Code, rec.Body.String())
	}
	expectEvidenceRequest(mock, "server_json", 200, true, "complete", "server_write_completed", 1)
	rec = request("/api/v1/user/server/fetch?auth_data=test", "")
	if rec.Code != 200 {
		t.Fatalf("json %d %s", rec.Code, rec.Body.String())
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing etag")
	}
	if strings.Contains(rec.Body.String(), "_entry_evidence") {
		t.Fatal("internal field exposed")
	}
	expectEvidenceRequest(mock, "server_json", 304, false, "not_emitted", "not_modified", 0)
	rec = request("/api/v1/user/server/fetch?auth_data=test", etag)
	if rec.Code != 304 || rec.Body.Len() != 0 {
		t.Fatalf("304 %d %s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBeginEntryDeliveryDoesNotClaimCompletionWithoutBodyOrAfterPanic(t *testing.T) {
	for _, panicked := range []bool{false, true} {
		t.Run(map[bool]string{false: "encoder failed", true: "handler panicked"}[panicked], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			req := httptest.NewRequest(http.MethodGet, "/subscribe", nil).WithContext(entryevidence.WithStore(context.Background(), entryevidence.NewStore(db)))
			w, req, finish := beginEntryDelivery(httptest.NewRecorder(), req, "subscription")
			c := entryevidence.FromContext(req.Context())
			c.SetUser(7, "")
			c.Coverage("complete", "server_write_completed")
			reason := "body_not_written"
			if panicked {
				reason = "handler_panicked"
			}
			mock.ExpectBegin()
			mock.ExpectExec("INSERT INTO v2_entry_delivery_request").WithArgs(sqlmock.AnyArg(), int64(7), "", "", sqlmock.AnyArg(), sqlmock.AnyArg(), "subscription", 200, false, "complete", "server_write_completed", 0, int64(0), reason).WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectCommit()
			caught := func() (caught bool) {
				defer func() { caught = recover() != nil }()
				func() {
					defer finish()
					w.WriteHeader(200)
					if panicked {
						panic("private panic text")
					}
				}()
				return
			}()
			if caught != panicked {
				t.Fatalf("panic forwarded=%v", caught)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
