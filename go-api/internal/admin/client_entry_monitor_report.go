package admin

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	clientEntryMonitorReportFailureLimit       = 20
	clientEntryMonitorReportFailureDetailLimit = 5
)

type clientEntryMonitorReportFailure struct {
	ProbeName string
	Detail    string
}

type clientEntryMonitorReportTarget struct {
	Key            string
	Name           string
	Host           string
	Port           int64
	Total          int
	Success        int
	Failure        int
	Stale          int
	Unknown        int
	LatestReported int64
	Failures       []clientEntryMonitorReportFailure
}

func formatClientEntryMonitorRunReport(run ClientEntryMonitorRun) string {
	resultTargets, normal, abnormal, probeCount := summarizeClientEntryMonitorRunResults(run.Results)
	totalResults := run.TotalResults
	if totalResults < run.ReceivedResults {
		totalResults = run.ReceivedResults
	}
	if totalResults < int64(len(run.Results)) {
		totalResults = int64(len(run.Results))
	}
	missing := run.ExpectedResults - run.ReceivedResults
	if missing < 0 {
		missing = 0
	}
	failedTargets := countClientEntryMonitorReportTargets(resultTargets, func(target *clientEntryMonitorReportTarget) bool {
		return target.Failure > 0
	})
	healthyTargets := countClientEntryMonitorReportTargets(resultTargets, func(target *clientEntryMonitorReportTarget) bool {
		return target.Total > 0 && target.Failure == 0 && target.Stale == 0 && target.Unknown == 0
	})

	var builder strings.Builder
	fmt.Fprintf(&builder, "用户入口一键检测 #%d\n状态：%s · 进度：%d/%d\n📊 目标：%d · 探针：%d · 异常目标：%d\n🟢 正常：%d · 🔴 异常：%d · 🟡 未返回：%d",
		run.ID, clientEntryMonitorRunStatusText(run.Status), run.ReceivedResults, run.ExpectedResults,
		len(resultTargets), probeCount, failedTargets, normal, abnormal, missing)
	if latest := latestClientEntryMonitorReportTime(resultTargets); latest > 0 {
		fmt.Fprintf(&builder, "\n最近上报：%s", compactClientEntryMonitorReportTime(latest))
	}

	if len(resultTargets) == 0 {
		builder.WriteString("\n\n暂无可用检测结果。")
		return builder.String()
	}
	if failedTargets > 0 {
		builder.WriteString("\n\n❌ 异常目标（仅展示异常）")
		appendClientEntryMonitorReportFailures(&builder, resultTargets)
		if failedTargets > clientEntryMonitorReportFailureLimit {
			fmt.Fprintf(&builder, "\n其余 %d 个异常目标请在后台查看。", failedTargets-clientEntryMonitorReportFailureLimit)
		}
	}
	if healthyTargets > 0 {
		fmt.Fprintf(&builder, "\n\n✅ 正常目标已折叠：%d 个", healthyTargets)
	}
	if run.ResultsTruncated || totalResults > int64(len(run.Results)) {
		fmt.Fprintf(&builder, "\n⚠️ 结果仅展示前 %d 条，完整统计以任务进度为准。", len(run.Results))
	}
	if failedTargets == 0 && healthyTargets == 0 {
		builder.WriteString("\n\n🟡 当前结果仍在等待或已过期，请稍后重试。")
	}
	return builder.String()
}

func formatClientEntryMonitorOverviewReport(overview ClientEntryMonitorOverview) string {
	targets, probeCount := summarizeClientEntryMonitorOverview(overview)
	normalTargets := countClientEntryMonitorReportTargets(targets, func(target *clientEntryMonitorReportTarget) bool {
		return target.Total > 0 && target.Failure == 0 && target.Stale == 0 && target.Unknown == 0
	})
	failedTargets := countClientEntryMonitorReportTargets(targets, func(target *clientEntryMonitorReportTarget) bool {
		return target.Failure > 0
	})
	staleTargets := countClientEntryMonitorReportTargets(targets, func(target *clientEntryMonitorReportTarget) bool {
		return target.Failure == 0 && (target.Stale > 0 || target.Unknown > 0)
	})

	var builder strings.Builder
	builder.WriteString("用户入口检测近期状态")
	fmt.Fprintf(&builder, "\n📊 目标：%d · 探针：%d · 正常：%d · 异常：%d · 过期/无数据：%d",
		len(targets), probeCount, normalTargets, failedTargets, staleTargets)
	if latest := latestClientEntryMonitorReportTime(targets); latest > 0 {
		fmt.Fprintf(&builder, "\n最近上报：%s", compactClientEntryMonitorReportTime(latest))
	}
	if len(targets) == 0 {
		builder.WriteString("\n\n暂无检测结果。")
		return builder.String()
	}
	if failedTargets > 0 {
		builder.WriteString("\n\n❌ 异常目标（仅展示异常）")
		appendClientEntryMonitorReportFailures(&builder, targets)
		if failedTargets > clientEntryMonitorReportFailureLimit {
			fmt.Fprintf(&builder, "\n其余 %d 个异常目标请在后台查看。", failedTargets-clientEntryMonitorReportFailureLimit)
		}
	}
	if normalTargets > 0 {
		fmt.Fprintf(&builder, "\n\n✅ 正常目标已折叠：%d 个", normalTargets)
	}
	if staleTargets > 0 {
		fmt.Fprintf(&builder, "\n🟡 过期/无数据目标已折叠：%d 个", staleTargets)
	}
	if failedTargets == 0 && normalTargets == 0 {
		builder.WriteString("\n\n🟡 暂无新鲜检测结果，请稍后重试。")
	}
	return builder.String()
}

