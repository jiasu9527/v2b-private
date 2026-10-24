package nodeapi

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"forest/go-api/internal/platform/ipgeo"
)

// WithOnlineGeoResolver enables location summaries without changing node/device
// accounting. The normal server always supplies a resolver, even if its local
// databases are unavailable, so the administrator can see explicit unknowns.
func (s *DBService) WithOnlineGeoResolver(resolver ipgeo.Resolver) *DBService {
	s.onlineGeo = resolver
	return s
}

type onlineLocationObservation struct {
	Location ipgeo.Location
	SeenAt   int64
}

func (s *DBService) saveOnlineLocationObservations(ctx context.Context, observations map[int64]onlineLocationObservation) error {
	userIDs := make([]int64, 0, len(observations))
	for id := range observations {
		userIDs = append(userIDs, id)
	}
	sort.Slice(userIDs, func(i, j int) bool { return userIDs[i] < userIDs[j] })
	const batchSize = 250
	for offset := 0; offset < len(userIDs); offset += batchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(offset+batchSize, len(userIDs))
		args := make([]any, 0, (end-offset)*8)
		values := make([]string, 0, end-offset)
		for _, id := range userIDs[offset:end] {
			if err := ctx.Err(); err != nil {
				return err
			}
			observation := observations[id]
			location := observation.Location
			base := len(args) + 1
			values = append(values, fmt.Sprintf("($%d::integer,$%d::text,$%d::text,$%d::text,$%d::text,$%d::text,$%d::text,$%d::bigint)", base, base+1, base+2, base+3, base+4, base+5, base+6, base+7))
			args = append(args, id, location.CountryCode, location.CountryName, location.ProvinceCode, location.ProvinceName, location.CityName, location.UnknownReason, observation.SeenAt)
		}
		_, err := s.db.ExecContext(ctx, `WITH incoming(user_id,country_code,country_name,province_code,province_name,city_name,unknown_reason,last_seen_at) AS (VALUES `+strings.Join(values, ",")+`)
INSERT INTO v2_user_online_location AS current (user_id,country_code,country_name,province_code,province_name,city_name,unknown_reason,last_seen_at)
SELECT incoming.* FROM incoming JOIN v2_user u ON u.id = incoming.user_id
ORDER BY incoming.user_id
ON CONFLICT (user_id) DO UPDATE SET
country_code = EXCLUDED.country_code, country_name = EXCLUDED.country_name,
province_code = EXCLUDED.province_code, province_name = EXCLUDED.province_name,
city_name = EXCLUDED.city_name,
unknown_reason = EXCLUDED.unknown_reason, last_seen_at = EXCLUDED.last_seen_at
WHERE (
EXCLUDED.last_seen_at > current.last_seen_at OR
(current.country_code = '' AND EXCLUDED.country_code <> ''
AND EXCLUDED.last_seen_at >= current.last_seen_at - 100) OR
(EXCLUDED.last_seen_at = current.last_seen_at AND current.country_code = EXCLUDED.country_code AND (
(current.province_code = '' AND EXCLUDED.province_code <> '') OR
(current.province_code = EXCLUDED.province_code AND current.city_name = '' AND EXCLUDED.city_name <> ''))))
AND NOT (EXCLUDED.country_code = '' AND current.country_code <> ''
AND EXCLUDED.last_seen_at - current.last_seen_at <= 600)
AND (
EXCLUDED.last_seen_at - current.last_seen_at >= 60 OR
(current.country_code,current.country_name,current.province_code,current.province_name,current.city_name,current.unknown_reason)
IS DISTINCT FROM (EXCLUDED.country_code,EXCLUDED.country_name,EXCLUDED.province_code,EXCLUDED.province_name,EXCLUDED.city_name,EXCLUDED.unknown_reason))`, args...)
		if err != nil {
			return fmt.Errorf("save online location summary: %w", err)
		}
	}
	return nil
}

