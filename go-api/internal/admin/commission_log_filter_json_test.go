package admin

import (
	"encoding/json"
	"testing"
)

func TestCommissionLogJSONFiltersRejectLossySelectors(t *testing.T) {
	for _, raw := range []string{
		`null`, `[]`, `"all"`, `{"unknown":1}`, `{"id":1,"id":2}`,
		`{"id":null}`, `{"ids":null}`, `{"ids":[]}`, `{"invite_user_id":null}`, `{"user_id":null}`,
		`{"trade_no":null}`, `{"trade_no":""}`, `{"trade_no":"  "}`, `{"created_from":null}`, `{"created_to":null}`, `{"commission_status":null}`, `{"visibility":null}`, `{"visibility":""}`,
		`{"id":"1"}`, `{"id":1.5}`, `{"id":0}`, `{"ids":[1,null]}`, `{"ids":[2147483648]}`, `{"created_to":2147483648}`,
		`{"created_from":20,"created_to":10}`, `{"visibility":"bogus"}`, `{"commission_status":4}`,
		`{"id":1} {"id":2}`, `{"id":1,}`,
	} {
		var f CommissionLogFilter
		if err := json.Unmarshal([]byte(raw), &f); err == nil {
			t.Fatalf("invalid selector became %#v: %s", f, raw)
		}
	}
	for _, raw := range []string{`{}`, `{"id":1}`, `{"ids":[1,2]}`, `{"invite_user_id":61624,"created_to":1790812800}`, `{"commission_status":0}`, `{"visibility":"deleted"}`} {
		var f CommissionLogFilter
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			t.Fatalf("valid selector rejected %s: %v", raw, err)
		}
	}
}
func TestCommissionLogJSONFilterOmissionAndRestoreDefaults(t *testing.T) {
	for _, raw := range []string{`{}`, `{"filter":{}}`} {
		var req CommissionLogListRequest
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			t.Fatal(raw, err)
		}
	}
	var req CommissionLogListRequest
	if err := json.Unmarshal([]byte(`{"filter":null}`), &req); err == nil {
		t.Fatal("null filter silently widened query")
	}
	var preview CommissionLogPreviewRequest
	if err := json.Unmarshal([]byte(`{"action":"restore","filter":{"id":1},"reason":"restore"}`), &preview); err != nil || preview.Filter.Visibility != "" {
		t.Fatal("JSON decoder stole restore default", preview, err)
	}
}
