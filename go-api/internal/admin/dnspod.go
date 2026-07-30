package admin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"forest/go-api/internal/dnspod"
)

const (
	dnspodSecretIDKey  = "dnspod_secret_id"
	dnspodSecretKeyKey = "dnspod_secret_key"
	dnspodEditionKey   = "dnspod_edition"
	dnspodAuthTypeKey  = "dnspod_auth_type"
	dnspodAPITokenKey  = "dnspod_api_token"
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
	Configured           bool     `json:"configured"`
	SecretIDMasked       string   `json:"secret_id_masked"`
	Source               string   `json:"source"`
	Edition              string   `json:"edition"`
	AuthType             string   `json:"auth_type"`
	CredentialMasked     string   `json:"credential_masked"`
	EnvironmentOverrides []string `json:"environment_overrides,omitempty"`
}

type DNSPodConfigSaveRequest struct {
	SecretID  string
	SecretKey string
	Edition   string
	AuthType  string
	APIToken  string
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
	DomainID   int64
	Current    int64
	PageSize   int64
	Keyword    string
	Subdomain  string
	RecordType string
	RecordLine string
}

type DNSPodRecordSaveRequest struct {
	Domain       string
	DomainID     int64
	DomainGrade  string
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
	credentials, err := s.dnspodCredentials()
	if err != nil {
		return DNSPodConfigStatus{}, err
	}
	configured := credentials.SecretID != "" && credentials.SecretKey != ""
	masked := maskDNSPodSecretID(credentials.SecretID)
	if credentials.AuthType == dnspod.AuthTypeToken {
		configured = dnspod.ValidLegacyToken(credentials.APIToken)
		masked = maskDNSPodAPIToken(credentials.APIToken)
	}
	return DNSPodConfigStatus{
		Configured:           configured,
		SecretIDMasked:       maskDNSPodSecretID(credentials.SecretID),
		Source:               credentials.Source,
		Edition:              credentials.Edition,
		AuthType:             credentials.AuthType,
		CredentialMasked:     masked,
		EnvironmentOverrides: activeDNSPodEnvironmentOverrides(),
	}, nil
}

