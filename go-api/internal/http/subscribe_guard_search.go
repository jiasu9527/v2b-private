package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
	usersvc "forest/go-api/internal/user"
)

const (
	subscribeGuardSearchMaxLogBytes     int64 = 512 << 20
	subscribeGuardSearchMaxEvents             = 500000
	subscribeGuardSearchMaxEvaluations        = 100000
	subscribeGuardSearchMaxLegacyTokens       = 1000
)

var (
	errSubscribeGuardSearchLimit   = errors.New("候选订阅记录过多，请缩小 UA、拉取时间或请求结果范围；仅增加账号条件不能缩小日志扫描范围")
	errSubscribeGuardSearchLogSize = errors.New("订阅日志文件超过 512 MiB，请先调整日志保留期并等待清理；增加筛选条件不会减少原始日志文件大小")
)

type subscribeGuardSearchLoader interface {
	LoadSubscribeGuardSearchSnapshot(context.Context, admin.SubscribeGuardSearchLoadRequest) (*admin.SubscribeGuardSearchSnapshot, error)
}

type subscribeGuardSearchQuery struct {
	Account, AccountMatch, UA, UAMatch, Result, Sort     string
	PolicyID, GroupID, StartAt, EndAt, Current, PageSize int64
}

func parseSubscribeGuardSearchQuery(values url.Values) (subscribeGuardSearchQuery, error) {
	q := subscribeGuardSearchQuery{Account: strings.TrimSpace(values.Get("account")), UA: strings.TrimSpace(values.Get("ua")), AccountMatch: values.Get("account_match"), UAMatch: values.Get("ua_match"), Result: values.Get("result"), Sort: values.Get("sort")}
	if len(q.Account) > 255 || len(q.UA) > 1024 {
		return q, errors.New("搜索内容过长")
	}
	if q.AccountMatch == "" {
		q.AccountMatch = "contains"
	}
	if q.AccountMatch != "contains" && q.AccountMatch != "exact" {
		return q, errors.New("邮箱匹配方式无效")
	}
	if q.UAMatch == "" {
		q.UAMatch = "contains"
	}
	if q.UAMatch != "contains" && q.UAMatch != "equals" && q.UAMatch != "not_contains" {
		return q, errors.New("UA 匹配方式无效")
	}
	if q.Result == "" {
		q.Result = "all"
	}
	if q.Result != "all" && q.Result != "allowed" && q.Result != "blocked" {
		return q, errors.New("请求结果筛选无效")
	}
	if q.Sort == "" {
		q.Sort = "last_at"
	}
	if q.Sort != "last_at" && q.Sort != "count" {
		return q, errors.New("排序方式无效")
	}
	for _, item := range []struct {
		key    string
		target *int64
	}{{"policy_id", &q.PolicyID}, {"group_id", &q.GroupID}, {"start_at", &q.StartAt}, {"end_at", &q.EndAt}} {
		raw := values.Get(item.key)
		if raw == "" {
			continue
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			return q, fmt.Errorf("%s 格式无效", item.key)
		}
		*item.target = n
	}
	if q.GroupID > 0 && q.PolicyID == 0 {
		return q, errors.New("请先选择入口规则")
	}
	if q.StartAt > 0 && q.EndAt > 0 && q.StartAt >= q.EndAt {
		return q, errors.New("结束时间必须晚于开始时间")
	}
	if isDecimalDigits(q.Account) {
		id, err := strconv.ParseInt(q.Account, 10, 64)
		if err != nil || id <= 0 {
			return q, errors.New("用户 ID 格式无效")
		}
		q.Account = strconv.FormatInt(id, 10)
	}
	var err error
	q.Current, q.PageSize, err = subscribeGuardUASearchPagination(map[string]string{"current": values.Get("current"), "page_size": values.Get("page_size")})
	return q, err
}

func (q subscribeGuardSearchQuery) includeWithoutEvents() bool {
	return (q.Account != "" || q.PolicyID > 0) && q.UA == "" && q.StartAt == 0 && q.EndAt == 0 && q.Result == "all"
}

