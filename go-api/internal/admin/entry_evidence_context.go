package admin

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

type EntryEvidenceContextUser struct {
	UserID         int64  `json:"user_id"`
	Email          string `json:"email"`
	Classification string `json:"classification"`
	FirstAt        int64  `json:"first_at"`
	LastAt         int64  `json:"last_at"`
	RequestCount   int64  `json:"request_count"`
	Orphaned       bool   `json:"orphaned"`
}
type EntryEvidenceContextPage struct {
	Items    []EntryEvidenceContextUser `json:"items"`
	Total    int64                      `json:"total"`
	Current  int                        `json:"current"`
	PageSize int                        `json:"page_size"`
	Counts   map[string]int64           `json:"counts"`
	Warnings []string                   `json:"warnings"`
}

// Historical evidence and incomplete pulls are context, never automatically
// added to the verified current-window candidate set or a split assignment.
func (s *DBService) EntryEvidenceContext(ctx context.Context, actor int64, req EntryEvidenceQueryRequest) (EntryEvidenceContextPage, error) {
	out := EntryEvidenceContextPage{Items: []EntryEvidenceContextUser{}, Counts: map[string]int64{"window_delivered": 0, "historical_exposure": 0, "request_only": 0, "orphaned": 0}, Warnings: []string{
		"仅统计实际下发采集启用后仍在保留期内的记录；旧订阅防护日志没有入口快照，不能补成历史下发。",
		"历史已暴露用户可能仍有缓存；仅有不完整/失败拉取记录不能确定其拿到过该入口，不会自动纳入冻结二分名单。",
		"无法关联表示当前用户已不存在，历史ID/邮箱仍保留；无记录不能证明从未获得入口。",
	}}
	if err := s.prepareEntryEvidence(ctx, actor, false); err != nil {
		return out, err
	}
	q, err := normalizeEvidenceQuery(req)
	if err != nil {
		return out, err
	}
	out.Current, out.PageSize = q.Current, q.PageSize
	policy, group := int64(0), int64(0)
	if q.Kind == "policy" {
		policy = q.TargetID
	} else {
		group = q.TargetID
	}
	cte := `WITH scoped AS (
SELECT r.*, EXISTS(SELECT 1 FROM v2_entry_delivery_node n WHERE n.request_id=r.id
 AND ($1::BIGINT=0 OR n.policy_id=$1) AND ($2::BIGINT=0 OR n.group_id=$2)
 AND lower(n.delivered_host)=$3 AND n.port=$4) AS target_match
FROM v2_entry_delivery_request r WHERE r.completed_at<$6
AND ($7='' OR strpos(lower(r.ua),lower($7))>0)
AND ($8='' OR lower(r.email_at_event)=$8 OR r.user_id::TEXT=$8)
), facts AS (
SELECT *, (response_complete AND coverage='complete' AND status BETWEEN 200 AND 299 AND target_match) AS delivered
FROM scoped
), grouped AS (
SELECT user_id,(array_agg(email_at_event ORDER BY completed_at DESC,id DESC))[1] AS email,
 MIN(completed_at) AS first_at,MAX(completed_at) AS last_at,COUNT(*) AS request_count,
 bool_or(delivered AND completed_at>=$5) AS in_window,
 bool_or(delivered AND completed_at<$5) AS historical
FROM facts WHERE delivered OR (target_match AND completed_at>=$5 AND (NOT response_complete OR coverage<>'complete' OR status NOT BETWEEN 200 AND 299))
GROUP BY user_id
), classified AS (
SELECT g.*,CASE WHEN in_window THEN 'window_delivered' WHEN historical THEN 'historical_exposure' ELSE 'request_only' END AS classification,
 NOT EXISTS(SELECT 1 FROM v2_user u WHERE u.id=g.user_id) AS orphaned FROM grouped g
) `
	args := []any{policy, group, q.Host, strconv.FormatInt(q.Port, 10), q.From, q.To, q.UA, q.Account}
	// One repeatable-read snapshot keeps category counts and rows consistent.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var window, historical, only, orphaned int64
	err = tx.QueryRowContext(ctx, cte+`SELECT COUNT(*),COUNT(*) FILTER(WHERE classification='window_delivered'),COUNT(*) FILTER(WHERE classification='historical_exposure'),COUNT(*) FILTER(WHERE classification='request_only'),COUNT(*) FILTER(WHERE orphaned) FROM classified`, args...).Scan(&out.Total, &window, &historical, &only, &orphaned)
	if err != nil {
		return out, err
	}
	out.Counts = map[string]int64{"window_delivered": window, "historical_exposure": historical, "request_only": only, "orphaned": orphaned}
	var unlinked int64
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_entry_delivery_request r
WHERE completed_at >= $1 AND completed_at < $2
AND ($3='' OR strpos(lower(ua),lower($3))>0)
AND ($4='' OR lower(email_at_event)=$4 OR user_id::TEXT=$4)
AND NOT EXISTS(SELECT 1 FROM v2_entry_delivery_node n WHERE n.request_id=r.id)`, q.From, q.To, q.UA, q.Account).Scan(&unlinked)
	if err != nil {
		return out, err
	}
	if unlinked > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("同一窗口另有 %d 条没有节点快照的全站请求，无法关联本入口，未纳入这里的用户分类或候选名单。", unlinked))
	}
	args = append(args, q.PageSize, (q.Current-1)*q.PageSize)
	rows, err := tx.QueryContext(ctx, cte+fmt.Sprintf(`SELECT user_id,email,classification,first_at,last_at,request_count,orphaned FROM classified ORDER BY in_window DESC,historical DESC,last_at DESC,user_id LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var item EntryEvidenceContextUser
		if err = rows.Scan(&item.UserID, &item.Email, &item.Classification, &item.FirstAt, &item.LastAt, &item.RequestCount, &item.Orphaned); err != nil {
			rows.Close()
			return out, err
		}
		out.Items = append(out.Items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
