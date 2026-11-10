package newapibridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const workspacePricingBodyLimit = 4 << 20

// These are public display metadata, not an alternative billing configuration.
// Pointer prices distinguish a missing rate from an explicitly free rate.
type WorkspacePricingModel struct {
	ModelName              string                                `json:"model_name"`
	Description            string                                `json:"description,omitempty"`
	Icon                   string                                `json:"icon,omitempty"`
	Tags                   string                                `json:"tags,omitempty"`
	VendorID               int64                                 `json:"vendor_id,omitempty"`
	OwnerBy                string                                `json:"owner_by,omitempty"`
	QuotaType              *int                                  `json:"quota_type,omitempty"`
	ModelRatio             *float64                              `json:"model_ratio,omitempty"`
	ModelPrice             *float64                              `json:"model_price,omitempty"`
	CompletionRatio        *float64                              `json:"completion_ratio,omitempty"`
	CacheRatio             *float64                              `json:"cache_ratio,omitempty"`
	CreateCacheRatio       *float64                              `json:"create_cache_ratio,omitempty"`
	ImageRatio             *float64                              `json:"image_ratio,omitempty"`
	AudioRatio             *float64                              `json:"audio_ratio,omitempty"`
	AudioCompletionRatio   *float64                              `json:"audio_completion_ratio,omitempty"`
	EnableGroups           []string                              `json:"enable_groups"`
	SupportedEndpointTypes []string                              `json:"supported_endpoint_types"`
	BillingMode            string                                `json:"billing_mode,omitempty"`
	BillingExpr            string                                `json:"billing_expr,omitempty"`
	BillingUsageSchema     map[string]WorkspacePricingUsageField `json:"billing_usage_schema,omitempty"`
	BillingUsageExamples   []WorkspacePricingUsageExample        `json:"billing_usage_examples,omitempty"`
	BillingPluginVariants  []WorkspacePricingPlugin              `json:"billing_plugin_variants,omitempty"`
	PricingVersion         string                                `json:"pricing_version,omitempty"`
}
type WorkspacePricingUsageField struct {
	Type        string         `json:"type,omitempty"`
	Unit        string         `json:"unit,omitempty"`
	UnitLabel   any            `json:"unitLabel,omitempty"`
	Enum        []string       `json:"enum,omitempty"`
	Description any            `json:"description,omitempty"`
	EnumLabels  map[string]any `json:"enumLabels,omitempty"`
}
type WorkspacePricingUsageExample struct {
	Label string         `json:"label"`
	Facts map[string]any `json:"facts"`
}
type WorkspacePricingPlugin struct {
	PluginKey            string                                `json:"plugin_key"`
	PluginName           string                                `json:"plugin_name"`
	Icon                 string                                `json:"icon,omitempty"`
	BillingExpr          string                                `json:"billing_expr"`
	BillingMode          string                                `json:"billing_mode"`
	BillingUsageSchema   map[string]WorkspacePricingUsageField `json:"billing_usage_schema,omitempty"`
	BillingUsageExamples []WorkspacePricingUsageExample        `json:"billing_usage_examples,omitempty"`
}
type WorkspacePricingVendor struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
}
type WorkspacePricingEndpoint struct {
	Path   string `json:"path"`
	Method string `json:"method"`
}
type WorkspacePricingCatalog struct {
	Data              []WorkspacePricingModel             `json:"data"`
	GroupRatio        map[string]float64                  `json:"group_ratio"`
	UsableGroup       map[string]string                   `json:"usable_group"`
	AutoGroups        []string                            `json:"auto_groups"`
	Vendors           []WorkspacePricingVendor            `json:"vendors"`
	SupportedEndpoint map[string]WorkspacePricingEndpoint `json:"supported_endpoint"`
	PricingVersion    string                              `json:"pricing_version"`
}
type WorkspacePricing struct {
	SourceURL       string                  `json:"source_url"`
	FetchedAt       int64                   `json:"fetched_at"`
	Scope           string                  `json:"scope"`
	Currency        string                  `json:"currency"`
	USDExchangeRate *float64                `json:"usd_exchange_rate"`
	QuotaPerUnit    *float64                `json:"quota_per_unit"`
	Catalog         WorkspacePricingCatalog `json:"catalog"`
	Warnings        []string                `json:"warnings,omitempty"`
}