func summarizeClientEntryMonitorRunResults(results []ClientEntryMonitorRunResult) ([]*clientEntryMonitorReportTarget, int, int, int) {
	targetsByKey := make(map[string]*clientEntryMonitorReportTarget)
	probeKeys := make(map[string]struct{})
	for _, result := range results {
		key := clientEntryMonitorReportResultKey(result.TargetID, result.TargetName, result.Host, result.Port)
		target := targetsByKey[key]
		if target == nil {
			target = &clientEntryMonitorReportTarget{Key: key, Name: compactClientEntryMonitorReportText(result.TargetName), Host: compactClientEntryMonitorReportText(result.Host), Port: result.Port}
			targetsByKey[key] = target
		}
		target.Total++
		if result.Success {
			target.Success++
		} else {
			target.Failure++
			target.Failures = append(target.Failures, clientEntryMonitorReportFailure{
				ProbeName: compactClientEntryMonitorReportText(result.ProbeName),
				Detail:    compactClientEntryMonitorReportText(result.Error),
			})
		}
		if result.ReportedAt > target.LatestReported {
			target.LatestReported = result.ReportedAt
		}
		probeKey := fmt.Sprintf("%d:%s", result.ProbeID, compactClientEntryMonitorReportText(result.ProbeName))
		probeKeys[probeKey] = struct{}{}
	}
	targets := sortedClientEntryMonitorReportTargets(targetsByKey)
	normal, abnormal := 0, 0
	for _, result := range results {
		if result.Success {
			normal++
		} else {
			abnormal++
		}
	}
	return targets, normal, abnormal, len(probeKeys)
}

func summarizeClientEntryMonitorOverview(overview ClientEntryMonitorOverview) ([]*clientEntryMonitorReportTarget, int) {
	targetsByKey := make(map[string]*clientEntryMonitorReportTarget)
	probeKeys := make(map[string]struct{})
	for _, monitor := range overview.Items {
		for targetIndex, targetState := range monitor.Targets {
			key := fmt.Sprintf("monitor:%d:target:%d", monitor.ID, targetState.ID)
			if targetState.ID <= 0 {
				key = fmt.Sprintf("monitor:%d:target:%d", monitor.ID, targetIndex)
			}
			target := &clientEntryMonitorReportTarget{
				Key:  key,
				Name: compactClientEntryMonitorReportText(monitor.PolicyName),
				Host: compactClientEntryMonitorReportText(targetState.Host),
				Port: targetState.Port,
			}
			targetsByKey[key] = target
			if len(targetState.States) == 0 {
				target.Total = 1
				target.Unknown = 1
				continue
			}
			for _, state := range targetState.States {
				target.Total++
				probeKeys[fmt.Sprintf("%d:%s", state.ProbeID, compactClientEntryMonitorReportText(state.ProbeName))] = struct{}{}
				if state.LastReportedAt != nil && *state.LastReportedAt > target.LatestReported {
					target.LatestReported = *state.LastReportedAt
				}
				if state.Stale {
					target.Stale++
					continue
				}
				if state.LastSuccess == nil {
					target.Unknown++
					continue
				}
				if *state.LastSuccess {
					target.Success++
					continue
				}
				target.Failure++
				target.Failures = append(target.Failures, clientEntryMonitorReportFailure{
					ProbeName: compactClientEntryMonitorReportText(state.ProbeName),
					Detail:    compactClientEntryMonitorReportText(state.LastError),
				})
			}
		}
	}
	probeCount := len(probeKeys)
	enabledProbeCount := 0
	for _, probe := range overview.Probes {
		if probe.Enabled {
			enabledProbeCount++
		}
	}
	if enabledProbeCount > probeCount {
		probeCount = enabledProbeCount
	}
	return sortedClientEntryMonitorReportTargets(targetsByKey), probeCount
}

