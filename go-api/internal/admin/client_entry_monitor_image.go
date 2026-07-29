package admin

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

const (
	clientEntryMonitorImageWidth        = 1200
	clientEntryMonitorImageHeaderHeight = 72
	clientEntryMonitorImageRowHeight    = 30
	clientEntryMonitorImageFooterHeight = 34
	clientEntryMonitorImageMaxRows      = 30
)

type clientEntryMonitorImageRow struct {
	Status string
	Target string
	Detail string
	Probe  string
	Last   string
}

// RecentClientEntryMonitorReportImage returns an in-memory PNG and its caption.
// The caller can upload the bytes directly; no report file is created on disk.
func (s *DBService) RecentClientEntryMonitorReportImage(ctx context.Context) ([]byte, string, error) {
	runs, err := s.ListClientEntryMonitorRuns(ctx, 1)
	if err != nil {
		return nil, "", err
	}
	if len(runs) > 0 {
		return renderClientEntryMonitorRunImage(runs[0])
	}
	overview, err := s.ListClientEntryMonitors(ctx)
	if err != nil {
		return nil, "", err
	}
	return renderClientEntryMonitorOverviewImage(overview)
}

func renderClientEntryMonitorRunImage(run ClientEntryMonitorRun) ([]byte, string, error) {
	targets, normal, abnormal, probeCount := summarizeClientEntryMonitorRunResults(run.Results)
	failedTargets := countClientEntryMonitorReportTargets(targets, func(target *clientEntryMonitorReportTarget) bool {
		return target.Failure > 0
	})
	rows := make([]clientEntryMonitorImageRow, 0, len(targets)+1)
	for _, target := range targets {
		status := "OK"
		if target.Failure > 0 {
			status = "DOWN"
		} else if target.Unknown > 0 || target.Stale > 0 {
			status = "WAIT"
		}
		rows = append(rows, clientEntryMonitorImageRow{
			Status: status,
			Target: imageTargetText(target),
			Detail: fmt.Sprintf("%d/%d", target.Success, target.Total),
			Probe:  imageFailureProbeText(target),
			Last:   compactClientEntryMonitorReportTime(target.LatestReported),
		})
	}
	totalResults := run.TotalResults
	if totalResults < run.ReceivedResults {
		totalResults = run.ReceivedResults
	}
	if totalResults < int64(len(run.Results)) {
		totalResults = int64(len(run.Results))
	}
	if len(rows) > clientEntryMonitorImageMaxRows {
		rows = rows[:clientEntryMonitorImageMaxRows]
		rows = append(rows, clientEntryMonitorImageRow{
			Status: "...",
			Target: fmt.Sprintf("%d more targets", len(targets)-clientEntryMonitorImageMaxRows),
			Detail: "folded",
		})
	}
	if len(rows) == 0 {
		rows = append(rows, clientEntryMonitorImageRow{Status: "WAIT", Target: "No result received", Detail: "0/0"})
	}
	stats := fmt.Sprintf("TARGETS %d   PROBES %d   DOWN TARGETS %d   OK %d   FAILED %d   MISSING %d", len(targets), probeCount, failedTargets, normal, abnormal, maxInt64(run.ExpectedResults-run.ReceivedResults, 0))
	title := fmt.Sprintf("ENTRY MONITOR · %s", clientEntryMonitorRunStatusText(run.Status))
	imageBytes, err := renderClientEntryMonitorTablePNG(title, stats, rows)
	if err != nil {
		return nil, "", err
	}
	caption := formatClientEntryMonitorRunReport(run)
	if run.ResultsTruncated || totalResults > int64(len(run.Results)) {
		caption += "\n图片仅展示异常优先的部分目标，完整结果请查看后台。"
	}
	return imageBytes, truncateTelegramCaption(caption), nil
}

