package user

import "time"

// TrafficResetMigrationMarker shares the production reset-cycle calculation
// with imports, so importing historical u/d cannot immediately clear usage.
func TrafficResetMigrationMarker(planID, method, expiredAt int64, now time.Time) (string, bool) {
	cycle, ok := trafficResetCurrentCycle(planID, method, expiredAt, now)
	return cycle.Marker, ok
}
