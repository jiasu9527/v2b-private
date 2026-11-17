package nodeapi

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

func normalizeObservationFilter(filter ObservationFilter) (ObservationFilter, error) {
	if filter.NodeType != "" && NormalizeNodeType(filter.NodeType) != "v2node" {
		return filter, ErrObservationInvalid
	}
	if filter.NodeID < 0 || filter.UserID < 0 {
		return filter, ErrObservationInvalid
	}
	filter.Account = strings.TrimSpace(filter.Account)
	if filter.Account != "" {
		if len(filter.Account) > 254 || strings.ContainsAny(filter.Account, " \t\r\n") {
			return filter, ErrObservationInvalid
		}
		if id, err := strconv.ParseInt(filter.Account, 10, 64); err == nil {
			if id <= 0 || (filter.UserID > 0 && filter.UserID != id) {
				return filter, ErrObservationInvalid
			}
			filter.UserID = id
			filter.Account = ""
		} else if !strings.Contains(filter.Account, "@") {
			return filter, ErrObservationInvalid
		}
	}
	filter.Target = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(filter.Target), "."))
	if filter.Target != "" {
		if len(filter.Target) > 253 || strings.ContainsAny(filter.Target, "/:@?# \\ \t\r\n") && net.ParseIP(filter.Target) == nil {
			return filter, ErrObservationInvalid
		}
		if ip := net.ParseIP(filter.Target); ip != nil {
			filter.Target = ip.String()
		}
	}
	now := time.Now().Unix()
	if filter.To == 0 {
		filter.To = now + 1
	}
	if filter.From == 0 {
		filter.From = filter.To - observationKeepDays*86400
	}
	// Allow one minute for a user-selected "last seven days" boundary to
	// survive the delay between opening the picker and submitting the query.
	// Data older than retention is already removed by maintenance.
	if filter.From < now-observationKeepDays*86400-60 || filter.To > now+301 || filter.To <= filter.From || filter.To-filter.From > observationKeepDays*86400 {
		return filter, ErrObservationInvalid
	}
	if filter.Page == 0 {
		filter.Page = 1
	}
	if filter.PageSize == 0 {
		filter.PageSize = 50
	}
	if filter.Page < 1 || filter.Page > 10000 || filter.PageSize < 1 || filter.PageSize > 100 {
		return filter, ErrObservationInvalid
	}
	return filter, nil
}