func renderClientEntryMonitorOverviewImage(overview ClientEntryMonitorOverview) ([]byte, string, error) {
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
	rows := make([]clientEntryMonitorImageRow, 0, len(targets)+1)
	for _, target := range targets {
		status := "OK"
		if target.Failure > 0 {
			status = "DOWN"
		} else if target.Unknown > 0 || target.Stale > 0 {
			status = "STALE"
		}
		rows = append(rows, clientEntryMonitorImageRow{
			Status: status,
			Target: imageTargetText(target),
			Detail: fmt.Sprintf("%d/%d", target.Success, target.Total),
			Probe:  imageFailureProbeText(target),
			Last:   compactClientEntryMonitorReportTime(target.LatestReported),
		})
	}
	if len(rows) > clientEntryMonitorImageMaxRows {
		rows = rows[:clientEntryMonitorImageMaxRows]
		rows = append(rows, clientEntryMonitorImageRow{
			Status: "...",
			Target: fmt.Sprintf("%d more targets", len(targets)-clientEntryMonitorImageMaxRows),
			Detail: "folded",
		})
	}
	if len(rows) == 0 {
		rows = append(rows, clientEntryMonitorImageRow{Status: "WAIT", Target: "No result received", Detail: "0/0"})
	}
	stats := fmt.Sprintf("TARGETS %d   PROBES %d   OK %d   DOWN %d   STALE %d", len(targets), probeCount, normalTargets, failedTargets, staleTargets)
	imageBytes, err := renderClientEntryMonitorTablePNG("ENTRY MONITOR · RECENT", stats, rows)
	if err != nil {
		return nil, "", err
	}
	return imageBytes, truncateTelegramCaption(formatClientEntryMonitorOverviewReport(overview)), nil
}

func renderClientEntryMonitorEventImage(message string) ([]byte, string, error) {
	lines := strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n")
	rows := make([]clientEntryMonitorImageRow, 0, len(lines))
	status := "ALERT"
	if strings.Contains(message, "恢复") {
		status = "OK"
	}
	for index, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if index == 0 && strings.Contains(line, "掉线") {
			status = "DOWN"
		}
		label, value := splitClientEntryMonitorEventLine(line)
		rows = append(rows, clientEntryMonitorImageRow{Status: status, Target: label, Detail: value})
	}
	if len(rows) == 0 {
		rows = append(rows, clientEntryMonitorImageRow{Status: status, Target: "ENTRY ALERT", Detail: "No detail"})
	}
	imageBytes, err := renderClientEntryMonitorTablePNG("ENTRY MONITOR · ALERT", "TARGET STATUS: "+status, rows)
	if err != nil {
		return nil, "", err
	}
	return imageBytes, truncateTelegramCaption(message), nil
}

func renderClientEntryMonitorTablePNG(title, stats string, rows []clientEntryMonitorImageRow) ([]byte, error) {
	if len(rows) > clientEntryMonitorImageMaxRows+1 {
		rows = rows[:clientEntryMonitorImageMaxRows+1]
	}
	height := clientEntryMonitorImageHeaderHeight + (len(rows)+1)*clientEntryMonitorImageRowHeight + clientEntryMonitorImageFooterHeight
	canvas := image.NewRGBA(image.Rect(0, 0, clientEntryMonitorImageWidth, height))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.RGBA{R: 248, G: 250, B: 252, A: 255}), image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(0, 0, clientEntryMonitorImageWidth, clientEntryMonitorImageHeaderHeight), image.NewUniform(color.RGBA{R: 31, G: 45, B: 61, A: 255}), image.Point{}, draw.Src)
	drawImageText(canvas, title, 24, 26, color.White)
	drawImageText(canvas, stats, 24, 51, color.RGBA{R: 203, G: 213, B: 225, A: 255})

	columns := []struct {
		title string
		x     int
		width int
	}{
		{title: "STATUS", x: 24, width: 92},
		{title: "TARGET", x: 122, width: 430},
		{title: "RESULT", x: 560, width: 120},
		{title: "FAILED PROBES / DETAIL", x: 690, width: 370},
		{title: "LAST", x: 1070, width: 105},
	}
	for _, column := range columns {
		drawImageText(canvas, column.title, column.x, clientEntryMonitorImageHeaderHeight+20, color.RGBA{R: 71, G: 85, B: 105, A: 255})
	}

	for index, row := range rows {
		top := clientEntryMonitorImageHeaderHeight + clientEntryMonitorImageRowHeight*(index+1)
		if index%2 == 0 {
			draw.Draw(canvas, image.Rect(0, top, clientEntryMonitorImageWidth, top+clientEntryMonitorImageRowHeight), image.NewUniform(color.White), image.Point{}, draw.Src)
		}
		statusColor := imageStatusColor(row.Status)
		draw.Draw(canvas, image.Rect(24, top+6, 104, top+24), image.NewUniform(statusColor), image.Point{}, draw.Src)
		drawImageText(canvas, row.Status, 31, top+19, color.White)
		drawImageText(canvas, truncateImageASCII(row.Target, 58), 122, top+20, color.RGBA{R: 15, G: 23, B: 42, A: 255})
		drawImageText(canvas, truncateImageASCII(row.Detail, 16), 560, top+20, color.RGBA{R: 15, G: 23, B: 42, A: 255})
		drawImageText(canvas, truncateImageASCII(row.Probe, 49), 690, top+20, color.RGBA{R: 71, G: 85, B: 105, A: 255})
		drawImageText(canvas, truncateImageASCII(row.Last, 14), 1070, top+20, color.RGBA{R: 71, G: 85, B: 105, A: 255})
	}
	drawImageText(canvas, "Generated in memory · detailed data remains in the admin panel", 24, height-12, color.RGBA{R: 100, G: 116, B: 139, A: 255})

	var output bytes.Buffer
	if err := png.Encode(&output, canvas); err != nil {
		return nil, fmt.Errorf("encode client entry monitor image: %w", err)
	}
	return output.Bytes(), nil
}

