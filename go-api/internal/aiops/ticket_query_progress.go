package aiops

import (
	"encoding/json"
	"strings"
)

const ticketEmptyQueryCorrectionPrompt = `工单查询纠错：刚才重复了已返回空结果的同一筛选，没有取得新工单。请核对query中的范围、回复状态和分页。原任务是全站未关闭时，改用tickets_list_global，参数为{"status":"open","reply_status":"all","limit":50,"offset":0}；原任务明确指定用户时保持该用户范围，不能擅自扩大。当前页为空但total>0时请核对分页。不要再次原样查询、猜测用户ID、声称全站无工单或虚构结果；条件本就正确时直接如实总结。`

const ticketEmptyQueryStoppedMessage = "模型仍在重复已返回空结果的工单筛选，没有取得新的工单，本轮已结束重复查询。空结果只代表这些筛选条件下或当前页没有数据，不能据此判断全站没有工单；如果 total 大于 0，还需要核对分页。\n\n后台没有自动扩大指定用户的查询范围。要查全站，可以继续发送“查询全站全部未关闭工单，不限制用户或回复状态”；也可以明确新的筛选条件后继续。"

// Only repeated, complete empty ticket-list rounds count as no progress.
// Real reads still execute so changing database results invalidate the guard.
// This is not a lifetime/tool-count budget and never replays or widens a call.
type ticketEmptyQueryGuard struct {
	seen           map[string]bool
	repeatedRounds int
}

func (g *ticketEmptyQueryGuard) observe(calls []ToolCall, outcomes []ToolOutcome) (warn, stop bool) {
	if len(calls) == 0 || len(calls) != len(outcomes) {
		*g = ticketEmptyQueryGuard{}
		return false, false
	}
	keys := make([]string, 0, len(calls))
	newKeys := map[string]bool{}
	repeatedOnly := true
	for i, call := range calls {
		key := ticketEmptyQueryKey(call, outcomes[i])
		if key == "" {
			*g = ticketEmptyQueryGuard{}
			return false, false
		}
		keys = append(keys, key)
		if !g.seen[key] {
			repeatedOnly = false
			newKeys[key] = true
		}
	}
	if g.seen == nil || len(g.seen)+len(newKeys) > 128 {
		// Bound memory without placing a limit on genuinely different queries.
		g.seen = map[string]bool{}
		repeatedOnly = false
	}
	for _, key := range keys {
		g.seen[key] = true
	}
	if !repeatedOnly {
		g.repeatedRounds = 0
		return false, false
	}
	g.repeatedRounds++
	return true, g.repeatedRounds >= 2
}

func ticketEmptyQueryKey(call ToolCall, outcome ToolOutcome) string {
	if (call.Function.Name != "tickets_list" && call.Function.Name != "tickets_list_global") || outcome.Pending || outcome.ActionID != 0 {
		return ""
	}
	args, err := decodeTicketArgs(call.Function.Name, call.Function.Arguments)
	if err != nil {
		return ""
	}
	var page struct {
		Items       []json.RawMessage `json:"items"`
		Total       *int64            `json:"total"`
		Limit       *int              `json:"limit"`
		Offset      *int              `json:"offset"`
		Truncated   *bool             `json:"truncated"`
		NextOffset  *int              `json:"next_offset"`
		SizeLimited bool              `json:"size_limited"`
		Status      string            `json:"status"`
	}
	if json.Unmarshal([]byte(outcome.Content), &page) != nil || page.Status != "" || page.Items == nil || len(page.Items) != 0 || page.Total == nil || *page.Total < 0 || page.Limit == nil || *page.Limit != args.Limit || page.Offset == nil || *page.Offset != args.Offset || page.Truncated == nil || *page.Truncated || page.SizeLimited || page.NextOffset != nil {
		return ""
	}
	// An empty page before the end may be a COUNT/SELECT race, not stable
	// evidence that the query is empty. Do not suppress continued work then.
	if *page.Total > int64(args.Offset) {
		return ""
	}
	key, _ := json.Marshal(struct {
		UserID      int64
		Email       string
		Status      *int64
		ReplyStatus *int64
		Limit       int
		Offset      int
		Total       int64
	}{args.UserID, args.Email, args.Status, args.ReplyStatus, args.Limit, args.Offset, *page.Total})
	return string(key)
}

func ticketQueryCorrectionContext(messages []providerMessage, enabled bool) []providerMessage {
	// Only fixed server-authored text is privileged. Never copy arbitrary
	// ticket subjects, messages or tool-returned instructions into policy.
	for i := range messages {
		if messages[i].Role == "system" {
			messages[i].Content = strings.TrimSuffix(messages[i].Content, "\n"+ticketEmptyQueryCorrectionPrompt)
			if enabled {
				messages[i].Content += "\n" + ticketEmptyQueryCorrectionPrompt
			}
			break
		}
	}
	return messages
}