func (q subscribeGuardSearchQuery) matchesEvent(event subscribeGuardEvent) bool {
	if q.StartAt > 0 && event.Time < q.StartAt {
		return false
	}
	if q.EndAt > 0 && event.Time >= q.EndAt {
		return false
	}
	if q.Result == "blocked" && !event.Blocked || q.Result == "allowed" && event.Blocked {
		return false
	}
	if q.UA != "" {
		haystack, needle := strings.ToLower(strings.TrimSpace(event.UA)), strings.ToLower(q.UA)
		switch q.UAMatch {
		case "equals":
			if haystack != needle {
				return false
			}
		case "not_contains":
			if strings.Contains(haystack, needle) {
				return false
			}
		default:
			if !strings.Contains(haystack, needle) {
				return false
			}
		}
	}
	return true
}

func handleAdminSubscribeGuardSearch(w http.ResponseWriter, r *http.Request, cfg config.Config, sessionService session.Service, userService usersvc.Service, adminService admin.Service) bool {
	disableResponseCache(w)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "请求方式不支持"})
		return true
	}
	identity, ok := authenticateRequest(w, r, sessionService, true)
	if !ok {
		return true
	}
	// Defense in depth for session implementations that do not enforce the flag.
	if identity == nil || identity.IsAdmin == 0 {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "仅管理员可查询订阅防护"})
		return true
	}
	query, err := parseSubscribeGuardSearchQuery(r.URL.Query())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	loader, ok := adminService.(subscribeGuardSearchLoader)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "订阅组合筛选服务不可用"})
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	events, availableFrom, availableTo, warnings, err := readSubscribeGuardSearchEvents(ctx, cfg, query, time.Now())
	if err != nil {
		return subscribeGuardSearchError(w, err)
	}
	ids, tokens, err := subscribeGuardSearchEventIdentities(ctx, events, userService)
	if err != nil {
		return subscribeGuardSearchError(w, err)
	}
	snapshot, err := loader.LoadSubscribeGuardSearchSnapshot(ctx, admin.SubscribeGuardSearchLoadRequest{Account: query.Account, AccountMatch: query.AccountMatch, UserIDs: ids, Tokens: tokens, IncludeWithoutEvents: query.includeWithoutEvents()})
	if err != nil {
		return subscribeGuardSearchError(w, err)
	}
	if err := validateSubscribeGuardSearchPolicy(query, snapshot); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	result, err := aggregateSubscribeGuardSearch(ctx, query, events, snapshot)
	if err != nil {
		return subscribeGuardSearchError(w, err)
	}
	result["log_keep_days"] = cfg.SubscribeGuardLogKeepDays
	result["available_from"] = availableFrom
	result["available_to"] = availableTo
	result["rule_basis"] = "current"
	if !cfg.SubscribeGuardEnable {
		warnings = append(warnings, "订阅防护当前未启用，未启用期间没有拉取记录。")
	}
	if query.StartAt > 0 && cfg.SubscribeGuardLogKeepDays > 0 && query.StartAt < time.Now().Add(-time.Duration(cfg.SubscribeGuardLogKeepDays)*24*time.Hour).Unix() {
		warnings = append(warnings, "所选时间早于日志保留范围，已清理的历史记录无法查询。")
	}
	if anyInt64(result["unresolved_events"]) > 0 {
		warnings = append(warnings, "部分符合事件条件的记录无法关联当前用户（例如旧 Token 已失效或账号已删除），未计入用户结果。")
	}
	if result["uncertain_no_event_users"].(int64) > 0 {
		warnings = append(warnings, "部分无拉取记录用户缺少实际 UA，无法确定 UA 条件及其优先级；仅展示可确定命中的规则。")
	}
	delete(result, "uncertain_no_event_users")
	result["warnings"] = warnings
	writeJSON(w, http.StatusOK, result)
	return true
}

