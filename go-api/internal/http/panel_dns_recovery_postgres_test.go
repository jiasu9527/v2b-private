package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/dnspod"
)

// Only the DNS provider boundary is synthetic. The AI worker, catalog,
// approval/action persistence, private HTTP adapter and DNS handlers are real.
type dnsHTTPRecoveryAdmin struct {
	*fakeAdminService
	mu          sync.Mutex
	unknown     bool
	saves       []admin.DNSPodRecordSaveRequest
	created     int
	recordReads int
	lineReads   int
}

func (f *dnsHTTPRecoveryAdmin) SaveDNSPodRecord(_ context.Context, req admin.DNSPodRecordSaveRequest) (dnspod.RecordMutationResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saves = append(f.saves, req)
	if len(f.saves) == 1 {
		if f.unknown {
			// A lost response can follow a successful provider mutation. The
			// worker must not create it again, even after a successful read.
			f.created++
			return dnspod.RecordMutationResult{}, errors.New("provider response lost PRIVATE_DNS_SENTINEL")
		}
		return dnspod.RecordMutationResult{}, &dnspod.PreflightError{Code: "dns_record_line_unavailable", Err: errors.New("provider line details PRIVATE_DNS_SENTINEL")}
	}
	if req.RecordLine != "Global" || req.RecordLineID != "3=0" || req.DomainGrade != "DPG_Ultimate" || req.SubDomain != "new-entry" {
		return dnspod.RecordMutationResult{}, errors.New("unexpected corrected DNS request")
	}
	f.created++
	return dnspod.RecordMutationResult{RecordID: 808}, nil
}

func (f *dnsHTTPRecoveryAdmin) ListDNSPodRecords(_ context.Context, _ admin.DNSPodRecordListRequest) (dnspod.DescribeRecordListResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordReads++
	result := dnspod.DescribeRecordListResult{Records: []dnspod.Record{}}
	if f.created > 0 {
		result.Records = []dnspod.Record{{RecordID: 808, Name: "new-entry", Type: "A", Value: "192.0.2.132"}}
		result.Total = 1
	}
	return result, nil
}

func (f *dnsHTTPRecoveryAdmin) ListDNSPodRecordLines(_ context.Context, _ string, _ int64, _, _ string) (dnspod.DescribeRecordLineListResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lineReads++
	return dnspod.DescribeRecordLineListResult{Lines: []dnspod.RecordLine{{LineID: "3=0", LineName: "Global", Useful: true}}}, nil
}

type dnsHTTPRecoveryRelayRequest struct {
	Messages []struct {
		Role       string `json:"role"`
		Content    string `json:"content"`
		ToolCallID string `json:"tool_call_id"`
	} `json:"messages"`
}

func dnsHTTPRecoveryToolResult(t *testing.T, req dnsHTTPRecoveryRelayRequest, id string) map[string]any {
	t.Helper()
	for _, message := range req.Messages {
		if message.Role == "tool" && message.ToolCallID == id {
			var result map[string]any
			if err := json.Unmarshal([]byte(message.Content), &result); err != nil {
				t.Errorf("invalid tool result %s: %v", id, err)
			}
			return result
		}
	}
	t.Errorf("missing tool result %s", id)
	return nil
}

func dnsHTTPRecoveryCall(id, operation string, parameters map[string]any) aiops.ToolCall {
	name := "panel_query"
	args := map[string]any{"operation": operation, "parameters": parameters}
	if operation == "dns.record_save" {
		name = "propose_panel_operation"
		args["reason"] = "isolated DNS recovery fixture"
	}
	raw, _ := json.Marshal(args)
	return aiops.ToolCall{ID: id, Type: "function", Function: aiops.ToolFunction{Name: name, Arguments: string(raw)}}
}

func dnsHTTPRecoveryReply(w http.ResponseWriter, calls ...aiops.ToolCall) {
	message := map[string]any{"role": "assistant", "content": "已完成本轮只读核对或明确的参数修正。"}
	if len(calls) > 0 {
		message = map[string]any{"role": "assistant", "tool_calls": calls}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message}}})
}

