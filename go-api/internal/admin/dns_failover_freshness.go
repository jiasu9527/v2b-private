package admin

import (
	"database/sql"
	"math"
)

// dnsFailoverProbeStateFresh accepts one missed report before treating a
// target state as stale. A task sleeps for checkIntervalSec only after its TCP
// check completes, so one expected cycle includes the rounded-up TCP timeout.
// ProbeOfflineSec remains the hard upper bound: a result can never outlive the
// rule's definition of an online probe.
func dnsFailoverProbeStateFresh(lastReportedAt sql.NullInt64, now, checkIntervalSec, tcpTimeoutMS, probeOfflineSec int64) bool {
	if !lastReportedAt.Valid || lastReportedAt.Int64 < 0 || lastReportedAt.Int64 > now || checkIntervalSec <= 0 || tcpTimeoutMS <= 0 || probeOfflineSec <= 0 {
		return false
	}

	tcpTimeoutSec := tcpTimeoutMS / 1000
	if tcpTimeoutMS%1000 != 0 {
		tcpTimeoutSec++
	}
	expectedCycleSec := int64(math.MaxInt64)
	if checkIntervalSec <= math.MaxInt64-tcpTimeoutSec {
		expectedCycleSec = checkIntervalSec + tcpTimeoutSec
	}
	freshnessSec := int64(math.MaxInt64)
	if expectedCycleSec <= math.MaxInt64/2 {
		freshnessSec = expectedCycleSec * 2
	}
	if probeOfflineSec < freshnessSec {
		freshnessSec = probeOfflineSec
	}

	cutoff := int64(math.MinInt64)
	if now >= math.MinInt64+freshnessSec {
		cutoff = now - freshnessSec
	}
	return lastReportedAt.Int64 >= cutoff
}
