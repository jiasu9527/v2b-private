package admin

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestFormatClientEntryMonitorProgressHandlesEmptyAndTerminalRuns(t *testing.T) {
	text := formatClientEntryMonitorProgress(ClientEntryMonitorRun{Status: "completed", StartedAt: 100}, nil, time.Unix(101, 0))
	for _, want := range []string{"总项：0", "0/0", "状态：已完成", "耗时：1秒", "更新时间："} {
		if !strings.Contains(text, want) {
			t.Fatalf("empty progress missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "#") {
		t.Fatalf("progress leaks internal identifier: %s", text)
	}
}

func TestFormatClientEntryMonitorProgressShowsProbeBatchesAndLatestFailures(t *testing.T) {
	pairs := []clientEntryMonitorRunPair{
		{TargetID: 1, ProbeID: 1, PolicyID: 1, PolicyName: "华东", ProbeName: "东京"},
		{TargetID: 2, ProbeID: 1, PolicyID: 1, PolicyName: "华东", ProbeName: "东京"},
		{TargetID: 1, ProbeID: 2, PolicyID: 2, PolicyName: "华南", ProbeName: "新加坡"},
		{TargetID: 2, ProbeID: 2, PolicyID: 2, PolicyName: "华南", ProbeName: "新加坡"},
	}
	run := ClientEntryMonitorRun{Status: "running", ExpectedResults: 4, ReceivedResults: 2, StartedAt: 100, Results: []ClientEntryMonitorRunResult{
		{TargetName: "较早", ProbeID: 1, ProbeName: "东京", Success: false, Error: "timeout", ReportedAt: 101},
		{TargetName: "较新", ProbeID: 2, ProbeName: "新加坡", Success: false, Error: "refused", ReportedAt: 102},
	}}
	text := formatClientEntryMonitorProgress(run, pairs, time.Unix(125, 0))
	for _, want := range []string{"规则组：华东、华南", "目标：2 · 探针：2 · 总项：4", "2/4（50%）", "待返回：2", "东京：1/2", "新加坡：1/2", "较新 / 新加坡：refused", "耗时：25秒"} {
		if !strings.Contains(text, want) {
			t.Fatalf("progress missing %q: %s", want, text)
		}
	}
	if strings.Index(text, "较新 / 新加坡") > strings.Index(text, "较早 / 东京") {
		t.Fatalf("failures are not newest-first: %s", text)
	}

	run.Status = "timeout"
	text = formatClientEntryMonitorProgress(run, pairs, time.Unix(130, 0))
	if !strings.Contains(text, "状态：已超时") {
		t.Fatalf("timeout progress = %s", text)
	}
}

func TestFormatClientEntryMonitorProgressFoldsLargeProbeSets(t *testing.T) {
	for _, count := range []int64{100, 500} {
		pairs := make([]clientEntryMonitorRunPair, 0, count)
		for probeID := int64(1); probeID <= count; probeID++ {
			pairs = append(pairs, clientEntryMonitorRunPair{TargetID: probeID, ProbeID: probeID, PolicyID: 1, PolicyName: "批量规则", ProbeName: "探针" + formatClientEntryMonitorProgressDuration(probeID)})
		}
		text := formatClientEntryMonitorProgress(ClientEntryMonitorRun{Status: "running", ExpectedResults: count, StartedAt: 100}, pairs, time.Unix(101, 0))
		folded := fmt.Sprintf("其余 %d 个探针已折叠", count-int64(clientEntryMonitorProgressProbeLimit))
		if len([]rune(text)) > clientEntryMonitorProgressMessageMax || !strings.Contains(text, folded) {
			t.Fatalf("count=%d large progress length=%d text=%s", count, len([]rune(text)), text)
		}
	}
}

func TestFormatClientEntryMonitorProgressUsesAggregateStatsWhenDetailsAreTruncated(t *testing.T) {
	pairs := []clientEntryMonitorRunPair{
		{TargetID: 1, ProbeID: 1, PolicyID: 1, PolicyName: "批量规则", ProbeName: "东京"},
		{TargetID: 2, ProbeID: 1, PolicyID: 1, PolicyName: "批量规则", ProbeName: "东京"},
		{TargetID: 3, ProbeID: 1, PolicyID: 1, PolicyName: "批量规则", ProbeName: "东京"},
	}
	run := ClientEntryMonitorRun{
		Status: "completed", ExpectedResults: 3, ReceivedResults: 3, ResultsTruncated: true,
		ResultStatsLoaded: true, SuccessfulResults: 2, FailedResults: 1,
		Results: []ClientEntryMonitorRunResult{{TargetID: 1, ProbeID: 1, ProbeName: "东京", Success: false}},
	}
	text := formatClientEntryMonitorProgress(run, pairs, time.Unix(100, 0))
	for _, want := range []string{"正常：2", "异常：1", "东京：至少 1/3", "结果明细仅展示最近 1/3 项"} {
		if !strings.Contains(text, want) {
			t.Fatalf("truncated progress missing %q: %s", want, text)
		}
	}
}
