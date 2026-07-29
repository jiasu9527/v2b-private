package admin

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const clientEntryMonitorProgressFailureLimit = 3

const (
	clientEntryMonitorProgressProbeLimit = 12
	clientEntryMonitorProgressMessageMax = 3800
)

// formatClientEntryMonitorProgress is intentionally compact enough for a
// Telegram message edit while retaining enough detail to diagnose a slow or
// failing on-demand run without opening the admin panel.
func formatClientEntryMonitorProgress(run ClientEntryMonitorRun, pairs []clientEntryMonitorRunPair, now time.Time) string {
	groupNames := make(map[int64]string)
	targets := make(map[int64]struct{})
	probes := make(map[int64]string)
	probeExpected := make(map[int64]int64)
	for _, pair := range pairs {
		if pair.PolicyID > 0 {
			name := strings.TrimSpace(pair.PolicyName)
			if name == "" {
				name = "未命名规则组"
			}
			groupNames[pair.PolicyID] = name
		}
		if pair.TargetID > 0 {
			targets[pair.TargetID] = struct{}{}
		}
		if pair.ProbeID > 0 {
			name := strings.TrimSpace(pair.ProbeName)
			if name == "" {
				name = "未命名探针"
			}
			probes[pair.ProbeID] = name
			probeExpected[pair.ProbeID]++
		}
	}
	groupList := orderedClientEntryMonitorProgressNames(groupNames)
	if len(groupList) == 0 {
		groupList = []string{"已选规则组"}
	}

	received := run.ReceivedResults
	if received < int64(len(run.Results)) {
		received = int64(len(run.Results))
	}
	expected := run.ExpectedResults
	if expected < received {
		expected = received
	}
	normal, abnormal := int64(0), int64(0)
	probeReceived := make(map[int64]int64)
	failures := make([]ClientEntryMonitorRunResult, 0)
	for _, result := range run.Results {
		probeReceived[result.ProbeID]++
		if result.Success {
			normal++
		} else {
			abnormal++
			failures = append(failures, result)
		}
	}
	if run.ResultStatsLoaded {
		normal = run.SuccessfulResults
		abnormal = run.FailedResults
	}
	pending := expected - received
	if pending < 0 {
		pending = 0
	}

	var builder strings.Builder
	builder.WriteString("🧭 用户入口主动检测")
	fmt.Fprintf(&builder, "\n规则组：%s", truncateClientEntryMonitorProgressText(strings.Join(groupList, "、"), 180))
	fmt.Fprintf(&builder, "\n目标：%d · 探针：%d · 总项：%d", len(targets), len(probes), expected)
	fmt.Fprintf(&builder, "\n进度：%s %d/%d", clientEntryMonitorProgressBar(received, expected, 12), received, expected)
	if expected > 0 {
		fmt.Fprintf(&builder, "（%d%%）", received*100/expected)
	}
	fmt.Fprintf(&builder, "\n🟢 正常：%d · 🔴 异常：%d · 🟡 待返回：%d", normal, abnormal, pending)

	probeIDs := make([]int64, 0, len(probes))
	for probeID := range probes {
		probeIDs = append(probeIDs, probeID)
	}
	sort.Slice(probeIDs, func(i, j int) bool { return probes[probeIDs[i]] < probes[probeIDs[j]] })
	if len(probeIDs) > 0 {
		builder.WriteString("\n\n探针进度")
		for index, probeID := range probeIDs {
			if index >= clientEntryMonitorProgressProbeLimit {
				fmt.Fprintf(&builder, "\n• 其余 %d 个探针已折叠", len(probeIDs)-index)
				break
			}
			if run.ResultsTruncated {
				fmt.Fprintf(&builder, "\n• %s：至少 %d/%d", truncateClientEntryMonitorProgressText(probes[probeID], 48), probeReceived[probeID], probeExpected[probeID])
			} else {
				fmt.Fprintf(&builder, "\n• %s：%d/%d", truncateClientEntryMonitorProgressText(probes[probeID], 48), probeReceived[probeID], probeExpected[probeID])
			}
		}
	}
	sort.SliceStable(failures, func(i, j int) bool { return failures[i].ReportedAt > failures[j].ReportedAt })
	if len(failures) > 0 {
		builder.WriteString("\n\n最近异常")
		for index, failure := range failures {
			if index >= clientEntryMonitorProgressFailureLimit {
				break
			}
			detail := strings.TrimSpace(failure.Error)
			if detail == "" {
				detail = "连接失败"
			}
			name := strings.TrimSpace(failure.TargetName)
			if name == "" {
				name = failure.Host
			}
			fmt.Fprintf(&builder, "\n• %s / %s：%s", truncateClientEntryMonitorProgressText(name, 42), truncateClientEntryMonitorProgressText(failure.ProbeName, 32), truncateClientEntryMonitorProgressText(detail, 96))
		}
	}
	if run.StartedAt > 0 {
		elapsed := now.Unix() - run.StartedAt
		if elapsed < 0 {
			elapsed = 0
		}
		fmt.Fprintf(&builder, "\n\n耗时：%s · 状态：%s", formatClientEntryMonitorProgressDuration(elapsed), clientEntryMonitorRunStatusText(run.Status))
	} else {
		fmt.Fprintf(&builder, "\n\n状态：%s", clientEntryMonitorRunStatusText(run.Status))
	}
	if received > int64(len(run.Results)) {
		fmt.Fprintf(&builder, "\n结果明细仅展示最近 %d/%d 项。", len(run.Results), received)
	}
	builder.WriteString("\n更新时间：" + now.Format("15:04:05"))
	return truncateClientEntryMonitorProgressText(builder.String(), clientEntryMonitorProgressMessageMax)
}

func orderedClientEntryMonitorProgressNames(values map[int64]string) []string {
	ids := make([]int64, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		result = append(result, values[id])
	}
	return result
}

func clientEntryMonitorProgressBar(done, total int64, width int) string {
	if width <= 0 {
		width = 12
	}
	filled := 0
	if total > 0 {
		filled = int(done * int64(width) / total)
		if filled > width {
			filled = width
		}
	}
	return "[" + strings.Repeat("■", filled) + strings.Repeat("□", width-filled) + "]"
}

func formatClientEntryMonitorProgressDuration(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%d秒", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%d分%02d秒", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%d时%02d分", seconds/3600, (seconds%3600)/60)
}

func truncateClientEntryMonitorProgressText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if limit <= 0 || len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit-1]) + "…"
}