func (s *DBService) SaveDNSPodConfig(ctx context.Context, request DNSPodConfigSaveRequest) (DNSPodConfigStatus, error) {
	if overrides := activeDNSPodEnvironmentOverrides(); !request.Clear && len(overrides) > 0 {
		return DNSPodConfigStatus{}, fmt.Errorf("当前 DNSPod 凭证由服务器环境变量覆盖（%s），后台保存不会生效；请先删除这些环境变量并重启服务", strings.Join(overrides, "、"))
	}
	var mutationErr error
	err := updateAdminConfigStore(adminConfigPath(), func(cfg *phpConfigFile) error {
		if request.Clear {
			delete(cfg.values, dnspodSecretIDKey)
			delete(cfg.values, dnspodSecretKeyKey)
			delete(cfg.values, dnspodEditionKey)
			delete(cfg.values, dnspodAuthTypeKey)
			delete(cfg.values, dnspodAPITokenKey)
			cfg.order = removeConfigKeys(cfg.order, dnspodSecretIDKey, dnspodSecretKeyKey, dnspodEditionKey, dnspodAuthTypeKey, dnspodAPITokenKey)
		} else {
			authType := normalizeDNSPodAuthType(request.AuthType, request.APIToken)
			if strings.TrimSpace(request.AuthType) == "" && strings.TrimSpace(request.APIToken) == "" {
				authType = normalizeDNSPodAuthType(cfg.stringValue(dnspodAuthTypeKey, ""), cfg.stringValue(dnspodAPITokenKey, ""))
			}
			credentials := dnspodCredentials{
				Edition: normalizeDNSPodEdition(request.Edition), AuthType: authType, Source: "config",
			}
			if credentials.AuthType == dnspod.AuthTypeToken {
				credentials.APIToken = strings.TrimSpace(request.APIToken)
				if credentials.APIToken == "" {
					credentials.APIToken = cfg.stringValue(dnspodAPITokenKey, "")
				}
				credentials.Edition = dnspod.EditionInternational
			} else {
				credentials.SecretID = strings.TrimSpace(request.SecretID)
				credentials.SecretKey = strings.TrimSpace(request.SecretKey)
				if credentials.SecretID == "" {
					credentials.SecretID = cfg.stringValue(dnspodSecretIDKey, "")
				}
				if credentials.SecretKey == "" {
					credentials.SecretKey = cfg.stringValue(dnspodSecretKeyKey, "")
				}
			}
			edition := normalizeDNSPodEdition(request.Edition)
			if strings.TrimSpace(request.Edition) == "" {
				edition = normalizeDNSPodEdition(cfg.stringValue(dnspodEditionKey, ""))
			}
			if credentials.AuthType == dnspod.AuthTypeTC3 {
				credentials.Edition = edition
			}
			if err := validateDNSPodCredentials(credentials); err != nil {
				mutationErr = err
				return err
			}
			if request.Verify {
				if err := s.testDNSPodCredentials(ctx, credentials); err != nil {
					mutationErr = err
					return err
				}
			}
			cfg.values[dnspodEditionKey] = phpConfigValue{kind: phpConfigScalar, scalar: credentials.Edition}
			cfg.values[dnspodAuthTypeKey] = phpConfigValue{kind: phpConfigScalar, scalar: credentials.AuthType}
			keys := []string{dnspodEditionKey, dnspodAuthTypeKey}
			if credentials.AuthType == dnspod.AuthTypeToken {
				delete(cfg.values, dnspodSecretIDKey)
				delete(cfg.values, dnspodSecretKeyKey)
				cfg.order = removeConfigKeys(cfg.order, dnspodSecretIDKey, dnspodSecretKeyKey)
				cfg.values[dnspodAPITokenKey] = phpConfigValue{kind: phpConfigScalar, scalar: credentials.APIToken}
				keys = append(keys, dnspodAPITokenKey)
			} else {
				delete(cfg.values, dnspodAPITokenKey)
				cfg.order = removeConfigKeys(cfg.order, dnspodAPITokenKey)
				cfg.values[dnspodSecretIDKey] = phpConfigValue{kind: phpConfigScalar, scalar: credentials.SecretID}
				cfg.values[dnspodSecretKeyKey] = phpConfigValue{kind: phpConfigScalar, scalar: credentials.SecretKey}
				keys = append(keys, dnspodSecretIDKey, dnspodSecretKeyKey)
			}
			cfg.order = appendMissingConfigKeys(cfg.order, sortedMissingKeys(cfg, keys...), cfg.values)
		}
		return nil
	})
	if err != nil {
		if mutationErr != nil {
			return DNSPodConfigStatus{}, mutationErr
		}
		return DNSPodConfigStatus{}, fmt.Errorf("保存 DNSPod 配置失败: %w", err)
	}
	return s.GetDNSPodConfig(ctx)
}

func activeDNSPodEnvironmentOverrides() []string {
	keys := []string{"DNSPOD_AUTH_TYPE", "DNSPOD_API_TOKEN", "DNSPOD_SECRET_ID", "DNSPOD_SECRET_KEY", "DNSPOD_EDITION"}
	active := make([]string, 0, len(keys))
	for _, key := range keys {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			active = append(active, key)
		}
	}
	return active
}

