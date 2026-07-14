package admin

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"

	"forest/go-api/internal/dnspod"
)

const (
	dnspodSecretIDKey  = "dnspod_secret_id"
	dnspodSecretKeyKey = "dnspod_secret_key"
	dnspodEditionKey   = "dnspod_edition"
)

type dnspodAPI interface {
	DescribeDomainList(context.Context, dnspod.DescribeDomainListRequest) (dnspod.DescribeDomainListResult, error)
	DescribeRecordList(context.Context, dnspod.DescribeRecordListRequest) (dnspod.DescribeRecordListResult, error)
	DescribeRecordType(context.Context, dnspod.DescribeRecordTypeRequest) (dnspod.DescribeRecordTypeResult, error)
	DescribeRecordLineList(context.Context, dnspod.DescribeRecordLineListRequest) (dnspod.DescribeRecordLineListResult, error)
	CreateRecord(context.Context, dnspod.RecordMutationRequest) (dnspod.RecordMutationResult, error)
	ModifyRecord(context.Context, dnspod.RecordMutationRequest) (dnspod.RecordMutationResult, error)
	DeleteRecord(context.Context, dnspod.DeleteRecordRequest) error
	ModifyRecordStatus(context.Context, dnspod.ModifyRecordStatusRequest) error
}

type DNSPodConfigStatus struct {
	Configured     bool   `json:"configured"`
	SecretIDMasked string `json:"secret_id_masked"`
	Source         string `json:"source"`
	Edition        string `json:"edition"`
}

type DNSPodConfigSaveRequest struct {
	SecretID  string
	SecretKey string
	Edition   string
	Verify    bool
	Clear     bool
}

type DNSPodDomainListRequest struct {
	Current  int64
	PageSize int64
	Keyword  string
	Type     string
}

type DNSPodRecordListRequest struct {
	Domain     string
	Current    int64
	PageSize   int64
	Keyword    string
	Subdomain  string
	RecordType string
	RecordLine string
}

type DNSPodRecordSaveRequest struct {
	Domain       string
	RecordID     int64
	SubDomain    string
	RecordType   string
	RecordLine   string
	RecordLineID string
	Value        string
	TTL          int64
	MX           int64
	Weight       *int64
}

func (s *DBService) GetDNSPodConfig(_ context.Context) (DNSPodConfigStatus, error) {
	secretID, secretKey, source, edition, err := s.dnspodCredentials()
	if err != nil {
		return DNSPodConfigStatus{}, err
	}
	return DNSPodConfigStatus{
		Configured:     secretID != "" && secretKey != "",
		SecretIDMasked: maskDNSPodSecretID(secretID),
		Source:         source,
		Edition:        edition,
	}, nil
}

func (s *DBService) SaveDNSPodConfig(ctx context.Context, request DNSPodConfigSaveRequest) (DNSPodConfigStatus, error) {
	cfg, err := loadAdminConfigStore(adminConfigPath())
	if err != nil {
		return DNSPodConfigStatus{}, err
	}
	if request.Clear {
		delete(cfg.values, dnspodSecretIDKey)
		delete(cfg.values, dnspodSecretKeyKey)
		delete(cfg.values, dnspodEditionKey)
		cfg.order = removeConfigKeys(cfg.order, dnspodSecretIDKey, dnspodSecretKeyKey, dnspodEditionKey)
	} else {
		secretID := strings.TrimSpace(request.SecretID)
		secretKey := strings.TrimSpace(request.SecretKey)
		if secretID == "" {
			secretID = cfg.stringValue(dnspodSecretIDKey, "")
		}
		if secretKey == "" {
			secretKey = cfg.stringValue(dnspodSecretKeyKey, "")
		}
		if secretID == "" || secretKey == "" {
			return DNSPodConfigStatus{}, errors.New("请填写完整的 DNSPod SecretId 和 SecretKey")
		}
		edition := normalizeDNSPodEdition(request.Edition)
		if strings.TrimSpace(request.Edition) == "" {
			edition = normalizeDNSPodEdition(cfg.stringValue(dnspodEditionKey, ""))
		}
		if request.Verify {
			if err := s.testDNSPodCredentials(ctx, secretID, secretKey, edition); err != nil {
				return DNSPodConfigStatus{}, err
			}
		}
		cfg.values[dnspodSecretIDKey] = phpConfigValue{kind: phpConfigScalar, scalar: secretID}
		cfg.values[dnspodSecretKeyKey] = phpConfigValue{kind: phpConfigScalar, scalar: secretKey}
		cfg.values[dnspodEditionKey] = phpConfigValue{kind: phpConfigScalar, scalar: edition}
		cfg.order = appendMissingConfigKeys(cfg.order, sortedMissingKeys(cfg, dnspodSecretIDKey, dnspodSecretKeyKey, dnspodEditionKey), cfg.values)
	}
	if err := writeJSONConfigFile(adminConfigPath(), cfg); err != nil {
		return DNSPodConfigStatus{}, errors.New("保存 DNSPod 配置失败")
	}
	return s.GetDNSPodConfig(ctx)
}