func subscribeGuardSearchError(w http.ResponseWriter, err error) bool {
	message := "订阅筛选读取失败，未返回不完整结果；请稍后重试并检查日志文件或数据库状态"
	if errors.Is(err, errSubscribeGuardSearchLimit) || errors.Is(err, errSubscribeGuardSearchLogSize) || errors.Is(err, admin.ErrSubscribeGuardSearchLimit) {
		message = err.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		message = "筛选超时或已取消，请缩小查询范围"
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": message})
	return true
}

// Read a fixed-size disk snapshot. Never call the old cleanup/snapshot helpers:
// they may mutate the file or silently replace it with the last 1000 RAM events.
func readSubscribeGuardSearchEvents(ctx context.Context, cfg config.Config, q subscribeGuardSearchQuery, now time.Time) ([]subscribeGuardEvent, int64, int64, []string, error) {
	warnings := []string{}
	events := []subscribeGuardEvent{}
	if err := ctx.Err(); err != nil {
		return nil, 0, 0, nil, err
	}
	file, err := os.Open(subscribeGuardLogPath(cfg))
	if errors.Is(err, os.ErrNotExist) {
		return events, 0, 0, append(warnings, "尚无可读取的订阅日志文件，未使用内存记录代替完整日志。"), nil
	}
	if err != nil {
		return nil, 0, 0, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, 0, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, 0, nil, errors.New("invalid log file")
	}
	if info.Size() > subscribeGuardSearchMaxLogBytes {
		return nil, 0, 0, nil, errSubscribeGuardSearchLogSize
	}
	scanner := bufio.NewScanner(io.NewSectionReader(file, 0, info.Size()))
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	var from, to, cutoff int64
	if cfg.SubscribeGuardLogKeepDays > 0 {
		cutoff = now.Add(-time.Duration(cfg.SubscribeGuardLogKeepDays) * 24 * time.Hour).Unix()
	}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, 0, 0, nil, err
		}
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var event subscribeGuardEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, 0, 0, nil, fmt.Errorf("invalid subscribe log: %w", err)
		}
		if event.Time < cutoff {
			continue
		}
		if from == 0 || event.Time < from {
			from = event.Time
		}
		if event.Time > to {
			to = event.Time
		}
		if !q.matchesEvent(event) {
			continue
		}
		events = append(events, event)
		if len(events) > subscribeGuardSearchMaxEvents {
			return nil, 0, 0, nil, errSubscribeGuardSearchLimit
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, 0, nil, err
	}
	after, err := file.Stat()
	if err != nil {
		return nil, 0, 0, nil, err
	}
	if after.Size() < info.Size() || (after.Size() == info.Size() && !after.ModTime().Equal(info.ModTime())) {
		return nil, 0, 0, nil, errors.New("log changed during search")
	}
	return events, from, to, warnings, nil
}

// Canonical tokens are joined in bulk. Legacy transient tokens use only the
// read-only peek capability, with a request cache and an explicit resource cap.
func subscribeGuardSearchEventIdentities(ctx context.Context, events []subscribeGuardEvent, userService usersvc.Service) ([]int64, []string, error) {
	ids := map[int64]bool{}
	tokens := map[string]bool{}
	legacy := map[string]int64{}
	resolver, _ := userService.(subscribeGuardUserResolver)
	for i := range events {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		event := &events[i]
		if event.UserID > 0 {
			ids[event.UserID] = true
			if len(ids)+len(tokens) > admin.SubscribeGuardSearchMaxUsers*2 {
				return nil, nil, errSubscribeGuardSearchLimit
			}
			continue
		}
		token := strings.TrimSpace(event.CanonicalToken)
		if token == "" {
			token = strings.TrimSpace(event.Token)
		}
		if token == "" {
			continue
		}
		tokens[token] = true
		if event.CanonicalToken == "" && resolver != nil {
			id, seen := legacy[token]
			if !seen {
				if len(legacy) >= subscribeGuardSearchMaxLegacyTokens {
					return nil, nil, errSubscribeGuardSearchLimit
				}
				var err error
				id, err = resolver.PeekClientUserID(ctx, token)
				if err != nil && !errors.Is(err, usersvc.ErrClientTokenInvalid) && !errors.Is(err, usersvc.ErrNotFound) {
					return nil, nil, err
				}
				legacy[token] = id
			}
			if id > 0 {
				event.UserID = id
				ids[id] = true
			}
		}
		if len(ids)+len(tokens) > admin.SubscribeGuardSearchMaxUsers*2 {
			return nil, nil, errSubscribeGuardSearchLimit
		}
	}
	userIDs := make([]int64, 0, len(ids))
	for id := range ids {
		userIDs = append(userIDs, id)
	}
	sort.Slice(userIDs, func(i, j int) bool { return userIDs[i] < userIDs[j] })
	tokenValues := make([]string, 0, len(tokens))
	for token := range tokens {
		tokenValues = append(tokenValues, token)
	}
	sort.Strings(tokenValues)
	return userIDs, tokenValues, nil
}

