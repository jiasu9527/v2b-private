package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type PanelOperation struct {
	Name        string          `json:"name"`
	Category    string          `json:"category"`
	Description string          `json:"description"`
	Method      string          `json:"method"`
	Path        string          `json:"path"`
	Mutating    bool            `json:"mutating"`
	AdminOnly   bool            `json:"admin_only"`
	Parameters  json.RawMessage `json:"parameters"`
}

type PanelOperationActor struct {
	ID      int64 `json:"id"`
	IsAdmin bool  `json:"is_admin"`
}

// Only adapter-generated error categories are exposed, never an HTTP response
// body or arbitrary service error. HTTP status alone cannot prove whether a
// mutation ran. NotExecuted is set only by trusted in-process preflight code.
type PanelOperationError struct {
	Status      int
	Code        string
	Message     string
	NotExecuted bool
	// Retryable is set only by trusted in-process preflight classification.
	// It must never be inferred from an upstream response body or status alone.
	Retryable bool
}

func (e *PanelOperationError) Error() string {
	_, message := panelOperationErrorText(e)
	return message
}

func panelOperationErrorText(e *PanelOperationError) (string, string) {
	if e.NotExecuted {
		switch e.Code {
		case "entry_retirement_invalid_arguments":
			return e.Code, "旧入口组清理参数校验失败，未执行清理；请读取entry_groups.retire_preview和entry_groups.retire的schema，使用真实叶子ID和预览返回的preview_id/preview_hash，不要原样重试"
		case "entry_retirement_conflict":
			return e.Code, "旧入口组清理预览已过期或配置、名单已变化，未执行清理；先用entry_groups.retirement按preview_id核对回执，再用entry_groups.audit重新核对范围并生成新预览，不要原样重试"
		case "entry_retirement_blocked":
			return e.Code, "旧入口组清理未通过覆盖或引用校验，未执行清理；请查询entry_groups.audit及相关规则，核对替代组、保底范围、合集和IP引用，不要改用整条规则删除绕过，也不要原样重试"
		case "entry_retirement_not_found":
			return e.Code, "旧入口组或清理预览不存在，未执行清理；请核对entry_groups.audit及entry_groups.retirement，后者id使用preview_id而不是operation_id，不要原样重试"
		case "entry_monitor_invalid_arguments":
			return e.Code, "入口监测参数校验失败，未执行监测配置写入；请查看操作schema并查询现状后修正参数。增量修改或分批添加请用entry_monitors.upsert；entry_monitors.save是全量替换，不能分批提交，不要原样重试"
		case "entry_monitor_conflict":
			return e.Code, "入口监测版本或目标已变化，未执行监测配置写入；请重新查询监测配置取得最新revision并核对目标，再提交修正后的请求。增量修改请用entry_monitors.upsert，不要原样重试"
		case "entry_order_invalid_arguments":
			return e.Code, "入口排序参数校验失败，未执行排序；请读取entry_rules.order取得真实kind/id和revision，用entry_rules.move将选中行移到after锚点后；完整sort必须包含所有普通规则和当前叶子，不能传父容器或仅A/B，不要原样重试"
		case "entry_order_conflict":
			return e.Code, "入口排序版本或可见行已变化，未执行排序；请重新读取entry_rules.order核对锚点和目标叶子，用最新revision重新规划，不能原样重试"
		case "ip_pool_invalid_arguments":
			return e.Code, "IP池批量删除参数校验失败，未执行删除；scope必填，指定名单须用selected并传非空ids，faulty/all不得传非空ids。请查询目录并修正参数，不要原样重试"
		case "dns_invalid_arguments":
			return e.Code, "DNS 参数未通过校验，未发送写请求；请查询域名与线路信息后修正参数"
		case "dns_domain_metadata":
			return e.Code, "未能确认域名的 ID 和套餐等级，未发送写请求；请查询 dns.domains，核对准确域名、DomainId 与 Grade"
		case "dns_record_line_unavailable":
			return e.Code, "未能确认所选 DNS 线路，未发送写请求；请用 dns.record_lines 查询该域名实际可用线路，核对套餐等级及线路名称和 ID"
		case "dns_preflight_failed":
			return e.Code, "DNS 写入前检查失败，未发送写请求；请查询 DNS 配置、域名和线路现状，排除原因后再提交修正请求"
		}
	}
	switch e.Status {
	case 400, 422:
		return "invalid_arguments", "后台操作参数未通过校验，请按该操作的参数说明修正"
	case 401, 403:
		return "forbidden", "后台操作权限不足或身份已变化，请停止当前操作"
	case 404:
		return "not_found", "后台操作目标不存在，请核对准确ID和操作目录"
	case 409:
		return "conflict", "后台操作状态已变化，请重新查询现状，不要重复写操作"
	case 429:
		return "rate_limited", "后台操作请求过于频繁，请稍后查询现状，不要重复写操作"
	default:
		return "backend_error", "后台操作返回异常，请核对服务与业务现状；写操作禁止自动重试"
	}
}

