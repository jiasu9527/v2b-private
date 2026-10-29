package admin

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func managedServerValidationPayload() map[string]any {
	return map[string]any{
		"group_id":           []any{int64(1)},
		"name":               "Node-A",
		"host":               "node.example.com",
		"port":               "443",
		"server_port":        int64(8443),
		"rate":               "1",
		"protocol":           "vless",
		"tls":                int64(1),
		"network":            "tcp",
		"disable_sni":        int64(0),
		"zero_rtt_handshake": int64(0),
	}
}

func TestValidateManagedServerSavePayloadReportsSafeValidationErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		serverType string
		modify     func(map[string]any)
		message    string
	}{
		{name: "valid", serverType: "v2node"},
		{name: "trimmed_type", serverType: " v2node "},
		{name: "protocol", serverType: "v2node", modify: func(p map[string]any) { delete(p, "protocol") }, message: "协议不能为空"},
		{name: "disable_sni", serverType: "v2node", modify: func(p map[string]any) { delete(p, "disable_sni") }, message: "disable_sni不能为空"},
		{name: "zero_rtt_handshake", serverType: "v2node", modify: func(p map[string]any) { delete(p, "zero_rtt_handshake") }, message: "zero_rtt_handshake不能为空"},
		{name: "id", serverType: "v2node", modify: func(p map[string]any) { p["id"] = "private-input" }, message: "保存失败"},
		{name: "tls_settings", serverType: "v2node", modify: func(p map[string]any) { p["tls_settings"] = "private-input" }, message: "tls配置有误"},
		{name: "host", serverType: "v2node", modify: func(p map[string]any) { p["host"] = "https://private-input/path" }, message: "节点地址必须是单个域名或 IP；入口规则请在用户入口分配中维护"},
		{name: "unknown_type", serverType: "private-input", message: "保存失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := managedServerValidationPayload()
			if tc.modify != nil {
				tc.modify(payload)
			}
			err := ValidateManagedServerSavePayload(tc.serverType, payload)
			if tc.message == "" {
				if err != nil {
					t.Fatalf("valid payload: %v", err)
				}
				return
			}
			var validationErr *ManagedServerValidationError
			if !errors.As(err, &validationErr) || validationErr.Error() != tc.message {
				t.Fatalf("error = %v, want typed validation error %q", err, tc.message)
			}
			if strings.Contains(err.Error(), "private-input") {
				t.Fatal("validation error exposed input")
			}
		})
	}
}

func TestValidateManagedServerSavePayloadDoesNotMutateNestedSettings(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "success", false: "late_failure"}[valid], func(t *testing.T) {
			payload := managedServerValidationPayload()
			payload["network"] = "xhttp"
			payload["network_settings"] = map[string]any{
				"acceptProxyProtocol": "1",
				"extra": map[string]any{
					"noGRPCHeader":        "1",
					"noSSEHeader":         "0",
					"scMaxBufferedPosts":  "32",
					"xmux":                map[string]any{"hKeepAlivePeriod": "30"},
					"downloadSettings":    map[string]any{"port": "443"},
					"untouchedInteger":    int64(9007199254740993),
					"untouchedJSONNumber": json.Number("9007199254740993"),
				},
			}
			if !valid {
				delete(payload, "zero_rtt_handshake")
			}
			beforeJSON, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			err = ValidateManagedServerSavePayload("v2node", payload)
			if (err == nil) != valid {
				t.Fatalf("validation error = %v, valid = %v", err, valid)
			}
			afterJSON, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			if string(beforeJSON) != string(afterJSON) {
				t.Fatal("validation changed nested request values")
			}
			extra := payload["network_settings"].(map[string]any)["extra"].(map[string]any)
			if extra["untouchedInteger"] != int64(9007199254740993) || extra["untouchedJSONNumber"] != json.Number("9007199254740993") {
				t.Fatal("validation changed integer value or type")
			}
		})
	}
}

func TestValidateManagedServerSavePayloadDoesNotFillGeneratedKeys(t *testing.T) {
	for _, tls := range []int64{1, 2} {
		payload := managedServerValidationPayload()
		payload["tls"] = tls
		tlsSettings := map[string]any{"ech": "custom", "ech_server_name": "cover.example.com"}
		encryptionSettings := map[string]any{}
		payload["tls_settings"] = tlsSettings
		payload["encryption"] = "mlkem768x25519plus"
		payload["encryption_settings"] = encryptionSettings
		if err := ValidateManagedServerSavePayload("v2node", payload); err != nil {
			t.Fatalf("tls %d: %v", tls, err)
		}
		if !reflect.DeepEqual(tlsSettings, map[string]any{"ech": "custom", "ech_server_name": "cover.example.com"}) {
			t.Fatalf("tls %d: validation added generated TLS keys to the request", tls)
		}
		if len(encryptionSettings) != 0 {
			t.Fatalf("tls %d: validation added generated encryption keys to the request", tls)
		}
	}
}

func TestSaveManagedServerRejectsPayloadBeforeSchemaAccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := &DBService{db: db}
	payload := managedServerValidationPayload()
	delete(payload, "protocol")
	saved, err := service.SaveManagedServer(context.Background(), "v2node", payload)
	var validationErr *ManagedServerValidationError
	if saved || !errors.As(err, &validationErr) || validationErr.Error() != "协议不能为空" {
		t.Fatalf("save = %v, error = %v, want parameter rejection", saved, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSaveManagedServerDatabaseErrorIsNotValidationError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := &DBService{db: db}
	expectEnsureClientEntrySchema(mock)
	mock.ExpectExec(`INSERT INTO "v2_server_v2node"`).WillReturnError(errors.New("storage unavailable"))
	saved, err := service.SaveManagedServer(context.Background(), "v2node", managedServerValidationPayload())
	var validationErr *ManagedServerValidationError
	if saved || err == nil || errors.As(err, &validationErr) {
		t.Fatalf("save = %v, error = %v, want non-validation database error", saved, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
