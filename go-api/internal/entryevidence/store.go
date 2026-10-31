package entryevidence

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

type Store struct {
	db          *sql.DB
	failures    atomic.Uint64
	lastFailure atomic.Int64
}

func NewStore(db *sql.DB) *Store                        { return &Store{db: db} }
func (s *Store) EnsureSchema(ctx context.Context) error { return EnsureSchema(ctx, s.db) }

// Record stores only typed, allowlisted routing metadata. It never accepts a
// response body, subscription URL, credentials, or arbitrary server maps.
func (s *Store) Record(ctx context.Context, request Request, nodes []Node) (err error) {
	defer func() {
		if err != nil {
			s.failures.Add(1)
			s.lastFailure.Store(time.Now().Unix())
		}
	}()
	if request.ID == "" || request.UserID <= 0 || request.RequestedAt <= 0 || request.CompletedAt < request.RequestedAt {
		return ErrValidation
	}
	if s.db == nil || len(request.ID) > 128 || request.Status < 100 || request.Status > 599 || request.BytesWritten < 0 {
		return ErrValidation
	}
	request.EmailAtEvent = bounded(request.EmailAtEvent, 254)
	request.UA = bounded(request.UA, 1024)
	switch request.Transport {
	case "subscription", "server_json", "app_clash", "forest_runtime", "forest_provider":
	default:
		request.Transport = "unknown"
	}
	switch request.Coverage {
	case "complete", "partial", "not_emitted", "unavailable":
	default:
		request.Coverage = "unavailable"
	}
	request.CoverageReason = bounded(request.CoverageReason, 200)
	switch request.WriteError {
	case "", "write_failed", "short_write", "flush_failed", "body_not_written", "handler_panicked":
	default:
		request.WriteError = "write_failed"
	}
	if request.Status < 200 || request.Status >= 300 || request.WriteError != "" || request.Coverage != "complete" && request.Coverage != "partial" {
		request.ResponseComplete = false
	}
	if request.Status == 304 {
		request.Coverage, request.CoverageReason = "not_emitted", "not_modified"
		nodes = nil
	}
	if len(nodes) > 2000 {
		nodes = nodes[:2000]
		request.Coverage = "partial"
		request.CoverageReason = "node_limit"
	}
	request.NodeCount = len(nodes)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO v2_entry_delivery_request(id,user_id,email_at_event,ua,requested_at,completed_at,transport,status,response_complete,coverage,coverage_reason,node_count,bytes_written,write_error) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, request.ID, request.UserID, request.EmailAtEvent, request.UA, request.RequestedAt, request.CompletedAt, request.Transport, request.Status, request.ResponseComplete, request.Coverage, request.CoverageReason, request.NodeCount, request.BytesWritten, request.WriteError)
	if err != nil {
		return err
	}
	// Bound parameter batches and round trips; a 2000-node subscription should
	// not require 2000 separate DB exchanges inside the response's one-second
	// evidence deadline. The enclosing transaction still makes all rows atomic.
	const batchSize = 250
	for start := 0; start < len(nodes); start += batchSize {
		end := min(start+batchSize, len(nodes))
		values := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*14)
		for i := start; i < end; i++ {
			node := safeNode(nodes[i])
			placeholders := make([]string, 14)
			for column := range placeholders {
				placeholders[column] = fmt.Sprintf("$%d", len(args)+column+1)
			}
			values = append(values, "("+strings.Join(placeholders, ",")+")")
			args = append(args, request.ID, i, node.ServerType, node.ServerID, node.PolicyID, node.GroupID, node.RuleRevision, node.SourceHost, node.DeliveredHost, node.Port, node.Resolved, node.ProviderID, node.CollectionID, node.Source)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO v2_entry_delivery_node(request_id,ordinal,server_type,server_id,policy_id,group_id,rule_revision,source_host,delivered_host,port,resolved,provider_id,collection_id,source) VALUES`+strings.Join(values, ","), args...)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

const requestColumns = `r.id,r.user_id,r.email_at_event,r.ua,r.requested_at,r.completed_at,r.transport,r.status,r.response_complete,r.coverage,r.coverage_reason,r.node_count,r.bytes_written,r.write_error`

func scanRequest(row interface{ Scan(...any) error }) (Request, error) {
	var v Request
	err := row.Scan(&v.ID, &v.UserID, &v.EmailAtEvent, &v.UA, &v.RequestedAt, &v.CompletedAt, &v.Transport, &v.Status, &v.ResponseComplete, &v.Coverage, &v.CoverageReason, &v.NodeCount, &v.BytesWritten, &v.WriteError)
	return v, err
}

func (s *Store) List(ctx context.Context, f Filter) (Page, error) {
	page := Page{Items: []Record{}, Limit: f.Limit, Offset: f.Offset}
	if page.Limit == 0 {
		page.Limit = 20
	}
	if page.Limit < 1 || page.Limit > 100 || f.Offset < 0 || f.Offset > 10000 || f.From < 0 || f.To < 0 || f.To > 0 && f.From >= f.To || f.UserID < 0 || f.PolicyID < 0 || f.GroupID < 0 {
		return page, ErrValidation
	}
	if f.Host != "" && safeHost(f.Host) == "" || f.Port != "" && !safePort.MatchString(f.Port) {
		return page, ErrValidation
	}
	where, args := []string{"TRUE"}, []any{}
	add := func(expression string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(expression, len(args)))
	}
	if f.UserID > 0 {
		add("r.user_id=$%d", f.UserID)
	}
	if f.Email != "" {
		add("LOWER(r.email_at_event)=$%d", strings.ToLower(strings.TrimSpace(f.Email)))
	}
	if f.UA != "" {
		add("strpos(LOWER(r.ua),LOWER($%d))>0", bounded(f.UA, 1024))
	}
	if f.From > 0 {
		add("r.completed_at >= $%d", f.From)
	}
	if f.To > 0 {
		add("r.completed_at < $%d", f.To)
	}
	if f.CompleteOnly {
		where = append(where, "r.response_complete=TRUE AND r.coverage='complete' AND r.status BETWEEN 200 AND 299")
	}
	nodeWhere := []string{"n.request_id=r.id"}
	addNode := func(expression string, value any) {
		args = append(args, value)
		nodeWhere = append(nodeWhere, fmt.Sprintf(expression, len(args)))
	}
	if f.PolicyID > 0 {
		addNode("n.policy_id=$%d", f.PolicyID)
	}
	if f.GroupID > 0 {
		addNode("n.group_id=$%d", f.GroupID)
	}
	if f.Host != "" {
		addNode("LOWER(n.delivered_host)=LOWER($%d)", safeHost(f.Host))
	}
	if f.Port != "" {
		addNode("n.port=$%d", f.Port)
	}
	if len(nodeWhere) > 1 {
		where = append(where, "EXISTS(SELECT 1 FROM v2_entry_delivery_node n WHERE "+strings.Join(nodeWhere, " AND ")+")")
	}
	clause := " FROM v2_entry_delivery_request r WHERE " + strings.Join(where, " AND ")
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*)"+clause, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	args = append(args, page.Limit, page.Offset)
	rows, err := s.db.QueryContext(ctx, "SELECT "+requestColumns+clause+fmt.Sprintf(" ORDER BY r.completed_at DESC,r.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		v, err := scanRequest(rows)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, Record{Request: v, Nodes: []Node{}})
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	rows.Close()
	if len(page.Items) == 0 {
		return page, nil
	}
	ids := make([]string, len(page.Items))
	indexes := map[string]int{}
	for i, item := range page.Items {
		ids[i] = item.ID
		indexes[item.ID] = i
	}
	nodes, err := s.db.QueryContext(ctx, `SELECT request_id,server_type,server_id,policy_id,group_id,rule_revision,source_host,delivered_host,port,resolved,provider_id,collection_id,source FROM v2_entry_delivery_node WHERE request_id=ANY($1) ORDER BY request_id,ordinal`, ids)
	if err != nil {
		return page, err
	}
	defer nodes.Close()
	for nodes.Next() {
		var id string
		var n Node
		if err := nodes.Scan(&id, &n.ServerType, &n.ServerID, &n.PolicyID, &n.GroupID, &n.RuleRevision, &n.SourceHost, &n.DeliveredHost, &n.Port, &n.Resolved, &n.ProviderID, &n.CollectionID, &n.Source); err != nil {
			return page, err
		}
		index := indexes[id]
		page.Items[index].Nodes = append(page.Items[index].Nodes, n)
	}
	return page, nodes.Err()
}

func (s *Store) Get(ctx context.Context, id string) (Record, error) {
	v, err := scanRequest(s.db.QueryRowContext(ctx, "SELECT "+requestColumns+" FROM v2_entry_delivery_request r WHERE r.id=$1", id))
	result := Record{Request: v, Nodes: []Node{}}
	if err != nil {
		return result, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT server_type,server_id,policy_id,group_id,rule_revision,source_host,delivered_host,port,resolved,provider_id,collection_id,source FROM v2_entry_delivery_node WHERE request_id=$1 ORDER BY ordinal`, id)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ServerType, &n.ServerID, &n.PolicyID, &n.GroupID, &n.RuleRevision, &n.SourceHost, &n.DeliveredHost, &n.Port, &n.Resolved, &n.ProviderID, &n.CollectionID, &n.Source); err != nil {
			return result, err
		}
		result.Nodes = append(result.Nodes, n)
	}
	return result, rows.Err()
}
func (s *Store) Settings(ctx context.Context) (Settings, error) {
	var v Settings
	err := s.db.QueryRowContext(ctx, `SELECT keep_days,updated_at FROM v2_entry_delivery_settings WHERE id=1`).Scan(&v.KeepDays, &v.UpdatedAt)
	return v, err
}
func (s *Store) SaveSettings(ctx context.Context, keepDays int) (Settings, error) {
	if keepDays < 1 || keepDays > 365 {
		return Settings{}, ErrValidation
	}
	v := Settings{KeepDays: keepDays, UpdatedAt: time.Now().Unix()}
	_, err := s.db.ExecContext(ctx, `UPDATE v2_entry_delivery_settings SET keep_days=$1,updated_at=$2 WHERE id=1`, v.KeepDays, v.UpdatedAt)
	return v, err
}
func (s *Store) Status(ctx context.Context) (Status, error) {
	v := Status{WriteFailures: s.failures.Load(), LastWriteFailureAt: s.lastFailure.Load()}
	settings, err := s.Settings(ctx)
	if err != nil {
		return v, err
	}
	v.KeepDays = settings.KeepDays
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(*) FILTER(WHERE response_complete AND coverage='complete'),COUNT(*) FILTER(WHERE coverage='partial'),COUNT(*) FILTER(WHERE NOT response_complete),COALESCE(MIN(completed_at),0),COALESCE(MAX(completed_at),0) FROM v2_entry_delivery_request`).Scan(&v.Requests, &v.Completed, &v.Partial, &v.Failed, &v.FirstAt, &v.LastAt)
	return v, err
}

// Frozen cases own their copied minimal evidence; retention never edits them.
func (s *Store) Cleanup(ctx context.Context) error {
	settings, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-time.Duration(settings.KeepDays) * 24 * time.Hour).Unix()
	for batch := 0; batch < 20; batch++ {
		result, err := s.db.ExecContext(ctx, `DELETE FROM v2_entry_delivery_request WHERE id IN(SELECT id FROM v2_entry_delivery_request WHERE completed_at<$1 ORDER BY completed_at LIMIT 2000)`, cutoff)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n < 2000 {
			return err
		}
	}
	return nil
}
func (s *Store) Run(ctx context.Context) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_ = s.Cleanup(cleanupCtx)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
