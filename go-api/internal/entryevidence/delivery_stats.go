package entryevidence

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// This is a fingerprint of observed delivery metadata, NOT an independent
// configuration activation round. Identical metadata reused later deliberately
// has the same fingerprint; historical activation times cannot be invented.
const deliveryVersionSQL = `md5(jsonb_build_array('delivery-snapshot-v1',n.policy_id,n.group_id,n.provider_id,n.collection_id,n.rule_revision,LOWER(n.source_host),LOWER(n.delivered_host),n.port,n.source,n.resolved)::text)`

const validDeliveryNodeSQL = `n.delivered_host<>'' AND n.port<>'' AND n.server_type IN ('shadowsocks','vmess','vless','trojan','tuic','hysteria','hysteria2','anytls','v2node','juicity','mieru','socks','http') AND n.source IN ('default','policy','split','support_override','provider','extra')`

var deliveryVersionPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

func validDeliveryNode(n Node) bool {
	return n.DeliveredHost != "" && n.Port != "" && n.ServerType != "unknown" && n.Source != "unavailable"
}

type deliveryFilter struct {
	requestWhere []string
	nodeWhere    []string
	args         []any
	limit        int
	needsNode    bool
}

func buildDeliveryFilter(f Filter, statistics bool) (out deliveryFilter, err error) {
	out.limit = f.Limit
	if out.limit == 0 {
		out.limit = 20
	}
	if out.limit < 1 || out.limit > 100 || f.Offset < 0 || f.Offset > 10000 || f.From < 0 || f.To < 0 || f.To > 0 && f.From >= f.To || f.UserID < 0 || f.PolicyID < 0 || f.GroupID < 0 || f.CollectionID < 0 || f.ProviderID < 0 {
		return out, ErrValidation
	}
	if f.Host != "" && safeHost(f.Host) == "" || f.Port != "" && !safePort.MatchString(f.Port) || f.RuleRevision != "" && !safeRevision.MatchString(f.RuleRevision) || f.Version != "" && !deliveryVersionPattern.MatchString(f.Version) {
		return out, ErrValidation
	}
	if f.ServerType != "" && safeNode(Node{ServerType: f.ServerType}).ServerType == "unknown" {
		return out, ErrValidation
	}
	if f.Transport != "" {
		switch f.Transport {
		case "subscription", "server_json", "app_clash", "forest_runtime", "forest_provider":
		default:
			return out, ErrValidation
		}
	}
	out.requestWhere = []string{"TRUE"}
	out.nodeWhere = []string{"n.request_id=r.id"}
	add := func(target *[]string, expression string, value any) {
		out.args = append(out.args, value)
		*target = append(*target, fmt.Sprintf(expression, len(out.args)))
	}
	request := func(expression string, value any) { add(&out.requestWhere, expression, value) }
	node := func(expression string, value any) { add(&out.nodeWhere, expression, value); out.needsNode = true }
	if f.UserID > 0 {
		request("r.user_id=$%d", f.UserID)
	}
	if f.Email != "" {
		request("LOWER(r.email_at_event)=$%d", strings.ToLower(strings.TrimSpace(f.Email)))
	}
	if f.UA != "" {
		request("strpos(LOWER(r.ua),LOWER($%d))>0", bounded(f.UA, 1024))
	}
	if f.From > 0 {
		request("r.completed_at >= $%d", f.From)
	}
	if f.To > 0 {
		request("r.completed_at < $%d", f.To)
	}
	if f.Transport != "" {
		request("r.transport=$%d", f.Transport)
	}
	if f.CompleteOnly || statistics {
		out.requestWhere = append(out.requestWhere, "r.response_complete=TRUE AND r.coverage='complete' AND r.status BETWEEN 200 AND 299")
	}
	if f.PolicyID > 0 {
		node("n.policy_id=$%d", f.PolicyID)
	}
	if f.GroupID > 0 {
		node("n.group_id=$%d", f.GroupID)
	}
	if f.CollectionID > 0 {
		node("n.collection_id=$%d", f.CollectionID)
	}
	if f.ProviderID > 0 {
		node("n.provider_id=$%d", f.ProviderID)
	}
	if f.ServerID != 0 {
		node("n.server_id=$%d", f.ServerID)
	}
	if f.ServerType != "" {
		node("n.server_type=$%d", f.ServerType)
	}
	if f.RuleRevision != "" {
		node("n.rule_revision=$%d", f.RuleRevision)
	}
	if f.Version != "" {
		node(deliveryVersionSQL+"=$%d", f.Version)
	}
	if f.Host != "" {
		node("LOWER(n.delivered_host)=LOWER($%d)", safeHost(f.Host))
	}
	if f.Port != "" {
		node("n.port=$%d", f.Port)
	}
	if statistics {
		out.nodeWhere = append(out.nodeWhere, validDeliveryNodeSQL)
		out.needsNode = true
	}
	return out, nil
}