func validateSubscribeGuardSearchPolicy(q subscribeGuardSearchQuery, snapshot *admin.SubscribeGuardSearchSnapshot) error {
	if q.PolicyID == 0 {
		return nil
	}
	for _, p := range snapshot.Policies {
		if p.ID != q.PolicyID {
			continue
		}
		if q.GroupID == 0 {
			return nil
		}
		for _, g := range p.SplitGroups {
			if g.ID == q.GroupID && g.IsLeaf {
				return nil
			}
		}
		return errors.New("所选子组不属于该规则，或不是可分配的叶子分组")
	}
	return errors.New("入口规则不存在，请刷新规则列表")
}

func subscribeGuardSearchPolicyMatches(q subscribeGuardSearchQuery, matches []admin.SubscribeGuardSearchPolicy) bool {
	if q.PolicyID == 0 {
		return true
	}
	for _, match := range matches {
		if match.ID == q.PolicyID && (q.GroupID == 0 || match.GroupID == q.GroupID) {
			return true
		}
	}
	return false
}

type subscribeGuardSearchAggregate struct {
	group      subscribeGuardUASearchGroup
	policies   []admin.SubscribeGuardSearchPolicy
	policyKeys map[string]bool
	uncertain  bool
}

func aggregateSubscribeGuardSearch(ctx context.Context, q subscribeGuardSearchQuery, events []subscribeGuardEvent, snapshot *admin.SubscribeGuardSearchSnapshot) (map[string]any, error) {
	groups := map[int64]*subscribeGuardSearchAggregate{}
	seenEvents := map[int64]bool{}
	type evaluationKey struct {
		userID int64
		ua     string
	}
	cache := map[evaluationKey][]admin.SubscribeGuardSearchPolicy{}
	var matched, unresolved, uncertain int64
	ensure := func(id int64) *subscribeGuardSearchAggregate {
		g := groups[id]
		if g == nil {
			g = &subscribeGuardSearchAggregate{group: subscribeGuardUASearchGroup{UserID: id, Recent: []subscribeGuardUserEvent{}, ips: map[string]struct{}{}, uas: map[string]struct{}{}}, policies: []admin.SubscribeGuardSearchPolicy{}, policyKeys: map[string]bool{}}
			groups[id] = g
		}
		return g
	}
	addPolicies := func(g *subscribeGuardSearchAggregate, matches []admin.SubscribeGuardSearchPolicy) {
		for _, p := range matches {
			key := fmt.Sprintf("%d/%d", p.ID, p.GroupID)
			if g.policyKeys[key] {
				continue
			}
			g.policyKeys[key] = true
			g.policies = append(g.policies, p)
		}
	}
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !q.matchesEvent(event) {
			continue
		}
		id := event.UserID
		if id <= 0 {
			token := strings.TrimSpace(event.CanonicalToken)
			if token == "" {
				token = strings.TrimSpace(event.Token)
			}
			id = snapshot.UserIDsByToken[token]
		}
		user, exists := snapshot.Users[id]
		if !exists {
			// Account searches cannot attribute an unknown token to the requested
			// account. Do not count unrelated filtered users as unresolved.
			if q.Account == "" {
				unresolved++
			}
			continue
		}
		if !subscribeGuardSearchAccountMatches(q, user) {
			continue
		}
		seenEvents[id] = true
		key := evaluationKey{id, event.UA}
		matches, cached := cache[key]
		if !cached {
			if len(cache) >= subscribeGuardSearchMaxEvaluations {
				return nil, errSubscribeGuardSearchLimit
			}
			matches, _ = snapshot.Match(id, event.UA, true)
			cache[key] = matches
		}
		if !subscribeGuardSearchPolicyMatches(q, matches) {
			continue
		}
		matched++
		g := ensure(id)
		addSubscribeGuardSearchEvent(&g.group, event)
		addPolicies(g, matches)
	}
	if q.includeWithoutEvents() {
		ids := make([]int64, 0, len(snapshot.Users))
		for id := range snapshot.Users {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if seenEvents[id] || !subscribeGuardSearchAccountMatches(q, snapshot.Users[id]) {
				continue
			}
			matches, unknown := snapshot.Match(id, "", false)
			if unknown {
				uncertain++
			}
			if !subscribeGuardSearchPolicyMatches(q, matches) {
				continue
			}
			g := ensure(id)
			g.uncertain = unknown
			addPolicies(g, matches)
		}
	}
	ordered := make([]*subscribeGuardSearchAggregate, 0, len(groups))
	for _, g := range groups {
		ordered = append(ordered, g)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i].group, ordered[j].group
		if q.Sort == "count" && a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.LastAt != b.LastAt {
			return a.LastAt > b.LastAt
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.UserID < b.UserID
	})
	total := int64(len(ordered))
	start := (q.Current - 1) * q.PageSize
	if start > total {
		start = total
	}
	end := start + q.PageSize
	if end > total {
		end = total
	}
	rows := make([]map[string]any, 0, end-start)
	for _, g := range ordered[start:end] {
		g.group.IPCount = int64(len(g.group.ips))
		g.group.UACount = int64(len(g.group.uas))
		row := subscribeGuardUASearchPublicRow(g.group, snapshot.Users[g.group.UserID].Public, nil)
		row["entry_policies"] = g.policies
		row["entry_policy_uncertain"] = g.uncertain
		if len(g.policies) > 0 {
			row["entry_policy"] = g.policies[0]
		}
		rows = append(rows, row)
	}
	return map[string]any{"data": rows, "total": total, "current": q.Current, "page_size": q.PageSize, "matched_events": matched, "unresolved_events": unresolved, "uncertain_no_event_users": uncertain}, nil
}

