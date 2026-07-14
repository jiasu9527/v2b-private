package admin

import (
	"database/sql"
	"testing"
)

func TestDNSFailoverProbeStateFreshUsesTwoExpectedCyclesCappedByProbeOffline(t *testing.T) {
	now := int64(1_700_000_000)
	tests := []struct {
		name          string
		lastReported  sql.NullInt64
		checkInterval int64
		tcpTimeoutMS  int64
		probeOffline  int64
		want          bool
	}{
		{name: "fresh at two expected cycles", lastReported: sql.NullInt64{Int64: now - 66, Valid: true}, checkInterval: 30, tcpTimeoutMS: 3000, probeOffline: 90, want: true},
		{name: "stale after two expected cycles", lastReported: sql.NullInt64{Int64: now - 67, Valid: true}, checkInterval: 30, tcpTimeoutMS: 3000, probeOffline: 90},
		{name: "offline timeout caps tolerance", lastReported: sql.NullInt64{Int64: now - 90, Valid: true}, checkInterval: 60, tcpTimeoutMS: 3000, probeOffline: 90, want: true},
		{name: "stale beyond offline timeout", lastReported: sql.NullInt64{Int64: now - 91, Valid: true}, checkInterval: 60, tcpTimeoutMS: 3000, probeOffline: 90},
		{name: "subsecond timeout rounds up", lastReported: sql.NullInt64{Int64: now - 22, Valid: true}, checkInterval: 10, tcpTimeoutMS: 1, probeOffline: 90, want: true},
		{name: "missing timestamp", checkInterval: 30, tcpTimeoutMS: 3000, probeOffline: 90},
		{name: "future timestamp", lastReported: sql.NullInt64{Int64: now + 1, Valid: true}, checkInterval: 30, tcpTimeoutMS: 3000, probeOffline: 90},
		{name: "invalid interval", lastReported: sql.NullInt64{Int64: now, Valid: true}, tcpTimeoutMS: 3000, probeOffline: 90},
		{name: "invalid timeout", lastReported: sql.NullInt64{Int64: now, Valid: true}, checkInterval: 30, probeOffline: 90},
		{name: "invalid offline timeout", lastReported: sql.NullInt64{Int64: now, Valid: true}, checkInterval: 30, tcpTimeoutMS: 3000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := dnsFailoverProbeStateFresh(test.lastReported, now, test.checkInterval, test.tcpTimeoutMS, test.probeOffline)
			if got != test.want {
				t.Fatalf("dnsFailoverProbeStateFresh() = %v, want %v", got, test.want)
			}
		})
	}
}
