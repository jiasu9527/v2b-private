package dnspod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	LegacyEndpoint        = "https://api.dnspod.com"
	legacyRequestInterval = 300 * time.Millisecond
)

var legacyRequestThrottle struct {
	sync.Mutex
	next time.Time
}

type LegacyOption func(*LegacyClient)

func WithLegacyEndpoint(endpoint string) LegacyOption {
	return func(client *LegacyClient) { client.endpoint = strings.TrimRight(endpoint, "/") }
}

func WithLegacyHTTPClient(httpClient *http.Client) LegacyOption {
	return func(client *LegacyClient) {
		if httpClient != nil {
			client.httpClient = httpClient
		}
	}
}

type LegacyClient struct {
	apiToken    string
	endpoint    string
	httpClient  *http.Client
	retryDelays []time.Duration
}

func NewLegacyClient(apiToken string, options ...LegacyOption) *LegacyClient {
	client := &LegacyClient{
		apiToken:    strings.TrimSpace(apiToken),
		endpoint:    LegacyEndpoint,
		httpClient:  &http.Client{Timeout: 15 * time.Second},
		retryDelays: []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond},
	}
	for _, option := range options {
		option(client)
	}
	return client
}

type LegacyAPIError struct {
	Code    string
	Message string
}

func (e *LegacyAPIError) Error() string {
	if e == nil {
		return "DNSPod 国际版 Token API 请求失败"
	}
	providerMessage := strings.TrimSpace(e.Message)
	message := providerMessage
	switch strings.TrimSpace(e.Code) {
	case "26":
		// The legacy international API uses the key returned by Record.Line
		// (for example, "default" or "asia"). Tencent Cloud TC3 line IDs
		// such as "10=0" are not valid values for record_line.
		message = "DNSPod 国际版记录线路无效，请使用 Record.Line 返回的线路名称对应的 key（默认线路为 default），不要填写腾讯云线路 ID"
	case "-1", "10004":
		message = legacyTokenAuthError(providerMessage)
	default:
		if strings.Contains(strings.ToLower(providerMessage), "token") || strings.Contains(strings.ToLower(providerMessage), "login") {
			message = legacyTokenAuthError(providerMessage)
		} else if message == "" {
			message = "DNSPod 国际版 Token API 返回错误"
		} else {
			message = "DNSPod 国际版 Token API 返回错误：" + message
		}
	}
	if strings.TrimSpace(e.Code) != "" {
		message += " 错误码=" + e.Code
	}
	return message
}

func legacyTokenAuthError(providerMessage string) string {
	message := "DNSPod API Token 鉴权被服务商拒绝"
	if providerMessage = strings.TrimSpace(providerMessage); providerMessage != "" {
		message += "：" + providerMessage
	}
	return message + "；请确认 Token 在 DNSPod 控制台仍处于启用状态，并检查服务器环境变量 DNSPOD_API_TOKEN 是否覆盖了后台配置"
}

func waitLegacyRequestSlot(ctx context.Context) error {
	now := time.Now()
	legacyRequestThrottle.Lock()
	slot := now
	if legacyRequestThrottle.next.After(slot) {
		slot = legacyRequestThrottle.next
	}
	legacyRequestThrottle.next = slot.Add(legacyRequestInterval)
	legacyRequestThrottle.Unlock()
	if wait := time.Until(slot); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func isTransientLegacyAPIError(err error) bool {
	var apiErr *LegacyAPIError
	if !errors.As(err, &apiErr) || strings.TrimSpace(apiErr.Code) != "-1" {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(apiErr.Message))
	return strings.Contains(message, "unknown error") || strings.Contains(message, "retry later") || strings.Contains(message, "try again")
}

func isLegacyRetrySafeAction(action string) bool {
	switch action {
	case "Domain.List", "Record.List", "Record.Type", "Record.Line", "Record.Modify", "Record.Status":
		return true
	default:
		return false
	}
}

func (c *LegacyClient) call(ctx context.Context, action string, values url.Values) (map[string]any, error) {
	if c == nil || !ValidLegacyToken(c.apiToken) {
		return nil, errors.New("DNSPod API Token 格式错误，应为 ID,Token")
	}
	if values == nil {
		values = url.Values{}
	}
	values.Set("login_token", c.apiToken)
	values.Set("format", "json")
	values.Set("error_on_empty", "no")
	for attempt := 0; ; attempt++ {
		if err := waitLegacyRequestSlot(ctx); err != nil {
			return nil, fmt.Errorf("等待 DNSPod 国际版请求限速失败：%w", err)
		}
		payload, err := c.callOnce(ctx, action, values)
		if err == nil || !isLegacyRetrySafeAction(action) || !isTransientLegacyAPIError(err) || attempt >= len(c.retryDelays) {
			return payload, err
		}
		timer := time.NewTimer(c.retryDelays[attempt])
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("重试 DNSPod 国际版请求失败：%w", ctx.Err())
		case <-timer.C:
		}
	}
}

