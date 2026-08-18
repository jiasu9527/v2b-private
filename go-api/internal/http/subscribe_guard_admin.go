package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"forest/go-api/internal/admin"

	"forest/go-api/internal/config"
	"forest/go-api/internal/nodeapi"
	"forest/go-api/internal/session"
	usersvc "forest/go-api/internal/user"
)

func handleAdminSubscribeGuardStats(w http.ResponseWriter, r *http.Request, cfg config.Config, sessionService session.Service, nodeService nodeapi.Service, userService usersvc.Service, adminService admin.Service) bool {
	disableResponseCache(w)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "请求方式不支持"})
		return true
	}
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	userRankSort, err := parseSubscribeGuardUserRankSort(r.URL.Query().Get("user_rank_sort"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	events := subscribeGuardEventsSnapshot(cfg)
	stats := subscribeGuardStatsSnapshotFromEvents(events)
	enrichSubscribeGuardUserRank(r.Context(), events, stats, userService, adminService, userRankSort)
	if nodeService != nil {
		sensitive, sensitiveErr := nodeService.SensitiveAccessStats(r.Context(), 20)
		if sensitiveErr == nil {
			stats["sensitive"] = sensitive
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": stats})
	return true
}

func handleAdminSubscribeGuardUserSearch(w http.ResponseWriter, r *http.Request, cfg config.Config, sessionService session.Service, adminService admin.Service) bool {
	disableResponseCache(w)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "请求方式不支持"})
		return true
	}
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	keyword := strings.TrimSpace(inputs["keyword"])
	if keyword == "" {
		writeJSON(w, http.StatusOK, map[string]any{"data": []map[string]any{}, "total": int64(0)})
		return true
	}
	if len(keyword) > 255 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "搜索内容过长"})
		return true
	}

	filter := admin.UserFilter{Key: "email", Condition: "模糊", Value: keyword}
	if isDecimalDigits(keyword) {
		userID, parseErr := strconv.ParseInt(keyword, 10, 64)
		if parseErr != nil || userID <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "用户ID格式不正确"})
			return true
		}
		filter = admin.UserFilter{Key: "id", Condition: "=", Value: strconv.FormatInt(userID, 10)}
	}
	result, err := adminService.ListUsers(r.Context(), admin.UserFetchRequest{
		Current:  1,
		PageSize: 20,
		Sort:     "id",
		SortType: "DESC",
		Filters:  []admin.UserFilter{filter},
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	if len(result.Data) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"data": []map[string]any{}, "total": result.Total})
		return true
	}
	latestUAs := subscribeGuardLatestUserSearchUAs(subscribeGuardEventsSnapshot(cfg), result.Data)
	matchRequests := make([]admin.ClientEntryUserPolicyMatchRequest, 0, len(result.Data))
	for _, user := range result.Data {
		userID := anyInt64(user["id"])
		if userID <= 0 {
			continue
		}
		matchRequests = append(matchRequests, admin.ClientEntryUserPolicyMatchRequest{
			UserID: userID,
			UA:     latestUAs[userID],
		})
	}
	matches, err := adminService.MatchClientEntryUserPolicies(r.Context(), matchRequests)
	if err != nil {
		return handleAdminError(w, err)
	}
	matchesByUserID := make(map[int64]*admin.ClientEntryUserPolicyRecord, len(matches))
	for _, match := range matches {
		if match.Found {
			matchesByUserID[match.UserID] = match.Matched
		}
	}
	rows := make([]map[string]any, 0, len(result.Data))
	for _, user := range result.Data {
		userID := anyInt64(user["id"])
		row := subscribeGuardUserSearchPublicRow(user)
		row["entry_policy"] = subscribeGuardEntryPolicyPublicRow(matchesByUserID[userID], latestUAs[userID])
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": rows, "total": result.Total})
	return true
}