func sortedClientEntryMonitorReportTargets(targetsByKey map[string]*clientEntryMonitorReportTarget) []*clientEntryMonitorReportTarget {
	targets := make([]*clientEntryMonitorReportTarget, 0, len(targetsByKey))
	for _, target := range targetsByKey {
		targets = append(targets, target)
	}
	sort.SliceStable(targets, func(i, j int) bool {
		leftFailed := targets[i].Failure > 0
		rightFailed := targets[j].Failure > 0
		if leftFailed != rightFailed {
			return leftFailed
		}
		return targets[i].Key < targets[j].Key
	})
	return targets
}

func appendClientEntryMonitorReportFailures(builder *strings.Builder, targets []*clientEntryMonitorReportTarget) {
	shown := 0
	for _, target := range targets {
		if target.Failure == 0 {
			continue
		}
		if shown >= clientEntryMonitorReportFailureLimit {
			break
		}
		fmt.Fprintf(builder, "\n• %s · %s · 失败 %d/%d", clientEntryMonitorReportTargetLabel(target), clientEntryMonitorReportTargetAddress(target), target.Failure, target.Total)
		failureCounts := make(map[string]int)
		failureOrder := make([]string, 0, len(target.Failures))
		failureDetails := make(map[string]clientEntryMonitorReportFailure)
		for _, failure := range target.Failures {
			key := failure.ProbeName + "\x00" + failure.Detail
			if _, exists := failureCounts[key]; !exists {
				failureOrder = append(failureOrder, key)
				failureDetails[key] = failure
			}
			failureCounts[key]++
		}
		for index, key := range failureOrder {
			if index >= clientEntryMonitorReportFailureDetailLimit {
				break
			}
			failure := failureDetails[key]
			probe := failure.ProbeName
			if probe == "" {
				probe = "未知探针"
			}
			detail := failure.Detail
			if detail == "" {
				detail = "无详情"
			}
			count := failureCounts[key]
			suffix := ""
			if count > 1 {
				suffix = fmt.Sprintf(" ×%d", count)
			}
			fmt.Fprintf(builder, "\n  %s：%s%s", truncateClientEntryMonitorReportText(probe, 40), truncateClientEntryMonitorReportText(detail, 100), suffix)
		}
		if len(failureOrder) > clientEntryMonitorReportFailureDetailLimit {
			fmt.Fprintf(builder, "\n  其余 %d 类失败结果已折叠。", len(failureOrder)-clientEntryMonitorReportFailureDetailLimit)
		}
		shown++
	}
}

func countClientEntryMonitorReportTargets(targets []*clientEntryMonitorReportTarget, predicate func(*clientEntryMonitorReportTarget) bool) int {
	count := 0
	for _, target := range targets {
		if predicate(target) {
			count++
		}
	}
	return count
}

func latestClientEntryMonitorReportTime(targets []*clientEntryMonitorReportTarget) int64 {
	var latest int64
	for _, target := range targets {
		if target.LatestReported > latest {
			latest = target.LatestReported
		}
	}
	return latest
}

func clientEntryMonitorReportResultKey(targetID int64, name, host string, port int64) string {
	if targetID > 0 {
		return fmt.Sprintf("target:%d", targetID)
	}
	return fmt.Sprintf("target:%s:%s:%d", compactClientEntryMonitorReportText(name), compactClientEntryMonitorReportText(host), port)
}

func clientEntryMonitorReportTargetLabel(target *clientEntryMonitorReportTarget) string {
	if target.Name != "" {
		return truncateClientEntryMonitorReportText(target.Name, 80)
	}
	return "入口目标"
}

func clientEntryMonitorReportTargetAddress(target *clientEntryMonitorReportTarget) string {
	host := target.Host
	if host == "" {
		host = "未知地址"
	}
	if target.Port > 0 {
		return fmt.Sprintf("%s:%d", truncateClientEntryMonitorReportText(host, 100), target.Port)
	}
	return truncateClientEntryMonitorReportText(host, 100)
}

func compactClientEntryMonitorReportTime(value int64) string {
	if value <= 0 {
		return "未知"
	}
	return time.Unix(value, 0).Format("01-02 15:04")
}

func formatClientEntryMonitorReportedAt(value int64) string {
	if value <= 0 {
		return "未知"
	}
	return time.Unix(value, 0).Format("2006-01-02 15:04:05 MST")
}

func clientEntryMonitorRunStatusText(status string) string {
	switch status {
	case "running":
		return "检测中"
	case "completed":
		return "已完成"
	case "timeout":
		return "已超时"
	default:
		return status
	}
}

func compactClientEntryMonitorReportText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func truncateClientEntryMonitorReportText(value string, limit int) string {
	value = compactClientEntryMonitorReportText(value)
	if limit <= 0 || len([]rune(value)) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "…"
}
