package admin

import (
	"bytes"
	"context"
	"image/png"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestClientEntryMonitorReportAggregatesTargetProbeFailures(t *testing.T) {
	results := []ClientEntryMonitorRunResult{
		{TargetID: 12, TargetName: "目标 A", Host: "entry-a.example.com", Port: 443, ProbeID: 1, ProbeName: "probe-a", Success: false, Error: "timeout", ReportedAt: 100},
		{TargetID: 12, TargetName: "目标 A", Host: "entry-a.example.com", Port: 443, ProbeID: 1, ProbeName: "probe-a", Success: false, Error: "timeout", ReportedAt: 101},
		{TargetID: 12, TargetName: "目标 A", Host: "entry-a.example.com", Port: 443, ProbeID: 2, ProbeName: "probe-b", Success: true, ReportedAt: 102},
		{TargetID: 13, TargetName: "目标 B", Host: "entry-b.example.com", Port: 8443, ProbeID: 1, ProbeName: "probe-a", Success: true, ReportedAt: 103},
	}

	targets, normal, abnormal, probeCount := summarizeClientEntryMonitorRunResults(results)
	if len(targets) != 2 {
		t.Fatalf("target count = %d, want 2", len(targets))
	}
	if targets[0].Failure != 2 || targets[0].Total != 3 || targets[0].Success != 1 {
		t.Fatalf("aggregated failed target = %#v", targets[0])
	}
	if targets[1].Failure != 0 || targets[1].Total != 1 || targets[1].Success != 1 {
		t.Fatalf("aggregated healthy target = %#v", targets[1])
	}
	if normal != 2 || abnormal != 2 || probeCount != 2 {
		t.Fatalf("summary counts = normal %d abnormal %d probes %d", normal, abnormal, probeCount)
	}

	report := formatClientEntryMonitorRunReport(ClientEntryMonitorRun{
		ID: 7, Status: "completed", ExpectedResults: 4, ReceivedResults: 4, Results: results,
	})
	for _, want := range []string{"异常目标：1", "失败 2/3", "×2", "正常目标已折叠：1 个"} {
		if !strings.Contains(report, want) {
			t.Fatalf("aggregated report missing %q: %s", want, report)
		}
	}
	if got := strings.Count(report, "\n• 目标 A ·"); got != 1 {
		t.Fatalf("failed target lines = %d, want 1", got)
	}
}

func TestRenderClientEntryMonitorRunImageReturnsPNG(t *testing.T) {
	latency := int64(42)
	run := ClientEntryMonitorRun{
		ID:              9,
		Status:          "completed",
		ExpectedResults: 2,
		ReceivedResults: 2,
		Results: []ClientEntryMonitorRunResult{
			{TargetID: 1, TargetName: "target-a", Host: "entry-a.example.com", Port: 443, ProbeID: 1, ProbeName: "probe-a", Success: false, Error: "timeout", ReportedAt: 100},
			{TargetID: 2, TargetName: "target-b", Host: "entry-b.example.com", Port: 443, ProbeID: 1, ProbeName: "probe-a", Success: true, LatencyMS: &latency, ReportedAt: 101},
		},
	}

	imageBytes, caption, err := renderClientEntryMonitorRunImage(run)
	if err != nil {
		t.Fatalf("renderClientEntryMonitorRunImage: %v", err)
	}
	if !bytes.HasPrefix(imageBytes, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		t.Fatalf("image does not have PNG signature: %x", imageBytes[:minTestInt(len(imageBytes), 8)])
	}
	decoded, err := png.Decode(bytes.NewReader(imageBytes))
	if err != nil {
		t.Fatalf("decode PNG: %v", err)
	}
	if got := decoded.Bounds().Dx(); got != clientEntryMonitorImageWidth {
		t.Fatalf("image width = %d, want %d", got, clientEntryMonitorImageWidth)
	}
	if decoded.Bounds().Dy() <= clientEntryMonitorImageHeaderHeight {
		t.Fatalf("image height = %d, want table rows", decoded.Bounds().Dy())
	}
	if !strings.Contains(caption, "异常目标") || !strings.Contains(caption, "target-a") {
		t.Fatalf("image caption = %q", caption)
	}
}

func TestListClientEntryMonitorRunsQueryOrdersFailuresFirst(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	mock.ExpectQuery(`(?s)SELECT id, policy_ids, status, expected_results,.*FROM v2_client_entry_monitor_run WHERE status = 'running' OR created_at >= \$1\s+ORDER BY id DESC LIMIT \$2`).
		WithArgs(sqlmock.AnyArg(), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "policy_ids", "status", "expected_results", "received_results",
			"started_at", "completed_at", "created_at",
		}).AddRow(int64(91), `[12]`, "completed", int64(2), int64(2), int64(100), int64(101), int64(100)))

	resultRows := sqlmock.NewRows([]string{
		"id", "run_id", "target_id", "target_name", "host", "port", "probe_id", "probe_name",
		"success", "latency_ms", "error", "resolved_ip", "reported_at",
	}).AddRow(int64(1), int64(91), int64(5), "入口", "entry.example.com", int64(443), int64(7), "probe",
		int64(0), nil, "timeout", "203.0.113.1", int64(101)).
		AddRow(int64(2), int64(91), int64(6), "入口 2", "entry2.example.com", int64(443), int64(7), "probe",
			int64(1), int64(12), "", "203.0.113.2", int64(101))
	mock.ExpectQuery(`(?s)WITH ranked_results AS \(.*ROW_NUMBER\(\) OVER \(PARTITION BY result\.run_id ORDER BY result\.success ASC, result\.target_id, result\.probe_id\).*WHERE result_rank <= \$2.*ORDER BY run_id DESC, success ASC, target_id, probe_id`).
		WithArgs(int64(91), clientEntryMonitorRunResultListLimit).
		WillReturnRows(resultRows)

	runs, err := service.ListClientEntryMonitorRuns(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListClientEntryMonitorRuns: %v", err)
	}
	if len(runs) != 1 || len(runs[0].Results) != 2 {
		t.Fatalf("runs = %#v", runs)
	}
	if runs[0].Results[0].Success {
		t.Fatalf("first result = %#v, want failure-first ordering", runs[0].Results[0])
	}
	if !runs[0].Results[1].Success {
		t.Fatalf("second result = %#v, want healthy result", runs[0].Results[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func minTestInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
