package admin

import (
	"bytes"
	"context"
	"image/png"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"golang.org/x/image/font"
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

func TestRenderClientEntryMonitorRunImageShowsPoliciesWithoutResults(t *testing.T) {
	run := ClientEntryMonitorRun{
		Status:          "running",
		ExpectedResults: 3,
		ExpectedPairs: []clientEntryMonitorRunPair{
			{PolicyID: 11, PolicyName: "华东入口组", TargetID: 1, ProbeID: 1},
			{PolicyID: 11, PolicyName: "华东入口组", TargetID: 2, ProbeID: 1},
			{PolicyID: 22, PolicyName: "备用入口组", TargetID: 3, ProbeID: 1},
		},
	}
	if summary := summarizeClientEntryMonitorRun(run); summary.UsesExpectedSnapshot {
		t.Fatalf("incomplete historical pairs unexpectedly used as a complete snapshot: %#v", summary)
	}

	policies := clientEntryMonitorRunImagePolicies(run)
	rows := appendClientEntryMonitorRunMissingPolicyRows(nil, run, policies)
	if len(rows) != 2 {
		t.Fatalf("placeholder rows = %#v, want one row per policy", rows)
	}
	if rows[0].PolicyName != "华东入口组" || rows[1].PolicyName != "备用入口组" {
		t.Fatalf("placeholder policy names = %#v", rows)
	}
	if rows[0].Target != "等待探针返回" || rows[0].Detail != "0/2" || rows[1].Detail != "0/1" {
		t.Fatalf("placeholder progress = %#v", rows)
	}

	imageBytes, _, err := renderClientEntryMonitorRunImage(run)
	if err != nil {
		t.Fatalf("renderClientEntryMonitorRunImage: %v", err)
	}
	decoded, err := png.Decode(bytes.NewReader(imageBytes))
	if err != nil {
		t.Fatalf("decode PNG: %v", err)
	}
	wantHeight := clientEntryMonitorImageHeaderHeight + 3*clientEntryMonitorImageRowHeight + clientEntryMonitorImageFooterHeight
	if got := decoded.Bounds().Dy(); got != wantHeight {
		t.Fatalf("image height = %d, want %d for two policy rows", got, wantHeight)
	}
}

func TestClientEntryMonitorRunSummaryKeepsPartialTargetWaiting(t *testing.T) {
	run := ClientEntryMonitorRun{
		Status:          "timeout",
		ExpectedResults: 3,
		ReceivedResults: 1,
		ExpectedPairs: []clientEntryMonitorRunPair{
			{PolicyID: 11, PolicyName: "华东入口组", TargetID: 101, TargetName: "上海入口", Host: "entry.example.com", Port: 443, ProbeID: 1, ProbeName: "上海探针"},
			{PolicyID: 11, PolicyName: "华东入口组", TargetID: 101, TargetName: "上海入口", Host: "entry.example.com", Port: 443, ProbeID: 2, ProbeName: "东京探针"},
			{PolicyID: 11, PolicyName: "华东入口组", TargetID: 101, TargetName: "上海入口", Host: "entry.example.com", Port: 443, ProbeID: 3, ProbeName: "新加坡探针"},
		},
		Results: []ClientEntryMonitorRunResult{
			{PolicyID: 11, PolicyName: "华东入口组", TargetID: 101, TargetName: "上海入口", Host: "entry.example.com", Port: 443, ProbeID: 1, ProbeName: "上海探针", Success: true, ReportedAt: 100},
		},
	}

	summary := summarizeClientEntryMonitorRun(run)
	if !summary.UsesExpectedSnapshot {
		t.Fatalf("complete expected pairs did not use snapshot summary: %#v", summary)
	}
	if len(summary.Targets) != 1 || summary.ProbeCount != 3 || summary.Missing != 2 {
		t.Fatalf("summary = %#v, want one target, three probes and two missing results", summary)
	}
	target := summary.Targets[0]
	if target.Total != 3 || target.Success != 1 || target.Unknown != 2 || target.Failure != 0 {
		t.Fatalf("partial target = %#v, want success 1/3 with two unknown probes", target)
	}

	rows := clientEntryMonitorRunImageRows(summary.Targets)
	if len(rows) != 1 || rows[0].Status != "等待" || rows[0].Detail != "1/3" {
		t.Fatalf("partial image rows = %#v", rows)
	}
	if !strings.Contains(rows[0].Probe, "东京探针") || !strings.Contains(rows[0].Probe, "新加坡探针") {
		t.Fatalf("partial image probe detail = %q, want both pending probes", rows[0].Probe)
	}

	caption := formatClientEntryMonitorRunReport(run)
	for _, want := range []string{"目标：1", "探针：3", "待返回目标：1", "未返回：2"} {
		if !strings.Contains(caption, want) {
			t.Fatalf("partial report missing %q: %s", want, caption)
		}
	}
	if strings.Contains(caption, "正常目标已折叠") {
		t.Fatalf("partial report incorrectly marks target healthy: %s", caption)
	}

	truncated := run
	truncated.ReceivedResults = 2
	if summary := summarizeClientEntryMonitorRun(truncated); summary.UsesExpectedSnapshot {
		t.Fatalf("truncated result details unexpectedly used as a complete snapshot: %#v", summary)
	}
}

func TestClientEntryMonitorRunSummaryUsesSnapshotWithZeroResults(t *testing.T) {
	run := ClientEntryMonitorRun{
		Status:          "running",
		ExpectedResults: 4,
		ExpectedPairs: []clientEntryMonitorRunPair{
			{PolicyID: 11, PolicyName: "华东入口组", TargetID: 101, TargetName: "上海入口", Host: "sh.example.com", Port: 443, ProbeID: 1, ProbeName: "上海探针"},
			{PolicyID: 11, PolicyName: "华东入口组", TargetID: 101, TargetName: "上海入口", Host: "sh.example.com", Port: 443, ProbeID: 2, ProbeName: "东京探针"},
			{PolicyID: 22, PolicyName: "备用入口组", TargetID: 202, TargetName: "香港入口", Host: "hk.example.com", Port: 8443, ProbeID: 1, ProbeName: "上海探针"},
			{PolicyID: 22, PolicyName: "备用入口组", TargetID: 202, TargetName: "香港入口", Host: "hk.example.com", Port: 8443, ProbeID: 2, ProbeName: "东京探针"},
		},
	}
	mismatched := run
	mismatched.ExpectedResults = 0
	if summary := summarizeClientEntryMonitorRun(mismatched); summary.UsesExpectedSnapshot {
		t.Fatalf("mismatched expected result count unexpectedly used as a complete snapshot: %#v", summary)
	}

	summary := summarizeClientEntryMonitorRun(run)
	if !summary.UsesExpectedSnapshot {
		t.Fatalf("zero-result run did not use complete snapshot: %#v", summary)
	}
	if len(summary.Targets) != 2 || summary.ProbeCount != 2 || summary.Missing != 4 {
		t.Fatalf("summary = %#v, want two targets, two probes and four missing results", summary)
	}
	rows := clientEntryMonitorRunImageRows(summary.Targets)
	if len(rows) != 2 {
		t.Fatalf("zero-result image rows = %#v, want two target rows", rows)
	}
	policyNames := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row.Status != "等待" || row.Detail != "0/2" {
			t.Fatalf("zero-result image row = %#v, want waiting 0/2", row)
		}
		policyNames[row.PolicyName] = struct{}{}
	}
	if _, ok := policyNames["华东入口组"]; !ok {
		t.Fatalf("zero-result rows missing policy 华东入口组: %#v", rows)
	}
	if _, ok := policyNames["备用入口组"]; !ok {
		t.Fatalf("zero-result rows missing policy 备用入口组: %#v", rows)
	}

	caption := formatClientEntryMonitorRunReport(run)
	for _, want := range []string{"目标：2", "探针：2", "待返回目标：2", "未返回：4"} {
		if !strings.Contains(caption, want) {
			t.Fatalf("zero-result report missing %q: %s", want, caption)
		}
	}
}