func (c *LegacyClient) callOnce(ctx context.Context, action string, values url.Values) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/"+action, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, fmt.Errorf("创建 DNSPod 国际版请求失败：%w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("User-Agent", "ForestDNSManager/1.0 (support@forest.example)")
	response, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("DNSPod 国际版请求失败：%w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("读取 DNSPod 国际版响应失败：%w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("DNSPod 国际版接口返回 HTTP %d：%s", response.StatusCode, strings.TrimSpace(string(raw)))
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, errors.New("DNSPod 国际版返回了无法识别的数据")
	}
	status, _ := payload["status"].(map[string]any)
	code := legacyString(status["code"])
	if code != "1" {
		return nil, &LegacyAPIError{Code: code, Message: legacyString(status["message"])}
	}
	return payload, nil
}

func (c *LegacyClient) DescribeDomainList(ctx context.Context, request DescribeDomainListRequest) (DescribeDomainListResult, error) {
	values := url.Values{}
	values.Set("offset", strconv.FormatInt(request.Offset, 10))
	values.Set("length", strconv.FormatInt(normalizeLegacyLimit(request.Limit), 10))
	if request.Keyword != "" {
		values.Set("keyword", request.Keyword)
	}
	if request.Type != "" {
		values.Set("type", request.Type)
	}
	payload, err := c.call(ctx, "Domain.List", values)
	if err != nil {
		return DescribeDomainListResult{}, err
	}
	items, _ := payload["domains"].([]any)
	domains := make([]Domain, 0, len(items))
	for _, item := range items {
		row, _ := item.(map[string]any)
		domains = append(domains, Domain{
			DomainID:    legacyInt64(row["id"]),
			Name:        legacyString(row["name"]),
			Status:      normalizeLegacyStatus(row["status"]),
			DNSStatus:   strings.ToUpper(legacyString(row["ext_status"])),
			Grade:       legacyString(row["grade"]),
			GradeTitle:  legacyString(row["grade_title"]),
			RecordCount: legacyInt64(row["records"]),
			TTL:         legacyInt64(row["ttl"]),
			Remark:      legacyString(row["remark"]),
			Punycode:    legacyString(row["punycode"]),
			CreatedOn:   legacyString(row["created_on"]),
			UpdatedOn:   legacyString(row["updated_on"]),
		})
	}
	info, _ := payload["info"].(map[string]any)
	total := legacyInt64(info["domain_total"])
	if total == 0 {
		total = int64(len(domains))
	}
	return DescribeDomainListResult{Domains: domains, Total: total, CountInfo: DomainCountInfo{DomainTotal: total}}, nil
}

func (c *LegacyClient) DescribeRecordList(ctx context.Context, request DescribeRecordListRequest) (DescribeRecordListResult, error) {
	if request.DomainID <= 0 {
		return DescribeRecordListResult{}, errors.New("DNSPod 国际版 Token API 需要有效的域名 ID 才能读取解析记录")
	}
	values := url.Values{}
	setLegacyDomain(values, request.Domain, request.DomainID)
	values.Set("offset", strconv.FormatInt(request.Offset, 10))
	values.Set("length", strconv.FormatInt(normalizeLegacyLimit(request.Limit), 10))
	if request.Subdomain != "" {
		values.Set("sub_domain", request.Subdomain)
	}
	if request.Keyword != "" {
		values.Set("keyword", request.Keyword)
	}
	if request.RecordType != "" {
		values.Set("record_type", request.RecordType)
	}
	if request.RecordLine != "" {
		values.Set("record_line", request.RecordLine)
	}
	payload, err := c.call(ctx, "Record.List", values)
	if err != nil {
		return DescribeRecordListResult{}, err
	}
	items, _ := payload["records"].([]any)
	records := make([]Record, 0, len(items))
	for _, item := range items {
		row, _ := item.(map[string]any)
		var weight *int64
		if raw := strings.TrimSpace(legacyString(row["weight"])); raw != "" {
			parsed := legacyInt64(row["weight"])
			weight = &parsed
		}
		records = append(records, Record{
			RecordID: legacyInt64(row["id"]), Name: legacyString(row["name"]), Type: strings.ToUpper(legacyString(row["type"])),
			Value: legacyString(row["value"]), Line: legacyString(row["line"]), LineID: legacyString(row["line_id"]),
			Status: normalizeLegacyStatus(row["status"]), TTL: legacyInt64(row["ttl"]), MX: legacyInt64(row["mx"]),
			Weight: weight, Remark: legacyString(row["remark"]), MonitorStatus: legacyString(row["monitor_status"]), UpdatedOn: legacyString(row["updated_on"]),
		})
	}
	info, _ := payload["info"].(map[string]any)
	total := legacyInt64(info["record_total"])
	if total == 0 {
		total = int64(len(records))
	}
	return DescribeRecordListResult{Records: records, Total: total, CountInfo: RecordCountInfo{TotalCount: total}}, nil
}

