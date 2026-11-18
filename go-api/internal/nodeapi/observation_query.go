package nodeapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
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
		filter.From = filter.To - 15*60
	}
	// Allow one minute for a user-selected "last seven days" boundary to
	// survive the delay between opening the picker and submitting the query.
	// Data older than retention is already removed by maintenance.
	// Keep a valid picker/cursor usable while the operator reads pages. The
	// SQL clamps the physical read to retention instead of failing after one
	// minute or exposing the extra day retained by daily partitions.
	if filter.From < now-observationKeepDays*86400-24*3600 || filter.To > now+301 || filter.To <= filter.From || filter.To-filter.From > observationKeepDays*86400 {
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
	if len(filter.Cursor) > 2048 || filter.Cursor == "" && (filter.Page-1)*filter.PageSize > 10000 {
		return filter, ErrObservationInvalid
	}
	return filter, nil
}

type observationCursor struct {
	Version int    `json:"v"`
	At      int64  `json:"at"`
	NodeID  int64  `json:"node"`
	EventID string `json:"event"`
	Seen    int64  `json:"seen"`
	Filter  string `json:"filter"`
}

func observationFilterKey(f ObservationFilter) string {
	encoded, _ := json.Marshal([]any{f.From, f.To, f.NodeType, f.NodeID, f.UserID, strings.ToLower(f.Account), f.Target, f.PageSize})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func decodeObservationCursor(f ObservationFilter) (observationCursor, error) {
	var cursor observationCursor
	if f.Cursor == "" {
		return cursor, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(f.Cursor)
	if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Version != 1 || cursor.Filter != observationFilterKey(f) ||
		cursor.At < f.From || cursor.At >= f.To || cursor.NodeID < 1 || !observationEventIDPattern.MatchString(cursor.EventID) || cursor.Seen < 1 || cursor.Seen > 1e12 {
		return cursor, ErrObservationInvalid
	}
	return cursor, nil
}

func observationReadError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", ErrObservationQueryTimeout, err)
	}
	return err
}

func observationItemAfter(a, b map[string]any) bool {
	if a["occurred_at"].(int64) != b["occurred_at"].(int64) {
		return a["occurred_at"].(int64) > b["occurred_at"].(int64)
	}
	if a["node_id"].(int64) != b["node_id"].(int64) {
		return a["node_id"].(int64) > b["node_id"].(int64)
	}
	return a["event_id"].(string) > b["event_id"].(string)
}