func drawImageText(dst draw.Image, value string, x, baseline int, textColor color.Color) {
	drawer := font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(textColor),
		Face: basicfont.Face7x13,
		Dot:  fixed.P(x, baseline),
	}
	drawer.DrawString(truncateImageASCII(value, 160))
}

func imageStatusColor(status string) color.Color {
	switch status {
	case "OK":
		return color.RGBA{R: 22, G: 163, B: 74, A: 255}
	case "DOWN":
		return color.RGBA{R: 220, G: 38, B: 38, A: 255}
	case "STALE", "WAIT":
		return color.RGBA{R: 217, G: 119, B: 6, A: 255}
	default:
		return color.RGBA{R: 71, G: 85, B: 105, A: 255}
	}
}

func imageTargetText(target *clientEntryMonitorReportTarget) string {
	address := target.Host
	if target.Port > 0 {
		address = fmt.Sprintf("%s:%d", address, target.Port)
	}
	name := target.Name
	if !isASCIIImageText(name) {
		name = ""
	}
	if name == "" {
		return address
	}
	return name + " · " + address
}

func imageFailureProbeText(target *clientEntryMonitorReportTarget) string {
	if len(target.Failures) == 0 {
		return "all probes OK"
	}
	parts := make([]string, 0, len(target.Failures))
	seen := make(map[string]struct{})
	for _, failure := range target.Failures {
		name := failure.ProbeName
		if !isASCIIImageText(name) || strings.TrimSpace(name) == "" {
			name = "probe"
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		parts = append(parts, name)
		if len(parts) >= 5 {
			break
		}
	}
	return strings.Join(parts, ", ")
}

func splitClientEntryMonitorEventLine(line string) (string, string) {
	for _, separator := range []string{"：", ":"} {
		if index := strings.Index(line, separator); index >= 0 {
			return imageEventLabel(strings.TrimSpace(line[:index])), truncateImageEventValue(strings.TrimSpace(line[index+len(separator):]))
		}
	}
	return imageEventLabel(line), truncateImageEventValue(line)
}

func imageEventLabel(value string) string {
	switch strings.TrimSpace(value) {
	case "用户入口掉线":
		return "ENTRY DOWN"
	case "用户入口恢复":
		return "ENTRY RECOVERED"
	case "规则":
		return "RULE"
	case "地址":
		return "ADDRESS"
	case "探针":
		return "PROBE"
	case "详情":
		return "DETAIL"
	case "时间":
		return "TIME"
	default:
		return truncateImageASCII(value, 40)
	}
}

func truncateImageEventValue(value string) string {
	value = strings.TrimSpace(value)
	switch value {
	case "无详情":
		return "no detail"
	case "掉线":
		return "down"
	case "恢复":
		return "recovered"
	default:
		return truncateImageASCII(value, 80)
	}
}

func truncateImageASCII(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if maxRunes <= 0 {
		return ""
	}
	result := make([]rune, 0, len(value))
	for _, runeValue := range value {
		switch {
		case runeValue >= 0x20 && runeValue <= 0x7e:
			result = append(result, runeValue)
		case runeValue == '·':
			result = append(result, '-')
		case runeValue == '：':
			result = append(result, ':')
		case runeValue == '…':
			result = append(result, '.')
		case unicode.IsSpace(runeValue):
			result = append(result, ' ')
		default:
			result = append(result, '?')
		}
		if len(result) >= maxRunes {
			break
		}
	}
	return strings.TrimSpace(string(result))
}

func isASCIIImageText(value string) bool {
	for _, runeValue := range value {
		if runeValue < 0x20 || runeValue > 0x7e {
			return false
		}
	}
	return strings.TrimSpace(value) != ""
}

func truncateTelegramCaption(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n"))
	const limit = 1024
	if len([]rune(value)) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit-20])) + "\n…其余详情请查看后台。"
}

func maxInt64(value, minimum int64) int64 {
	if value < minimum {
		return minimum
	}
	return value
}