func (s *DBService) TestDNSPodConfig(ctx context.Context, request DNSPodConfigSaveRequest) error {
	stored, err := s.dnspodCredentials()
	if err != nil {
		return err
	}
	credentials := dnspodCredentials{
		SecretID: strings.TrimSpace(request.SecretID), SecretKey: strings.TrimSpace(request.SecretKey), APIToken: strings.TrimSpace(request.APIToken),
		Edition: normalizeDNSPodEdition(request.Edition), AuthType: normalizeDNSPodAuthType(request.AuthType, request.APIToken), Source: "temporary",
	}
	if credentials.SecretID == "" {
		credentials.SecretID = stored.SecretID
	}
	if credentials.SecretKey == "" {
		credentials.SecretKey = stored.SecretKey
	}
	if credentials.APIToken == "" {
		credentials.APIToken = stored.APIToken
	}
	if strings.TrimSpace(request.Edition) == "" {
		credentials.Edition = stored.Edition
	}
	if strings.TrimSpace(request.AuthType) == "" {
		credentials.AuthType = stored.AuthType
	}
	if err := validateDNSPodCredentials(credentials); err != nil {
		return err
	}
	return s.testDNSPodCredentials(ctx, credentials)
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
		DomainID:   request.DomainID,
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

func (s *DBService) ListDNSPodRecordLines(ctx context.Context, domain string, domainID int64, domainGrade, recordType string) (dnspod.DescribeRecordLineListResult, error) {
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
		Domain: domain, DomainID: domainID, DomainGrade: domainGrade, RecordType: recordType,
	})
}

func (s *DBService) SaveDNSPodRecord(ctx context.Context, request DNSPodRecordSaveRequest) (dnspod.RecordMutationResult, error) {
	request.Domain = strings.TrimSpace(request.Domain)
	request.DomainGrade = strings.TrimSpace(request.DomainGrade)
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
		Domain: request.Domain, DomainID: request.DomainID, DomainGrade: request.DomainGrade, RecordID: request.RecordID, SubDomain: request.SubDomain,
		RecordType: request.RecordType, RecordLine: request.RecordLine, RecordLineID: request.RecordLineID,
		Value: request.Value, TTL: request.TTL, MX: request.MX, Weight: request.Weight,
	}
	if request.RecordID > 0 {
		return client.ModifyRecord(ctx, mutation)
	}
	return client.CreateRecord(ctx, mutation)
}

func (s *DBService) DeleteDNSPodRecord(ctx context.Context, domain string, domainID, recordID int64) error {
	domain = strings.TrimSpace(domain)
	if domain == "" || recordID <= 0 {
		return errors.New("域名或记录 ID 无效")
	}
	client, err := s.dnspodClient()
	if err != nil {
		return err
	}
	return client.DeleteRecord(ctx, dnspod.DeleteRecordRequest{Domain: domain, DomainID: domainID, RecordID: recordID})
}

func (s *DBService) SetDNSPodRecordStatus(ctx context.Context, domain string, domainID, recordID int64, status string) error {
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
	return client.ModifyRecordStatus(ctx, dnspod.ModifyRecordStatusRequest{Domain: domain, DomainID: domainID, RecordID: recordID, Status: status})
}

type dnspodCredentials struct {
	SecretID  string
	SecretKey string
	APIToken  string
	AuthType  string
	Edition   string
	Source    string
}

func (s *DBService) dnspodCredentials() (dnspodCredentials, error) {
	cfg, err := loadAdminConfigStore(adminConfigPath())
	if err != nil {
		return dnspodCredentials{}, err
	}
	credentials := dnspodCredentials{
		SecretID:  strings.TrimSpace(cfg.stringValue(dnspodSecretIDKey, "")),
		SecretKey: strings.TrimSpace(cfg.stringValue(dnspodSecretKeyKey, "")),
		APIToken:  strings.TrimSpace(cfg.stringValue(dnspodAPITokenKey, "")),
		Edition:   normalizeDNSPodEdition(cfg.stringValue(dnspodEditionKey, "")),
		Source:    "config",
	}
	credentials.AuthType = normalizeDNSPodAuthType(cfg.stringValue(dnspodAuthTypeKey, ""), credentials.APIToken)
	if credentials.SecretID == "" && credentials.SecretKey == "" && credentials.APIToken == "" {
		credentials.Source = ""
	}
	envID := strings.TrimSpace(os.Getenv("DNSPOD_SECRET_ID"))
	envKey := strings.TrimSpace(os.Getenv("DNSPOD_SECRET_KEY"))
	envToken := strings.TrimSpace(os.Getenv("DNSPOD_API_TOKEN"))
	envAuthType := strings.TrimSpace(os.Getenv("DNSPOD_AUTH_TYPE"))
	envEdition := strings.TrimSpace(os.Getenv("DNSPOD_EDITION"))
	if envID != "" {
		credentials.SecretID = envID
	}
	if envKey != "" {
		credentials.SecretKey = envKey
	}
	if envToken != "" {
		credentials.APIToken = envToken
	}
	switch {
	case envAuthType != "":
		credentials.AuthType = normalizeDNSPodAuthType(envAuthType, envToken)
	case envToken != "":
		credentials.AuthType = dnspod.AuthTypeToken
	case envID != "" || envKey != "":
		credentials.AuthType = dnspod.AuthTypeTC3
	}
	if envEdition != "" {
		credentials.Edition = normalizeDNSPodEdition(envEdition)
	}
	if credentials.AuthType == dnspod.AuthTypeToken {
		credentials.Edition = dnspod.EditionInternational
	}
	if envID != "" || envKey != "" || envToken != "" || envAuthType != "" || envEdition != "" {
		credentials.Source = "env"
	}
	return credentials, nil
}

