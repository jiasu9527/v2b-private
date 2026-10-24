package nodeapi

import (
	"context"
	"fmt"
	"time"
)

const onlineLocationRefreshBatch = 500

// Only still-online users with live runtime cache entries can supply missing
// observations or refresh an old summary. Old summaries remain valid until a
// new valid location replaces them. A keyset cursor bounds each attempt without
// starving higher IDs when a lower ID has an unresolvable IP or no city data.
const onlineLocationRefreshQuery = `SELECT u.id, kv.v
FROM v2_user u
JOIN v2_runtime_kv kv ON kv.k = 'ALIVE_IP_USER_' || u.id::text
LEFT JOIN v2_user_online_location l ON l.user_id = u.id
WHERE u.t >= $1 AND u.id > $3 AND (kv.expire_at = 0 OR kv.expire_at > $2)
AND (l.user_id IS NULL OR l.country_code = '' OR l.last_seen_at < $1 OR l.last_seen_at > $2
OR (l.country_code IN ('CN','HK','MO','TW') AND (l.province_code = '' OR l.city_name = '')))
ORDER BY u.id LIMIT $4`

// ReconcileOnlineLocations recovers cached IP reports after an upgrade or a
// transient summary-write failure. It keeps original observation timestamps,
// never stores the IP in the summary, and never contacts external IP services.
// Callers may ignore errors because geography must not block panel access.
func (s *DBService) ReconcileOnlineLocations(ctx context.Context) error {
	return s.reconcileOnlineLocationsAt(ctx, time.Now().Unix())
}

func (s *DBService) reconcileOnlineLocationsAt(ctx context.Context, now int64) error {
	if s == nil || s.db == nil || s.onlineGeo == nil || !s.onlineGeoRefreshMu.TryLock() {
		return nil
	}
	defer s.onlineGeoRefreshMu.Unlock()
	if s.onlineGeoRefreshAt > 0 && now >= s.onlineGeoRefreshAt && now-s.onlineGeoRefreshAt < 30 {
		return nil
	}
	s.onlineGeoRefreshAt = now
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, onlineLocationRefreshQuery, now-600, now, s.onlineGeoRefreshCursor, onlineLocationRefreshBatch)
	if err != nil {
		return fmt.Errorf("query cached online locations: %w", err)
	}
	observations := make(map[int64]onlineLocationObservation)
	var scanned int
	var lastID int64
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return fmt.Errorf("scan cached online location: %w", err)
		}
		lastID, scanned = id, scanned+1
		state, err := decodeJSONObject([]byte(raw))
		if err != nil {
			continue // A malformed cache value cannot block the other users.
		}
		if ctx.Err() != nil {
			rows.Close()
			return ctx.Err()
		}
		if observation, ok := onlineLocationFromAliveState(s.onlineGeo, state, now); ok {
			observations[id] = observation
		}
	}
	err = rows.Err()
	rows.Close() // Release the connection before the batched upsert.
	if err != nil {
		return fmt.Errorf("iterate cached online locations: %w", err)
	}
	if err := s.saveOnlineLocationObservations(ctx, observations); err != nil {
		return err
	}
	if scanned == onlineLocationRefreshBatch {
		s.onlineGeoRefreshCursor = lastID
	} else {
		s.onlineGeoRefreshCursor = 0
	}
	return nil
}
