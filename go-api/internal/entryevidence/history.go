package entryevidence

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

type HistoryQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// HistoricalUserIDs uses final delivered addresses, never current routing rules.
// Deleted users remain in the evidence ledger but cannot receive new assignments.
func HistoricalUserIDs(ctx context.Context, db HistoryQueryer, host string, from, to int64) ([]int64, error) {
	return HistoricalSelectionUserIDs(ctx, db, HistorySelection{Host: host}, from, to)
}

type HistorySelection struct {
	Host         string   `json:"host,omitempty"`
	GroupIDs     []int64  `json:"group_ids,omitempty"`
	ExcludeHosts []string `json:"exclude_hosts,omitempty"`
}

// NormalizeHistorySelection binds selection semantics without consulting current
// groups: retired leaf IDs still identify their immutable delivery snapshots.
func NormalizeHistorySelection(input HistorySelection) (HistorySelection, error) {
	out := HistorySelection{GroupIDs: []int64{}, ExcludeHosts: []string{}}
	if len(input.GroupIDs) > 100 || len(input.ExcludeHosts) > 20 {
		return out, ErrValidation
	}
	var err error
	if input.Host != "" {
		out.Host, err = HistoryHost(input.Host)
		if err != nil {
			return out, err
		}
	}
	groups := map[int64]bool{}
	for _, id := range input.GroupIDs {
		if id <= 0 {
			return out, ErrValidation
		}
		if !groups[id] {
			groups[id] = true
			out.GroupIDs = append(out.GroupIDs, id)
		}
	}
	if out.Host == "" && len(out.GroupIDs) == 0 {
		return out, ErrValidation
	}
	hosts := map[string]bool{}
	for _, value := range input.ExcludeHosts {
		host, err := HistoryHost(value)
		if err != nil {
			return out, err
		}
		if !hosts[host] {
			hosts[host] = true
			out.ExcludeHosts = append(out.ExcludeHosts, host)
		}
	}
	sort.Slice(out.GroupIDs, func(i, j int) bool { return out.GroupIDs[i] < out.GroupIDs[j] })
	sort.Strings(out.ExcludeHosts)
	return out, nil
}

// A candidate must have actually received a selected node in [from,to).
// Exclusions remove the entire user if any successful response in that same
// window delivered an excluded host, regardless of its group, node, UA or port.
func HistoricalSelectionUserIDs(ctx context.Context, db HistoryQueryer, input HistorySelection, from, to int64) ([]int64, error) {
	selection, err := NormalizeHistorySelection(input)
	if err != nil || from <= 0 || to <= from {
		return nil, ErrValidation
	}
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT r.user_id FROM v2_entry_delivery_request r JOIN v2_user u ON u.id=r.user_id
 WHERE r.completed_at >= $2 AND r.completed_at < $3 AND r.response_complete AND r.coverage='complete' AND r.write_error='' AND r.status BETWEEN 200 AND 299
 AND EXISTS(SELECT 1 FROM v2_entry_delivery_node n WHERE n.request_id=r.id
 AND ($1='' OR LOWER(n.delivered_host)=$1)
 AND (cardinality($4::bigint[])=0 OR (n.group_id=ANY($4::bigint[]) AND n.source='split')) AND `+validDeliveryNodeSQL+`)
 AND (cardinality($5::text[])=0 OR NOT EXISTS (
 SELECT 1 FROM v2_entry_delivery_request excluded
 WHERE excluded.user_id=r.user_id AND excluded.completed_at >= $2 AND excluded.completed_at < $3
 AND excluded.response_complete AND excluded.coverage='complete' AND excluded.write_error='' AND excluded.status BETWEEN 200 AND 299
 AND EXISTS(SELECT 1 FROM v2_entry_delivery_node n WHERE n.request_id=excluded.id
 AND LOWER(n.delivered_host)=ANY($5::text[]) AND `+validDeliveryNodeSQL+`)))
 ORDER BY r.user_id`, selection.Host, from, to, selection.GroupIDs, selection.ExcludeHosts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

type DeliveryHost struct {
	Host   string `json:"host"`
	Users  int64  `json:"users"`
	LastAt int64  `json:"last_at"`
}
type DeliveryHostsPage struct {
	Items  []DeliveryHost `json:"items"`
	Total  int64          `json:"total"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

func (s *Store) Hosts(ctx context.Context, f Filter) (out DeliveryHostsPage, err error) {
	filter, err := buildDeliveryFilter(f, true)
	out = DeliveryHostsPage{Items: []DeliveryHost{}, Limit: filter.limit, Offset: f.Offset}
	if err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	clause := filter.nodeClause()
	grouped := clause + ` GROUP BY LOWER(n.delivered_host)`
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1`+grouped+`) hosts`, filter.args...).Scan(&out.Total); err != nil {
		return out, err
	}
	args := append(filter.args, out.Limit, out.Offset)
	rows, err := tx.QueryContext(ctx, `SELECT LOWER(n.delivered_host),COUNT(DISTINCT r.user_id),MAX(r.completed_at)`+grouped+fmt.Sprintf(` ORDER BY MAX(r.completed_at) DESC,LOWER(n.delivered_host) LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v DeliveryHost
		if err = rows.Scan(&v.Host, &v.Users, &v.LastAt); err != nil {
			return out, err
		}
		out.Items = append(out.Items, v)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	rows.Close()
	err = tx.Commit()
	return out, err
}

// HistoryHost validates without resolving: DNS today cannot establish past delivery.
func HistoryHost(value string) (string, error) {
	host := safeHost(strings.TrimSpace(value))
	if host == "" {
		return "", ErrValidation
	}
	return strings.ToLower(host), nil
}
