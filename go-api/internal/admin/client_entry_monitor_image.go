package admin

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

const (
	clientEntryMonitorImageWidth        = 1200
	clientEntryMonitorImageHeaderHeight = 72
	clientEntryMonitorImageRowHeight    = 30
	clientEntryMonitorImageFooterHeight = 34
	clientEntryMonitorImageMaxRows      = 30
	clientEntryMonitorImageBodyFontSize = 15
)

var (
	//go:embed assets/NotoSansCJKsc-Regular.otf
	clientEntryMonitorImageFontData []byte

	//go:embed assets/LICENSE-NOTO
	clientEntryMonitorImageFontLicense string

	clientEntryMonitorImageFontOnce sync.Once
	clientEntryMonitorImageFont     *opentype.Font
	clientEntryMonitorImageFontErr  error
)

type clientEntryMonitorImageRow struct {
	Status     string
	PolicyName string
	Target     string
	Detail     string
	Probe      string
	Last       string
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
	summary := summarizeClientEntryMonitorRun(run)
	targets := summary.Targets
	policies := clientEntryMonitorRunImagePolicies(run)
	visibleFailedTargets := countClientEntryMonitorReportTargets(targets, func(target *clientEntryMonitorReportTarget) bool {
		return target.Failure > 0
	})
	failedTargets := summary.FailedTargetCount
	if failedTargets < visibleFailedTargets {
		failedTargets = visibleFailedTargets
	}
	rows := clientEntryMonitorRunImageRows(targets)
	if !summary.UsesExpectedSnapshot {
		rows = appendClientEntryMonitorRunMissingPolicyRows(rows, run, policies)
	}
	totalResults := run.TotalResults
	if totalResults < run.ReceivedResults {
		totalResults = run.ReceivedResults
	}
	if totalResults < int64(len(run.Results)) {
		totalResults = int64(len(run.Results))
	}
	if len(rows) > clientEntryMonitorImageMaxRows {
		hiddenRows := len(rows) - clientEntryMonitorImageMaxRows
		rows = rows[:clientEntryMonitorImageMaxRows]
		rows = append(rows, clientEntryMonitorImageRow{
			Status: "更多",
			Target: fmt.Sprintf("其余 %d 行", hiddenRows),
			Detail: "已折叠",
		})
	}
	if len(rows) == 0 {
		rows = append(rows, clientEntryMonitorImageRow{Status: "等待", Target: "暂无检测结果", Detail: "0/0"})
	}
	stats := fmt.Sprintf("规则组 %d   目标 %d   探针 %d   异常目标 %d   正常 %d   失败 %d   未返回 %d", len(policies), summary.TargetCount, summary.ProbeCount, failedTargets, summary.Normal, summary.Abnormal, summary.Missing)
	title := fmt.Sprintf("用户入口检测 · %s", clientEntryMonitorRunStatusText(run.Status))
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

func clientEntryMonitorRunImageRows(targets []*clientEntryMonitorReportTarget) []clientEntryMonitorImageRow {
	rows := make([]clientEntryMonitorImageRow, 0, len(targets))
	for _, target := range targets {
		status := "正常"
		if target.Failure > 0 {
			status = "掉线"
		} else if target.Unknown > 0 || target.Stale > 0 {
			status = "等待"
		}
		rows = append(rows, clientEntryMonitorImageRow{
			Status:     status,
			PolicyName: target.PolicyName,
			Target:     imageTargetText(target),
			Detail:     fmt.Sprintf("%d/%d", target.Success, target.Total),
			Probe:      imageFailureProbeText(target),
			Last:       compactClientEntryMonitorReportTime(target.LatestReported),
		})
	}
	return rows
}

type clientEntryMonitorRunImagePolicy struct {
	ID   int64
	Name string
}

func clientEntryMonitorRunImagePolicies(run ClientEntryMonitorRun) []clientEntryMonitorRunImagePolicy {
	policies := make([]clientEntryMonitorRunImagePolicy, 0)
	seen := make(map[string]struct{})
	appendPolicy := func(policyID int64, policyName string) {
		policyName = strings.Join(strings.Fields(strings.TrimSpace(policyName)), " ")
		key := clientEntryMonitorRunImagePolicyKey(policyID, policyName)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		if policyName == "" {
			if policyID > 0 {
				policyName = fmt.Sprintf("规则组 #%d", policyID)
			} else {
				policyName = "未命名规则组"
			}
		}
		policies = append(policies, clientEntryMonitorRunImagePolicy{ID: policyID, Name: policyName})
	}
	for _, pair := range run.ExpectedPairs {
		appendPolicy(pair.PolicyID, pair.PolicyName)
	}
	for _, policyID := range run.PolicyIDs {
		appendPolicy(policyID, "")
	}
	for _, result := range run.Results {
		appendPolicy(result.PolicyID, result.PolicyName)
	}
	return policies
}

func appendClientEntryMonitorRunMissingPolicyRows(rows []clientEntryMonitorImageRow, run ClientEntryMonitorRun, policies []clientEntryMonitorRunImagePolicy) []clientEntryMonitorImageRow {
	reportedPolicies := make(map[string]struct{})
	for _, result := range run.Results {
		reportedPolicies[clientEntryMonitorRunImagePolicyKey(result.PolicyID, result.PolicyName)] = struct{}{}
	}
	expectedByPolicy := make(map[string]int)
	for _, pair := range run.ExpectedPairs {
		expectedByPolicy[clientEntryMonitorRunImagePolicyKey(pair.PolicyID, pair.PolicyName)]++
	}
	for _, policy := range policies {
		policyKey := clientEntryMonitorRunImagePolicyKey(policy.ID, policy.Name)
		if _, reported := reportedPolicies[policyKey]; reported {
			continue
		}
		rows = append(rows, clientEntryMonitorImageRow{
			Status:     "等待",
			PolicyName: policy.Name,
			Target:     "等待探针返回",
			Detail:     fmt.Sprintf("0/%d", expectedByPolicy[policyKey]),
			Probe:      "等待探针上报",
			Last:       "未知",
		})
	}
	return rows
}

func clientEntryMonitorRunImagePolicyKey(policyID int64, policyName string) string {
	if policyID > 0 {
		return fmt.Sprintf("id:%d", policyID)
	}
	return "name:" + strings.Join(strings.Fields(strings.TrimSpace(policyName)), " ")
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
		status := "正常"
		if target.Failure > 0 {
			status = "掉线"
		} else if target.Unknown > 0 || target.Stale > 0 {
			status = "过期"
		}
		rows = append(rows, clientEntryMonitorImageRow{
			Status:     status,
			PolicyName: target.PolicyName,
			Target:     imageTargetText(target),
			Detail:     fmt.Sprintf("%d/%d", target.Success, target.Total),
			Probe:      imageFailureProbeText(target),
			Last:       compactClientEntryMonitorReportTime(target.LatestReported),
		})
	}
	if len(rows) > clientEntryMonitorImageMaxRows {
		rows = rows[:clientEntryMonitorImageMaxRows]
		rows = append(rows, clientEntryMonitorImageRow{
			Status: "更多",
			Target: fmt.Sprintf("其余 %d 个目标", len(targets)-clientEntryMonitorImageMaxRows),
			Detail: "已折叠",
		})
	}
	if len(rows) == 0 {
		rows = append(rows, clientEntryMonitorImageRow{Status: "等待", Target: "暂无检测结果", Detail: "0/0"})
	}
	stats := fmt.Sprintf("目标 %d   探针 %d   正常 %d   异常 %d   过期/无数据 %d", len(targets), probeCount, normalTargets, failedTargets, staleTargets)
	imageBytes, err := renderClientEntryMonitorTablePNG("用户入口检测 · 最近状态", stats, rows)
	if err != nil {
		return nil, "", err
	}
	return imageBytes, truncateTelegramCaption(formatClientEntryMonitorOverviewReport(overview)), nil
}