// subscribeGuardLatestUserSearchUAs finds the most recent retained request UA
// for each searched user. New records carry user_id directly; canonical tokens
// are only used as a best-effort bridge for older records.
func subscribeGuardLatestUserSearchUAs(events []subscribeGuardEvent, users []map[string]any) map[int64]string {
	wantedUserIDs := make(map[int64]struct{}, len(users))
	userIDsByToken := make(map[string]int64, len(users))
	for _, user := range users {
		userID := anyInt64(user["id"])
		if userID <= 0 {
			continue
		}
		wantedUserIDs[userID] = struct{}{}
		if token := stringValue(user["token"]); token != "" {
			userIDsByToken[token] = userID
		}
	}

	latestUAs := make(map[int64]string, len(wantedUserIDs))
	latestTimes := make(map[int64]int64, len(wantedUserIDs))
	latestIndexes := make(map[int64]int, len(wantedUserIDs))
	for index, event := range events {
		userID := event.UserID
		if userID <= 0 {
			if canonicalToken := strings.TrimSpace(event.CanonicalToken); canonicalToken != "" {
				userID = userIDsByToken[canonicalToken]
			}
			if userID <= 0 {
				userID = userIDsByToken[strings.TrimSpace(event.Token)]
			}
		}
		if _, wanted := wantedUserIDs[userID]; !wanted {
			continue
		}
		latestTime, alreadyFound := latestTimes[userID]
		if alreadyFound && (event.Time < latestTime || (event.Time == latestTime && index < latestIndexes[userID])) {
			continue
		}
		latestUAs[userID] = strings.TrimSpace(event.UA)
		latestTimes[userID] = event.Time
		latestIndexes[userID] = index
	}
	return latestUAs
}

const (
	subscribeGuardUASearchDefaultPageSize int64 = 20
	subscribeGuardUASearchMaxPageSize     int64 = 100
	subscribeGuardUASearchMaxPage         int64 = 100000
)

func handleAdminSubscribeGuardUASearch(w http.ResponseWriter, r *http.Request, cfg config.Config, sessionService session.Service, userService usersvc.Service, adminService admin.Service) bool {
	disableResponseCache(w)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "请求方式不支持"})
		return true
	}
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	keyword := strings.TrimSpace(inputs["keyword"])
	if keyword == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "请输入 UA 关键词"})
		return true
	}
	if len(keyword) > 255 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "UA 关键词过长"})
		return true
	}
	current, pageSize, err := subscribeGuardUASearchPagination(inputs)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	groups, matchedEvents, unresolvedEvents := subscribeGuardUASearchSnapshot(r.Context(), cfg, keyword, userService)
	total := int64(len(groups))
	rows := make([]map[string]any, 0, pageSize)
	start := (current - 1) * pageSize
	if start < total {
		end := start + pageSize
		if end > total {
			end = total
		}
		pageGroups := groups[start:end]
		matchRequests := make([]admin.ClientEntryUserPolicyMatchRequest, 0, len(pageGroups))
		for _, group := range pageGroups {
			matchRequests = append(matchRequests, admin.ClientEntryUserPolicyMatchRequest{
				UserID: group.UserID,
				UA:     subscribeGuardUASearchLatestUA(group),
			})
		}
		matches, matchErr := adminService.MatchClientEntryUserPolicies(r.Context(), matchRequests)
		if matchErr != nil {
			return handleAdminError(w, matchErr)
		}
		matchesByUserID := make(map[int64]*admin.ClientEntryUserPolicyRecord, len(matches))
		for _, match := range matches {
			if match.Found {
				matchesByUserID[match.UserID] = match.Matched
			}
		}
		for _, group := range pageGroups {
			info, infoErr := adminService.GetUserInfoByID(r.Context(), group.UserID)
			if infoErr != nil {
				return handleAdminError(w, infoErr)
			}
			rows = append(rows, subscribeGuardUASearchPublicRow(group, info, matchesByUserID[group.UserID]))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":              rows,
		"total":             total,
		"current":           current,
		"page_size":         pageSize,
		"matched_events":    matchedEvents,
		"unresolved_events": unresolvedEvents,
	})
	return true
}

func subscribeGuardUASearchPagination(inputs map[string]string) (int64, int64, error) {
	current := int64(1)
	if raw := strings.TrimSpace(inputs["current"]); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 || parsed > subscribeGuardUASearchMaxPage {
			return 0, 0, fmt.Errorf("页码无效")
		}
		current = parsed
	}
	pageSize := subscribeGuardUASearchDefaultPageSize
	if raw := strings.TrimSpace(inputs["page_size"]); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 || parsed > subscribeGuardUASearchMaxPageSize {
			return 0, 0, fmt.Errorf("每页数量无效（1-%d）", subscribeGuardUASearchMaxPageSize)
		}
		pageSize = parsed
	}
	return current, pageSize, nil
}

