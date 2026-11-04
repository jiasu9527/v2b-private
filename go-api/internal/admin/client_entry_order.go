package admin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

var ErrClientEntryOrderInvalid = errors.New("入口排序参数无效：使用policy或split_group及真实ID，移动项不能包含锚点或重复项")
var ErrClientEntryOrderConflict = errors.New("规则列表已变化，请刷新后重试：入口排序版本或可见项已变化，需重新读取顺序及revision")

type ClientEntryOrderRow struct {
	Kind            string `json:"kind"`
	ID              int64  `json:"id"`
	PolicyID        int64  `json:"policy_id"`
	Name            string `json:"name"`
	PolicyName      string `json:"policy_name"`
	Sort            int64  `json:"sort"`
	Enabled         int64  `json:"enabled"`
	UpdatedAt       int64  `json:"updated_at"`
	PolicyUpdatedAt int64  `json:"policy_updated_at"`
}
type ClientEntryOrderPage struct {
	Items    []ClientEntryOrderRow `json:"items"`
	Total    int                   `json:"total"`
	Current  int                   `json:"current"`
	PageSize int                   `json:"page_size"`
	Revision string                `json:"revision"`
}
type ClientEntryOrderMoveRequest struct {
	Items            []ClientEntryUserPolicySortItem `json:"items"`
	After            ClientEntryUserPolicySortItem   `json:"after"`
	ExpectedRevision string                          `json:"expected_revision"`
}
type ClientEntryOrderMoveResult struct {
	Changed  bool                            `json:"changed"`
	Revision string                          `json:"revision"`
	Moved    []ClientEntryUserPolicySortItem `json:"moved"`
	After    ClientEntryUserPolicySortItem   `json:"after"`
	Total    int                             `json:"total"`
}

func ValidateClientEntryOrderItems(items []ClientEntryUserPolicySortItem) error {
	seen := map[ClientEntryUserPolicySortItem]bool{}
	if len(items) == 0 {
		return ErrClientEntryOrderInvalid
	}
	for _, item := range items {
		if (item.Kind != "policy" && item.Kind != "split_group") || item.ID <= 0 || seen[item] {
			return ErrClientEntryOrderInvalid
		}
		seen[item] = true
	}
	return nil
}
func ValidateClientEntryOrderMove(req ClientEntryOrderMoveRequest) error {
	if len(req.Items) > 100 || ValidateClientEntryOrderItems(req.Items) != nil || ValidateClientEntryOrderItems([]ClientEntryUserPolicySortItem{req.After}) != nil {
		return ErrClientEntryOrderInvalid
	}
	hash, err := hex.DecodeString(req.ExpectedRevision)
	if err != nil || len(hash) != 32 || hex.EncodeToString(hash) != req.ExpectedRevision {
		return ErrClientEntryOrderInvalid
	}
	for _, item := range req.Items {
		if item == req.After {
			return ErrClientEntryOrderInvalid
		}
	}
	return nil
}
func clientEntryOrderRevision(rows []ClientEntryOrderRow) string {
	raw, _ := json.Marshal(rows)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}