func (s *DBService) TestDNSPodConfig(ctx context.Context, secretID, secretKey, edition string) error {
	secretID = strings.TrimSpace(secretID)
	secretKey = strings.TrimSpace(secretKey)
	if secretID == "" || secretKey == "" {
		storedID, storedKey, _, storedEdition, err := s.dnspodCredentials()
		if err != nil {
			return err
		}
		if secretID == "" {
			secretID = storedID
		}
		if secretKey == "" {
			secretKey = storedKey
		}
		if strings.TrimSpace(edition) == "" {
			edition = storedEdition
		}
	}
	if secretID == "" || secretKey == "" {
		return errors.New("请先配置 DNSPod SecretId 和 SecretKey")
	}
	return s.testDNSPodCredentials(ctx, secretID, secretKey, normalizeDNSPodEdition(edition))
}

func (s *DBService) ListDNSPodDomains(ctx context.Context, request DNSPodDomainListRequest) (dnspod.DescribeDomainListResult, error) {
	client, err := s.dnspodClient()
	if err != nil {
		return dnspod.DescribeDomainListResult{}, err
	}
	current, pageSize := normalizeDNSPodPage(request.Current, request.PageSize)
	return client.DescribeDomainList(ctx, dnspod.DescribeDomainListRequest{
		Type:    strings.TrimSpace(request.Type),
		Offset:  (current - 1) * pageSize,
		Limit:   pageSize,
		Keyword: strings.TrimSpace(request.Keyword),
	})
}

func (s *DBService) ListDNSPodRecords(ctx context.Context, request DNSPodRecordListRequest) (dnspod.DescribeRecordListResult, error) {
	domain := strings.TrimSpace(request.Domain)
	if domain == "" {
		return dnspod.DescribeRecordListResult{}, errors.New("请选择域名")
	}
	client, err := s.dnspodClient()
	if err != nil {
		return dnspod.DescribeRecordListResult{}, err
	}
	current, pageSize := normalizeDNSPodPage(request.Current, request.PageSize)
	return client.DescribeRecordList(ctx, dnspod.DescribeRecordListRequest{
		Domain:     domain,
		Subdomain:  strings.TrimSpace(request.Subdomain),
		RecordType: strings.ToUpper(strings.TrimSpace(request.RecordType)),
		RecordLine: strings.TrimSpace(request.RecordLine),
		Keyword:    strings.TrimSpace(request.Keyword),
		Offset:     (current - 1) * pageSize,
		Limit:      pageSize,
	})
}

func (s *DBService) ListDNSPodRecordTypes(ctx context.Context, domainGrade string) (dnspod.DescribeRecordTypeResult, error) {
	domainGrade = strings.TrimSpace(domainGrade)
	if domainGrade == "" {
		domainGrade = "DP_FREE"
	}
	client, err := s.dnspodClient()
	if err != nil {
		return dnspod.DescribeRecordTypeResult{}, err
	}
	return client.DescribeRecordType(ctx, dnspod.DescribeRecordTypeRequest{DomainGrade: domainGrade})
}

func (s *DBService) ListDNSPodRecordLines(ctx context.Context, domain, domainGrade, recordType string) (dnspod.DescribeRecordLineListResult, error) {
	domain = strings.TrimSpace(domain)
	domainGrade = strings.TrimSpace(domainGrade)
	recordType = strings.ToUpper(strings.TrimSpace(recordType))
	if domain == "" || recordType == "" {
		return dnspod.DescribeRecordLineListResult{}, errors.New("域名和记录类型不能为空")
	}
	if domainGrade == "" {
		domainGrade = "DP_FREE"
	}
	client, err := s.dnspodClient()
	if err != nil {
		return dnspod.DescribeRecordLineListResult{}, err
	}
	return client.DescribeRecordLineList(ctx, dnspod.DescribeRecordLineListRequest{
		Domain: domain, DomainGrade: domainGrade, RecordType: recordType,
	})
}

func (s *DBService) SaveDNSPodRecord(ctx context.Context, request DNSPodRecordSaveRequest) (dnspod.RecordMutationResult, error) {
	request.Domain = strings.TrimSpace(request.Domain)
	request.SubDomain = strings.TrimSpace(request.SubDomain)
	request.RecordType = strings.ToUpper(strings.TrimSpace(request.RecordType))
	request.RecordLine = strings.TrimSpace(request.RecordLine)
	request.RecordLineID = strings.TrimSpace(request.RecordLineID)
	request.Value = strings.TrimSpace(request.Value)
	if request.Domain == "" || request.RecordType == "" || request.Value == "" {
		return dnspod.RecordMutationResult{}, errors.New("域名、记录类型和记录值不能为空")
	}
	if request.SubDomain == "" {
		request.SubDomain = "@"
	}
	if request.RecordLine == "" && request.RecordLineID == "" {
		request.RecordLine = "默认"
	}
	if request.TTL < 0 || request.MX < 0 || (request.Weight != nil && *request.Weight < 0) {
		return dnspod.RecordMutationResult{}, errors.New("TTL、MX 和权重不能小于 0")
	}
	client, err := s.dnspodClient()
	if err != nil {
		return dnspod.RecordMutationResult{}, err
	}
	mutation := dnspod.RecordMutationRequest{
		Domain: request.Domain, RecordID: request.RecordID, SubDomain: request.SubDomain,
		RecordType: request.RecordType, RecordLine: request.RecordLine, RecordLineID: request.RecordLineID,
		Value: request.Value, TTL: request.TTL, MX: request.MX, Weight: request.Weight,
	}
	if request.RecordID > 0 {
		return client.ModifyRecord(ctx, mutation)
	}
	return client.CreateRecord(ctx, mutation)
}