// WorkspacePricing is available before linking an AI account. The Forest actor
// still needs a current session and enabled bridge; no New API login is created.
func (s *Service) WorkspacePricing(ctx context.Context, id int64, auth string) (WorkspacePricing, error) {
	cfg, err := s.workspaceContext(ctx, id, auth)
	if err != nil {
		return WorkspacePricing{}, err
	}
	return s.fetchWorkspacePricing(ctx, cfg)
}

func (s *Service) fetchWorkspacePricing(ctx context.Context, cfg Settings) (WorkspacePricing, error) {
	out := WorkspacePricing{}
	base, err := url.Parse(cfg.APIURL)
	if err != nil || base.Host == "" || base.User != nil || (base.Scheme != "https" && base.Scheme != "http") || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		return out, ErrInvalid
	}
	origin := strings.TrimRight(base.String(), "/")
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var catalog struct {
		Success bool `json:"success"`
		WorkspacePricingCatalog
	}
	if err = s.workspacePublicPricingJSON(ctx, origin+"/api/pricing", &catalog); err != nil {
		return out, err
	}
	if !catalog.Success || catalog.Data == nil || len(catalog.Data) > 10000 {
		return out, ErrRemoteUnavailable
	}
	for _, model := range catalog.Data {
		if strings.TrimSpace(model.ModelName) == "" || len(model.ModelName) > 256 {
			return out, ErrRemoteUnavailable
		}
	}
	if catalog.GroupRatio == nil {
		catalog.GroupRatio = map[string]float64{}
	}
	if catalog.UsableGroup == nil {
		catalog.UsableGroup = map[string]string{}
	}
	if catalog.AutoGroups == nil {
		catalog.AutoGroups = []string{}
	}
	if catalog.Vendors == nil {
		catalog.Vendors = []WorkspacePricingVendor{}
	}
	if catalog.SupportedEndpoint == nil {
		catalog.SupportedEndpoint = map[string]WorkspacePricingEndpoint{}
	}
	out = WorkspacePricing{SourceURL: origin + "/pricing", FetchedAt: time.Now().Unix(), Scope: "public_catalog", Currency: "CNY", Catalog: catalog.WorkspacePricingCatalog}
	var status struct {
		Success bool `json:"success"`
		Data    struct {
			USDExchangeRate *float64 `json:"usd_exchange_rate"`
			QuotaPerUnit    *float64 `json:"quota_per_unit"`
		} `json:"data"`
	}
	if s.workspacePublicPricingJSON(ctx, origin+"/api/status", &status) == nil && status.Success {
		if positivePricingNumber(status.Data.USDExchangeRate) {
			out.USDExchangeRate = status.Data.USDExchangeRate
		}
		if positivePricingNumber(status.Data.QuotaPerUnit) {
			out.QuotaPerUnit = status.Data.QuotaPerUnit
		}
	}
	if out.USDExchangeRate == nil || out.QuotaPerUnit == nil {
		out.Warnings = []string{"上游人民币换算或配额参数不可用，缺失的价格无法换算；原始计价信息仍可查看。"}
	}
	return out, nil
}

func positivePricingNumber(value *float64) bool {
	return value != nil && *value > 0 && !math.IsNaN(*value) && !math.IsInf(*value, 0)
}

// Deliberately separate from authenticated workspaceRequest: /api/pricing has
// important sibling metadata outside data, and neither public read gets a token.
func (s *Service) workspacePublicPricingJSON(ctx context.Context, target string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return ErrInvalid
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Forest-AI-Pricing/1")
	client := http.Client{Timeout: 15 * time.Second}
	if s.client != nil {
		client = *s.client
		client.Timeout = 15 * time.Second
	}
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return ErrRemoteUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ErrRemoteUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, workspacePricingBodyLimit+1))
	if err != nil || len(data) > workspacePricingBodyLimit {
		return ErrRemoteUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(result) != nil {
		return ErrRemoteUnavailable
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrRemoteUnavailable
	}
	return nil
}
