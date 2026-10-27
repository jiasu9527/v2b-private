package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/cliententry"
)

const SubscribeGuardSearchMaxUsers = 50000

var ErrSubscribeGuardSearchLimit = errors.New("筛选候选用户过多，请增加邮箱、UA 或拉取时间条件")

type SubscribeGuardSearchLoadRequest struct {
	Account              string
	AccountMatch         string
	UserIDs              []int64
	Tokens               []string
	IncludeWithoutEvents bool
}

type SubscribeGuardSearchUser struct {
	ID        int64
	Email     string
	Token     string `json:"-"` // Internal join key only.
	PlanID    int64
	CreatedAt sql.NullInt64
	Public    map[string]any
}

type SubscribeGuardSearchSnapshot struct {
	Users          map[int64]SubscribeGuardSearchUser
	UserIDsByToken map[string]int64
	Policies       []ClientEntryUserPolicyRecord
	Assignments    map[int64]map[int64]int64
	EvaluatedAt    int64
}

type SubscribeGuardSearchPolicy struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Mode             string `json:"mode"`
	Action           string `json:"action"`
	EntryHost        string `json:"entry_host"`
	ResolveEntryHost int64  `json:"resolve_entry_host"`
	GroupID          int64  `json:"group_id"`
	GroupName        string `json:"group_name"`
	EvaluatedUA      string `json:"evaluated_ua"`
}