func subscribeGuardUASearchPublicRow(group subscribeGuardUASearchGroup, info map[string]any, matchedPolicy *admin.ClientEntryUserPolicyRecord) map[string]any {
	row := map[string]any{
		"user_id":      group.UserID,
		"count":        group.Count,
		"allowed":      group.Allowed,
		"blocked":      group.Blocked,
		"ip_count":     group.IPCount,
		"ua_count":     group.UACount,
		"first_at":     group.FirstAt,
		"last_at":      group.LastAt,
		"recent":       group.Recent,
		"entry_policy": subscribeGuardEntryPolicyPublicRow(matchedPolicy, subscribeGuardUASearchLatestUA(group)),
	}
	for _, key := range []string{
		"id", "email", "banned", "plan_id", "plan_name", "u", "d", "transfer_enable", "expired_at",
	} {
		if value, ok := info[key]; ok {
			row[key] = value
		}
	}
	row["id"] = group.UserID
	if _, ok := row["email"]; !ok || strings.TrimSpace(fmt.Sprint(row["email"])) == "" {
		row["email"] = fmt.Sprintf("用户 #%d", group.UserID)
	}
	return row
}

func subscribeGuardUASearchLatestUA(group subscribeGuardUASearchGroup) string {
	if len(group.Recent) == 0 {
		return ""
	}
	return strings.TrimSpace(group.Recent[0].UA)
}

func subscribeGuardEntryPolicyPublicRow(policy *admin.ClientEntryUserPolicyRecord, evaluatedUA string) any {
	if policy == nil {
		return nil
	}
	return map[string]any{
		"id":                 policy.ID,
		"name":               strings.TrimSpace(policy.Name),
		"mode":               strings.TrimSpace(policy.Mode),
		"action":             strings.TrimSpace(policy.Action),
		"entry_host":         strings.TrimSpace(policy.EntryHost),
		"resolve_entry_host": policy.ResolveEntryHost,
		"evaluated_ua":       strings.TrimSpace(evaluatedUA),
	}
}

func isDecimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func subscribeGuardUserSearchPublicRow(user map[string]any) map[string]any {
	row := map[string]any{}
	for _, key := range []string{
		"id", "email", "banned", "plan_id", "plan_name", "u", "d", "transfer_enable", "expired_at",
	} {
		if value, ok := user[key]; ok {
			row[key] = value
		}
	}
	return row
}

func handleAdminSubscribeGuardUserDetail(w http.ResponseWriter, r *http.Request, cfg config.Config, sessionService session.Service, adminService admin.Service) bool {
	disableResponseCache(w)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "请求方式不支持"})
		return true
	}
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	userID, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || userID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "用户不存在"})
		return true
	}
	info, err := adminService.GetUserInfoByID(r.Context(), userID)
	if err != nil {
		return handleAdminError(w, err)
	}
	if anyInt64(info["deleted_user"]) != 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "用户不存在"})
		return true
	}
	user := subscribeGuardUserPublicDetail(info)
	stats := subscribeGuardUserStatsSnapshot(cfg, userID, subscribeGuardCanonicalToken(info))
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"user": user, "stats": stats}})
	return true
}

func subscribeGuardUserPublicDetail(info map[string]any) map[string]any {
	result := map[string]any{}
	for _, key := range []string{
		"id", "email", "banned", "plan_id", "group_id", "u", "d", "transfer_enable",
		"device_limit", "expired_at", "t", "last_login_at", "balance", "commission_balance",
		"discount", "speed_limit", "invite_user_id", "invite_code", "remarks", "created_at",
		"updated_at", "is_admin", "is_staff",
	} {
		if value, ok := info[key]; ok {
			result[key] = value
		}
	}
	if inviteUser, ok := info["invite_user"].(map[string]any); ok {
		result["invite_user"] = map[string]any{
			"id":    inviteUser["id"],
			"email": inviteUser["email"],
		}
	}
	return result
}

func subscribeGuardCanonicalToken(info map[string]any) string {
	value, ok := info["token"]
	if !ok || value == nil {
		return ""
	}
	token, _ := value.(string)
	return strings.TrimSpace(token)
}

const (
	subscribeGuardUserRankSortCount   = "count"
	subscribeGuardUserRankSortIPCount = "ip_count"
	subscribeGuardUserRankLimit       = 20
	subscribeGuardUserRankDetailLimit = 100
	// Legacy token-only events require a database lookup before they can be
	// attributed to a user. Cap candidates so attacker-controlled tokens cannot
	// turn one dashboard refresh into an unbounded number of database queries.
	subscribeGuardLegacyUserRankResolveLimit = 40
)

