package admin

import "testing"

func TestEntryEvidenceProbeAddressWhitelist(t *testing.T) {
	h, p, ip, ok := evidenceProbeAddress(`{"host":"ENTRY.EXAMPLE.TEST","port":54101,"resolved_ip":"2001:0db8::1","error":"password=secret","arbitrary":{"key":"secret"}}`)
	if !ok || h != "entry.example.test" || p != 54101 || ip != "2001:db8::1" {
		t.Fatal(h, p, ip, ok)
	}
	_, _, ip, ok = evidenceProbeAddress(`{"host":"entry.test","port":54101,"resolved_ip":"token=secret"}`)
	if !ok || ip != "" {
		t.Fatal("free text accepted as resolved IP", ip, ok)
	}
	for _, bad := range []string{`{"host":"https://user:pass@entry.test","port":54101}`, `{"host":"entry.test","port":"54101"}`, `{"host":"entry.test","port":0}`, `{"host":"entry.test","port":65536}`, `null`, `garbage`} {
		if _, _, _, ok = evidenceProbeAddress(bad); ok {
			t.Fatal("invalid event address accepted", bad)
		}
	}
}