func subscribeGuardSearchAccountMatches(q subscribeGuardSearchQuery, user admin.SubscribeGuardSearchUser) bool {
	if q.Account == "" {
		return true
	}
	if isDecimalDigits(q.Account) {
		return strconv.FormatInt(user.ID, 10) == q.Account
	}
	if q.AccountMatch == "exact" {
		return strings.EqualFold(user.Email, q.Account)
	}
	return strings.Contains(strings.ToLower(user.Email), strings.ToLower(q.Account))
}

func addSubscribeGuardSearchEvent(g *subscribeGuardUASearchGroup, e subscribeGuardEvent) {
	g.Count++
	if e.Blocked {
		g.Blocked++
	} else {
		g.Allowed++
	}
	if g.Count == 1 || e.Time < g.FirstAt {
		g.FirstAt = e.Time
	}
	if g.Count == 1 || e.Time > g.LastAt {
		g.LastAt = e.Time
	}
	if e.IP != "" {
		g.ips[e.IP] = struct{}{}
	}
	if e.UA != "" {
		g.uas[e.UA] = struct{}{}
	}
	g.Recent = append(g.Recent, subscribeGuardUserEvent{Time: e.Time, IP: e.IP, UA: e.UA, Status: e.Status, Reason: e.Reason, Blocked: e.Blocked})
	sort.SliceStable(g.Recent, func(i, j int) bool { return g.Recent[i].Time > g.Recent[j].Time })
	if len(g.Recent) > subscribeGuardUASearchRecentLimit {
		g.Recent = g.Recent[:subscribeGuardUASearchRecentLimit]
	}
}
