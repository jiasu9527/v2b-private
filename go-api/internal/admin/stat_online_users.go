package admin

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/platform/ipgeo"
)

var ErrOnlineUsersRequest = errors.New("在线用户查询参数无效")

type OnlineUsersRequest struct {
	Scope        string
	CountryCode  string
	ProvinceCode string
	City         string
	Reason       string
	Search       string
	Page         int64
	PageSize     int64
}

type OnlineUser struct {
	ID             int64  `json:"id"`
	Email          string `json:"email"`
	LastSeenAt     int64  `json:"last_seen_at"`
	LocationSeenAt int64  `json:"location_seen_at"`
	CountryCode    string `json:"country_code"`
	CountryName    string `json:"country_name"`
	ProvinceCode   string `json:"province_code"`
	ProvinceName   string `json:"province_name"`
	CityName       string `json:"city_name"`
	UnknownReason  string `json:"unknown_reason"`
}

type OnlineUsers struct {
	Total               int64        `json:"total"`
	Page                int64        `json:"page"`
	PageSize            int64        `json:"page_size"`
	GeneratedAt         int64        `json:"generated_at"`
	OnlineWindowSeconds int64        `json:"online_window_seconds"`
	Users               []OnlineUser `json:"users"`
}

// Normalization mirrors the statistics aggregator and NormalizeChinaCityName.
// The shared live_locations CTE keeps the activity window and last-known
// geography identical to the map. Count and page share one SQL snapshot;
// even a page beyond the last result returns the real count and an empty list.
const onlineUsersQuery = onlineLocationsCTE + `,
checked_locations AS (
	SELECT *, UPPER(BTRIM(country_code)) AS normalized_country_code,
		CASE
			WHEN unknown_reason <> '' THEN CASE WHEN unknown_reason IN
				('not_reported','location_stale','invalid_timestamp','database_unavailable','not_found','invalid_ip','non_public_ip')
				THEN unknown_reason ELSE 'not_found' END
			WHEN UPPER(BTRIM(country_code)) !~ '^[A-Z]{2}$' OR UPPER(BTRIM(country_code)) IN ('ZZ','XX') THEN 'not_found'
			ELSE ''
		END AS normalized_reason
	FROM live_locations
), normalized_locations AS (
	SELECT id, email, last_seen_at, location_seen_at,
		CASE WHEN normalized_reason = '' THEN normalized_country_code ELSE '' END AS country_code,
		CASE WHEN normalized_reason = '' THEN COALESCE(NULLIF(BTRIM(country_name), ''), normalized_country_code) ELSE '' END AS country_name,
		CASE WHEN normalized_reason = '' AND normalized_country_code IN ('CN','HK','MO','TW') AND BTRIM(province_code) ~ '^[0-9]{6}$' THEN BTRIM(province_code) ELSE '' END AS province_code,
		CASE WHEN normalized_reason = '' AND normalized_country_code IN ('CN','HK','MO','TW') AND BTRIM(province_code) ~ '^[0-9]{6}$' THEN COALESCE(NULLIF(BTRIM(province_name), ''), BTRIM(province_code)) ELSE '' END AS province_name,
		CASE WHEN normalized_reason = '' AND normalized_country_code IN ('CN','HK','MO','TW') AND BTRIM(province_code) ~ '^[0-9]{6}$' THEN
			CASE
				WHEN LOWER(BTRIM(city_name)) IN ('','0','未知','未知城市','城市未知','unknown','null','n/a','-') OR CHAR_LENGTH(BTRIM(city_name)) > 128 THEN ''
				WHEN CHAR_LENGTH(BTRIM(city_name)) > 2 AND BTRIM(city_name) LIKE '%地区' THEN LEFT(BTRIM(city_name), -2)
				WHEN CHAR_LENGTH(BTRIM(city_name)) > 1 AND BTRIM(city_name) LIKE '%市' THEN LEFT(BTRIM(city_name), -1)
				ELSE BTRIM(city_name)
			END
		ELSE '' END AS city_name,
		normalized_reason AS unknown_reason
	FROM checked_locations
), filtered AS (
	SELECT * FROM normalized_locations WHERE
		($3 = 'all'
		 OR ($3 = 'country' AND country_code = $4 AND unknown_reason = '')
		 OR ($3 = 'province' AND country_code IN ('CN','HK','MO','TW') AND province_code = $5 AND unknown_reason = '')
		 OR ($3 = 'city' AND country_code IN ('CN','HK','MO','TW') AND province_code = $5 AND city_name = $6 AND unknown_reason = '')
		 OR ($3 = 'unknown' AND unknown_reason <> '' AND ($7 = '' OR unknown_reason = $7)))
		AND ($8::text = '' OR LOWER(email) = LOWER($8) OR id = $9::bigint)
)
SELECT totals.total, COALESCE(page.id, 0) AS id, COALESCE(page.email, '') AS email,
	COALESCE(page.last_seen_at, 0) AS last_seen_at, COALESCE(page.location_seen_at, 0) AS location_seen_at,
	COALESCE(page.country_code, '') AS country_code, COALESCE(page.country_name, '') AS country_name,
	COALESCE(page.province_code, '') AS province_code, COALESCE(page.province_name, '') AS province_name,
	COALESCE(page.city_name, '') AS city_name, COALESCE(page.unknown_reason, '') AS unknown_reason
FROM (SELECT COUNT(*) AS total FROM filtered) totals
LEFT JOIN LATERAL (
	SELECT * FROM filtered ORDER BY last_seen_at DESC, id ASC LIMIT $10 OFFSET $11
) page ON TRUE
ORDER BY page.last_seen_at DESC, page.id ASC`