func renderClientEntryMonitorEventImage(message string) ([]byte, string, error) {
	lines := strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n")
	rows := make([]clientEntryMonitorImageRow, 0, len(lines))
	status := "告警"
	if strings.Contains(message, "恢复") {
		status = "正常"
	}
	for index, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if index == 0 && strings.Contains(line, "掉线") {
			status = "掉线"
		}
		label, value := splitClientEntryMonitorEventLine(line)
		rows = append(rows, clientEntryMonitorImageRow{Status: status, Target: label, Detail: value})
	}
	if len(rows) == 0 {
		rows = append(rows, clientEntryMonitorImageRow{Status: status, Target: "入口告警", Detail: "暂无详情"})
	}
	imageBytes, err := renderClientEntryMonitorTablePNG("用户入口检测 · 告警", "目标状态："+status, rows)
	if err != nil {
		return nil, "", err
	}
	return imageBytes, truncateTelegramCaption(message), nil
}

func renderClientEntryMonitorTablePNG(title, stats string, rows []clientEntryMonitorImageRow) ([]byte, error) {
	if len(rows) > clientEntryMonitorImageMaxRows+1 {
		rows = rows[:clientEntryMonitorImageMaxRows+1]
	}
	textRenderer, err := newClientEntryMonitorImageTextRenderer()
	if err != nil {
		return nil, err
	}
	defer func() { _ = textRenderer.Close() }()
	height := clientEntryMonitorImageHeaderHeight + (len(rows)+1)*clientEntryMonitorImageRowHeight + clientEntryMonitorImageFooterHeight
	canvas := image.NewRGBA(image.Rect(0, 0, clientEntryMonitorImageWidth, height))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.RGBA{R: 248, G: 250, B: 252, A: 255}), image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(0, 0, clientEntryMonitorImageWidth, clientEntryMonitorImageHeaderHeight), image.NewUniform(color.RGBA{R: 31, G: 45, B: 61, A: 255}), image.Point{}, draw.Src)
	textRenderer.draw(canvas, title, 24, 28, 1152, 20, color.White)
	textRenderer.draw(canvas, stats, 24, 53, 1152, 14, color.RGBA{R: 203, G: 213, B: 225, A: 255})

	columns := []struct {
		title string
		x     int
		width int
	}{
		{title: "状态", x: 24, width: 86},
		{title: "规则组", x: 118, width: 190},
		{title: "目标", x: 316, width: 290},
		{title: "结果", x: 614, width: 94},
		{title: "异常探针 / 详情", x: 716, width: 320},
		{title: "最近上报", x: 1044, width: 132},
	}
	for _, column := range columns {
		textRenderer.draw(canvas, column.title, column.x, clientEntryMonitorImageHeaderHeight+21, column.width, 14, color.RGBA{R: 71, G: 85, B: 105, A: 255})
	}

	for index, row := range rows {
		top := clientEntryMonitorImageHeaderHeight + clientEntryMonitorImageRowHeight*(index+1)
		if index%2 == 0 {
			draw.Draw(canvas, image.Rect(0, top, clientEntryMonitorImageWidth, top+clientEntryMonitorImageRowHeight), image.NewUniform(color.White), image.Point{}, draw.Src)
		}
		statusColor := imageStatusColor(row.Status)
		draw.Draw(canvas, image.Rect(24, top+5, 104, top+25), image.NewUniform(statusColor), image.Point{}, draw.Src)
		textRenderer.draw(canvas, row.Status, 31, top+20, 66, 14, color.White)
		textRenderer.draw(canvas, row.PolicyName, 118, top+21, 190, clientEntryMonitorImageBodyFontSize, color.RGBA{R: 15, G: 23, B: 42, A: 255})
		textRenderer.draw(canvas, row.Target, 316, top+21, 290, clientEntryMonitorImageBodyFontSize, color.RGBA{R: 15, G: 23, B: 42, A: 255})
		textRenderer.draw(canvas, row.Detail, 614, top+21, 94, clientEntryMonitorImageBodyFontSize, color.RGBA{R: 15, G: 23, B: 42, A: 255})
		textRenderer.draw(canvas, row.Probe, 716, top+21, 320, clientEntryMonitorImageBodyFontSize, color.RGBA{R: 71, G: 85, B: 105, A: 255})
		textRenderer.draw(canvas, row.Last, 1044, top+21, 132, clientEntryMonitorImageBodyFontSize, color.RGBA{R: 71, G: 85, B: 105, A: 255})
	}
	textRenderer.draw(canvas, "图片仅在内存生成，完整数据请查看后台", 24, height-12, 1152, 13, color.RGBA{R: 100, G: 116, B: 139, A: 255})

	var output bytes.Buffer
	if err := png.Encode(&output, canvas); err != nil {
		return nil, fmt.Errorf("encode client entry monitor image: %w", err)
	}
	return output.Bytes(), nil
}