type PanelOperations interface {
	Operations() []PanelOperation
	Validate(context.Context, PanelOperationActor, string, json.RawMessage) error
	Execute(context.Context, PanelOperationActor, string, json.RawMessage) (json.RawMessage, error)
}

func (s *Service) SetPanelOperations(operations PanelOperations) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.panelOperations = operations
	s.mu.Unlock()
}

func (s *Service) getPanelOperations() PanelOperations {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.panelOperations
}

var panelOperationName = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

const maxPanelResultBytes = 4 << 20
const maxPanelParametersBytes = 24000
const panelResultLifetime = 30 * time.Minute

// This error carries only byte counts. Parameter contents may contain secrets
// and must never be attached to validation diagnostics.
type panelParametersTooLargeError struct {
	Bytes int
	Limit int
}

func (e *panelParametersTooLargeError) Error() string {
	return fmt.Sprintf("后台操作parameters为%d字节，超过%d字节上限，未执行操作；请缩小参数并优先使用增量操作。入口监测分批添加或修改请用entry_monitors.upsert；entry_monitors.save是全量替换，不能分批提交。不要原样重试", e.Bytes, e.Limit)
}

func (e *panelParametersTooLargeError) Unwrap() error { return ErrValidation }

func panelOperationPermitted(op PanelOperation) bool {
	if !panelOperationName.MatchString(op.Name) || op.Path == "" || !strings.HasPrefix(op.Path, "/") || path.Clean(op.Path) != op.Path || strings.ContainsAny(op.Path, "%?#\\\x00") || strings.Contains(op.Path, ":") || strings.Contains(op.Path, "//") {
		return false
	}
	if op.Method != "GET" && op.Method != "POST" && op.Method != "PUT" && op.Method != "PATCH" && op.Method != "DELETE" {
		return false
	}
	for _, segment := range append(strings.Split(strings.ToLower(op.Path), "/"), strings.ToLower(op.Category)) {
		if oneOf(segment, "payment", "payments", "payment_configuration", "new-api", "new_api", "ai-ops", "ai_ops") {
			return false
		}
	}
	var schema struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(op.Parameters, &schema) == nil && schema.Type == "object"
}

func findPanelOperation(runtime PanelOperations, name string) (PanelOperation, error) {
	if runtime == nil {
		return PanelOperation{}, ErrUnavailable
	}
	var match *PanelOperation
	for _, operation := range runtime.Operations() {
		if operation.Name == name {
			if match != nil || !panelOperationPermitted(operation) {
				return PanelOperation{}, ErrForbidden
			}
			copy := operation
			match = &copy
		}
	}
	if match == nil {
		return PanelOperation{}, ErrForbidden
	}
	return *match, nil
}