func TestClientEntryMonitorRunSummaryUsesFullStatsWithTruncatedDetails(t *testing.T) {
	run := ClientEntryMonitorRun{
		ExpectedResults:   300,
		ReceivedResults:   300,
		ResultsTruncated:  true,
		ResultStatsLoaded: true,
		SuccessfulResults: 260,
		FailedResults:     40,
		ResultTargetCount: 150,
		FailedTargetCount: 12,
		ResultProbeCount:  6,
		ExpectedPairs: []clientEntryMonitorRunPair{
			{TargetID: 1, ProbeID: 1},
			{TargetID: 2, ProbeID: 2},
		},
		Results: []ClientEntryMonitorRunResult{
			{TargetID: 1, ProbeID: 1, Success: false},
		},
	}

	summary := summarizeClientEntryMonitorRun(run)
	if summary.UsesExpectedSnapshot {
		t.Fatal("truncated details unexpectedly used the per-pair snapshot")
	}
	if summary.Normal != 260 || summary.Abnormal != 40 || summary.TargetCount != 150 || summary.FailedTargetCount != 12 || summary.ProbeCount != 6 {
		t.Fatalf("truncated aggregate summary = %#v", summary)
	}
	report := formatClientEntryMonitorRunReport(run)
	for _, want := range []string{"目标：150", "探针：6", "异常目标：12", "正常：260", "异常：40"} {
		if !strings.Contains(report, want) {
			t.Fatalf("truncated report missing %q: %s", want, report)
		}
	}
}