type clientEntryMonitorImageTextRenderer struct {
	font       *opentype.Font
	faces      map[int]font.Face
	glyphCache map[rune]bool
}

func newClientEntryMonitorImageTextRenderer() (*clientEntryMonitorImageTextRenderer, error) {
	clientEntryMonitorImageFontOnce.Do(func() {
		clientEntryMonitorImageFont, clientEntryMonitorImageFontErr = opentype.Parse(clientEntryMonitorImageFontData)
	})
	if clientEntryMonitorImageFontErr != nil {
		return nil, fmt.Errorf("parse embedded Noto Sans CJK font: %w", clientEntryMonitorImageFontErr)
	}
	renderer := &clientEntryMonitorImageTextRenderer{
		font:       clientEntryMonitorImageFont,
		faces:      make(map[int]font.Face),
		glyphCache: make(map[rune]bool),
	}
	for _, size := range []int{13, 14, clientEntryMonitorImageBodyFontSize, 20} {
		if _, err := renderer.face(size); err != nil {
			_ = renderer.Close()
			return nil, err
		}
	}
	return renderer, nil
}

func (r *clientEntryMonitorImageTextRenderer) Close() error {
	var firstErr error
	for size, face := range r.faces {
		if err := face.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(r.faces, size)
	}
	return firstErr
}