func observationWhere(filter ObservationFilter) (string, []any) {
	clauses := []string{"e.occurred_at >= $1", "e.occurred_at < $2"}
	args := []any{filter.From, filter.To}
	if filter.NodeType != "" {
		args = append(args, "v2node")
		clauses = append(clauses, "e.node_type = $"+strconv.Itoa(len(args)))
	}
	if filter.NodeID > 0 {
		args = append(args, filter.NodeID)
		clauses = append(clauses, "e.node_id = $"+strconv.Itoa(len(args)))
	}
	if filter.UserID > 0 {
		args = append(args, filter.UserID)
		clauses = append(clauses, "e.user_id = $"+strconv.Itoa(len(args)))
	}
	if filter.Account != "" {
		args = append(args, filter.Account)
		clauses = append(clauses, "e.user_id IN (SELECT id FROM v2_user WHERE lower(email)=lower($"+strconv.Itoa(len(args))+"))")
	}
	if filter.Target != "" {
		args = append(args, filter.Target)
		placeholder := "$" + strconv.Itoa(len(args))
		clauses = append(clauses, "(e.target_host = "+placeholder+" OR e.target_ip = "+placeholder+")")
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func (s *DBService) ObservationEvents(ctx context.Context, filter ObservationFilter) (map[string]any, error) {
	var err error
	filter, err = normalizeObservationFilter(filter)
	if err != nil {
		return nil, err
	}
	if err := s.ensureObservationSchema(ctx); err != nil {
		return nil, err
	}
	where, args := observationWhere(filter)
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_node_observation_event e`+where, args...).Scan(&total); err != nil {
		return nil, err
	}
	limitArg := len(args) + 1
	offsetArg := len(args) + 2
	args = append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)
	rows, err := s.db.QueryContext(ctx, `SELECT e.node_type,e.node_id,e.event_id,e.user_id,COALESCE(u.email,''),e.occurred_at,e.client_ip,e.target_host,e.target_ip,e.target_port,e.network,e.result,e.uplink_bytes,e.downlink_bytes
FROM v2_node_observation_event e LEFT JOIN v2_user u ON u.id=e.user_id`+where+
		` ORDER BY e.occurred_at DESC,e.id DESC LIMIT $`+strconv.Itoa(limitArg)+` OFFSET $`+strconv.Itoa(offsetArg), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var nodeType, eventID, email, clientIP, targetHost, targetIP, network, result string
		var nodeID, userID, occurredAt, uplink, downlink int64
		var targetPort int
		if err := rows.Scan(&nodeType, &nodeID, &eventID, &userID, &email, &occurredAt, &clientIP, &targetHost, &targetIP, &targetPort, &network, &result, &uplink, &downlink); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"node_type": nodeType, "node_id": nodeID, "event_id": eventID, "user_id": userID, "email": email, "occurred_at": occurredAt, "client_ip": clientIP, "target_host": targetHost, "target_ip": targetIP, "target_port": targetPort, "network": network, "result": result, "uplink_bytes": uplink, "downlink_bytes": downlink})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"items": items, "total": total, "page": filter.Page, "page_size": filter.PageSize}, nil
}

func (s *DBService) ObservationSummary(ctx context.Context, filter ObservationFilter) (map[string]any, error) {
	var err error
	filter, err = normalizeObservationFilter(filter)
	if err != nil {
		return nil, err
	}
	if err := s.ensureObservationSchema(ctx); err != nil {
		return nil, err
	}
	where, args := observationWhere(filter)
	var events, users, known, unknown int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(DISTINCT e.user_id),COUNT(*) FILTER(WHERE e.target_host<>''),COUNT(*) FILTER(WHERE e.target_host='') FROM v2_node_observation_event e`+where, args...).Scan(&events, &users, &known, &unknown); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.target_host,COUNT(DISTINCT e.user_id),COUNT(*) FROM v2_node_observation_event e`+where+
		` AND e.target_host<>'' GROUP BY e.target_host ORDER BY COUNT(DISTINCT e.user_id) DESC,COUNT(*) DESC,e.target_host LIMIT 30`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	topDomains := make([]map[string]any, 0)
	for rows.Next() {
		var domain string
		var domainUsers, domainEvents int64
		if err := rows.Scan(&domain, &domainUsers, &domainEvents); err != nil {
			return nil, fmt.Errorf("scan observation domain: %w", err)
		}
		topDomains = append(topDomains, map[string]any{"domain": domain, "users": domainUsers, "events": domainEvents})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ipRows, err := s.db.QueryContext(ctx, `SELECT e.target_ip,COUNT(DISTINCT e.user_id),COUNT(*) FROM v2_node_observation_event e`+where+
		` AND e.target_ip<>'' GROUP BY e.target_ip ORDER BY COUNT(DISTINCT e.user_id) DESC,COUNT(*) DESC,e.target_ip LIMIT 30`, args...)
	if err != nil {
		return nil, err
	}
	defer ipRows.Close()
	topIPs := make([]map[string]any, 0)
	for ipRows.Next() {
		var address string
		var ipUsers, ipEvents int64
		if err := ipRows.Scan(&address, &ipUsers, &ipEvents); err != nil {
			return nil, fmt.Errorf("scan observation target IP: %w", err)
		}
		topIPs = append(topIPs, map[string]any{"ip": address, "users": ipUsers, "events": ipEvents})
	}
	if err := ipRows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"events": events, "users": users, "known_domain_events": known, "unknown_domain_events": unknown, "top_domains": topDomains, "top_ips": topIPs}, nil
}