type subscribeGuardUserRankAggregate struct {
	UserID int64
	Token  string
	Count  int64
	UAs    map[string]struct{}
	IPs    map[string]struct{}
}

func parseSubscribeGuardUserRankSort(raw string) (string, error) {
	sortBy := strings.ToLower(strings.TrimSpace(raw))
	if sortBy == "" {
		return subscribeGuardUserRankSortCount, nil
	}
	if sortBy != subscribeGuardUserRankSortCount && sortBy != subscribeGuardUserRankSortIPCount {
		return "", fmt.Errorf("用户排行榜排序方式无效")
	}
	return sortBy, nil
}

// subscribeGuardUserRankItems scans every retained event and only applies the
// user limit after aggregation and sorting. Legacy token-only candidates are
// separately pre-ranked and lookup-capped to keep dashboard refresh bounded.
func subscribeGuardUserRankItems(ctx context.Context, cfg config.Config, userService usersvc.Service, sortBy string, limit int) []map[string]any {
	return subscribeGuardUserRankItemsFromEvents(ctx, subscribeGuardEventsSnapshot(cfg), userService, sortBy, limit)
}

func subscribeGuardUserRankItemsFromEvents(ctx context.Context, events []subscribeGuardEvent, userService usersvc.Service, sortBy string, limit int) []map[string]any {
	if sortBy != subscribeGuardUserRankSortIPCount {
		sortBy = subscribeGuardUserRankSortCount
	}
	aggregates := make(map[int64]*subscribeGuardUserRankAggregate)
	legacyAggregates := make(map[string]*subscribeGuardUserRankAggregate)
	for _, event := range events {
		if event.UserID > 0 {
			aggregate := ensureSubscribeGuardUserRankAggregate(aggregates, event.UserID)
			addSubscribeGuardUserRankEvent(aggregate, event)
			continue
		}

		token := strings.TrimSpace(event.CanonicalToken)
		if token == "" {
			token = strings.TrimSpace(event.Token)
		}
		if token == "" {
			continue
		}
		aggregate := legacyAggregates[token]
		if aggregate == nil {
			aggregate = &subscribeGuardUserRankAggregate{
				Token: token,
				UAs:   make(map[string]struct{}),
				IPs:   make(map[string]struct{}),
			}
			legacyAggregates[token] = aggregate
		}
		addSubscribeGuardUserRankEvent(aggregate, event)
	}

	legacyCandidates := make([]*subscribeGuardUserRankAggregate, 0, len(legacyAggregates))
	for _, aggregate := range legacyAggregates {
		legacyCandidates = append(legacyCandidates, aggregate)
	}
	sort.Slice(legacyCandidates, func(i, j int) bool {
		if subscribeGuardUserRankAggregateLess(legacyCandidates[i], legacyCandidates[j], sortBy) {
			return true
		}
		if subscribeGuardUserRankAggregateLess(legacyCandidates[j], legacyCandidates[i], sortBy) {
			return false
		}
		return legacyCandidates[i].Token < legacyCandidates[j].Token
	})
	if len(legacyCandidates) > subscribeGuardLegacyUserRankResolveLimit {
		legacyCandidates = legacyCandidates[:subscribeGuardLegacyUserRankResolveLimit]
	}
	if userService != nil {
		for _, legacy := range legacyCandidates {
			userID, err := peekSubscribeGuardLegacyUserID(ctx, userService, legacy.Token)
			if err != nil || userID <= 0 {
				continue
			}
			mergeSubscribeGuardUserRankAggregate(ensureSubscribeGuardUserRankAggregate(aggregates, userID), legacy)
		}
	}

	items := make([]*subscribeGuardUserRankAggregate, 0, len(aggregates))
	for _, aggregate := range aggregates {
		items = append(items, aggregate)
	}
	sort.Slice(items, func(i, j int) bool {
		if subscribeGuardUserRankAggregateLess(items[i], items[j], sortBy) {
			return true
		}
		if subscribeGuardUserRankAggregateLess(items[j], items[i], sortBy) {
			return false
		}
		return items[i].UserID < items[j].UserID
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}

	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		uaValues := limitedSortedSubscribeGuardSet(item.UAs, subscribeGuardUserRankDetailLimit)
		ipValues := limitedSortedSubscribeGuardSet(item.IPs, subscribeGuardUserRankDetailLimit)
		result = append(result, map[string]any{
			"user_id":  item.UserID,
			"count":    item.Count,
			"ua_count": int64(len(item.UAs)),
			"uas":      uaValues,
			"ip_count": int64(len(item.IPs)),
			"ips":      ipValues,
		})
	}
	return result
}

