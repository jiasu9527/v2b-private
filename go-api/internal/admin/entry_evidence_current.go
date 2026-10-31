package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"forest/go-api/internal/cliententry"
	"github.com/jackc/pgx/v5/pgconn"
)

type evidenceCurrentPolicy struct {
	id                 int64
	mode, action, host string
	priority           sql.NullInt64
	conditions         []cliententry.Condition
	members            []AIEntryRuleMember
}
type evidenceCurrentAssignment struct {
	group    int64
	host     string
	priority sql.NullInt64
}
type evidenceCurrentRoute struct {
	policy   *evidenceCurrentPolicy
	group    int64
	host     string
	priority sql.NullInt64
}

func evidenceCurrentLess(a, b evidenceCurrentRoute) bool {
	if a.priority.Valid != b.priority.Valid {
		return a.priority.Valid
	}
	if a.priority.Valid && a.priority.Int64 != b.priority.Int64 {
		return a.priority.Int64 < b.priority.Int64
	}
	return a.policy.id < b.policy.id
}
func evidenceCurrentCoverageBatched(ctx context.Context, tx *sql.Tx, rule AIEntryRuleSnapshot, items []EntryEvidenceCandidate) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.UserID)
	}
	wanted := make(map[AIEntryRuleMember]bool, len(rule.Members))
	for _, m := range rule.Members {
		wanted[m] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.id,p.mode,p.conditions,p.action,p.entry_host,p.sort,m.server_type,m.server_id
FROM v2_client_entry_user_policy p JOIN v2_client_entry_user_policy_member m ON m.policy_id=p.id
WHERE p.enabled=1 ORDER BY p.id,m.sort ASC NULLS LAST,m.id`)
	if err != nil {
		return err
	}
	policies := map[int64]*evidenceCurrentPolicy{}
	policyIDs := []int64{}
	for rows.Next() {
		var p evidenceCurrentPolicy
		var raw string
		var member AIEntryRuleMember
		if err = rows.Scan(&p.id, &p.mode, &raw, &p.action, &p.host, &p.priority, &member.ServerType, &member.ServerID); err != nil {
			rows.Close()
			return err
		}
		if !wanted[member] {
			continue
		}
		existing := policies[p.id]
		if existing == nil {
			p.conditions, err = cliententry.DecodeConditions(raw)
			if err != nil {
				rows.Close()
				return err
			}
			existing = &p
			policies[p.id] = existing
			policyIDs = append(policyIDs, p.id)
		}
		existing.members = append(existing.members, member)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,email,COALESCE(plan_id,0),created_at FROM v2_user WHERE id=ANY($1) ORDER BY id FOR SHARE NOWAIT`, ids)
	if err != nil {
		return evidenceUserLockError(err)
	}
	subjects := make(map[int64]cliententry.Subject, len(items))
	now := time.Now().Unix()
	for rows.Next() {
		var subject cliententry.Subject
		var created sql.NullInt64
		if err = rows.Scan(&subject.UserID, &subject.Email, &subject.PlanID, &created); err != nil {
			rows.Close()
			return err
		}
		subject.RegistrationDays = clientEntrySimulationRegistrationDays(created, now)
		subjects[subject.UserID] = subject
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return evidenceUserLockError(err)
	}
	// A support override runs after policy routing. Conservatively reject any
	// active override instead of claiming this rule-only simulation models that
	// second routing stage or silently bypassing it with an overlay policy.
	// Read after the selected user rows are locked: support mutations lock
	// their target user too, so a new override cannot slip past this guard.
	if err := evidenceRejectSupportOverrides(ctx, tx, ids, time.Now().Unix()); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `SELECT a.user_id,a.policy_id,g.id,g.entry_host,g.global_sort
FROM v2_client_entry_user_policy_split_assignment a
JOIN v2_client_entry_user_policy_split_group g ON g.id=a.group_id AND g.policy_id=a.policy_id
WHERE a.user_id=ANY($1) AND a.policy_id=ANY($2)
ORDER BY a.user_id,a.policy_id FOR SHARE OF a,g`, ids, policyIDs)
	if err != nil {
		return err
	}
	assignments := map[int64]map[int64]evidenceCurrentAssignment{}
	for rows.Next() {
		var userID, policyID int64
		var a evidenceCurrentAssignment
		if err = rows.Scan(&userID, &policyID, &a.group, &a.host, &a.priority); err != nil {
			rows.Close()
			return err
		}
		if assignments[userID] == nil {
			assignments[userID] = map[int64]evidenceCurrentAssignment{}
		}
		assignments[userID][policyID] = a
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	standard := []evidenceCurrentRoute{}
	for _, p := range policies {
		if p.mode != "split" {
			standard = append(standard, evidenceCurrentRoute{policy: p, host: p.host, priority: p.priority})
		}
	}
	sort.Slice(standard, func(i, j int) bool { return evidenceCurrentLess(standard[i], standard[j]) })
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		subject, ok := subjects[item.UserID]
		if !ok {
			return fmt.Errorf("%w：用户%d已不存在", ErrEntryEvidenceConflict, item.UserID)
		}
		split := make([]evidenceCurrentRoute, 0, len(assignments[item.UserID]))
		for policyID, a := range assignments[item.UserID] {
			p := policies[policyID]
			if p == nil || p.mode != "split" || a.host == "" {
				continue
			}
			priority := a.priority
			if !priority.Valid {
				priority = p.priority
			}
			split = append(split, evidenceCurrentRoute{policy: p, group: a.group, host: a.host, priority: priority})
		}
		sort.Slice(split, func(i, j int) bool { return evidenceCurrentLess(split[i], split[j]) })
		ordered := make([]evidenceCurrentRoute, 0, len(standard)+len(split))
		i, j := 0, 0
		for i < len(standard) || j < len(split) {
			if j == len(split) || i < len(standard) && evidenceCurrentLess(standard[i], split[j]) {
				ordered = append(ordered, standard[i])
				i++
			} else {
				ordered = append(ordered, split[j])
				j++
			}
		}
		for _, ua := range item.UAs {
			subject.UA = ua
			matched := make(map[AIEntryRuleMember]bool, len(wanted))
			for _, route := range ordered {
				p := route.policy
				if !cliententry.MatchAll(cliententry.RuntimeConditions(p.mode, p.conditions), subject) {
					continue
				}
				for _, member := range p.members {
					if matched[member] {
						continue
					}
					if p.id != rule.PolicyID || rule.Kind == "split_group" && route.group != rule.TargetID || p.action != cliententry.ActionOverride {
						return fmt.Errorf("%w：用户%d在当前UA和节点下已被其他规则接管", ErrEntryEvidenceConflict, item.UserID)
					}
					matched[member] = true
				}
				if len(matched) == len(wanted) {
					break
				}
			}
			if len(matched) != len(wanted) {
				return fmt.Errorf("%w：用户%d在当前UA或节点下不再命中来源规则", ErrEntryEvidenceConflict, item.UserID)
			}
		}
	}
	return nil
}

func evidenceRejectSupportOverrides(ctx context.Context, tx *sql.Tx, ids []int64, now int64) error {
	var userID int64
	err := tx.QueryRowContext(ctx, `SELECT user_id FROM v2_support_entry_override
WHERE user_id=ANY($1) AND active=1 AND (expires_at IS NULL OR expires_at<=0 OR expires_at>$2)
ORDER BY user_id,source_policy_id LIMIT 1 FOR SHARE`, ids, now).Scan(&userID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w：用户%d仍有生效中的人工入口覆盖；请先核对或恢复该覆盖，再预览冻结名单，不能用二分规则绕过后置路由", ErrEntryEvidenceConflict, userID)
}

func evidenceUserLockError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "55P03" {
		return fmt.Errorf("%w：候选用户正在被其他操作修改，请稍后重新预览；未改变入口", ErrEntryEvidenceConflict)
	}
	return err
}
