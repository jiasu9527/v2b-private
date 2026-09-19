package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"forest/go-api/internal/cliententry"
)

// Collections only own the shared host. Matching, users, nodes and global order
// remain on their original policy/leaf rows, including when a collection is dissolved.
type ClientEntryCollectionItem struct {
	Kind string `json:"kind"`
	ID   int64  `json:"id"`
}
type ClientEntryCollectionRecord struct {
	ID        int64                       `json:"id"`
	Name      string                      `json:"name"`
	EntryHost string                      `json:"entry_host"`
	Items     []ClientEntryCollectionItem `json:"items"`
	Version   int64                       `json:"version"`
}
type ClientEntryCollectionSaveRequest struct {
	ID        int64                       `json:"id,omitempty"`
	Name      string                      `json:"name"`
	EntryHost string                      `json:"entry_host"`
	Items     []ClientEntryCollectionItem `json:"items"`
	Version   int64                       `json:"version,omitempty"`
}
type ClientEntryCollectionMemberRemoveRequest struct {
	ID       int64  `json:"id"`
	Kind     string `json:"kind"`
	MemberID int64  `json:"member_id"`
	Version  int64  `json:"version"`
}

var errClientEntryCollectionChanged = errors.New("入口合集或成员已变化，请刷新后重试")