func (s *DBService) dnspodClient() (dnspodAPI, error) {
	credentials, err := s.dnspodCredentials()
	if err != nil {
		return nil, err
	}
	if err := validateDNSPodCredentials(credentials); err != nil {
		return nil, err
	}
	if credentials.AuthType == dnspod.AuthTypeToken {
		if s.dnspodLegacyClientFactory != nil {
			return s.dnspodLegacyClientFactory(credentials.APIToken), nil
		}
		return dnspod.NewLegacyClient(credentials.APIToken), nil
	}
	if s.dnspodClientFactory != nil {
		return s.dnspodClientFactory(credentials.SecretID, credentials.SecretKey, credentials.Edition), nil
	}
	return dnspod.NewClient(credentials.SecretID, credentials.SecretKey, dnspod.WithEndpoint(dnspod.EndpointForEdition(credentials.Edition))), nil
}

func (s *DBService) testDNSPodCredentials(ctx context.Context, credentials dnspodCredentials) error {
	var client dnspodAPI
	if credentials.AuthType == dnspod.AuthTypeToken {
		if s.dnspodLegacyClientFactory != nil {
			client = s.dnspodLegacyClientFactory(credentials.APIToken)
		} else {
			client = dnspod.NewLegacyClient(credentials.APIToken)
		}
	} else if s.dnspodClientFactory != nil {
		client = s.dnspodClientFactory(credentials.SecretID, credentials.SecretKey, credentials.Edition)
	} else {
		client = dnspod.NewClient(credentials.SecretID, credentials.SecretKey, dnspod.WithEndpoint(dnspod.EndpointForEdition(credentials.Edition)))
	}
	_, err := client.DescribeDomainList(ctx, dnspod.DescribeDomainListRequest{Offset: 0, Limit: 1})
	return err
}

func validateDNSPodCredentials(credentials dnspodCredentials) error {
	if credentials.AuthType == dnspod.AuthTypeToken {
		if !dnspod.ValidLegacyToken(credentials.APIToken) {
			return errors.New("请填写完整的 DNSPod API Token，格式为 ID,Token")
		}
		return nil
	}
	if credentials.SecretID == "" || credentials.SecretKey == "" {
		return errors.New("请填写完整的 DNSPod SecretId 和 SecretKey")
	}
	return nil
}

func normalizeDNSPodAuthType(authType, apiToken string) string {
	if strings.EqualFold(strings.TrimSpace(authType), dnspod.AuthTypeToken) ||
		(strings.TrimSpace(authType) == "" && strings.TrimSpace(apiToken) != "") {
		return dnspod.AuthTypeToken
	}
	return dnspod.AuthTypeTC3
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

func maskDNSPodAPIToken(value string) string {
	parts := strings.SplitN(strings.TrimSpace(value), ",", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
		return ""
	}
	return strings.TrimSpace(parts[0]) + ",****"
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
