package admin

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"forest/go-api/internal/platform/ipgeo"
)

const onlineStatsWindowSeconds int64 = 600

// OnlineRegion counts people, not devices, sessions, addresses or node reports.
// Share is the percentage of all currently online users, including unknown ones.
type OnlineRegion struct {
	Code  string  `json:"code"`
	Name  string  `json:"name"`
	Users int64   `json:"users"`
	Share float64 `json:"share"`
}

type OnlineUnknownReason struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Users int64  `json:"users"`
}

// Code is a normalized city name within ProvinceCode, not an administrative
// district code. No map polygon is required to retain a known city's users.
type OnlineCity struct {
	ProvinceCode string  `json:"province_code"`
	Code         string  `json:"code"`
	Name         string  `json:"name"`
	Users        int64   `json:"users"`
	Share        float64 `json:"share"`
}

type OnlineStats struct {
	GeneratedAt         int64                 `json:"generated_at"`
	OnlineWindowSeconds int64                 `json:"online_window_seconds"`
	CurrentOnline       int64                 `json:"current_online"`
	Unique24h           int64                 `json:"unique_24h"`
	Unique7d            int64                 `json:"unique_7d"`
	TotalUsers          int64                 `json:"total_users"`
	OnlineShare         float64               `json:"online_share"`
	GeoLocatedUsers     int64                 `json:"geo_located_users"`
	GeoUnknownUsers     int64                 `json:"geo_unknown_users"`
	GeoCoverage         float64               `json:"geo_coverage"`
	Countries           []OnlineRegion        `json:"countries"`
	ChinaProvinces      []OnlineRegion        `json:"china_provinces"`
	ChinaCities         []OnlineCity          `json:"china_cities"`
	UnknownReasons      []OnlineUnknownReason `json:"unknown_reasons"`
	GeoDatabase         ipgeo.GeoStatus       `json:"geo_database"`
}

// The summary and location groups use one SQL statement and therefore one
// PostgreSQL snapshot. A user's latest activity timestamp is already unique per
// account; historical windows need no connection-event log. The location table
// likewise has a primary key on user_id, preventing multi-device double counts.
const onlineLocationsCTE = `WITH live_locations AS (
	SELECT
		u.id, u.email, u.t AS last_seen_at, COALESCE(l.last_seen_at, 0) AS location_seen_at,
		CASE WHEN l.last_seen_at BETWEEN $1 AND $2 THEN l.country_code ELSE '' END AS country_code,
		CASE WHEN l.last_seen_at BETWEEN $1 AND $2 THEN l.country_name ELSE '' END AS country_name,
		CASE WHEN l.last_seen_at BETWEEN $1 AND $2 THEN l.province_code ELSE '' END AS province_code,
		CASE WHEN l.last_seen_at BETWEEN $1 AND $2 THEN l.province_name ELSE '' END AS province_name,
		CASE WHEN l.last_seen_at BETWEEN $1 AND $2 THEN l.city_name ELSE '' END AS city_name,
		CASE
			WHEN l.user_id IS NULL THEN 'not_reported'
			WHEN l.last_seen_at > $2 THEN 'invalid_timestamp'
			WHEN l.last_seen_at < $1 THEN 'location_stale'
			WHEN l.country_code = '' THEN COALESCE(NULLIF(l.unknown_reason, ''), 'not_found')
			ELSE ''
		END AS unknown_reason
	FROM v2_user u
	LEFT JOIN v2_user_online_location l ON l.user_id = u.id
	WHERE u.t >= $1
)`

const onlineStatsQuery = onlineLocationsCTE + `
SELECT 'summary' AS kind, '' AS country_code, '' AS country_name,
	'' AS province_code, '' AS province_name, '' AS city_name, '' AS unknown_reason,
	COUNT(*) AS users,
	COUNT(*) FILTER (WHERE t >= $1) AS current_online,
	COUNT(*) FILTER (WHERE t >= $3) AS unique_24h,
	COUNT(*) FILTER (WHERE t >= $4) AS unique_7d
FROM v2_user
UNION ALL
SELECT 'region', country_code, country_name, province_code, province_name, city_name, unknown_reason,
	COUNT(*), 0, 0, 0
FROM live_locations
GROUP BY country_code, country_name, province_code, province_name, city_name, unknown_reason`

func (s *DBService) WithOnlineGeoResolver(resolver ipgeo.Resolver) *DBService {
	s.onlineGeo = resolver
	return s
}

func (s *DBService) GetOnlineStats(ctx context.Context) (OnlineStats, error) {
	s.refreshOnlineLocations(ctx)
	return s.getOnlineStatsAt(ctx, time.Now().Unix())
}

// The callback reconciles bounded recent node cache entries and applies its own
// timeout and throttle. A cache failure must not hide database-backed statistics.
func (s *DBService) WithOnlineLocationRefresh(refresh func(context.Context) error) *DBService {
	s.onlineLocationRefresh = refresh
	return s
}

func (s *DBService) refreshOnlineLocations(ctx context.Context) {
	if s != nil && s.onlineLocationRefresh != nil {
		_ = s.onlineLocationRefresh(ctx)
	}
}

