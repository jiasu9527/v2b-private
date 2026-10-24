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

func (s *DBService) recordOnlineLocations(ctx context.Context, users map[int64][]string, now int64) error {
	if s.onlineGeo == nil || len(users) == 0 {
		return nil
	}
	userIDs := make([]int64, 0, len(users))
	for id, ips := range users {
		// An empty alive list is a disconnect, not a new location observation.
		if id > 0 && len(ips) > 0 {
			userIDs = append(userIDs, id)
		}
	}
	sort.Slice(userIDs, func(i, j int) bool { return userIDs[i] < userIDs[j] })
	const batchSize = 250
	for offset := 0; offset < len(userIDs); offset += batchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(offset+batchSize, len(userIDs))
		args := make([]any, 0, (end-offset)*7)
		values := make([]string, 0, end-offset)
		for _, id := range userIDs[offset:end] {
			if err := ctx.Err(); err != nil {
				return err
			}
			location := selectOnlineLocation(s.onlineGeo, users[id])
			base := len(args) + 1
			values = append(values, fmt.Sprintf("($%d::integer,$%d::text,$%d::text,$%d::text,$%d::text,$%d::text,$%d::bigint)", base, base+1, base+2, base+3, base+4, base+5, base+6))
			args = append(args, id, location.CountryCode, location.CountryName, location.ProvinceCode, location.ProvinceName, location.UnknownReason, now)
		}
		_, err := s.db.ExecContext(ctx, `WITH incoming(user_id,country_code,country_name,province_code,province_name,unknown_reason,last_seen_at) AS (VALUES `+strings.Join(values, ",")+`)
INSERT INTO v2_user_online_location AS current (user_id,country_code,country_name,province_code,province_name,unknown_reason,last_seen_at)
SELECT incoming.* FROM incoming JOIN v2_user u ON u.id = incoming.user_id
ORDER BY incoming.user_id
ON CONFLICT (user_id) DO UPDATE SET
country_code = EXCLUDED.country_code, country_name = EXCLUDED.country_name,
province_code = EXCLUDED.province_code, province_name = EXCLUDED.province_name,
unknown_reason = EXCLUDED.unknown_reason, last_seen_at = EXCLUDED.last_seen_at
WHERE EXCLUDED.last_seen_at > current.last_seen_at AND (
EXCLUDED.last_seen_at - current.last_seen_at >= 60 OR
(current.country_code,current.country_name,current.province_code,current.province_name,current.unknown_reason)
IS DISTINCT FROM (EXCLUDED.country_code,EXCLUDED.country_name,EXCLUDED.province_code,EXCLUDED.province_name,EXCLUDED.unknown_reason))`, args...)
		if err != nil {
			return fmt.Errorf("save online location summary: %w", err)
		}
	}
	return nil
}

// A user can connect from multiple devices. Choose one resolvable address from
// the latest node report, deterministically, rather than counting them twice.
func selectOnlineLocation(resolver ipgeo.Resolver, rawIPs []string) ipgeo.Location {
	ips := make([]string, 0, len(rawIPs))
	seen := make(map[string]struct{}, len(rawIPs))
	for _, raw := range rawIPs {
		// Node protocols may append a device tag, e.g. 1.2.3.4_android.
		ip := strings.TrimSpace(strings.SplitN(raw, "_", 2)[0])
		if _, exists := seen[ip]; exists {
			continue
		}
		seen[ip] = struct{}{}
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	fallback := ipgeo.Location{UnknownReason: "invalid_ip"}
	for _, ip := range ips {
		location := resolver.Resolve(ip)
		if location.Known {
			return location
		}
		if unknownLocationPriority(location.UnknownReason) > unknownLocationPriority(fallback.UnknownReason) {
			fallback = location
		}
	}
	return fallback
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

func (s *DBService) reportOnlineLocationsBestEffort(ctx context.Context, users map[int64][]string, now int64) {
	// Analytics must never cause a node to retry device reports or disrupt access.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.recordOnlineLocations(ctx, users, now); err != nil {
		previous := s.onlineGeoLastError.Load()
		if now-previous >= 300 && s.onlineGeoLastError.CompareAndSwap(previous, now) {
			log.Printf("online location summary could not be saved; node report accepted")
		}
	}
}