func (c *LegacyClient) DescribeRecordType(ctx context.Context, request DescribeRecordTypeRequest) (DescribeRecordTypeResult, error) {
	payload, err := c.call(ctx, "Record.Type", url.Values{"domain_grade": {legacyDomainGrade(request.DomainGrade)}})
	if err != nil {
		return DescribeRecordTypeResult{}, err
	}
	items, _ := payload["types"].([]any)
	types := make([]string, 0, len(items))
	for _, item := range items {
		types = append(types, strings.ToUpper(legacyString(item)))
	}
	return DescribeRecordTypeResult{Types: types}, nil
}

func (c *LegacyClient) DescribeRecordLineList(ctx context.Context, request DescribeRecordLineListRequest) (DescribeRecordLineListResult, error) {
	values := url.Values{"domain_grade": {legacyDomainGrade(request.DomainGrade)}}
	setLegacyDomain(values, request.Domain, request.DomainID)
	payload, err := c.call(ctx, "Record.Line", values)
	if err != nil {
		return DescribeRecordLineListResult{}, err
	}
	// The international Token API returns two parallel fields: line_ids maps
	// the provider's line name to its numeric ID, while lines preserves the
	// display/mutation order.  Parsing lines alone loses every entry because it
	// is commonly just an array of strings.
	lines := parseLegacyRecordLineIDs(payload["line_ids"], payload["lines"])
	if len(lines) == 0 {
		lines = parseLegacyRecordLines(payload["lines"])
	}
	if len(lines) == 0 {
		lines = parseLegacyRecordLines(payload["LineList"])
	}
	if len(lines) == 0 {
		lines = parseLegacyRecordLines(payload["line_list"])
	}
	if len(lines) == 0 {
		lines = append(lines, RecordLine{LineID: "default", LineName: "Default", Useful: true})
	}
	return DescribeRecordLineListResult{Lines: lines}, nil
}

func legacyDomainGrade(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "DPG_Free"
	}
	// Admin API callers historically used these upper-case defaults. Preserve
	// provider-returned grades verbatim, but canonicalise the known defaults so
	// the legacy endpoint receives the spelling used by Domain.List.
	switch strings.ToUpper(value) {
	case "DP_FREE":
		return "DP_Free"
	case "DPG_FREE":
		return "DPG_Free"
	default:
		return value
	}
}

func parseLegacyRecordLineIDs(rawIDs, rawOrder any) []RecordLine {
	ids, ok := rawIDs.(map[string]any)
	if !ok || len(ids) == 0 {
		return nil
	}

	orderedNames := legacyRecordLineNames(rawOrder)
	seen := make(map[string]struct{}, len(ids))
	lines := make([]RecordLine, 0, len(ids))
	appendLine := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		lookupName := name
		rawID, exists := ids[lookupName]
		if !exists {
			// Provider line names are currently exact matches, but tolerate a
			// case-only discrepancy without making the map order observable.
			for candidate, value := range ids {
				if strings.EqualFold(strings.TrimSpace(candidate), name) {
					lookupName, rawID, exists = candidate, value, true
					break
				}
			}
		}
		if !exists {
			return
		}
		if _, exists := seen[lookupName]; exists {
			return
		}
		lineID := strings.TrimSpace(legacyString(rawID))
		if row, isObject := rawID.(map[string]any); isObject {
			lineID = legacyObjectString(row, "line_id", "LineId", "id", "value")
		}
		if lineID == "" {
			lineID = lookupName
		}
		seen[lookupName] = struct{}{}
		lines = append(lines, RecordLine{LineID: lineID, LineName: lookupName, Useful: true})
	}

	for _, name := range orderedNames {
		appendLine(name)
	}
	// A partially populated lines array must not hide entries from line_ids.
	// Sort the remainder to keep responses stable despite Go map iteration.
	for _, name := range sortedLegacyKeys(ids) {
		appendLine(name)
	}
	return lines
}