func (f deliveryFilter) requestClause() string {
	where := strings.Join(f.requestWhere, " AND ")
	if f.needsNode {
		where += " AND EXISTS(SELECT 1 FROM v2_entry_delivery_node n WHERE " + strings.Join(f.nodeWhere, " AND ") + ")"
	}
	return " FROM v2_entry_delivery_request r WHERE " + where
}

func (f deliveryFilter) nodeClause() string {
	return " FROM v2_entry_delivery_request r JOIN v2_entry_delivery_node n ON n.request_id=r.id WHERE " + strings.Join(f.requestWhere, " AND ") + " AND " + strings.Join(f.nodeWhere, " AND ")
}

// Statistics uses EXISTS, not a node join: multiple matching nodes in one
// subscription, including different collection members, remain one request.
func (s *Store) Statistics(ctx context.Context, f Filter) (out DeliveryStatistics, err error) {
	filter, err := buildDeliveryFilter(f, true)
	if err != nil {
		return out, err
	}
	err = s.db.QueryRowContext(ctx, "SELECT COUNT(DISTINCT r.user_id),COUNT(*),COALESCE(MIN(r.completed_at),0),COALESCE(MAX(r.completed_at),0)"+filter.requestClause(), filter.args...).Scan(&out.Users, &out.Requests, &out.FirstAt, &out.LastAt)
	return out, err
}

// Counts and pages share one read-only snapshot, including concurrent cleanup.
func (s *Store) Users(ctx context.Context, f Filter) (out DeliveryUsersPage, err error) {
	filter, err := buildDeliveryFilter(f, true)
	out = DeliveryUsersPage{Items: []DeliveryUser{}, Limit: filter.limit, Offset: f.Offset}
	if err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	clause := filter.requestClause()
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(DISTINCT r.user_id)"+clause, filter.args...).Scan(&out.Total); err != nil {
		return out, err
	}
	args := append(filter.args, out.Limit, out.Offset)
	query := `WITH matched AS MATERIALIZED (SELECT r.id,r.user_id,r.email_at_event,r.completed_at` + clause + `), summary AS (SELECT user_id,COUNT(*) AS requests,MIN(completed_at) AS first_at,MAX(completed_at) AS last_at FROM matched GROUP BY user_id), latest AS (SELECT DISTINCT ON (user_id) user_id,email_at_event FROM matched ORDER BY user_id,completed_at DESC,id DESC) SELECT s.user_id,l.email_at_event,s.first_at,s.last_at,s.requests FROM summary s JOIN latest l USING(user_id)` + fmt.Sprintf(" ORDER BY s.last_at DESC,s.user_id LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item DeliveryUser
		if err = rows.Scan(&item.UserID, &item.EmailAtEvent, &item.FirstAt, &item.LastAt, &item.Requests); err != nil {
			return out, err
		}
		out.Items = append(out.Items, item)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	rows.Close()
	err = tx.Commit()
	return out, err
}

const deliveryVersionFields = `n.policy_id,n.group_id,n.provider_id,n.collection_id,n.rule_revision,LOWER(n.source_host),LOWER(n.delivered_host),n.port,n.source,n.resolved`

func (s *Store) Versions(ctx context.Context, f Filter) (out DeliveryVersionsPage, err error) {
	filter, err := buildDeliveryFilter(f, true)
	out = DeliveryVersionsPage{Items: []DeliveryVersion{}, Limit: filter.limit, Offset: f.Offset}
	if err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	grouped := filter.nodeClause() + " GROUP BY " + deliveryVersionFields
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM (SELECT 1"+grouped+") versions", filter.args...).Scan(&out.Total); err != nil {
		return out, err
	}
	args := append(filter.args, out.Limit, out.Offset)
	query := "SELECT " + deliveryVersionSQL + "," + deliveryVersionFields + ",COUNT(DISTINCT r.user_id),COUNT(DISTINCT r.id),MIN(r.completed_at),MAX(r.completed_at)" + grouped + fmt.Sprintf(" ORDER BY MAX(r.completed_at) DESC,1 LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item DeliveryVersion
		if err = rows.Scan(&item.Version, &item.PolicyID, &item.GroupID, &item.ProviderID, &item.CollectionID, &item.RuleRevision, &item.SourceHost, &item.DeliveredHost, &item.Port, &item.Source, &item.Resolved, &item.Users, &item.Requests, &item.FirstAt, &item.LastAt); err != nil {
			return out, err
		}
		out.Items = append(out.Items, item)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	rows.Close()
	err = tx.Commit()
	return out, err
}