func (s *DBService) DeleteDNSPodRecord(ctx context.Context, domain string, recordID int64) error {
	domain = strings.TrimSpace(domain)
	if domain == "" || recordID <= 0 {
		return errors.New("域名或记录 ID 无效")
	}
	client, err := s.dnspodClient()
	if err != nil {
		return err
	}
	return client.DeleteRecord(ctx, dnspod.DeleteRecordRequest{Domain: domain, RecordID: recordID})
}

func (s *DBService) SetDNSPodRecordStatus(ctx context.Context, domain string, recordID int64, status string) error {
	domain = strings.TrimSpace(domain)
	status = strings.ToUpper(strings.TrimSpace(status))
	if domain == "" || recordID <= 0 {
		return errors.New("域名或记录 ID 无效")
	}
	if status != "ENABLE" && status != "DISABLE" {
		return errors.New("记录状态只能是 ENABLE 或 DISABLE")
	}
	client, err := s.dnspodClient()
	if err != nil {
		return err
	}
	return client.ModifyRecordStatus(ctx, dnspod.ModifyRecordStatusRequest{Domain: domain, RecordID: recordID, Status: status})
}

func (s *DBService) dnspodCredentials() (secretID, secretKey, source, edition string, err error) {
	envID := strings.TrimSpace(os.Getenv("DNSPOD_SECRET_ID"))
	envKey := strings.TrimSpace(os.Getenv("DNSPOD_SECRET_KEY"))
	if envID != "" || envKey != "" {
		return envID, envKey, "env", normalizeDNSPodEdition(os.Getenv("DNSPOD_EDITION")), nil
	}
	cfg, err := loadAdminConfigStore(adminConfigPath())
	if err != nil {
		return "", "", "", "", err
	}
	secretID = strings.TrimSpace(cfg.stringValue(dnspodSecretIDKey, ""))
	secretKey = strings.TrimSpace(cfg.stringValue(dnspodSecretKeyKey, ""))
	edition = normalizeDNSPodEdition(cfg.stringValue(dnspodEditionKey, ""))
	source = "config"
	if secretID == "" && secretKey == "" {
		source = ""
	}
	return secretID, secretKey, source, edition, nil
}

func (s *DBService) dnspodClient() (dnspodAPI, error) {
	secretID, secretKey, _, edition, err := s.dnspodCredentials()
	if err != nil {
		return nil, err
	}
	if secretID == "" || secretKey == "" {
		return nil, errors.New("请先配置 DNSPod SecretId 和 SecretKey")
	}
	if s.dnspodClientFactory != nil {
		return s.dnspodClientFactory(secretID, secretKey, edition), nil
	}
	return dnspod.NewClient(secretID, secretKey, dnspod.WithEndpoint(dnspod.EndpointForEdition(edition))), nil
}

func (s *DBService) testDNSPodCredentials(ctx context.Context, secretID, secretKey, edition string) error {
	var client dnspodAPI
	if s.dnspodClientFactory != nil {
		client = s.dnspodClientFactory(secretID, secretKey, edition)
	} else {
		client = dnspod.NewClient(secretID, secretKey, dnspod.WithEndpoint(dnspod.EndpointForEdition(edition)))
	}
	_, err := client.DescribeDomainList(ctx, dnspod.DescribeDomainListRequest{Offset: 0, Limit: 1})
	return err
}

func normalizeDNSPodEdition(edition string) string {
	if strings.EqualFold(strings.TrimSpace(edition), dnspod.EditionChina) {
		return dnspod.EditionChina
	}
	return dnspod.EditionInternational
}

func normalizeDNSPodPage(current, pageSize int64) (int64, int64) {
	if current < 1 {
		current = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return current, pageSize
}

func maskDNSPodSecretID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return strings.Repeat("*", len(value))
	}
	prefix := 6
	if len(value) < prefix+4 {
		prefix = len(value) - 4
	}
	return value[:prefix] + "****" + value[len(value)-4:]
}

func removeConfigKeys(order []string, keys ...string) []string {
	drop := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		drop[key] = struct{}{}
	}
	result := make([]string, 0, len(order))
	for _, key := range order {
		if _, ok := drop[key]; !ok {
			result = append(result, key)
		}
	}
	return result
}

func sortedMissingKeys(cfg *phpConfigFile, keys ...string) []string {
	result := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(cfg.order))
	for _, key := range cfg.order {
		seen[key] = struct{}{}
	}
	for _, key := range keys {
		if _, ok := seen[key]; !ok {
			result = append(result, key)
		}
	}
	sort.Strings(result)
	return result
}