func legacyRecordLineNames(raw any) []string {
	values, ok := raw.([]any)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(values))
	for _, value := range values {
		if row, isObject := value.(map[string]any); isObject {
			if name := legacyObjectString(row, "name", "line", "LineName", "label"); name != "" {
				names = append(names, name)
			}
			continue
		}
		if name := strings.TrimSpace(legacyString(value)); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func parseLegacyRecordLines(raw any) []RecordLine {
	switch values := raw.(type) {
	case map[string]any:
		keys := sortedLegacyKeys(values)
		lines := make([]RecordLine, 0, len(keys))
		for _, key := range keys {
			row, isObject := values[key].(map[string]any)
			if !isObject {
				lines = append(lines, RecordLine{LineID: key, LineName: legacyString(values[key]), Useful: true})
				continue
			}
			line := RecordLine{LineID: key, LineName: legacyObjectString(row, "name", "line", "LineName"), Useful: true}
			line.SubGroup = parseLegacyRecordLineChildren(row["sub_area"])
			if len(line.SubGroup) == 0 {
				line.SubGroup = parseLegacyRecordLines(row["SubGroup"])
			}
			lines = append(lines, line)
		}
		return lines
	case []any:
		lines := make([]RecordLine, 0, len(values))
		for _, value := range values {
			row, ok := value.(map[string]any)
			if !ok {
				name := strings.TrimSpace(legacyString(value))
				if name != "" {
					lines = append(lines, RecordLine{LineID: name, LineName: name, Useful: true})
				}
				continue
			}
			lineID := legacyObjectString(row, "line_id", "LineId", "id", "value")
			lineName := legacyObjectString(row, "name", "line", "LineName", "label")
			if lineID == "" {
				lineID = lineName
			}
			if lineName == "" {
				lineName = lineID
			}
			if lineID == "" {
				continue
			}
			line := RecordLine{LineID: lineID, LineName: lineName, Useful: true}
			line.SubGroup = parseLegacyRecordLineChildren(row["sub_area"])
			if len(line.SubGroup) == 0 {
				line.SubGroup = parseLegacyRecordLines(row["SubGroup"])
			}
			lines = append(lines, line)
		}
		return lines
	default:
		return nil
	}
}

func parseLegacyRecordLineChildren(raw any) []RecordLine {
	values, ok := raw.(map[string]any)
	if !ok {
		return parseLegacyRecordLines(raw)
	}
	children := make([]RecordLine, 0, len(values))
	for _, key := range sortedLegacyKeys(values) {
		children = append(children, RecordLine{LineID: key, LineName: legacyString(values[key]), Useful: true})
	}
	return children
}

func legacyObjectString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			if result := strings.TrimSpace(legacyString(value)); result != "" {
				return result
			}
		}
	}
	return ""
}

func (c *LegacyClient) CreateRecord(ctx context.Context, request RecordMutationRequest) (RecordMutationResult, error) {
	return c.mutateRecord(ctx, "Record.Create", request)
}

func (c *LegacyClient) ModifyRecord(ctx context.Context, request RecordMutationRequest) (RecordMutationResult, error) {
	if request.RecordID <= 0 {
		return RecordMutationResult{}, errors.New("DNSPod 记录 ID 无效")
	}
	return c.mutateRecord(ctx, "Record.Modify", request)
}

func (c *LegacyClient) mutateRecord(ctx context.Context, action string, request RecordMutationRequest) (RecordMutationResult, error) {
	if request.DomainID <= 0 {
		return RecordMutationResult{}, errors.New("DNSPod 国际版 Token API 需要有效的域名 ID")
	}
	recordLine, err := c.resolveLegacyRecordLine(ctx, request)
	if err != nil {
		return RecordMutationResult{}, err
	}
	values := url.Values{
		"domain_id":   {strconv.FormatInt(request.DomainID, 10)},
		"sub_domain":  {request.SubDomain},
		"record_type": {request.RecordType},
		"record_line": {recordLine},
		"value":       {request.Value},
	}
	if request.RecordID > 0 {
		values.Set("record_id", strconv.FormatInt(request.RecordID, 10))
	}
	if request.TTL > 0 {
		values.Set("ttl", strconv.FormatInt(request.TTL, 10))
	}
	if request.MX > 0 || strings.EqualFold(request.RecordType, "MX") {
		values.Set("mx", strconv.FormatInt(request.MX, 10))
	}
	if request.Weight != nil {
		values.Set("weight", strconv.FormatInt(*request.Weight, 10))
	}
	payload, err := c.call(ctx, action, values)
	if err != nil {
		return RecordMutationResult{}, err
	}
	record, _ := payload["record"].(map[string]any)
	return RecordMutationResult{RecordID: legacyInt64(record["id"])}, nil
}