func NormalizeOnlineUsersRequest(req OnlineUsersRequest) (OnlineUsersRequest, error) {
	req.Scope = strings.TrimSpace(req.Scope)
	if req.Scope == "" {
		req.Scope = "all"
	}
	req.CountryCode = strings.ToUpper(strings.TrimSpace(req.CountryCode))
	req.ProvinceCode = strings.TrimSpace(req.ProvinceCode)
	req.City = strings.TrimSpace(req.City)
	req.Reason = strings.TrimSpace(req.Reason)
	req.Search = strings.TrimSpace(req.Search)
	if req.Page == 0 {
		req.Page = 1
	}
	if req.PageSize == 0 {
		req.PageSize = 20
	}
	if req.Page < 1 || req.PageSize < 1 || req.PageSize > 100 || req.Page-1 > math.MaxInt64/req.PageSize {
		return req, fmt.Errorf("%w：页码必须为正整数，每页最多 100 人", ErrOnlineUsersRequest)
	}
	if len([]rune(req.Search)) > 255 || len([]rune(req.City)) > 128 {
		return req, fmt.Errorf("%w：搜索或城市名称过长", ErrOnlineUsersRequest)
	}
	switch req.Scope {
	case "all", "unknown":
	case "country":
		if !onlineCountryCodeValid(req.CountryCode) {
			return req, fmt.Errorf("%w：国家代码无效", ErrOnlineUsersRequest)
		}
	case "province", "city":
		if req.ProvinceCode != "" && !onlineProvinceCodeValid(req.ProvinceCode) {
			return req, fmt.Errorf("%w：省份代码无效", ErrOnlineUsersRequest)
		}
		req.City = ipgeo.NormalizeChinaCityName(req.City)
		if req.Scope == "city" && req.ProvinceCode == "" && req.City != "" {
			return req, fmt.Errorf("%w：查看城市需指定省份", ErrOnlineUsersRequest)
		}
	default:
		return req, fmt.Errorf("%w：查询范围无效", ErrOnlineUsersRequest)
	}
	if req.Reason != "" {
		if _, exists := onlineUnknownNames[req.Reason]; !exists || req.Scope != "unknown" {
			return req, fmt.Errorf("%w：位置未知原因无效", ErrOnlineUsersRequest)
		}
	}
	return req, nil
}

func (s *DBService) GetOnlineUsers(ctx context.Context, req OnlineUsersRequest) (OnlineUsers, error) {
	if _, err := NormalizeOnlineUsersRequest(req); err != nil {
		return OnlineUsers{}, err
	}
	s.refreshOnlineLocations(ctx)
	return s.getOnlineUsersAt(ctx, req, time.Now().Unix())
}

func (s *DBService) getOnlineUsersAt(ctx context.Context, req OnlineUsersRequest, now int64) (OnlineUsers, error) {
	req, err := NormalizeOnlineUsersRequest(req)
	if err != nil {
		return OnlineUsers{}, err
	}
	result := OnlineUsers{Page: req.Page, PageSize: req.PageSize, GeneratedAt: now, OnlineWindowSeconds: onlineStatsWindowSeconds, Users: []OnlineUser{}}
	if s == nil || s.db == nil {
		return result, ErrUnavailable
	}
	searchID, err := strconv.ParseInt(req.Search, 10, 64)
	if err != nil || searchID < 1 {
		searchID = 0
	}
	rows, err := s.db.QueryContext(ctx, onlineUsersQuery, now-onlineStatsWindowSeconds, now,
		req.Scope, req.CountryCode, req.ProvinceCode, req.City, req.Reason, req.Search, searchID, req.PageSize, (req.Page-1)*req.PageSize)
	if err != nil {
		return result, fmt.Errorf("query online users: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var user OnlineUser
		if err := rows.Scan(&result.Total, &user.ID, &user.Email, &user.LastSeenAt, &user.LocationSeenAt,
			&user.CountryCode, &user.CountryName, &user.ProvinceCode, &user.ProvinceName, &user.CityName, &user.UnknownReason); err != nil {
			return result, fmt.Errorf("scan online users: %w", err)
		}
		if user.ID > 0 {
			result.Users = append(result.Users, user)
		}
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("iterate online users: %w", err)
	}
	return result, nil
}