// LoadSubscribeGuardSearchSnapshot does no schema creation or assignment writes.
// Explicit IDs/tokens are batched; only an account/rule-only search may include
// users with no retained requests. All limits fail closed instead of truncating.
func (s *DBService) LoadSubscribeGuardSearchSnapshot(ctx context.Context, req SubscribeGuardSearchLoadRequest) (*SubscribeGuardSearchSnapshot, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	if len(req.UserIDs)+len(req.Tokens) > SubscribeGuardSearchMaxUsers*2 {
		return nil, ErrSubscribeGuardSearchLimit
	}
	result := &SubscribeGuardSearchSnapshot{Users: map[int64]SubscribeGuardSearchUser{}, UserIDsByToken: map[string]int64{}, Assignments: map[int64]map[int64]int64{}, EvaluatedAt: time.Now().Unix()}
	policies, err := s.listClientEntryUserPolicies(ctx, false)
	if err != nil {
		return nil, err
	}
	result.Policies = policies
	// Each batch has at most 1000 bind parameters, well below PostgreSQL's limit.
	if req.IncludeWithoutEvents {
		if err := s.loadSubscribeGuardSearchUsers(ctx, req, nil, nil, result); err != nil {
			return nil, err
		}
	} else {
		for start := 0; start < len(req.UserIDs); start += 1000 {
			end := start + 1000
			if end > len(req.UserIDs) {
				end = len(req.UserIDs)
			}
			if err := s.loadSubscribeGuardSearchUsers(ctx, req, req.UserIDs[start:end], nil, result); err != nil {
				return nil, err
			}
		}
		for start := 0; start < len(req.Tokens); start += 1000 {
			end := start + 1000
			if end > len(req.Tokens) {
				end = len(req.Tokens)
			}
			if err := s.loadSubscribeGuardSearchUsers(ctx, req, nil, req.Tokens[start:end], result); err != nil {
				return nil, err
			}
		}
	}
	hasSplit := false
	for _, p := range policies {
		if p.Mode == ClientEntryUserPolicyModeSplit {
			hasSplit = true
			break
		}
	}
	if !hasSplit {
		return result, nil
	}
	ids := make([]int64, 0, len(result.Users))
	for id := range result.Users {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for start := 0; start < len(ids); start += 1000 {
		end := start + 1000
		if end > len(ids) {
			end = len(ids)
		}
		args := make([]any, 0, end-start)
		placeholders := make([]string, 0, end-start)
		for _, id := range ids[start:end] {
			args = append(args, id)
			placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
		}
		rows, err := s.db.QueryContext(ctx, `SELECT user_id, policy_id, group_id FROM v2_client_entry_user_policy_split_assignment WHERE user_id IN (`+strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("读取入口固定分组失败: %w", err)
		}
		for rows.Next() {
			var userID, policyID, groupID int64
			if err := rows.Scan(&userID, &policyID, &groupID); err != nil {
				rows.Close()
				return nil, err
			}
			if result.Assignments[userID] == nil {
				result.Assignments[userID] = map[int64]int64{}
			}
			result.Assignments[userID][policyID] = groupID
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	return result, nil
}

func (s *DBService) loadSubscribeGuardSearchUsers(ctx context.Context, req SubscribeGuardSearchLoadRequest, ids []int64, tokens []string, result *SubscribeGuardSearchSnapshot) error {
	args := []any{}
	where := []string{}
	if req.Account != "" {
		id, idErr := strconv.ParseInt(req.Account, 10, 64)
		decimalID := strings.IndexFunc(req.Account, func(r rune) bool { return r < '0' || r > '9' }) == -1
		if decimalID && idErr == nil && id > 0 {
			args = append(args, id)
			where = append(where, fmt.Sprintf("u.id = $%d", len(args)))
		} else {
			args = append(args, strings.ToLower(req.Account))
			if req.AccountMatch == "exact" {
				where = append(where, fmt.Sprintf("LOWER(u.email) = $%d", len(args)))
			} else {
				// STRPOS treats SQL wildcard characters literally, just like the UA filter.
				where = append(where, fmt.Sprintf("STRPOS(LOWER(u.email), $%d) > 0", len(args)))
			}
		}
	}
	placeholders := []string{}
	for _, id := range ids {
		args = append(args, id)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}
	if len(ids) > 0 {
		where = append(where, "u.id IN ("+strings.Join(placeholders, ",")+")")
	}
	placeholders = nil
	for _, token := range tokens {
		args = append(args, token)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}
	if len(tokens) > 0 {
		where = append(where, "u.token IN ("+strings.Join(placeholders, ",")+")")
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT u.id, u.email, u.token, u.plan_id, u.created_at, u.banned, u.u, u.d, u.transfer_enable, u.expired_at, p.name FROM v2_user u LEFT JOIN v2_plan p ON p.id = u.plan_id`+clause+` ORDER BY u.id LIMIT 50001`, args...)
	if err != nil {
		return fmt.Errorf("读取筛选用户失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var user SubscribeGuardSearchUser
		var planID, banned, up, down, transfer, expired sql.NullInt64
		var planName, token sql.NullString
		if err := rows.Scan(&user.ID, &user.Email, &token, &planID, &user.CreatedAt, &banned, &up, &down, &transfer, &expired, &planName); err != nil {
			return err
		}
		user.Token = strings.TrimSpace(token.String)
		user.PlanID = planID.Int64
		var expiry any
		if expired.Valid {
			expiry = expired.Int64
		}
		user.Public = map[string]any{"id": user.ID, "email": user.Email, "plan_id": planID.Int64, "plan_name": planName.String, "banned": banned.Int64, "u": up.Int64, "d": down.Int64, "transfer_enable": transfer.Int64, "expired_at": expiry}
		result.Users[user.ID] = user
		if user.Token != "" {
			result.UserIDsByToken[user.Token] = user.ID
		}
		if len(result.Users) > SubscribeGuardSearchMaxUsers {
			return ErrSubscribeGuardSearchLimit
		}
	}
	return rows.Err()
}

// Match evaluates the current first-winning policy independently for each
// member node. Missing UA is unknown, not an invented empty-UA request: an
// earlier possibly matching UA rule makes that member's winner uncertain.
func (snapshot *SubscribeGuardSearchSnapshot) Match(userID int64, ua string, uaKnown bool) ([]SubscribeGuardSearchPolicy, bool) {
	user, ok := snapshot.Users[userID]
	if !ok {
		return nil, false
	}
	subject := cliententry.Subject{UserID: user.ID, Email: strings.ToLower(strings.TrimSpace(user.Email)), PlanID: user.PlanID, RegistrationDays: clientEntrySimulationRegistrationDays(user.CreatedAt, snapshot.EvaluatedAt), UA: ua}
	type candidate struct {
		policy    ClientEntryUserPolicyRecord
		match     SubscribeGuardSearchPolicy
		rank      int64
		uncertain bool
	}
	candidates := []candidate{}
	for _, p := range snapshot.Policies {
		if p.Enabled == 0 || len(p.Members) == 0 {
			continue
		}
		c := candidate{policy: p, rank: p.Sort, match: SubscribeGuardSearchPolicy{ID: p.ID, Name: p.Name, Mode: p.Mode, Action: p.Action, EntryHost: p.EntryHost, ResolveEntryHost: p.ResolveEntryHost, EvaluatedUA: ua}}
		if p.Mode == ClientEntryUserPolicyModeSplit {
			groupID := snapshot.Assignments[userID][p.ID]
			found := false
			for _, g := range p.SplitGroups {
				if g.ID != groupID || !g.IsLeaf || strings.TrimSpace(g.EntryHost) == "" {
					continue
				}
				c.match.GroupID = g.ID
				c.match.GroupName = g.Name
				c.match.EntryHost = g.EntryHost
				if g.ResolveEntryHost != nil {
					c.match.ResolveEntryHost = *g.ResolveEntryHost
				}
				if g.GlobalSort != nil {
					c.rank = *g.GlobalSort
				}
				found = true
				break
			}
			if !found {
				continue
			}
		} else {
			conditions := p.Conditions
			if !uaKnown {
				conditions = make([]cliententry.Condition, 0, len(p.Conditions))
				for _, condition := range p.Conditions {
					if condition.Field == "ua" {
						c.uncertain = true
					} else {
						conditions = append(conditions, condition)
					}
				}
			}
			if !cliententry.MatchAll(conditions, subject) {
				continue
			}
		}
		candidates = append(candidates, c)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].rank != candidates[j].rank {
			return candidates[i].rank < candidates[j].rank
		}
		return candidates[i].policy.ID < candidates[j].policy.ID
	})
	type memberKey struct {
		kind string
		id   int64
	}
	claimed := map[memberKey]bool{}
	matches := []SubscribeGuardSearchPolicy{}
	uncertain := false
	for _, c := range candidates {
		wins := false
		for _, member := range c.policy.Members {
			key := memberKey{strings.ToLower(strings.TrimSpace(member.ServerType)), member.ServerID}
			if claimed[key] {
				continue
			}
			claimed[key] = true
			if c.uncertain {
				uncertain = true
			} else {
				wins = true
			}
		}
		if wins {
			matches = append(matches, c.match)
		}
	}
	return matches, uncertain
}