func panelActor(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id int64, lock bool) (PanelOperationActor, error) {
	actor := PanelOperationActor{ID: id}
	query := `SELECT is_admin=1 FROM v2_user WHERE id=$1 AND banned=0 AND (is_admin=1 OR is_staff=1)`
	if lock {
		query += ` FOR SHARE`
	}
	err := q.QueryRowContext(ctx, query, id).Scan(&actor.IsAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrForbidden
	}
	return actor, err
}

func panelAdminSession(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, sessionID int64, lock bool) error {
	query := `SELECT u.id FROM v2_ai_session s JOIN v2_user u ON u.id=s.created_by WHERE s.id=$1 AND u.is_admin=1 AND u.banned=0`
	if lock {
		query += ` FOR SHARE OF u`
	}
	var owner int64
	err := q.QueryRowContext(ctx, query, sessionID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrForbidden
	}
	return err
}

func normalizedPanelParameters(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) > maxPanelParametersBytes {
		return nil, &panelParametersTooLargeError{Bytes: len(raw), Limit: maxPanelParametersBytes}
	}
	if len(raw) == 0 || !utf8.Valid(raw) {
		return nil, ErrValidation
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, ErrValidation
	}
	return json.Marshal(object)
}

func panelSensitiveField(key string) bool {
	key = strings.ToLower(key)
	for _, suffix := range []string{"_enabled", "_enable", "_configured", "_count", "_limit", "_expires_at", "_expire", "_in_path"} {
		if strings.HasSuffix(key, suffix) {
			return false
		}
	}
	if strings.Contains(key, "_limit_") {
		return false
	}
	key = strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", "")
	for _, token := range []string{"password", "passwd", "secret", "token", "privatekey", "publickey", "apikey", "accesskey", "credential", "authorization", "cookie", "security", "serverkey", "encryptionkey", "appkey", "smtpkey"} {
		if strings.Contains(key, token) {
			return true
		}
	}
	return key == "key" || key == "auth" || key == "uuid" || key == "pass" || key == "mailpass" || key == "smtppass"
}

func previewPanelParameters(raw json.RawMessage) (json.RawMessage, []string) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return json.RawMessage(`{}`), nil
	}
	secrets := []string{}
	var scrub func(any) any
	var collect func(any)
	collect = func(item any) {
		switch v := item.(type) {
		case string:
			if v != "" {
				secrets = append(secrets, v)
			}
		case map[string]any:
			for _, child := range v {
				collect(child)
			}
		case []any:
			for _, child := range v {
				collect(child)
			}
		}
	}
	scrub = func(item any) any {
		switch v := item.(type) {
		case map[string]any:
			for key, child := range v {
				if panelSensitiveField(key) {
					collect(child)
					v[key] = "[REDACTED]"
				} else {
					v[key] = scrub(child)
				}
			}
		case []any:
			for i, child := range v {
				v[i] = scrub(child)
			}
		}
		return item
	}
	value = scrub(value)
	var redactKnown func(any) any
	redactKnown = func(item any) any {
		switch v := item.(type) {
		case string:
			for _, secret := range secrets {
				v = strings.ReplaceAll(v, secret, "[REDACTED]")
			}
			return v
		case map[string]any:
			for key, child := range v {
				v[key] = redactKnown(child)
			}
		case []any:
			for i, child := range v {
				v[i] = redactKnown(child)
			}
		}
		return item
	}
	encoded, err := json.Marshal(redactKnown(value))
	if err != nil {
		return json.RawMessage(`{}`), secrets
	}
	return encoded, secrets
}

func redactPanelOperationToolCalls(calls []ToolCall) []ToolCall {
	out := append([]ToolCall(nil), calls...)
	for index := range out {
		if !oneOf(out[index].Function.Name, "panel_query", "propose_panel_operation") {
			continue
		}
		preview, _ := previewPanelParameters(json.RawMessage(out[index].Function.Arguments))
		out[index].Function.Arguments = string(preview)
	}
	return out
}