func TestClientEntryMonitorRunImageAddsOnlyMissingPolicyRows(t *testing.T) {
	run := ClientEntryMonitorRun{
		ExpectedPairs: []clientEntryMonitorRunPair{
			{PolicyID: 11, PolicyName: "华东入口组", TargetID: 1, ProbeID: 1},
			{PolicyID: 22, PolicyName: "备用入口组", TargetID: 2, ProbeID: 1},
		},
		Results: []ClientEntryMonitorRunResult{
			{PolicyID: 11, PolicyName: "华东入口组", TargetID: 1, ProbeID: 1, Success: true},
		},
	}
	rows := []clientEntryMonitorImageRow{{Status: "正常", PolicyName: "华东入口组"}}
	rows = appendClientEntryMonitorRunMissingPolicyRows(rows, run, clientEntryMonitorRunImagePolicies(run))
	if len(rows) != 2 || rows[1].PolicyName != "备用入口组" || rows[1].Status != "等待" {
		t.Fatalf("partial result rows = %#v", rows)
	}
}

func TestClientEntryMonitorImageTextRendererKeepsChineseAndReplacesMissingGlyphs(t *testing.T) {
	renderer, err := newClientEntryMonitorImageTextRenderer()
	if err != nil {
		t.Fatalf("newClientEntryMonitorImageTextRenderer: %v", err)
	}
	t.Cleanup(func() { _ = renderer.Close() })
	face, err := renderer.face(clientEntryMonitorImageBodyFontSize)
	if err != nil {
		t.Fatalf("renderer.face: %v", err)
	}

	value := "规则组：上海入口😀"
	got := renderer.truncate(value, 1000, face)
	if !strings.Contains(got, "规则组：上海入口") {
		t.Fatalf("Chinese text was not retained: %q", got)
	}
	if strings.ContainsAny(got, "😀?□") {
		t.Fatalf("missing emoji was not safely omitted: %q", got)
	}
}

func TestClientEntryMonitorImageTextRendererTruncatesByPixelWidth(t *testing.T) {
	renderer, err := newClientEntryMonitorImageTextRenderer()
	if err != nil {
		t.Fatalf("newClientEntryMonitorImageTextRenderer: %v", err)
	}
	t.Cleanup(func() { _ = renderer.Close() })
	face, err := renderer.face(clientEntryMonitorImageBodyFontSize)
	if err != nil {
		t.Fatalf("renderer.face: %v", err)
	}

	const maxWidth = 100
	got := renderer.truncate(strings.Repeat("宽窄", 24), maxWidth, face)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated text = %q, want ellipsis", got)
	}
	if width := font.MeasureString(face, got).Ceil(); width > maxWidth {
		t.Fatalf("truncated text width = %d, limit = %d", width, maxWidth)
	}
}

func TestImageTargetTextOmitsRepeatedPolicyPrefix(t *testing.T) {
	target := &clientEntryMonitorReportTarget{
		PolicyName: "华东规则组",
		Name:       "华东规则组 · 上海入口",
		Host:       "entry.example.com",
		Port:       443,
	}
	if got, want := imageTargetText(target), "上海入口 · entry.example.com:443"; got != want {
		t.Fatalf("imageTargetText = %q, want %q", got, want)
	}
}

func TestListClientEntryMonitorRunsQueryOrdersFailuresFirst(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	mock.ExpectQuery(`(?s)SELECT id, policy_ids, expected_pairs, status, expected_results,.*FROM v2_client_entry_monitor_run WHERE status = 'running' OR created_at >= \$1\s+ORDER BY id DESC LIMIT \$2`).
		WithArgs(sqlmock.AnyArg(), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "policy_ids", "expected_pairs", "status", "expected_results", "received_results",
			"progress_message_id", "progress_reported_results", "progress_reported_status",
			"progress_next_attempt_at", "progress_last_error",
			"started_at", "completed_at", "created_at",
		}).AddRow(int64(91), `[12]`, `[{"target_id":5,"probe_id":7,"target_version":1,"policy_id":12,"policy_name":"华东规则组","probe_name":"probe"}]`, "completed", int64(2), int64(2), nil, int64(2), "completed", int64(0), "", int64(100), int64(101), int64(100)))

	resultRows := sqlmock.NewRows([]string{
		"id", "run_id", "policy_id", "policy_name", "target_id", "target_name", "host", "port", "probe_id", "probe_name",
		"success", "latency_ms", "error", "resolved_ip", "reported_at",
	}).AddRow(int64(1), int64(91), int64(12), "华东规则组", int64(5), "入口", "entry.example.com", int64(443), int64(7), "probe",
		int64(0), nil, "timeout", "203.0.113.1", int64(101)).
		AddRow(int64(2), int64(91), int64(12), "华东规则组", int64(6), "入口 2", "entry2.example.com", int64(443), int64(7), "probe",
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
	if len(runs[0].ExpectedPairs) != 1 || runs[0].ExpectedPairs[0].PolicyName != "华东规则组" {
		t.Fatalf("expected pairs = %#v", runs[0].ExpectedPairs)
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
