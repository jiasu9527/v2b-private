package dnspod

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	ChinaEndpoint         = "https://dnspod.tencentcloudapi.com"
	InternationalEndpoint = "https://dnspod.intl.tencentcloudapi.com"
	DefaultEndpoint       = ChinaEndpoint
	EditionChina          = "china"
	EditionInternational  = "international"
	AuthTypeTC3           = "tc3"
	AuthTypeToken         = "token"
	APIVersion            = "2021-03-23"
	serviceName           = "dnspod"
	algorithm             = "TC3-HMAC-SHA256"
)

func EndpointForEdition(edition string) string {
	if strings.EqualFold(strings.TrimSpace(edition), EditionChina) {
		return ChinaEndpoint
	}
	return InternationalEndpoint
}

type Option func(*Client)

func WithEndpoint(endpoint string) Option {
	return func(client *Client) { client.endpoint = strings.TrimRight(endpoint, "/") }
}

func WithHTTPClient(httpClient *http.Client) Option {
	return func(client *Client) {
		if httpClient != nil {
			client.httpClient = httpClient
		}
	}
}

func WithClock(clock func() time.Time) Option {
	return func(client *Client) {
		if clock != nil {
			client.clock = clock
		}
	}
}

type Client struct {
	secretID   string
	secretKey  string
	endpoint   string
	httpClient *http.Client
	clock      func() time.Time
}

func NewClient(secretID, secretKey string, options ...Option) *Client {
	client := &Client{
		secretID:   strings.TrimSpace(secretID),
		secretKey:  strings.TrimSpace(secretKey),
		endpoint:   DefaultEndpoint,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		clock:      time.Now,
	}
	for _, option := range options {
		option(client)
	}
	return client
}

type APIError struct {
	Code      string `json:"Code"`
	Message   string `json:"Message"`
	RequestID string `json:"-"`
}

func (e *APIError) Error() string {
	if e == nil {
		return "DNSPod 请求失败"
	}
	message := dnspodAPIErrorMessage(e.Code, e.Message)
	parts := []string{message}
	if strings.TrimSpace(e.Code) != "" {
		parts = append(parts, "错误码="+e.Code)
	}
	if strings.TrimSpace(e.RequestID) != "" {
		parts = append(parts, "请求ID="+e.RequestID)
	}
	return strings.Join(parts, " ")
}

func dnspodAPIErrorMessage(code, providerMessage string) string {
	code = strings.TrimSpace(code)
	switch code {
	case "AuthFailure.SecretIdNotFound", "AuthFailure.InvalidSecretId":
		return "DNSPod SecretId 不存在，请确认填写的是腾讯云 API 密钥管理中的 SecretId"
	case "AuthFailure.SignatureFailure":
		return "DNSPod 签名验证失败，请确认 SecretKey 与 SecretId 匹配且没有多余空格"
	case "AuthFailure.UnauthorizedOperation", "UnauthorizedOperation":
		return "当前腾讯云 API 密钥没有 DNSPod 操作权限，请为所属用户授权 DNSPod 权限"
	case "AuthFailure.TokenFailure":
		return "DNSPod 临时凭证无效或已经过期"
	case "AuthFailure.SignatureExpire":
		return "DNSPod 请求时间校验失败，请检查服务器时间是否准确"
	case "RequestLimitExceeded":
		return "DNSPod API 请求过于频繁，请稍后重试"
	}
	providerMessage = strings.TrimSpace(providerMessage)
	if providerMessage == "" {
		return "DNSPod API 返回错误"
	}
	return "DNSPod API 返回错误：" + providerMessage
}

type responseMeta struct {
	Error     *APIError `json:"Error"`
	RequestID string    `json:"RequestId"`
}