type panelResultChunk struct {
	ResultID          int64  `json:"result_id"`
	Operation         string `json:"operation"`
	ContentOffset     int64  `json:"content_offset"`
	Content           string `json:"content"`
	TotalCharacters   int64  `json:"total_characters"`
	Truncated         bool   `json:"truncated"`
	NextContentOffset *int64 `json:"next_content_offset,omitempty"`
	ExpiresAt         int64  `json:"expires_at"`
}

func (s *Service) savePanelResult(ctx context.Context, actorID, sessionID int64, operation string, raw json.RawMessage, forceChunk bool) (string, error) {
	if len(raw) > maxPanelResultBytes || !json.Valid(raw) || !utf8.Valid(raw) {
		return "", ErrUnavailable
	}
	// The adapter owns path-aware response scrubbing. Do not hide operational
	// flags such as token_configured or password_limit_enable by key guessing.
	if !forceChunk && len(raw) <= 18000 {
		out, err := json.Marshal(struct {
			Operation string          `json:"operation"`
			Data      json.RawMessage `json:"data"`
		}{operation, raw})
		return string(out), err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err = lockAIState(ctx, tx); err != nil {
		return "", err
	}
	if err = s.requireOperatorTx(ctx, tx, actorID); err != nil {
		return "", err
	}
	if err = s.requireSessionAccessTx(ctx, tx, actorID, sessionID); err != nil {
		return "", err
	}
	now := time.Now().Unix()
	if _, err = tx.ExecContext(ctx, `DELETE FROM v2_ai_result WHERE expires_at<=$1`, now); err != nil {
		return "", err
	}
	var id int64
	descriptor, err := findPanelOperation(s.getPanelOperations(), operation)
	if err != nil {
		return "", err
	}
	if err = tx.QueryRowContext(ctx, `INSERT INTO v2_ai_result(session_id,actor_id,operation,data,expires_at,admin_only) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, sessionID, actorID, operation, string(raw), now+int64(panelResultLifetime/time.Second), descriptor.AdminOnly).Scan(&id); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return s.readPanelResult(ctx, actorID, sessionID, id, 0, 1000)
}

func (s *Service) readPanelResult(ctx context.Context, actorID, sessionID, resultID, offset int64, limit int) (string, error) {
	if resultID <= 0 || offset < 0 || limit < 1 || limit > 2000 {
		return "", ErrValidation
	}
	if err := s.requireSessionAccess(ctx, actorID, sessionID); err != nil {
		return "", err
	}
	chunk := panelResultChunk{ContentOffset: offset}
	err := s.db.QueryRowContext(ctx, `SELECT id,operation,CASE WHEN $3::BIGINT>=char_length(data) THEN '' ELSE substring(data FROM ($3::BIGINT+1)::INTEGER FOR $4) END,char_length(data),expires_at FROM v2_ai_result WHERE id=$1 AND session_id=$2 AND expires_at>$5 AND (NOT admin_only OR EXISTS(SELECT 1 FROM v2_user WHERE id=$6 AND is_admin=1 AND banned=0))`, resultID, sessionID, offset, limit, time.Now().Unix(), actorID).Scan(&chunk.ResultID, &chunk.Operation, &chunk.Content, &chunk.TotalCharacters, &chunk.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	next := offset + int64(utf8.RuneCountInString(chunk.Content))
	chunk.Truncated = next < chunk.TotalCharacters
	if chunk.Truncated {
		chunk.NextContentOffset = &next
	}
	raw, err := json.Marshal(chunk)
	return string(raw), err
}

func sortedPanelOperations(runtime PanelOperations, actor PanelOperationActor, category, query string) []PanelOperation {
	out := []PanelOperation{}
	if runtime == nil {
		return out
	}
	query = strings.ToLower(query)
	for _, op := range runtime.Operations() {
		if !panelOperationPermitted(op) || (op.AdminOnly && !actor.IsAdmin) || (category != "" && op.Category != category) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(op.Name+" "+op.Description+" "+op.Category), query) {
			continue
		}
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
