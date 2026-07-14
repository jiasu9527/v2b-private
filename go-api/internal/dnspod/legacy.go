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
	"time"
)

const LegacyEndpoint = "https://api.dnspod.com"

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
	apiToken   string
	endpoint   string
	httpClient *http.Client
}

func NewLegacyClient(apiToken string, options ...LegacyOption) *LegacyClient {
	client := &LegacyClient{
		apiToken:   strings.TrimSpace(apiToken),
		endpoint:   LegacyEndpoint,
		httpClient: &http.Client{Timeout: 15 * time.Second},
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
	message := strings.TrimSpace(e.Message)
	if e.Code == "-1" || strings.Contains(strings.ToLower(message), "token") || strings.Contains(strings.ToLower(message), "login") {
		message = "DNSPod API Token 鉴权失败，请确认填写的是完整的 ID,Token，且 Token 仍然有效"
	} else if message == "" {
		message = "DNSPod 国际版 Token API 返回错误"
	} else {
		message = "DNSPod 国际版 Token API 返回错误：" + message
	}
	if strings.TrimSpace(e.Code) != "" {
		message += " 错误码=" + e.Code
	}
	return message
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
	payload, err := c.call(ctx, "Record.Type", url.Values{"domain_grade": {request.DomainGrade}})
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
	values := url.Values{"domain_grade": {request.DomainGrade}}
	setLegacyDomain(values, request.Domain, request.DomainID)
	payload, err := c.call(ctx, "Record.Line", values)
	if err != nil {
		return DescribeRecordLineListResult{}, err
	}
	lineMap, _ := payload["lines"].(map[string]any)
	keys := sortedLegacyKeys(lineMap)
	lines := make([]RecordLine, 0, len(keys))
	for _, key := range keys {
		row, _ := lineMap[key].(map[string]any)
		line := RecordLine{LineID: key, LineName: legacyString(row["name"]), Useful: true}
		subAreas, _ := row["sub_area"].(map[string]any)
		for _, subKey := range sortedLegacyKeys(subAreas) {
			line.SubGroup = append(line.SubGroup, RecordLine{LineID: subKey, LineName: legacyString(subAreas[subKey]), Useful: true})
		}
		lines = append(lines, line)
	}
	return DescribeRecordLineListResult{Lines: lines}, nil
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
	values := url.Values{
		"domain_id":   {strconv.FormatInt(request.DomainID, 10)},
		"sub_domain":  {request.SubDomain},
		"record_type": {request.RecordType},
		"record_line": {legacyRecordLine(request.RecordLine, request.RecordLineID)},
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
	if strings.TrimSpace(lineID) != "" {
		return strings.TrimSpace(lineID)
	}
	line = strings.TrimSpace(line)
	if line == "" || line == "默认" || strings.EqualFold(line, "default") {
		return "default"
	}
	return line
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