func (c *Client) call(ctx context.Context, action string, payload any, result any) error {
	if c == nil || c.secretID == "" || c.secretKey == "" {
		return errors.New("DNSPod SecretId 或 SecretKey 未配置")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode DNSPod request: %w", err)
	}
	endpoint, err := url.Parse(c.endpoint)
	if err != nil || endpoint.Host == "" {
		return errors.New("DNSPod API 地址无效")
	}
	now := c.clock().UTC()
	timestamp := strconv.FormatInt(now.Unix(), 10)
	authorization := c.authorization(endpoint.Host, timestamp, now.Format("2006-01-02"), body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create DNSPod request: %w", err)
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Host", endpoint.Host)
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Timestamp", timestamp)
	req.Header.Set("X-TC-Version", APIVersion)

	response, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("DNSPod 请求失败：%w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read DNSPod response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("DNSPod 接口返回 HTTP %d：%s", response.StatusCode, strings.TrimSpace(string(raw)))
	}
	var envelope struct {
		Response json.RawMessage `json:"Response"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Response) == 0 {
		return errors.New("DNSPod 返回了无法识别的数据")
	}
	var meta responseMeta
	if err := json.Unmarshal(envelope.Response, &meta); err != nil {
		return errors.New("DNSPod 返回了无法识别的数据")
	}
	if meta.Error != nil {
		meta.Error.RequestID = meta.RequestID
		return meta.Error
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(envelope.Response, result); err != nil {
		return fmt.Errorf("decode DNSPod response: %w", err)
	}
	return nil
}

func (c *Client) authorization(host, timestamp, date string, payload []byte) string {
	canonicalHeaders := "content-type:application/json; charset=utf-8\n" + "host:" + host + "\n"
	signedHeaders := "content-type;host"
	canonicalRequest := strings.Join([]string{
		http.MethodPost,
		"/",
		"",
		canonicalHeaders,
		signedHeaders,
		sha256Hex(payload),
	}, "\n")
	credentialScope := date + "/" + serviceName + "/tc3_request"
	stringToSign := strings.Join([]string{
		algorithm,
		timestamp,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	secretDate := hmacSHA256([]byte("TC3"+c.secretKey), date)
	secretService := hmacSHA256(secretDate, serviceName)
	secretSigning := hmacSHA256(secretService, "tc3_request")
	signature := hex.EncodeToString(hmacSHA256(secretSigning, stringToSign))
	return fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s", algorithm, c.secretID, credentialScope, signedHeaders, signature)
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(data))
	return mac.Sum(nil)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type Domain struct {
	DomainID     int64    `json:"DomainId"`
	Name         string   `json:"Name"`
	Status       string   `json:"Status"`
	DNSStatus    string   `json:"DNSStatus"`
	Grade        string   `json:"Grade"`
	GradeTitle   string   `json:"GradeTitle"`
	RecordCount  int64    `json:"RecordCount"`
	TTL          int64    `json:"TTL"`
	Remark       string   `json:"Remark"`
	Punycode     string   `json:"Punycode"`
	EffectiveDNS []string `json:"EffectiveDNS"`
	CreatedOn    string   `json:"CreatedOn"`
	UpdatedOn    string   `json:"UpdatedOn"`
}

type DomainCountInfo struct {
	DomainTotal int64 `json:"DomainTotal"`
}

type DescribeDomainListRequest struct {
	Type    string `json:"Type,omitempty"`
	Offset  int64  `json:"Offset"`
	Limit   int64  `json:"Limit"`
	GroupID int64  `json:"GroupId,omitempty"`
	Keyword string `json:"Keyword,omitempty"`
}

type DescribeDomainListResult struct {
	Domains   []Domain        `json:"DomainList"`
	CountInfo DomainCountInfo `json:"DomainCountInfo"`
	RequestID string          `json:"RequestId"`
	Total     int64           `json:"-"`
}

func (c *Client) DescribeDomainList(ctx context.Context, request DescribeDomainListRequest) (DescribeDomainListResult, error) {
	var result DescribeDomainListResult
	err := c.call(ctx, "DescribeDomainList", request, &result)
	result.Total = result.CountInfo.DomainTotal
	return result, err
}

type Record struct {
	RecordID      int64  `json:"RecordId"`
	Name          string `json:"Name"`
	Type          string `json:"Type"`
	Value         string `json:"Value"`
	Line          string `json:"Line"`
	LineID        string `json:"LineId"`
	Status        string `json:"Status"`
	TTL           int64  `json:"TTL"`
	MX            int64  `json:"MX"`
	Weight        *int64 `json:"Weight"`
	Remark        string `json:"Remark"`
	MonitorStatus string `json:"MonitorStatus"`
	UpdatedOn     string `json:"UpdatedOn"`
}

type RecordCountInfo struct {
	TotalCount int64 `json:"TotalCount"`
}

type DescribeRecordListRequest struct {
	Domain       string `json:"Domain"`
	DomainID     int64  `json:"-"`
	Subdomain    string `json:"Subdomain,omitempty"`
	RecordType   string `json:"RecordType,omitempty"`
	RecordLine   string `json:"RecordLine,omitempty"`
	RecordLineID string `json:"RecordLineId,omitempty"`
	Keyword      string `json:"Keyword,omitempty"`
	Offset       int64  `json:"Offset"`
	Limit        int64  `json:"Limit"`
}

type DescribeRecordListResult struct {
	Records   []Record        `json:"RecordList"`
	CountInfo RecordCountInfo `json:"RecordCountInfo"`
	RequestID string          `json:"RequestId"`
	Total     int64           `json:"-"`
}

func (c *Client) DescribeRecordList(ctx context.Context, request DescribeRecordListRequest) (DescribeRecordListResult, error) {
	var result DescribeRecordListResult
	err := c.call(ctx, "DescribeRecordList", request, &result)
	result.Total = result.CountInfo.TotalCount
	return result, err
}

type DescribeRecordTypeRequest struct {
	DomainGrade string `json:"DomainGrade"`
}

type DescribeRecordTypeResult struct {
	Types     []string `json:"TypeList"`
	RequestID string   `json:"RequestId"`
}

func (c *Client) DescribeRecordType(ctx context.Context, request DescribeRecordTypeRequest) (DescribeRecordTypeResult, error) {
	var result DescribeRecordTypeResult
	err := c.call(ctx, "DescribeRecordType", request, &result)
	return result, err
}

type RecordLine struct {
	LineID   string       `json:"LineId"`
	LineName string       `json:"LineName"`
	Useful   bool         `json:"Useful"`
	Grade    string       `json:"Grade"`
	SubGroup []RecordLine `json:"SubGroup,omitempty"`
}

type DescribeRecordLineListRequest struct {
	Domain      string `json:"Domain"`
	DomainID    int64  `json:"-"`
	DomainGrade string `json:"DomainGrade"`
	RecordType  string `json:"RecordType"`
}

type DescribeRecordLineListResult struct {
	Lines     []RecordLine `json:"LineList"`
	RequestID string       `json:"RequestId"`
}

func (c *Client) DescribeRecordLineList(ctx context.Context, request DescribeRecordLineListRequest) (DescribeRecordLineListResult, error) {
	var result DescribeRecordLineListResult
	err := c.call(ctx, "DescribeRecordLineList", request, &result)
	return result, err
}

type RecordMutationRequest struct {
	Domain       string `json:"Domain"`
	DomainID     int64  `json:"-"`
	DomainGrade  string `json:"-"`
	RecordID     int64  `json:"RecordId,omitempty"`
	SubDomain    string `json:"SubDomain"`
	RecordType   string `json:"RecordType"`
	RecordLine   string `json:"RecordLine,omitempty"`
	RecordLineID string `json:"RecordLineId,omitempty"`
	Value        string `json:"Value"`
	TTL          int64  `json:"TTL,omitempty"`
	MX           int64  `json:"MX,omitempty"`
	Weight       *int64 `json:"Weight,omitempty"`
}

type RecordMutationResult struct {
	RecordID  int64  `json:"RecordId"`
	RequestID string `json:"RequestId"`
}

func (c *Client) CreateRecord(ctx context.Context, request RecordMutationRequest) (RecordMutationResult, error) {
	request.RecordID = 0
	var result RecordMutationResult
	err := c.call(ctx, "CreateRecord", request, &result)
	return result, err
}

func (c *Client) ModifyRecord(ctx context.Context, request RecordMutationRequest) (RecordMutationResult, error) {
	var result RecordMutationResult
	err := c.call(ctx, "ModifyRecord", request, &result)
	return result, err
}

type DeleteRecordRequest struct {
	Domain   string `json:"Domain"`
	DomainID int64  `json:"-"`
	RecordID int64  `json:"RecordId"`
}

func (c *Client) DeleteRecord(ctx context.Context, request DeleteRecordRequest) error {
	return c.call(ctx, "DeleteRecord", request, nil)
}

type ModifyRecordStatusRequest struct {
	Domain   string `json:"Domain"`
	DomainID int64  `json:"-"`
	RecordID int64  `json:"RecordId"`
	Status   string `json:"Status"`
}

func (c *Client) ModifyRecordStatus(ctx context.Context, request ModifyRecordStatusRequest) error {
	return c.call(ctx, "ModifyRecordStatus", request, nil)
}