// A user can connect from multiple devices. Choose the most precise location
// within the newest resolvable observation time, without counting devices twice.
func selectOnlineLocationObservation(resolver ipgeo.Resolver, observed map[string]int64) onlineLocationObservation {
	ips := make([]string, 0, len(observed))
	seen := make(map[string]int64, len(observed))
	for raw, at := range observed {
		// Node protocols may append a device tag, e.g. 1.2.3.4_android.
		ip := strings.TrimSpace(strings.SplitN(raw, "_", 2)[0])
		if address, err := ipgeo.ParseAddress(ip); err == nil {
			ip = address.String()
		}
		if previous, exists := seen[ip]; exists {
			seen[ip] = max(at, previous)
			continue
		}
		seen[ip] = at
		ips = append(ips, ip)
	}
	sort.Slice(ips, func(i, j int) bool {
		if seen[ips[i]] != seen[ips[j]] {
			return seen[ips[i]] > seen[ips[j]]
		}
		return ips[i] < ips[j]
	})
	fallback := onlineLocationObservation{Location: ipgeo.Location{UnknownReason: "invalid_ip"}}
	best := onlineLocationObservation{}
	for _, ip := range ips {
		if best.Location.Known && seen[ip] < best.SeenAt {
			break
		}
		location := resolver.Resolve(ip)
		if location.Known {
			if locationPrecision(location) > locationPrecision(best.Location) {
				best = onlineLocationObservation{Location: location, SeenAt: seen[ip]}
			}
			if best.Location.CityName != "" {
				break
			}
			continue
		}
		if fallback.SeenAt == 0 || unknownLocationPriority(location.UnknownReason) > unknownLocationPriority(fallback.Location.UnknownReason) {
			fallback = onlineLocationObservation{Location: location, SeenAt: seen[ip]}
		}
	}
	if best.Location.Known {
		return best
	}
	return fallback
}

func locationPrecision(location ipgeo.Location) int {
	if !location.Known {
		return 0
	}
	if location.CityName != "" {
		return 3
	}
	if location.ProvinceCode != "" {
		return 2
	}
	return 1
}

// An alive report can update only one of a user's connected nodes. Prefer the
// freshest resolvable address across their still-live entries, retaining its
// original observation time so a broken report cannot renew an old location.
func onlineLocationFromAliveState(resolver ipgeo.Resolver, state map[string]any, now int64) (onlineLocationObservation, bool) {
	observed := make(map[string]int64)
	for key, value := range state {
		if key == "alive_ip" {
			continue
		}
		entry, ok := value.(map[string]any)
		if !ok {
			continue
		}
		at := mapAnyInt64(entry["lastupdateAt"])
		if at <= 0 || at > now || at < now-100 {
			continue
		}
		for _, raw := range stringSliceFromAny(entry["aliveips"]) {
			observed[raw] = max(observed[raw], at)
		}
	}
	if len(observed) == 0 {
		return onlineLocationObservation{}, false
	}
	return selectOnlineLocationObservation(resolver, observed), true
}

func unknownLocationPriority(reason string) int {
	switch reason {
	case "database_unavailable":
		return 4
	case "not_found":
		return 3
	case "non_public_ip":
		return 2
	default:
		return 1
	}
}

func (s *DBService) reportOnlineAliveStatesBestEffort(ctx context.Context, states map[int64]map[string]any, now int64) {
	if s.onlineGeo == nil || len(states) == 0 {
		return
	}
	// Analytics must never cause a node to retry device reports or disrupt access.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	observations := make(map[int64]onlineLocationObservation, len(states))
	for id, state := range states {
		if ctx.Err() != nil {
			s.logOnlineLocationError(now)
			return
		}
		if observation, ok := onlineLocationFromAliveState(s.onlineGeo, state, now); ok {
			observations[id] = observation
		}
	}
	if err := s.saveOnlineLocationObservations(ctx, observations); err != nil {
		s.logOnlineLocationError(now)
	}
}

func (s *DBService) logOnlineLocationError(now int64) {
	previous := s.onlineGeoLastError.Load()
	if now-previous >= 300 && s.onlineGeoLastError.CompareAndSwap(previous, now) {
		log.Printf("online location summary could not be saved; node report accepted")
	}
}