func (s *DBService) getOnlineStatsAt(ctx context.Context, now int64) (OnlineStats, error) {
	result := OnlineStats{
		GeneratedAt:         now,
		OnlineWindowSeconds: onlineStatsWindowSeconds,
		Countries:           make([]OnlineRegion, 0),
		ChinaProvinces:      make([]OnlineRegion, 0),
		ChinaCities:         make([]OnlineCity, 0),
		UnknownReasons:      make([]OnlineUnknownReason, 0),
		GeoDatabase:         ipgeo.GeoStatus{Status: "unavailable"},
	}
	if s == nil || s.db == nil {
		return result, ErrUnavailable
	}
	if s.onlineGeo != nil {
		result.GeoDatabase = s.onlineGeo.Status()
	}
	rows, err := s.db.QueryContext(ctx, onlineStatsQuery, now-onlineStatsWindowSeconds, now, now-86400, now-7*86400)
	if err != nil {
		return result, fmt.Errorf("query online statistics: %w", err)
	}
	defer rows.Close()

	countries := make(map[string]*OnlineRegion)
	provinces := make(map[string]*OnlineRegion)
	cities := make(map[string]*OnlineCity)
	unknown := make(map[string]int64)
	hasSummary := false
	for rows.Next() {
		var kind, countryCode, countryName, provinceCode, provinceName, cityName, reason string
		var users, current, daily, weekly int64
		if err := rows.Scan(&kind, &countryCode, &countryName, &provinceCode, &provinceName, &cityName, &reason, &users, &current, &daily, &weekly); err != nil {
			return result, fmt.Errorf("scan online statistics: %w", err)
		}
		if kind == "summary" {
			result.TotalUsers, result.CurrentOnline, result.Unique24h, result.Unique7d = users, current, daily, weekly
			hasSummary = true
			continue
		}
		countryCode = strings.ToUpper(strings.TrimSpace(countryCode))
		if reason != "" || !onlineCountryCodeValid(countryCode) {
			if reason == "" {
				reason = "not_found"
			}
			if _, ok := onlineUnknownNames[reason]; !ok {
				reason = "not_found"
			}
			unknown[reason] += users
			result.GeoUnknownUsers += users
			continue
		}
		result.GeoLocatedUsers += users
		addOnlineRegion(countries, countryCode, countryName, users)
		if countryCode == "CN" || countryCode == "HK" || countryCode == "MO" || countryCode == "TW" {
			provinceCode, provinceName = strings.TrimSpace(provinceCode), strings.TrimSpace(provinceName)
			if !onlineProvinceCodeValid(provinceCode) {
				provinceCode, provinceName = "", "省份未知"
				cityName = ""
			}
			addOnlineRegion(provinces, provinceCode, provinceName, users)
			addOnlineCity(cities, provinceCode, cityName, users)
		}
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("iterate online statistics: %w", err)
	}
	if !hasSummary {
		return result, fmt.Errorf("query online statistics: summary missing")
	}
	result.OnlineShare = onlinePercentage(result.CurrentOnline, result.TotalUsers)
	result.GeoCoverage = onlinePercentage(result.GeoLocatedUsers, result.CurrentOnline)
	result.Countries = sortedOnlineRegions(countries, result.CurrentOnline)
	result.ChinaProvinces = sortedOnlineRegions(provinces, result.CurrentOnline)
	result.ChinaCities = sortedOnlineCities(cities, result.CurrentOnline)
	for code, users := range unknown {
		result.UnknownReasons = append(result.UnknownReasons, OnlineUnknownReason{Code: code, Name: onlineUnknownNames[code], Users: users})
	}
	sort.Slice(result.UnknownReasons, func(i, j int) bool {
		if result.UnknownReasons[i].Users != result.UnknownReasons[j].Users {
			return result.UnknownReasons[i].Users > result.UnknownReasons[j].Users
		}
		return result.UnknownReasons[i].Code < result.UnknownReasons[j].Code
	})
	return result, nil
}

func addOnlineCity(cities map[string]*OnlineCity, province, rawName string, users int64) {
	code := ipgeo.NormalizeChinaCityName(rawName)
	key := province + ":" + code
	if city, ok := cities[key]; ok {
		city.Users += users
		return
	}
	name := code
	if name == "" {
		name = "城市未知"
	}
	cities[key] = &OnlineCity{ProvinceCode: province, Code: code, Name: name, Users: users}
}

func sortedOnlineCities(cities map[string]*OnlineCity, total int64) []OnlineCity {
	result := make([]OnlineCity, 0, len(cities))
	for _, city := range cities {
		city.Share = onlinePercentage(city.Users, total)
		result = append(result, *city)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Users != result[j].Users {
			return result[i].Users > result[j].Users
		}
		if result[i].ProvinceCode != result[j].ProvinceCode {
			return result[i].ProvinceCode < result[j].ProvinceCode
		}
		return result[i].Code < result[j].Code
	})
	return result
}

var onlineUnknownNames = map[string]string{
	"not_reported":         "节点尚未上报用户地址",
	"location_stale":       "位置上报已超过在线时间窗口",
	"invalid_timestamp":    "位置上报时间异常",
	"database_unavailable": "离线定位库未就绪",
	"not_found":            "未匹配到地区",
	"invalid_ip":           "地址格式无法识别",
	"non_public_ip":        "内网或保留地址",
}

func onlineCountryCodeValid(code string) bool {
	return len(code) == 2 && code[0] >= 'A' && code[0] <= 'Z' && code[1] >= 'A' && code[1] <= 'Z' && code != "ZZ" && code != "XX"
}

func onlineProvinceCodeValid(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, char := range code {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func addOnlineRegion(regions map[string]*OnlineRegion, code, name string, users int64) {
	if region, ok := regions[code]; ok {
		region.Users += users
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = code
	}
	regions[code] = &OnlineRegion{Code: code, Name: name, Users: users}
}

func sortedOnlineRegions(regions map[string]*OnlineRegion, total int64) []OnlineRegion {
	result := make([]OnlineRegion, 0, len(regions))
	for _, region := range regions {
		region.Share = onlinePercentage(region.Users, total)
		result = append(result, *region)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Users != result[j].Users {
			return result[i].Users > result[j].Users
		}
		return result[i].Code < result[j].Code
	})
	return result
}

func onlinePercentage(users, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return math.Round(float64(users)/float64(total)*10000) / 100
}
