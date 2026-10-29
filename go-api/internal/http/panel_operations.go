package httpapi

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

// This adapter is private to the AI service. The model selects a catalog name,
// never a URL, HTTP method, session token, or an arbitrary database statement.
type panelOperations struct {
	cfg        config.Config
	state      *routerState
	operations map[string]aiops.PanelOperation
}

func newPanelOperations(cfg config.Config, state *routerState) *panelOperations {
	p := &panelOperations{cfg: cfg, state: state, operations: map[string]aiops.PanelOperation{}}
	for _, op := range panelOperationCatalog() {
		p.operations[op.Name] = op
	}
	return p
}

func (p *panelOperations) Operations() []aiops.PanelOperation {
	result := make([]aiops.PanelOperation, 0, len(p.operations))
	for _, op := range p.operations {
		if panelOperationPathAllowed(op.Path) {
			op.Parameters = append(json.RawMessage(nil), op.Parameters...)
			result = append(result, op)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func panelOperationPathAllowed(path string) bool {
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#%\\") || strings.Contains(path, "..") || strings.Contains(path, "//") {
		return false
	}
	for _, blocked := range []string{"/payment", "/ai-ops", "/session", "/auth", "/passport"} {
		if path == blocked || strings.HasPrefix(path, blocked+"/") {
			return false
		}
	}
	return true
}

func panelOperationParams(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if len(raw) > 32768 {
		return nil, aiops.ErrValidation
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var params map[string]any
	if decoder.Decode(&params) != nil || params == nil {
		return nil, aiops.ErrValidation
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return nil, aiops.ErrValidation
	}
	for key := range params {
		if strings.TrimSpace(key) != key || key == "" || strings.EqualFold(key, "auth_data") || strings.EqualFold(key, "authorization") || strings.ContainsAny(key, "[]\x00") {
			return nil, aiops.ErrValidation
		}
	}
	return params, nil
}

func (p *panelOperations) Validate(ctx context.Context, actor aiops.PanelOperationActor, name string, raw json.RawMessage) error {
	op, ok := p.operations[name]
	if !ok || !panelOperationPathAllowed(op.Path) || actor.ID <= 0 || op.AdminOnly && !actor.IsAdmin {
		return aiops.ErrForbidden
	}
	if op.Method != http.MethodGet && op.Method != http.MethodPost && op.Method != http.MethodPut && op.Method != http.MethodDelete && op.Method != http.MethodPatch {
		return aiops.ErrForbidden
	}
	params, err := panelOperationParams(raw)
	if err != nil {
		return err
	}
	if op.Method == http.MethodPost {
		// Reuse the business validator before proposing or dispatching a write.
		// A missing protocol must not become an ambiguous HTTP failure after
		// automatic approval, and no connection values may be guessed here.
		parts := strings.Split(op.Path, "/")
		if len(parts) == 4 && parts[1] == "server" && parts[3] == "save" {
			switch parts[2] {
			case "shadowsocks", "vmess", "vless", "trojan", "tuic", "hysteria", "anytls", "v2node":
				if err := admin.ValidateManagedServerSavePayload(parts[2], params); err != nil {
					return err
				}
			}
		}
	}
	if len(op.Parameters) > 0 {
		var schema map[string]any
		if json.Unmarshal(op.Parameters, &schema) != nil || !panelSchemaMatches(params, schema) {
			return aiops.ErrValidation
		}
	}
	if strings.Contains(op.Path, "{id}") {
		if _, ok := panelPositiveID(params["id"]); !ok {
			return aiops.ErrValidation
		}
	}
	if strings.Contains(strings.ReplaceAll(op.Path, "{id}", "1"), "{") {
		return aiops.ErrForbidden
	}
	if op.Path == "/server/manage/getNodes" {
		if value, exists := params["node_id"]; exists {
			if _, ok := panelPositiveID(value); !ok {
				return aiops.ErrValidation
			}
		}
		if value, exists := params["include_install_command"]; exists {
			include, ok := value.(bool)
			if !ok {
				return aiops.ErrValidation
			}
			if include {
				if _, ok := panelPositiveID(params["node_id"]); !ok {
					return aiops.ErrValidation
				}
			}
		}
	}
	if err := p.validateConfig(ctx, actor, op.Path, params); err != nil {
		return err
	}
	return p.validateUserScope(ctx, actor, op.Path, params)
}

func (p *panelOperations) Execute(ctx context.Context, actor aiops.PanelOperationActor, name string, raw json.RawMessage) (json.RawMessage, error) {
	if err := p.Validate(ctx, actor, name, raw); err != nil {
		return nil, err
	}
	op := p.operations[name]
	params, _ := panelOperationParams(raw)
	includeServerCredential := false
	includeInstallation := false
	var installationNodeID int64
	if op.Path == "/server/manage/getNodes" {
		includeInstallation, _ = params["include_install_command"].(bool)
		installationNodeID, _ = panelPositiveID(params["node_id"])
		delete(params, "include_install_command")
		delete(params, "node_id")
	}
	if op.Path == "/config/fetch" {
		includeServerCredential, _ = params["include_credentials"].(bool)
		delete(params, "include_credentials")
	}
	path := op.Path
	if strings.Contains(path, "{id}") {
		id, _ := panelPositiveID(params["id"])
		path = strings.ReplaceAll(path, "{id}", strconv.FormatInt(id, 10))
		delete(params, "id")
	}
	if !actor.IsAdmin {
		panelScopeUserFilters(path, params)
		ctx = admin.WithAIStaffScope(ctx)
	}
	// Build a private router with the same business services. The temporary
	// identity grants only this already-validated operation, keeps the real actor
	// ID for audit, and never creates a browser session or usable bearer token.
	cfg := p.cfg
	if p.state.runtime != nil {
		cfg = p.state.runtime.CurrentConfig()
	}
	cfg.AdminPath = "__ai_operation_internal__"
	cfg.AccessLogEnabled = false
	cfg.SlowRequestLogThreshold = 0
	actorEmail := ""
	if path == "/config/testSendMail" && p.state.admin != nil {
		account, err := p.state.admin.GetUserInfoByID(ctx, actor.ID)
		if err != nil {
			return nil, aiops.ErrUnavailable
		}
		actorEmail, _ = account["email"].(string)
		if actorEmail == "" {
			return nil, aiops.ErrUnavailable
		}
	}
	router := NewRouter(cfg, func(state *routerState) {
		*state = *p.state
		state.runtime = nil
		state.aiOps = nil
		state.session = panelOperationSession{actor: actor, email: actorEmail}
	})
	values := url.Values{}
	if err := panelFlattenParams(values, "", params, 0); err != nil {
		return nil, err
	}
	address := "http://ai-operation.invalid/api/v1/" + cfg.AdminPath + path
	var body io.Reader
	if op.Method == http.MethodGet {
		address += "?" + values.Encode()
	} else {
		encoded, err := json.Marshal(params)
		if err != nil {
			return nil, aiops.ErrValidation
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, op.Method, address, body)
	if err != nil {
		return nil, aiops.ErrValidation
	}
	req.Header.Set("Authorization", "internal-operation-only")
	if op.Method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
		// Legacy list/filter handlers consume bracket-form fields while newer
		// handlers strictly decode JSON. Supply both views from one input object.
		req.PostForm = values
		req.Form = values
	}
	recorder := &panelOperationResponse{header: http.Header{}, status: 200}
	router.ServeHTTP(recorder, req)
	if recorder.overflow {
		return nil, fmt.Errorf("后台响应超过 4 MiB，结果未返回；操作可能已完成，禁止自动重试写入，请缩小查询范围核对")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if recorder.status < 200 || recorder.status >= 300 {
		// Do not expose SQL, config contents, upstream bodies or submitted secrets
		// via a backend error. The operation and HTTP code are enough to retry reads.
		return nil, &aiops.PanelOperationError{Status: recorder.status}
	}
	contentType := recorder.header.Get("Content-Type")
	if op.Path == "/user/dumpCSV" || op.Path == "/user/generate" || op.Path == "/coupon/generate" || op.Path == "/giftcard/generate" {
		contentType = "text/csv"
	}
	response := recorder.body.Bytes()
	if op.Path == "/server/manage/getNodes" && installationNodeID > 0 {
		return filterPanelInstallationResponse(response, installationNodeID, includeInstallation)
	}
	if op.Path == "/config/fetch" {
		response, err = filterPanelConfigResponse(response, fmt.Sprint(params["key"]))
		if err != nil {
			return nil, err
		}
	}
	if op.Path == "/system/getSystemLog" {
		response, err = filterPanelSystemLogs(response)
		if err != nil {
			return nil, err
		}
	}
	sanitized, err := sanitizePanelResponse(response, contentType)
	if err != nil {
		return nil, err
	}
	return restorePanelExplicitCredentials(op.Path, op.Method, includeServerCredential, response, sanitized)
}

type panelOperationSession struct {
	actor aiops.PanelOperationActor
	email string
}

func (s panelOperationSession) Authenticate(_ context.Context, token string, _ bool) (*session.Identity, error) {
	if token != "internal-operation-only" || s.actor.ID <= 0 {
		return nil, session.ErrUnauthorized
	}
	return &session.Identity{ID: s.actor.ID, Email: s.email, IsAdmin: 1}, nil
}
func (s panelOperationSession) AuthenticateFresh(ctx context.Context, token string, requireAdmin bool) (*session.Identity, error) {
	return s.Authenticate(ctx, token, requireAdmin)
}
func (panelOperationSession) ListSessions(context.Context, int64) (map[string]session.SessionMeta, error) {
	return nil, session.ErrUnauthorized
}
func (panelOperationSession) RemoveSession(context.Context, int64, string) (bool, error) {
	return false, session.ErrUnauthorized
}

const panelOperationResponseLimit = 4 << 20

type panelOperationResponse struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	overflow bool
}

func (w *panelOperationResponse) Header() http.Header    { return w.header }
func (w *panelOperationResponse) WriteHeader(status int) { w.status = status }
func (w *panelOperationResponse) Write(data []byte) (int, error) {
	if w.body.Len()+len(data) > panelOperationResponseLimit {
		w.overflow = true
		return len(data), nil
	}
	if !w.overflow {
		_, _ = w.body.Write(data)
	}
	return len(data), nil
}

func panelPositiveID(v any) (int64, bool) {
	var text string
	switch v := v.(type) {
	case json.Number:
		text = string(v)
	case string:
		text = v
	default:
		return 0, false
	}
	id, err := strconv.ParseInt(text, 10, 64)
	return id, err == nil && id > 0
}

func panelFlattenParams(values url.Values, key string, value any, depth int) error {
	if depth > 10 {
		return aiops.ErrValidation
	}
	switch value := value.(type) {
	case map[string]any:
		for name, item := range value {
			if name == "" || strings.ContainsAny(name, "[]\x00") {
				return aiops.ErrValidation
			}
			child := name
			if key != "" {
				child = key + "[" + name + "]"
			}
			if err := panelFlattenParams(values, child, item, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for i, item := range value {
			if err := panelFlattenParams(values, key+"["+strconv.Itoa(i)+"]", item, depth+1); err != nil {
				return err
			}
		}
	case nil:
		values.Set(key, "")
	default:
		values.Set(key, fmt.Sprint(value))
	}
	return nil
}

func panelSchemaMatches(value any, schema map[string]any) bool {
	if enum, ok := schema["enum"].([]any); ok {
		matched := false
		for _, choice := range enum {
			a, _ := json.Marshal(choice)
			b, _ := json.Marshal(value)
			if bytes.Equal(a, b) {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	kind, _ := schema["type"].(string)
	if kinds, ok := schema["type"].([]any); ok {
		for _, k := range kinds {
			copySchema := map[string]any{}
			for key, v := range schema {
				copySchema[key] = v
			}
			copySchema["type"] = k
			if panelSchemaMatches(value, copySchema) {
				return true
			}
		}
		return false
	}
	switch kind {
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return false
		}
		props, _ := schema["properties"].(map[string]any)
		if required, ok := schema["required"].([]any); ok {
			for _, key := range required {
				if _, exists := obj[fmt.Sprint(key)]; !exists {
					return false
				}
			}
		}
		for key, item := range obj {
			if property, ok := props[key].(map[string]any); ok {
				if !panelSchemaMatches(item, property) {
					return false
				}
			} else if schema["additionalProperties"] == false {
				return false
			} else if sub, ok := schema["additionalProperties"].(map[string]any); ok && !panelSchemaMatches(item, sub) {
				return false
			}
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return false
		}
		if max, ok := schema["maxItems"].(float64); ok && float64(len(items)) > max {
			return false
		}
		if min, ok := schema["minItems"].(float64); ok && float64(len(items)) < min {
			return false
		}
		if sub, ok := schema["items"].(map[string]any); ok {
			for _, item := range items {
				if !panelSchemaMatches(item, sub) {
					return false
				}
			}
		}
	case "integer", "number":
		n, ok := value.(json.Number)
		if !ok {
			return false
		}
		if kind == "integer" {
			if _, err := n.Int64(); err != nil {
				return false
			}
		}
		v, err := n.Float64()
		if err != nil {
			return false
		}
		if max, ok := schema["maximum"].(float64); ok && v > max {
			return false
		}
		if min, ok := schema["minimum"].(float64); ok && v < min {
			return false
		}
	case "string":
		v, ok := value.(string)
		if !ok {
			return false
		}
		if max, ok := schema["maxLength"].(float64); ok && float64(len([]rune(v))) > max {
			return false
		}
		if min, ok := schema["minLength"].(float64); ok && float64(len([]rune(v))) < min {
			return false
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return false
		}
	case "null":
		if value != nil {
			return false
		}
	}
	return true
}

func sensitivePanelKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	switch key {
	case "password_limit_enable", "password_limit_count", "password_limit_expire", "subscribe_token_in_path", "token_configured", "secret_configured", "has_api_key", "has_password", "has_private_key", "has_credentials":
		return false
	}
	normalized := strings.ReplaceAll(key, "_", "")
	for _, piece := range []string{"password", "secret", "privatekey", "apikey", "credential", "authdata", "accesstoken", "refreshtoken", "clientkey", "installcommand", "accesskey", "serverkey", "encryptionkey"} {
		if strings.Contains(normalized, piece) {
			return true
		}
	}
	return key == "key" || key == "auth" || key == "token" || key == "uuid" || key == "authorization" || key == "server_token" || strings.HasSuffix(key, "_token") || key == "payment_config" || key == "payment_configuration" || key == "merchant_id" || key == "notify_domain" || key == "recaptcha_key" || key == "cookie" || key == "cookies" || key == "pass" || key == "passwd"
}

func scrubPanelValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			_, userRecord := typed["email"]
			if sensitivePanelKey(key) || userRecord && (key == "subscribe_url" || key == "subscribe_urls") {
				typed[key] = "[已隐藏凭据]"
			} else {
				typed[key] = scrubPanelValue(item)
			}
		}
	case []any:
		for i, item := range typed {
			typed[i] = scrubPanelValue(item)
		}
	}
	return value
}

func sanitizePanelResponse(raw []byte, contentType string) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) == nil {
		return json.Marshal(scrubPanelValue(value))
	}
	if strings.Contains(strings.ToLower(contentType), "csv") {
		reader := csv.NewReader(bytes.NewReader(raw))
		reader.FieldsPerRecord = -1
		rows, err := reader.ReadAll()
		if err != nil {
			return nil, errors.New("后台导出内容无效")
		}
		if len(rows) > 0 {
			for column, key := range rows[0] {
				key = strings.TrimPrefix(key, "\uFEFF")
				if sensitivePanelKey(key) || key == "密码" || key == "订阅链接" || key == "订阅地址" {
					for i := 1; i < len(rows); i++ {
						if column < len(rows[i]) {
							rows[i][column] = "[已隐藏凭据]"
						}
					}
				}
			}
		}
		return json.Marshal(map[string]any{"format": "csv", "rows": rows})
	}
	return nil, errors.New("后台响应不是可读取的 JSON 或 CSV，未返回原始内容")
}