func dnsHTTPRecoveryWait(t *testing.T, ai *aiops.Service, actor, session int64) aiops.SessionDetail {
	t.Helper()
	for deadline := time.Now().Add(12 * time.Second); ; {
		detail, err := ai.GetSession(context.Background(), actor, session)
		if err != nil {
			t.Fatal(err)
		}
		if len(detail.Turns) > 0 && detail.Turns[0].Status != "running" && detail.Turns[0].Status != "queued" && detail.Session.Status != "running" {
			return detail
		}
		if time.Now().After(deadline) {
			t.Fatalf("DNS HTTP recovery flow timed out: %+v", detail)
		}
		time.Sleep(15 * time.Millisecond)
	}
}

func TestPanelDNSRecoveryHTTPPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_DNS_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_AIOPS_DNS_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var userCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM v2_user`).Scan(&userCount); err != nil || userCount != 0 {
		t.Fatal("requires an isolated empty DNS test database", userCount, err)
	}
	var actorID int64
	marker := fmt.Sprint(time.Now().UnixNano())
	if err := db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at) VALUES($1,'fixture','fixture',$2,1,1,1) RETURNING id`, "dns-http-"+marker+"@example.test", "dns-http-"+marker).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_user WHERE id=$1`, actorID)
	ctx := context.Background()
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprintf("unknown=%v", unknown), func(t *testing.T) {
			service := &dnsHTTPRecoveryAdmin{fakeAdminService: &fakeAdminService{}, unknown: unknown}
			cfg := config.Config{AdminPath: "dns-recovery-test", AppKey: "isolated-dns-recovery-app-key"}
			ai := aiops.NewService(cfg, db, admin.NewDBService(cfg, db))
			t.Cleanup(func() {
				ai.Close()
				if err := ai.ResetData(ctx, actorID); err != nil {
					t.Error("clean DNS AI fixture", err)
				}
			})
			NewRouter(cfg, WithAIOps(ai), WithAdminService(service))
			initial := map[string]any{"domain": "example.test", "domain_id": 6440564, "record_type": "A", "sub_domain": "new-entry", "record_line": "Unavailable", "record_line_id": "9=9", "value": "192.0.2.132", "ttl": 600}
			corrected := map[string]any{"domain": "example.test", "domain_id": 6440564, "domain_grade": "DPG_Ultimate", "record_type": "A", "sub_domain": "new-entry", "record_line": "Global", "record_line_id": "3=0", "value": "192.0.2.132", "ttl": 600}
			stale := map[string]any{"domain": "example.test", "record_type": "A", "sub_domain": "must-not-run", "value": "192.0.2.133"}
			query := map[string]any{"domain": "example.test", "domain_id": 6440564, "subdomain": "new-entry"}
			var rounds atomic.Int32
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req dnsHTTPRecoveryRelayRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error("invalid relay request", err)
					http.Error(w, "bad request", http.StatusBadRequest)
					return
				}
				for _, message := range req.Messages {
					if strings.Contains(message.Content, "PRIVATE_DNS_SENTINEL") {
						t.Error("raw DNS provider cause reached model")
					}
				}
				switch rounds.Add(1) {
				case 1:
					dnsHTTPRecoveryReply(w, dnsHTTPRecoveryCall("first", "dns.record_save", initial), dnsHTTPRecoveryCall("stale-batch", "dns.record_save", stale))
				case 2:
					first := dnsHTTPRecoveryToolResult(t, req, "first")
					if unknown {
						if first["status"] != "unknown" || !strings.Contains(fmt.Sprint(first["recovery_instruction"]), "只读") {
							t.Error("unknown HTTP write did not enter readonly recovery", first)
						}
					} else if first["status"] != "failed" || !strings.Contains(fmt.Sprint(first["recovery_instruction"]), "未执行写入") {
						t.Error("typed preflight did not survive HTTP/action recovery", first)
					}
					if got := dnsHTTPRecoveryToolResult(t, req, "stale-batch"); got["status"] != "skipped" {
						t.Error("stale same-batch mutation was not skipped", got)
					}
					if unknown {
						dnsHTTPRecoveryReply(w, dnsHTTPRecoveryCall("blocked-retry", "dns.record_save", corrected), dnsHTTPRecoveryCall("verify", "dns.records", query))
					} else {
						dnsHTTPRecoveryReply(w, dnsHTTPRecoveryCall("verify", "dns.records", query), dnsHTTPRecoveryCall("lines", "dns.record_lines", map[string]any{"domain": "example.test", "domain_id": 6440564, "record_type": "A", "domain_grade": "DPG_Ultimate"}))
					}
				case 3:
					verified := dnsHTTPRecoveryToolResult(t, req, "verify")
					if verified["status"] == "error" || verified["status"] == "skipped" {
						t.Error("readonly DNS verification was blocked", verified)
					}
					if unknown {
						if got := dnsHTTPRecoveryToolResult(t, req, "blocked-retry"); got["code"] != "panel_recovery_read_only" {
							t.Error("unknown write was retried", got)
						}
						raw, _ := json.Marshal(verified)
						if !strings.Contains(string(raw), "808") || !strings.Contains(string(raw), "192.0.2.132") {
							t.Error("provider-side committed record was not returned by readonly query", string(raw))
						}
						dnsHTTPRecoveryReply(w)
					} else {
						lines := dnsHTTPRecoveryToolResult(t, req, "lines")
						raw, _ := json.Marshal(lines)
						if !strings.Contains(string(raw), "Global") || !strings.Contains(string(raw), "3=0") {
							t.Error("actual line data was not returned", string(raw))
						}
						dnsHTTPRecoveryReply(w, dnsHTTPRecoveryCall("corrected", "dns.record_save", corrected))
					}
				case 4:
					if unknown {
						t.Error("unknown flow unexpectedly requested another model round")
					}
					if got := dnsHTTPRecoveryToolResult(t, req, "corrected"); got["status"] != "succeeded" {
						t.Error("corrected DNS create did not succeed", got)
					}
					dnsHTTPRecoveryReply(w)
				default:
					t.Error("unexpected extra model request")
					dnsHTTPRecoveryReply(w)
				}
			}))
			defer relay.Close()
			if _, err := ai.SaveConfig(ctx, actorID, aiops.ConfigSaveRequest{Enabled: true, BaseURL: relay.URL, APIKey: "local-dns-fixture", Models: aiops.DefaultModels}); err != nil {
				t.Fatal(err)
			}
			session, err := ai.CreateSession(ctx, actorID, aiops.CreateSessionRequest{Model: aiops.DefaultModels[0]})
			if err != nil {
				t.Fatal(err)
			}
			if session.AccessMode != "full" {
				t.Fatal("fixture requires default full access", session)
			}
			if _, err := ai.SendMessage(ctx, actorID, aiops.SendMessageRequest{SessionID: session.ID, Model: aiops.DefaultModels[0], Content: "创建入口 DNS；前置校验失败时查询后修正，结果未知时仅核对。", RequestID: fmt.Sprintf("dns-http-%s-%v", marker, unknown), AccessMode: "full", AccessVersion: &session.AccessVersion}); err != nil {
				t.Fatal(err)
			}
			detail := dnsHTTPRecoveryWait(t, ai, actorID, session.ID)
			actions, err := ai.ListActions(ctx, actorID, session.ID)
			if err != nil {
				t.Fatal(err)
			}
			statuses := make([]string, 0, len(actions))
			for _, action := range actions {
				statuses = append(statuses, action.Status)
				if strings.Contains(action.Error+action.Output, "PRIVATE_DNS_SENTINEL") {
					t.Error("provider cause leaked into persisted action")
				}
			}
			sort.Strings(statuses)
			service.mu.Lock()
			defer service.mu.Unlock()
			if service.created != 1 || service.recordReads != 1 {
				t.Fatalf("unexpected DNS effects: created=%d record_reads=%d", service.created, service.recordReads)
			}
			if unknown {
				if detail.Turns[0].Status != "failed" || !strings.Contains(detail.Turns[0].Error, "待核对") || strings.Join(statuses, ",") != "unknown" || len(service.saves) != 1 || rounds.Load() != 3 || service.lineReads != 0 {
					t.Fatalf("unknown result not preserved: turn=%+v statuses=%v saves=%d rounds=%d line_reads=%d", detail.Turns[0], statuses, len(service.saves), rounds.Load(), service.lineReads)
				}
			} else if detail.Turns[0].Status != "completed" || strings.Join(statuses, ",") != "failed,succeeded" || len(service.saves) != 2 || rounds.Load() != 4 || service.lineReads != 1 {
				t.Fatalf("preflight recovery failed: turn=%+v statuses=%v saves=%d rounds=%d line_reads=%d", detail.Turns[0], statuses, len(service.saves), rounds.Load(), service.lineReads)
			}
		})
	}
}