// resolveLegacyRecordLine converts the modern RecordLine/RecordLineId pair to
// the single legacy record_line key required by DNSPod's international Token
// API. The old API does not support record_line_id; sending a Tencent Cloud
// line ID (for example, 10=0) as record_line causes error 26.
func (c *LegacyClient) resolveLegacyRecordLine(ctx context.Context, request RecordMutationRequest) (string, error) {
	line := strings.TrimSpace(request.RecordLine)
	lineID := strings.TrimSpace(request.RecordLineID)
	if key, ok := legacyRecordLineKey(line, lineID); ok {
		return key, nil
	}

	lines, err := c.DescribeRecordLineList(ctx, DescribeRecordLineListRequest{
		Domain: request.Domain, DomainID: request.DomainID, DomainGrade: request.DomainGrade, RecordType: request.RecordType,
	})
	if err != nil {
		if fallback := legacyExistingRecordLineName(request); fallback != "" {
			return fallback, nil
		}
		return "", fmt.Errorf("解析 DNSPod 国际版记录线路失败：%w", err)
	}
	if key := findLegacyRecordLine(lines.Lines, line, lineID); key != "" {
		return key, nil
	}
	if fallback := legacyExistingRecordLineName(request); fallback != "" {
		return fallback, nil
	}
	return "", fmt.Errorf("DNSPod 国际版无法识别记录线路（名称=%q，ID=%q），请重新选择国际版线路", line, lineID)
}

func legacyExistingRecordLineName(request RecordMutationRequest) string {
	if request.RecordID <= 0 {
		return ""
	}
	line := strings.TrimSpace(request.RecordLine)
	if line == "" || isModernRecordLineID(line) {
		return ""
	}
	return line
}

// legacyRecordLineKey handles values that can be converted without another
// API request. A numeric line ID is deliberately not returned directly; it
// must first be resolved against this international account's Record.Line
// response, then sent using the provider's mutation value.
func legacyRecordLineKey(line, lineID string) (string, bool) {
	line = strings.TrimSpace(line)
	lineID = strings.TrimSpace(lineID)
	if isLegacyDefaultLine(line) || (line == "" && isLegacyDefaultLine(lineID)) {
		return "default", true
	}
	if line == "" && lineID == "" {
		return "default", true
	}
	if line != "" && isLikelyLegacyRecordLineKey(line) {
		return line, true
	}
	if lineID != "" && !isModernRecordLineID(lineID) && !isLegacyDefaultLine(lineID) {
		// A UI fallback can copy the display name into both fields. In that
		// case query Record.Line instead of sending the display name verbatim.
		if line == "" || !legacyLineLabelsEqual(line, lineID) || isLikelyLegacyRecordLineKey(lineID) {
			return lineID, true
		}
	}
	return "", false
}

func isLikelyLegacyRecordLineKey(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if value == strings.ToLower(value) {
		for _, character := range value {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' && character != '-' {
				return false
			}
		}
		return true
	}
	upper := strings.ToUpper(value)
	if value != upper {
		return false
	}
	runes := []rune(value)
	if len(runes) != 2 {
		return false
	}
	for _, character := range runes {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}

func isLegacyDefaultLine(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "default", "默认", "0", "0=0":
		return true
	default:
		return false
	}
}

