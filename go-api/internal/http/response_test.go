package httpapi

import (
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteJSONTranslatesTopLevelErrorMessageToChinese(t *testing.T) {
	rec := httptest.NewRecorder()

	writeJSON(rec, http.StatusServiceUnavailable, map[string]any{
		"message": "session service unavailable",
	})

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	if payload["message"] != "会话服务不可用" {
		t.Fatalf("expected translated message, got %#v", payload["message"])
	}
}

func TestWriteJSONTranslatesLegacyNodeMsgToChinese(t *testing.T) {
	rec := httptest.NewRecorder()

	writeJSON(rec, http.StatusOK, map[string]any{
		"ret": float64(0),
		"msg": "node_id is invalid",
	})

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	if payload["msg"] != "节点 ID 无效" {
		t.Fatalf("expected translated msg, got %#v", payload["msg"])
	}
}

func TestWriteJSONKeepsSuccessMsgUnchanged(t *testing.T) {
	rec := httptest.NewRecorder()

	writeJSON(rec, http.StatusOK, map[string]any{
		"ret": float64(1),
		"msg": "ok",
	})

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	if payload["msg"] != "ok" {
		t.Fatalf("expected success msg to stay ok, got %#v", payload["msg"])
	}
}

func TestWriteJSONFallsBackToGenericChineseForUnknownEnglishError(t *testing.T) {
	rec := httptest.NewRecorder()

	writeJSON(rec, http.StatusInternalServerError, map[string]any{
		"message": "boom from upstream dependency",
	})

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	if payload["message"] != "系统繁忙，请稍后重试" {
		t.Fatalf("expected generic chinese fallback, got %#v", payload["message"])
	}
}

func TestWritePlainTextTranslatesErrorBodyToChinese(t *testing.T) {
	rec := httptest.NewRecorder()

	writePlainText(rec, http.StatusInternalServerError, "read request body: unexpected EOF")

	if rec.Body.String() != "读取请求体失败" {
		t.Fatalf("expected localized plain text body, got %q", rec.Body.String())
	}
}

func TestRenderHTMLLocalizesTemplateExecutionError(t *testing.T) {
	rec := httptest.NewRecorder()
	tmpl := template.Must(template.New("page").Funcs(template.FuncMap{
		"boom": func() (string, error) {
			return "", errors.New("session service unavailable")
		},
	}).Parse(`{{boom}}`))

	renderHTML(rec, tmpl, nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rec.Code)
	}
	if rec.Body.String() != "系统繁忙，请稍后重试" {
		t.Fatalf("expected localized html error body, got %q", rec.Body.String())
	}
}
