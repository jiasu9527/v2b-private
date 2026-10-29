package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
// body or arbitrary service error. Status retains whether a request was sent.
type PanelOperationError struct {
	Status  int
	Code    string
	Message string
}

func (e *PanelOperationError) Error() string {
	_, message := panelOperationErrorText(e)
	return message
}

func panelOperationErrorText(e *PanelOperationError) (string, string) {
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
const panelResultLifetime = 30 * time.Minute

func panelOperationPermitted(op PanelOperation) bool {
	if !panelOperationName.MatchString(op.Name) || op.Path == "" || !strings.HasPrefix(op.Path, "/") || path.Clean(op.Path) != op.Path || strings.ContainsAny(op.Path, "%?#\\\x00") || strings.Contains(op.Path, ":") || strings.Contains(op.Path, "//") {
		return false
	}
	if op.Method != "GET" && op.Method != "POST" && op.Method != "PUT" && op.Method != "PATCH" && op.Method != "DELETE" {
		return false
	}
	for _, segment := range append(strings.Split(strings.ToLower(op.Path), "/"), strings.ToLower(op.Category)) {
		if oneOf(segment, "payment", "payments", "payment_configuration", "ai-ops", "ai_ops") {
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
	if len(raw) == 0 || len(raw) > 24000 || !utf8.Valid(raw) {
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
