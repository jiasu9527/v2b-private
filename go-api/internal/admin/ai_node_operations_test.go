package admin

import (
	"encoding/json"
	"strings"
	"testing"
)

func aiNodeFixture(t *testing.T, extra string) map[string]any {
	t.Helper()
	var m map[string]any
	err := json.Unmarshal([]byte(`{"id":1,"name":"test","host":"192.0.2.1","port":"443","server_port":443,"group_id":"[1]","rate":"1","tls":1,"network":"tcp","insecure":0,"disable_sni":0,"zero_rtt_handshake":0,"version":2,"cipher":"aes-128-gcm","protocol":"vless","show":1,"created_at":1,"updated_at":1`+extra+`}`), &m)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func aiNodePatch(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		t.Fatal(raw)
	}
	return m
}
func TestAIManagedNodeProtocolUpdates(t *testing.T) {
	for _, kind := range []string{"vmess", "vless", "trojan", "shadowsocks", "tuic", "hysteria", "anytls", "v2node"} {
		t.Run(kind, func(t *testing.T) {
			row := aiNodeFixture(t, ` ,"obfs":"salamander","obfs_password":"keep-password"`)
			values, before, after, err := PrepareAIManagedNodeUpdate(kind, row, aiNodePatch(t, `{"host":"new.example.test","port":54101,"server_port":55101,"rate":2.5}`))
			if err != nil {
				t.Fatal(err)
			}
			if len(values) != 4 || after["host"] != "new.example.test" || before["host"] != "192.0.2.1" {
				t.Fatalf("wrong diff %v %v", values, after)
			}
			if _, ok := values["obfs_password"]; ok {
				t.Fatal("unrequested secret modified")
			}
		})
	}
}
func TestAIManagedNodeSettingsPreserveSecrets(t *testing.T) {
	row := aiNodeFixture(t, `,"tls_settings":"{\"private_key\":\"keep-private\",\"public_key\":\"keep-public\",\"dns_env\":\"SECRET=keep\",\"server_name\":\"old.test\"}","network_settings":"{\"path\":\"/old\",\"headers\":{\"Host\":\"old.test\",\"Authorization\":\"keep-auth\"}}"`)
	values, before, after, err := PrepareAIManagedNodeUpdate("vless", row, aiNodePatch(t, `{"tls_settings":{"server_name":"new.test"},"network_settings":{"path":"/new","headers":{"Host":"new.test"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(values)
	for _, secret := range []string{"keep-private", "SECRET=keep", "keep-auth"} {
		if !strings.Contains(string(raw), secret) {
			t.Fatal("lost stored secret", secret)
		}
	}
	public, _ := json.Marshal([]any{before, after, AIManagedNodePublic("vless", row)})
	for _, secret := range []string{"keep-private", "SECRET=keep", "keep-auth", "keep-public"} {
		if strings.Contains(string(public), secret) {
			t.Fatal("secret leaked", secret)
		}
	}
}
func TestAIManagedNodeRejectsInvalidEdits(t *testing.T) {
	cases := []string{`{"server_port":70000}`, `{"port":"54101"}`, `{"group_id":[1,1]}`, `{"tls_settings":{"private_key":"bad"}}`, `{"network_settings":{"headers":{"Authorization":"bad"}}}`, `{"network_settings":{"extra":{"key":"bad"}}}`, `{"rate":-1}`, `{"unknown":1}`, `{"tls":3}`}
	for _, raw := range cases {
		if ValidateAIManagedNodeChanges("vless", aiNodePatch(t, raw)) == nil {
			t.Fatal("accepted", raw)
		}
	}
	if ValidateAIManagedNodeChanges("tuic", aiNodePatch(t, `{"network":"ws"}`)) == nil {
		t.Fatal("wrong protocol accepted")
	}
}
func TestAIManagedNodeTransportSideEffects(t *testing.T) {
	row := aiNodeFixture(t, `,"flow":"xtls-rprx-vision","tls_settings":"{\"server_name\":\"old.test\"}"`)
	values, _, after, err := PrepareAIManagedNodeUpdate("vless", row, aiNodePatch(t, `{"network":"ws"}`))
	if err != nil {
		t.Fatal(err)
	}
	if value, present := values["flow"]; !present || value != nil {
		t.Fatal("flow not cleared")
	}
	if _, present := after["flow"]; !present {
		t.Fatal("coupled flow hidden")
	}
	if _, _, _, err = PrepareAIManagedNodeUpdate("vless", row, aiNodePatch(t, `{"tls":2}`)); err == nil {
		t.Fatal("silently created REALITY keys")
	}
}
func TestAIManagedNodeVMessSettingsAlias(t *testing.T) {
	row := aiNodeFixture(t, `,"tlsSettings":"{\"serverName\":\"old.test\",\"allowInsecure\":\"0\",\"private_key\":\"keep\"}"`)
	values, _, after, err := PrepareAIManagedNodeUpdate("vmess", row, aiNodePatch(t, `{"tls_settings":{"server_name":"new.test","allow_insecure":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	settings, e := aiNodeObject(values["tlsSettings"])
	if e != nil || settings["serverName"] != "new.test" || settings["allowInsecure"] != float64(1) || settings["private_key"] != "keep" {
		t.Fatal(settings, e)
	}
	if _, ok := settings["server_name"]; ok {
		t.Fatal("shadow alias retained")
	}
	if after["tls_settings"] == nil {
		t.Fatal("alias diff missing")
	}
}

func TestAIManagedNodeNullableFieldsAndColumnLimits(t *testing.T) {
	row := aiNodeFixture(t, `,"sort":7,"listen_ip":"192.0.2.5"`)
	values, _, after, err := PrepareAIManagedNodeUpdate("v2node", row, aiNodePatch(t, `{"sort":null,"listen_ip":null}`))
	if err != nil || values["sort"] != nil || values["listen_ip"] != "0.0.0.0" || after["listen_ip"] != "0.0.0.0" {
		t.Fatal(values, after, err)
	}
	for _, kind := range []string{"tuic", "hysteria", "anytls"} {
		if ValidateAIManagedNodeChanges(kind, map[string]any{"server_name": strings.Repeat("a", 65)}) == nil {
			t.Fatal("oversize SNI accepted", kind)
		}
	}
	groups := []any{}
	for i := 0; i < 50; i++ {
		groups = append(groups, float64(2000000000+i))
	}
	if ValidateAIManagedNodeChanges("vmess", map[string]any{"group_id": groups}) == nil {
		t.Fatal("oversize group list accepted")
	}
	for _, patch := range []string{`{"tls_settings":{"private_key":null}}`, `{"network_settings":{"private":null}}`} {
		if ValidateAIManagedNodeChanges("vless", aiNodePatch(t, patch)) == nil {
			t.Fatal("unknown null accepted", patch)
		}
	}
}
