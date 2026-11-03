package entryevidence

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type HistoryQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// HistoricalUserIDs uses final delivered addresses, never current routing rules.
// Deleted users remain in the evidence ledger but cannot receive new assignments.
func HistoricalUserIDs(ctx context.Context, db HistoryQueryer, host string, from, to int64) ([]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT r.user_id FROM v2_entry_delivery_request r JOIN v2_user u ON u.id=r.user_id
 WHERE r.completed_at >= $2 AND r.completed_at < $3 AND r.response_complete AND r.coverage='complete' AND r.status BETWEEN 200 AND 299
 AND EXISTS(SELECT 1 FROM v2_entry_delivery_node n WHERE n.request_id=r.id AND LOWER(n.delivered_host)=LOWER($1) AND `+validDeliveryNodeSQL+`) ORDER BY r.user_id`, host, from, to)
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