func loadClientEntryVisibleOrder(ctx context.Context, tx *sql.Tx, lock bool) ([]ClientEntryOrderRow, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE OF p"
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.id,p.name,p.sort,p.enabled,p.updated_at FROM v2_client_entry_user_policy p WHERE p.mode<>'split' ORDER BY p.id`+suffix)
	if err != nil {
		return nil, err
	}
	out := []ClientEntryOrderRow{}
	for rows.Next() {
		r := ClientEntryOrderRow{Kind: "policy"}
		if err = rows.Scan(&r.ID, &r.Name, &r.Sort, &r.Enabled, &r.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		r.PolicyID, r.PolicyName, r.PolicyUpdatedAt = r.ID, r.Name, r.UpdatedAt
		out = append(out, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	suffix = ""
	if lock {
		suffix = " FOR UPDATE OF g,p"
	}
	rows, err = tx.QueryContext(ctx, `SELECT g.id,p.id,g.name,p.name,COALESCE(g.global_sort,p.sort),p.enabled,g.updated_at,p.updated_at FROM v2_client_entry_user_policy_split_group g JOIN v2_client_entry_user_policy p ON p.id=g.policy_id WHERE p.mode='split' AND NOT EXISTS(SELECT 1 FROM v2_client_entry_user_policy_split_group child WHERE child.parent_id=g.id) ORDER BY g.id`+suffix)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		r := ClientEntryOrderRow{Kind: "split_group"}
		if err = rows.Scan(&r.ID, &r.PolicyID, &r.Name, &r.PolicyName, &r.Sort, &r.Enabled, &r.UpdatedAt, &r.PolicyUpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Sort != b.Sort {
			return a.Sort < b.Sort
		}
		if a.PolicyID != b.PolicyID {
			return a.PolicyID < b.PolicyID
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.ID < b.ID
	})
	return out, nil
}
func (s *DBService) ListClientEntryVisibleOrder(ctx context.Context, current, size int) (ClientEntryOrderPage, error) {
	if s == nil || s.db == nil {
		return ClientEntryOrderPage{}, ErrUnavailable
	}
	if current < 1 || current > 10000 || size < 1 || size > 100 {
		return ClientEntryOrderPage{}, ErrClientEntryOrderInvalid
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return ClientEntryOrderPage{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ClientEntryOrderPage{}, err
	}
	defer tx.Rollback()
	rows, err := loadClientEntryVisibleOrder(ctx, tx, false)
	if err != nil {
		return ClientEntryOrderPage{}, err
	}
	result := ClientEntryOrderPage{Items: []ClientEntryOrderRow{}, Total: len(rows), Current: current, PageSize: size, Revision: clientEntryOrderRevision(rows)}
	start := min((current-1)*size, len(rows))
	end := min(start+size, len(rows))
	result.Items = rows[start:end]
	if err = tx.Commit(); err != nil {
		return ClientEntryOrderPage{}, err
	}
	return result, nil
}

// Move only the selected visible rows, preserving all other relative positions.
func planClientEntryOrderMove(rows []ClientEntryOrderRow, req ClientEntryOrderMoveRequest) ([]ClientEntryOrderRow, bool, error) {
	if err := ValidateClientEntryOrderMove(req); err != nil {
		return nil, false, err
	}
	if clientEntryOrderRevision(rows) != req.ExpectedRevision {
		return nil, false, ErrClientEntryOrderConflict
	}
	byKey := map[ClientEntryUserPolicySortItem]ClientEntryOrderRow{}
	for _, r := range rows {
		byKey[ClientEntryUserPolicySortItem{Kind: r.Kind, ID: r.ID}] = r
	}
	if _, ok := byKey[req.After]; !ok {
		return nil, false, ErrClientEntryOrderConflict
	}
	selected := map[ClientEntryUserPolicySortItem]bool{}
	for _, item := range req.Items {
		if _, ok := byKey[item]; !ok {
			return nil, false, ErrClientEntryOrderConflict
		}
		selected[item] = true
	}
	out := make([]ClientEntryOrderRow, 0, len(rows))
	for _, r := range rows {
		key := ClientEntryUserPolicySortItem{Kind: r.Kind, ID: r.ID}
		if selected[key] {
			continue
		}
		out = append(out, r)
		if key == req.After {
			for _, item := range req.Items {
				out = append(out, byKey[item])
			}
		}
	}
	changed := false
	for i := range rows {
		if rows[i].Kind != out[i].Kind || rows[i].ID != out[i].ID {
			changed = true
		}
	}
	return out, changed, nil
}
func (s *DBService) MoveClientEntryVisibleRows(ctx context.Context, req ClientEntryOrderMoveRequest) (ClientEntryOrderMoveResult, error) {
	if err := ValidateClientEntryOrderMove(req); err != nil {
		return ClientEntryOrderMoveResult{}, err
	}
	if s == nil || s.db == nil {
		return ClientEntryOrderMoveResult{}, ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return ClientEntryOrderMoveResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ClientEntryOrderMoveResult{}, err
	}
	defer tx.Rollback()
	if err = lockClientEntryVisibleOrder(ctx, tx); err != nil {
		return ClientEntryOrderMoveResult{}, err
	}
	rows, err := loadClientEntryVisibleOrder(ctx, tx, true)
	if err != nil {
		return ClientEntryOrderMoveResult{}, err
	}
	ordered, changed, err := planClientEntryOrderMove(rows, req)
	if err != nil {
		return ClientEntryOrderMoveResult{}, err
	}
	if changed {
		items := make([]ClientEntryUserPolicySortItem, 0, len(ordered))
		for _, r := range ordered {
			items = append(items, ClientEntryUserPolicySortItem{Kind: r.Kind, ID: r.ID})
		}
		if err = persistClientEntryVisibleOrder(ctx, tx, items, time.Now().Unix()); err != nil {
			return ClientEntryOrderMoveResult{}, err
		}
	}
	// Re-read in this transaction to return a revision of committed ordering,
	// including compatibility parent sorts and update timestamps.
	rows, err = loadClientEntryVisibleOrder(ctx, tx, false)
	if err != nil {
		return ClientEntryOrderMoveResult{}, err
	}
	result := ClientEntryOrderMoveResult{Changed: changed, Revision: clientEntryOrderRevision(rows), Moved: req.Items, After: req.After, Total: len(rows)}
	if err = tx.Commit(); err != nil {
		return ClientEntryOrderMoveResult{}, err
	}
	if changed {
		s.markClientEntryMonitorTargetsDirty()
	}
	return result, nil
}