func observationWhere(filter ObservationFilter) (string, []any) {
	clauses := []string{"e.occurred_at >= $1", "e.occurred_at < $2"}
	args := []any{max(filter.From, time.Now().Unix()-observationKeepDays*86400), filter.To}
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
		if net.ParseIP(filter.Target) != nil {
			clauses = append(clauses, "e.target_ip = "+placeholder)
		} else {
			clauses = append(clauses, "e.target_host = "+placeholder)
		}
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func (s *DBService) ObservationEvents(ctx context.Context, filter ObservationFilter) (map[string]any, error) {
	var err error
	filter, err = normalizeObservationFilter(filter)
	if err != nil {
		return nil, err
	}
	cursor, err := decodeObservationCursor(filter)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := s.ensureObservationSchema(ctx); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// LOCK is a utility statement: acquire relation locks before the first
	// repeatable-read snapshot. PostgreSQL TRUNCATE is not MVCC-safe, so an
	// empty hot heap must not be reclaimed between snapshot creation and the
	// hot read. The lock recursively includes compact partitions.
	if _, err := tx.ExecContext(ctx, `LOCK TABLE v2_node_observation_event,v2_node_observation_compact IN ACCESS SHARE MODE`); err != nil {
		return nil, observationReadError(ctx, err)
	}
	where, args := observationWhere(filter)
	seen := int64((filter.Page - 1) * filter.PageSize)
	offset := int(seen)
	if filter.Cursor != "" {
		seen, offset = cursor.Seen, 0
		base := len(args)
		// The leading timestamp bound permits time-index scans and partition
		// pruning. The tuple makes ties deterministic across nodes and stores.
		where += fmt.Sprintf(` AND e.occurred_at <= $%d AND (e.occurred_at,e.node_id,e.event_id COLLATE "C") < ($%d,$%d,$%d)`, base+1, base+1, base+2, base+3)
		args = append(args, cursor.At, cursor.NodeID, cursor.EventID)
	}
	limitArg := len(args) + 1
	offsetArg := len(args) + 2
	args = append(args, filter.PageSize+offset+1, 0)
	// Never count the full event table before displaying one page. Limit the
	// events first, so account decoration also touches at most page_size+1 rows.
	rows, err := tx.QueryContext(ctx, `SELECT e.node_type,e.node_id,e.event_id,e.user_id,COALESCE(u.email,''),e.occurred_at,e.client_ip,e.target_host,e.target_ip,e.target_port,e.network,e.result,e.uplink_bytes,e.downlink_bytes
FROM (SELECT e.* FROM v2_node_observation_history e`+where+
		` ORDER BY e.occurred_at DESC,e.node_id DESC,e.event_id COLLATE "C" DESC LIMIT $`+strconv.Itoa(limitArg)+` OFFSET $`+strconv.Itoa(offsetArg)+`) e
LEFT JOIN v2_user u ON u.id=e.user_id ORDER BY e.occurred_at DESC,e.node_id DESC,e.event_id COLLATE "C" DESC`, args...)
	if err != nil {
		return nil, observationReadError(ctx, err)
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
		return nil, observationReadError(ctx, err)
	}
	rows.Close()
	cold, err := s.archivedObservationEvents(ctx, tx, filter, cursor, filter.PageSize+offset+1)
	if err != nil {
		return nil, observationReadError(ctx, err)
	}
	items = append(items, cold...)
	sort.Slice(items, func(i, j int) bool { return observationItemAfter(items[i], items[j]) })
	// A repeatable-read snapshot makes archive insert + hot delete atomic to
	// this reader. Defensive identity dedup also protects older mixed writers.
	unique := items[:0]
	keys := make(map[string]bool, len(items))
	for _, item := range items {
		key := fmt.Sprintf("%v:%v:%v", item["node_type"], item["node_id"], item["event_id"])
		if !keys[key] {
			keys[key] = true
			unique = append(unique, item)
		}
	}
	items = unique
	if offset > len(items) {
		items = nil
	} else {
		items = items[offset:]
	}
	if err = tx.Commit(); err != nil {
		return nil, observationReadError(ctx, err)
	}
	hasMore := len(items) > filter.PageSize
	if hasMore {
		items = items[:filter.PageSize]
	}
	total := seen + int64(len(items))
	nextCursor := ""
	if hasMore {
		last := items[len(items)-1]
		encoded, _ := json.Marshal(observationCursor{Version: 1, At: last["occurred_at"].(int64), NodeID: last["node_id"].(int64), EventID: last["event_id"].(string), Seen: total, Filter: observationFilterKey(filter)})
		nextCursor = base64.RawURLEncoding.EncodeToString(encoded)
		total++ // A lower bound, explicitly not a full-table count.
	}
	return map[string]any{"items": items, "total": total, "total_exact": !hasMore && seen == 0, "has_more": hasMore, "next_cursor": nextCursor, "page": filter.Page, "page_size": filter.PageSize}, nil
}

var observationSummarySlots = make(chan struct{}, 1)

func (s *DBService) ObservationSummary(ctx context.Context, filter ObservationFilter) (result map[string]any, resultErr error) {
	var err error
	filter, err = normalizeObservationFilter(filter)
	if err != nil {
		return nil, err
	}
	select {
	case observationSummarySlots <- struct{}{}:
		defer func() { <-observationSummarySlots }()
	default:
		return nil, ErrObservationQueryBusy
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	defer func() { resultErr = observationReadError(ctx, resultErr) }()
	if err := s.ensureObservationSchema(ctx); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// LOCK is a utility statement: acquire relation locks before the first
	// repeatable-read snapshot. PostgreSQL TRUNCATE is not MVCC-safe, so an
	// empty hot heap must not be reclaimed between snapshot creation and the
	// hot read. The lock recursively includes compact partitions.
	if _, err := tx.ExecContext(ctx, `LOCK TABLE v2_node_observation_event,v2_node_observation_compact IN ACCESS SHARE MODE`); err != nil {
		return nil, observationReadError(ctx, err)
	}
	where, args := observationWhere(filter)
	rows, err := tx.QueryContext(ctx, `SELECT e.user_id,e.target_host,e.target_ip,COUNT(*) FROM v2_node_observation_history e`+where+` GROUP BY e.user_id,e.target_host,e.target_ip`, args...)
	if err != nil {
		return nil, err
	}
	acc := newObservationSummaryAccumulator()
	for rows.Next() {
		var userID, count int64
		var host, ip string
		if err = rows.Scan(&userID, &host, &ip, &count); err != nil {
			rows.Close()
			return nil, err
		}
		if err = acc.add(userID, host, ip, count); err != nil {
			rows.Close()
			return nil, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	err = scanArchivedObservations(ctx, tx, filter, func(e archivedObservationEvent) error { return acc.add(e.UserID, e.TargetHost, e.TargetIP, 1) })
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return acc.result(), nil
}