func limitedSortedSubscribeGuardSet(values map[string]struct{}, limit int) []string {
	result := sortedSubscribeGuardSet(values)
	if limit > 0 && len(result) > limit {
		return result[:limit]
	}
	return result
}

func ensureSubscribeGuardUserRankAggregate(aggregates map[int64]*subscribeGuardUserRankAggregate, userID int64) *subscribeGuardUserRankAggregate {
	aggregate := aggregates[userID]
	if aggregate == nil {
		aggregate = &subscribeGuardUserRankAggregate{
			UserID: userID,
			UAs:    make(map[string]struct{}),
			IPs:    make(map[string]struct{}),
		}
		aggregates[userID] = aggregate
	}
	return aggregate
}

func addSubscribeGuardUserRankEvent(aggregate *subscribeGuardUserRankAggregate, event subscribeGuardEvent) {
	aggregate.Count++
	if event.UA != "" {
		aggregate.UAs[event.UA] = struct{}{}
	}
	if event.IP != "" {
		aggregate.IPs[event.IP] = struct{}{}
	}
}

func mergeSubscribeGuardUserRankAggregate(target, source *subscribeGuardUserRankAggregate) {
	target.Count += source.Count
	for ua := range source.UAs {
		target.UAs[ua] = struct{}{}
	}
	for ip := range source.IPs {
		target.IPs[ip] = struct{}{}
	}
}

func subscribeGuardUserRankAggregateLess(left, right *subscribeGuardUserRankAggregate, sortBy string) bool {
	leftIPCount, rightIPCount := len(left.IPs), len(right.IPs)
	if sortBy == subscribeGuardUserRankSortIPCount {
		if leftIPCount != rightIPCount {
			return leftIPCount > rightIPCount
		}
		return left.Count > right.Count
	}
	if left.Count != right.Count {
		return left.Count > right.Count
	}
	return leftIPCount > rightIPCount
}

func enrichSubscribeGuardUserRank(ctx context.Context, events []subscribeGuardEvent, stats map[string]any, userService usersvc.Service, adminService admin.Service, sortBy string) {
	stats["user_rank_sort"] = sortBy
	result := make([]map[string]any, 0, subscribeGuardUserRankLimit)
	if userService != nil {
		items := subscribeGuardUserRankItemsFromEvents(ctx, events, userService, sortBy, subscribeGuardUserRankLimit)
		for _, item := range items {
			userID := anyInt64(item["user_id"])
			if userID <= 0 {
				continue
			}
			if next, ok := subscribeGuardUserRankRow(ctx, userID, item, userService, adminService); ok {
				result = append(result, next)
			}
		}
	}
	stats["top_subscribe_users"] = result
}

func peekSubscribeGuardLegacyUserID(ctx context.Context, userService usersvc.Service, token string) (int64, error) {
	if resolver, ok := userService.(subscribeGuardUserResolver); ok {
		return resolver.PeekClientUserID(ctx, token)
	}
	return userService.ResolveClientUserID(ctx, token)
}

func subscribeGuardUserRankRow(ctx context.Context, userID int64, item map[string]any, userService usersvc.Service, adminService admin.Service) (map[string]any, bool) {
	subscribe, err := userService.Subscribe(ctx, userID)
	if err != nil {
		return nil, false
	}
	banned := int64(0)
	if adminService != nil {
		if info, err := adminService.GetUserInfoByID(ctx, userID); err == nil {
			banned = anyInt64(info["banned"])
		}
	}
	return map[string]any{
		"user_id":  userID,
		"email":    subscribe.Email,
		"count":    item["count"],
		"ua_count": item["ua_count"],
		"uas":      item["uas"],
		"ip_count": item["ip_count"],
		"ips":      item["ips"],
		"banned":   banned,
	}, true
}

func handleAdminSubscribeGuardSetUserBanned(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "用户不存在"})
		return true
	}
	banned, err := strconv.ParseInt(strings.TrimSpace(inputs["banned"]), 10, 64)
	if err != nil || (banned != 0 && banned != 1) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "账号状态格式不正确"})
		return true
	}

	updated, err := adminService.SetUserBanned(r.Context(), id, banned)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": updated})
	return true
}