func (r *clientEntryMonitorImageTextRenderer) draw(dst draw.Image, value string, x, baseline, maxWidth, size int, textColor color.Color) {
	face, err := r.face(size)
	if err != nil {
		return
	}
	drawer := font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(textColor),
		Face: face,
		Dot:  fixed.P(x, baseline),
	}
	drawer.DrawString(r.truncate(value, maxWidth, face))
}

func (r *clientEntryMonitorImageTextRenderer) face(size int) (font.Face, error) {
	if face := r.faces[size]; face != nil {
		return face, nil
	}
	face, err := opentype.NewFace(r.font, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return nil, fmt.Errorf("create Noto Sans CJK face: %w", err)
	}
	r.faces[size] = face
	return face, nil
}

func (r *clientEntryMonitorImageTextRenderer) truncate(value string, maxWidth int, face font.Face) string {
	value = r.sanitize(value)
	if maxWidth <= 0 || value == "" {
		return ""
	}
	measure := func(text string) int { return font.MeasureString(face, text).Ceil() }
	if measure(value) <= maxWidth {
		return value
	}
	suffix := "…"
	if !r.supportsGlyph('…') {
		suffix = "..."
	}
	if measure(suffix) > maxWidth {
		return ""
	}
	runes := []rune(value)
	for len(runes) > 0 && measure(string(runes)+suffix) > maxWidth {
		runes = runes[:len(runes)-1]
	}
	return strings.TrimSpace(string(runes)) + suffix
}

func (r *clientEntryMonitorImageTextRenderer) sanitize(value string) string {
	var builder strings.Builder
	for _, runeValue := range strings.TrimSpace(value) {
		if unicode.IsControl(runeValue) {
			continue
		}
		if unicode.IsSpace(runeValue) {
			builder.WriteByte(' ')
			continue
		}
		if r.supportsGlyph(runeValue) {
			builder.WriteRune(runeValue)
		}
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}

func (r *clientEntryMonitorImageTextRenderer) supportsGlyph(runeValue rune) bool {
	if runeValue == ' ' {
		return true
	}
	if supported, ok := r.glyphCache[runeValue]; ok {
		return supported
	}
	var buffer sfnt.Buffer
	glyphIndex, err := r.font.GlyphIndex(&buffer, runeValue)
	supported := err == nil && glyphIndex != 0
	r.glyphCache[runeValue] = supported
	return supported
}

func imageStatusColor(status string) color.Color {
	switch status {
	case "正常", "OK":
		return color.RGBA{R: 22, G: 163, B: 74, A: 255}
	case "掉线", "DOWN":
		return color.RGBA{R: 220, G: 38, B: 38, A: 255}
	case "过期", "等待", "STALE", "WAIT":
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
	name := strings.TrimSpace(target.Name)
	policyName := strings.TrimSpace(target.PolicyName)
	if name == policyName {
		name = ""
	} else if policyName != "" {
		for _, separator := range []string{" · ", "：", ":", " - "} {
			prefix := policyName + separator
			if strings.HasPrefix(name, prefix) {
				name = strings.TrimSpace(strings.TrimPrefix(name, prefix))
				break
			}
		}
	}
	if name == "" {
		return address
	}
	return name + " · " + address
}

func imageFailureProbeText(target *clientEntryMonitorReportTarget) string {
	if len(target.Failures) == 0 {
		if target.Unknown > 0 {
			return imagePendingProbeText(target)
		}
		return "全部探针正常"
	}
	parts := make([]string, 0, len(target.Failures))
	seen := make(map[string]struct{})
	for _, failure := range target.Failures {
		name := failure.ProbeName
		if strings.TrimSpace(name) == "" {
			name = "未知探针"
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
	if target.Unknown > 0 {
		parts = append(parts, imagePendingProbeText(target))
	}
	return strings.Join(parts, ", ")
}

func imagePendingProbeText(target *clientEntryMonitorReportTarget) string {
	if len(target.PendingProbes) == 0 {
		return fmt.Sprintf("等待 %d 个探针", target.Unknown)
	}
	names := target.PendingProbes
	if len(names) > 3 {
		names = names[:3]
	}
	text := "等待 " + strings.Join(names, ", ")
	if len(target.PendingProbes) > len(names) {
		text += fmt.Sprintf(" 等 %d 个探针", target.Unknown)
	}
	return text
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
		return "用户入口掉线"
	case "用户入口恢复":
		return "用户入口恢复"
	case "规则":
		return "规则组"
	case "地址":
		return "目标地址"
	case "探针":
		return "探针"
	case "详情":
		return "详情"
	case "时间":
		return "时间"
	default:
		return strings.TrimSpace(value)
	}
}

func truncateImageEventValue(value string) string {
	value = strings.TrimSpace(value)
	switch value {
	case "无详情":
		return "暂无详情"
	case "掉线":
		return "掉线"
	case "恢复":
		return "恢复"
	default:
		return value
	}
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