func (s *DBService) ListClientEntryCollections(ctx context.Context) ([]ClientEntryCollectionRecord, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.name, c.entry_host, c.version, m.policy_id, m.split_group_id
FROM v2_client_entry_collection c
LEFT JOIN v2_client_entry_collection_member m ON m.collection_id = c.id
ORDER BY c.id, m.policy_id NULLS LAST, m.split_group_id NULLS LAST`)
	if err != nil {
		return nil, fmt.Errorf("读取入口合集失败: %w", err)
	}
	defer rows.Close()
	result := []ClientEntryCollectionRecord{}
	for rows.Next() {
		var record ClientEntryCollectionRecord
		var policyID, groupID sql.NullInt64
		if err := rows.Scan(&record.ID, &record.Name, &record.EntryHost, &record.Version, &policyID, &groupID); err != nil {
			return nil, err
		}
		if len(result) == 0 || result[len(result)-1].ID != record.ID {
			record.Items = []ClientEntryCollectionItem{}
			result = append(result, record)
		}
		if policyID.Valid {
			result[len(result)-1].Items = append(result[len(result)-1].Items, ClientEntryCollectionItem{Kind: "policy", ID: policyID.Int64})
		}
		if groupID.Valid {
			result[len(result)-1].Items = append(result[len(result)-1].Items, ClientEntryCollectionItem{Kind: "split_group", ID: groupID.Int64})
		}
	}
	return result, rows.Err()
}

func normalizeClientEntryCollectionSaveRequest(req ClientEntryCollectionSaveRequest) (ClientEntryCollectionSaveRequest, error) {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len([]rune(req.Name)) > 255 {
		return req, errors.New("合集名称不能为空且不能超过 255 个字符")
	}
	host, err := cliententry.NormalizeHost(req.EntryHost)
	if err != nil || host == "" {
		return req, errors.New("请填写有效的统一入口域名或 IP")
	}
	req.EntryHost = host
	minimum := 2
	if req.ID != 0 {
		if req.ID <= 0 || req.Version <= 0 {
			return req, errClientEntryCollectionChanged
		}
		minimum = 1
	}
	if len(req.Items) < minimum || len(req.Items) > 10000 {
		return req, fmt.Errorf("合集至少需要 %d 个分组，最多 10000 个", minimum)
	}
	req.Items = append([]ClientEntryCollectionItem(nil), req.Items...)
	seen := map[ClientEntryCollectionItem]bool{}
	for i := range req.Items {
		req.Items[i].Kind = strings.ToLower(strings.TrimSpace(req.Items[i].Kind))
		item := req.Items[i]
		if (item.Kind != "policy" && item.Kind != "split_group") || item.ID <= 0 || seen[item] {
			return req, errors.New("合集成员无效或重复")
		}
		seen[item] = true
	}
	sort.Slice(req.Items, func(i, j int) bool {
		if req.Items[i].Kind != req.Items[j].Kind {
			return req.Items[i].Kind < req.Items[j].Kind
		}
		return req.Items[i].ID < req.Items[j].ID
	})
	return req, nil
}

func (s *DBService) SaveClientEntryCollection(ctx context.Context, req ClientEntryCollectionSaveRequest) (ClientEntryCollectionRecord, error) {
	if s.db == nil {
		return ClientEntryCollectionRecord{}, ErrUnavailable
	}
	prepared, err := normalizeClientEntryCollectionSaveRequest(req)
	if err != nil {
		return ClientEntryCollectionRecord{}, err
	}
	if err = s.ensureClientEntrySchema(ctx); err != nil {
		return ClientEntryCollectionRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ClientEntryCollectionRecord{}, err
	}
	defer tx.Rollback()
	if err = lockClientEntryVisibleOrder(ctx, tx); err != nil {
		return ClientEntryCollectionRecord{}, err
	}
	now := time.Now().Unix()
	record := ClientEntryCollectionRecord{Name: prepared.Name, EntryHost: prepared.EntryHost, Items: prepared.Items, Version: 1}
	if prepared.ID != 0 {
		record.ID = prepared.ID
		if err = lockClientEntryCollectionVersion(ctx, tx, record.ID, prepared.Version); err != nil {
			return ClientEntryCollectionRecord{}, err
		}
		record.Version = prepared.Version + 1
	}
	for _, item := range prepared.Items {
		var currentCollection sql.NullInt64
		var stmt string
		if item.Kind == "policy" {
			stmt = `SELECT m.collection_id FROM v2_client_entry_user_policy p
LEFT JOIN v2_client_entry_collection_member m ON m.policy_id=p.id
WHERE p.id=$1 AND p.mode='standard' AND p.action='override' FOR UPDATE OF p`
		} else {
			stmt = `SELECT m.collection_id FROM v2_client_entry_user_policy_split_group g
JOIN v2_client_entry_user_policy p ON p.id=g.policy_id
LEFT JOIN v2_client_entry_collection_member m ON m.split_group_id=g.id
WHERE g.id=$1 AND p.mode='split' AND NOT EXISTS (SELECT 1 FROM v2_client_entry_user_policy_split_group child WHERE child.parent_id=g.id)
FOR UPDATE OF g`
		}
		if err = tx.QueryRowContext(ctx, stmt, item.ID).Scan(&currentCollection); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ClientEntryCollectionRecord{}, errors.New("只能合并覆盖入口的普通规则或当前固定名单分组，请刷新列表")
			}
			return ClientEntryCollectionRecord{}, err
		}
		if currentCollection.Valid && currentCollection.Int64 != record.ID {
			return ClientEntryCollectionRecord{}, errors.New("所选分组已加入其他合集，请先移出原合集")
		}
	}
	if prepared.ID == 0 {
		err = tx.QueryRowContext(ctx, `INSERT INTO v2_client_entry_collection(name,entry_host,version,created_at,updated_at) VALUES($1,$2,1,$3,$3) RETURNING id`, record.Name, record.EntryHost, now).Scan(&record.ID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE v2_client_entry_collection SET name=$2,entry_host=$3,version=version+1,updated_at=$4 WHERE id=$1`, record.ID, record.Name, record.EntryHost, now)
	}
	if err != nil {
		return ClientEntryCollectionRecord{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM v2_client_entry_collection_member WHERE collection_id=$1`, record.ID); err != nil {
		return ClientEntryCollectionRecord{}, err
	}
	for _, item := range prepared.Items {
		var policyID, groupID any
		var update string
		if item.Kind == "policy" {
			policyID = item.ID
			update = `UPDATE v2_client_entry_user_policy SET entry_host=$2,updated_at=$3 WHERE id=$1`
		} else {
			groupID = item.ID
			update = `UPDATE v2_client_entry_user_policy_split_group SET entry_host=$2,updated_at=$3 WHERE id=$1`
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO v2_client_entry_collection_member(collection_id,policy_id,split_group_id) VALUES($1,$2::INTEGER,$3::BIGINT)`, record.ID, policyID, groupID); err != nil {
			return ClientEntryCollectionRecord{}, err
		}
		if _, err = tx.ExecContext(ctx, update, item.ID, record.EntryHost, now); err != nil {
			return ClientEntryCollectionRecord{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return ClientEntryCollectionRecord{}, err
	}
	s.markClientEntryMonitorTargetsDirty()
	return record, nil
}

func lockClientEntryCollectionVersion(ctx context.Context, tx *sql.Tx, id, version int64) error {
	var current int64
	err := tx.QueryRowContext(ctx, `SELECT version FROM v2_client_entry_collection WHERE id=$1 FOR UPDATE`, id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && current != version) {
		return errClientEntryCollectionChanged
	}
	return err
}

func (s *DBService) DeleteClientEntryCollection(ctx context.Context, id, version int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if id <= 0 || version <= 0 {
		return false, errClientEntryCollectionChanged
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = lockClientEntryVisibleOrder(ctx, tx); err != nil {
		return false, err
	}
	if err = lockClientEntryCollectionVersion(ctx, tx, id, version); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM v2_client_entry_collection WHERE id=$1`, id); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *DBService) RemoveClientEntryCollectionMember(ctx context.Context, id, version int64, item ClientEntryCollectionItem) (bool, error) {
	req := ClientEntryCollectionMemberRemoveRequest{ID: id, Version: version, Kind: item.Kind, MemberID: item.ID}
	if s.db == nil {
		return false, ErrUnavailable
	}
	if req.ID <= 0 || req.Version <= 0 || req.MemberID <= 0 || (req.Kind != "policy" && req.Kind != "split_group") {
		return false, errClientEntryCollectionChanged
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = lockClientEntryVisibleOrder(ctx, tx); err != nil {
		return false, err
	}
	if err = lockClientEntryCollectionVersion(ctx, tx, req.ID, req.Version); err != nil {
		return false, err
	}
	column := "policy_id"
	if req.Kind == "split_group" {
		column = "split_group_id"
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_collection_member WHERE collection_id=$1 AND `+column+`=$2`, req.ID, req.MemberID)
	if err != nil {
		return false, err
	}
	if err = requireClientEntryRuleAffected(result, "合集成员"); err != nil {
		return false, errClientEntryCollectionChanged
	}
	if _, err = tx.ExecContext(ctx, `UPDATE v2_client_entry_collection SET version=version+1,updated_at=$2 WHERE id=$1`, req.ID, time.Now().Unix()); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM v2_client_entry_collection WHERE id=$1 AND NOT EXISTS (SELECT 1 FROM v2_client_entry_collection_member WHERE collection_id=$1)`, req.ID); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// Called inside the same global-order transaction before a member is split or
// deleted. New leaves intentionally receive no collection membership.
func detachClientEntryCollectionMembers(ctx context.Context, tx *sql.Tx, kind string, id int64) error {
	predicate := `policy_id=$1 OR split_group_id IN (SELECT id FROM v2_client_entry_user_policy_split_group WHERE policy_id=$1)`
	if kind == "split_group" {
		predicate = `split_group_id=$1`
	}
	_, err := tx.ExecContext(ctx, `WITH removed AS (
 DELETE FROM v2_client_entry_collection_member WHERE `+predicate+` RETURNING collection_id
)
UPDATE v2_client_entry_collection SET version=version+1,updated_at=$2
WHERE id IN (SELECT collection_id FROM removed)`, id, time.Now().Unix())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM v2_client_entry_collection c WHERE NOT EXISTS (SELECT 1 FROM v2_client_entry_collection_member m WHERE m.collection_id=c.id)`)
	return err
}