func isModernRecordLineID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, part := range strings.Split(value, "=") {
		if part == "" {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}

func findLegacyRecordLine(lines []RecordLine, line, lineID string) string {
	line = strings.TrimSpace(line)
	lineID = strings.TrimSpace(lineID)
	var byName string
	var visit func([]RecordLine) string
	visit = func(items []RecordLine) string {
		for _, item := range items {
			itemID := strings.TrimSpace(item.LineID)
			mutationValue := itemID
			if isModernRecordLineID(itemID) {
				// Some international accounts return array-shaped line data such
				// as {line_id:"3=0", name:"Global"}. The numeric ID is not a
				// valid legacy record_line mutation value, but its provider-returned
				// name is. Trust the live lookup instead of rejecting the record.
				mutationValue = strings.TrimSpace(item.LineName)
			}
			if lineID != "" && mutationValue != "" && strings.EqualFold(itemID, lineID) {
				return mutationValue
			}
			if byName == "" && line != "" && mutationValue != "" &&
				(legacyLineLabelsEqual(item.LineName, line) || legacyLineLabelsEqual(itemID, line)) {
				byName = mutationValue
			}
			if nested := visit(item.SubGroup); nested != "" {
				return nested
			}
		}
		return ""
	}
	if matched := visit(lines); matched != "" {
		return matched
	}
	return byName
}

func legacyLineLabelsEqual(left, right string) bool {
	left = normalizeLegacyLineLabel(left)
	right = normalizeLegacyLineLabel(right)
	if left == right {
		return true
	}
	return legacyLineLabelAlias(left) == legacyLineLabelAlias(right)
}

func normalizeLegacyLineLabel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("_", " ", "-", " ", "／", "/").Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

func legacyLineLabelAlias(value string) string {
	switch value {
	case "默认":
		return "default"
	case "全球", "全网", "global", "worldwide":
		return "global"
	case "中国", "中国大陆", "china", "mainland", "domestic":
		return "china"
	case "中国移动", "移动", "china mobile", "mobile", "cmcc":
		return "china mobile"
	case "中国联通", "联通", "china unicom", "unicom", "cucc":
		return "china unicom"
	case "中国电信", "电信", "china telecom", "telecom", "ctcc":
		return "china telecom"
	case "中国教育网", "教育网", "china education", "china education network", "education", "cernet":
		return "china education"
	default:
		return value
	}
}

func (c *LegacyClient) DeleteRecord(ctx context.Context, request DeleteRecordRequest) error {
	if request.DomainID <= 0 || request.RecordID <= 0 {
		return errors.New("DNSPod 国际版 Token API 需要有效的域名 ID 和记录 ID")
	}
	_, err := c.call(ctx, "Record.Remove", url.Values{
		"domain_id": {strconv.FormatInt(request.DomainID, 10)}, "record_id": {strconv.FormatInt(request.RecordID, 10)},
	})
	return err
}

func (c *LegacyClient) ModifyRecordStatus(ctx context.Context, request ModifyRecordStatusRequest) error {
	if request.DomainID <= 0 || request.RecordID <= 0 {
		return errors.New("DNSPod 国际版 Token API 需要有效的域名 ID 和记录 ID")
	}
	status := "disable"
	if strings.EqualFold(request.Status, "ENABLE") || strings.EqualFold(request.Status, "enable") {
		status = "enable"
	}
	_, err := c.call(ctx, "Record.Status", url.Values{
		"domain_id": {strconv.FormatInt(request.DomainID, 10)}, "record_id": {strconv.FormatInt(request.RecordID, 10)}, "status": {status},
	})
	return err
}

func ValidLegacyToken(token string) bool {
	parts := strings.SplitN(strings.TrimSpace(token), ",", 2)
	return len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != ""
}

func normalizeLegacyLimit(limit int64) int64 {
	if limit <= 0 {
		return 20
	}
	if limit > 500 {
		return 500
	}
	return limit
}

func setLegacyDomain(values url.Values, domain string, domainID int64) {
	if domainID > 0 {
		values.Set("domain_id", strconv.FormatInt(domainID, 10))
	} else if strings.TrimSpace(domain) != "" {
		values.Set("domain", strings.TrimSpace(domain))
	}
}

func legacyRecordLine(line, lineID string) string {
	if key, ok := legacyRecordLineKey(line, lineID); ok {
		return key
	}
	if strings.TrimSpace(line) != "" {
		return strings.TrimSpace(line)
	}
	return "default"
}

func legacyString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

func legacyInt64(value any) int64 {
	parsed, _ := strconv.ParseInt(strings.TrimSpace(legacyString(value)), 10, 64)
	return parsed
}

func normalizeLegacyStatus(value any) string {
	status := strings.ToLower(strings.TrimSpace(legacyString(value)))
	if status == "enable" || status == "enabled" || status == "1" {
		return "ENABLE"
	}
	if status == "disable" || status == "disabled" || status == "0" {
		return "DISABLE"
	}
	return strings.ToUpper(status)
}

func sortedLegacyKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
