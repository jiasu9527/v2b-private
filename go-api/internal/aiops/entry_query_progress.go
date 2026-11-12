package aiops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
)

const entryQueryCorrectionPrompt = `入口查询纠错：相同参数已重复返回空检测目标或参数/版本错误，没有取得新证据。entry_probe_targets 的 policy_id 是规则 ID，target_id 是该接口实际返回的检测目标 ID，两者不是同一命名空间；不知道 target_id 时省略，只按已核实的 policy_id 查询，不能猜成 1 或复制规则/节点/分组 ID。policy_id 与 target_id 同时填写取交集。targets.total=0 且 truncated=false 时不要重复原样查询，也不能据此断言入口正常或故障；可能是筛选不匹配或未配置监测。host_references 首次查询省略 revision，后续分页仅使用该接口返回的拓扑 revision，不能填 entry_probe_targets 的数字 revision；冲突时回到首读重新核对。保持用户原任务、邮箱、目标入口和必要条件，不擅自扩大业务范围，不自动发新探测或修改配置；仍无法取得证据时，如实总结已确认信息和缺口。`

const entryQueryStoppedMessage = "入口查询仍在重复相同筛选的空结果或相同参数／版本错误，没有取得新的入口状态证据，本轮已结束重复查询，之前的查询结果和已执行操作记录均已保留。\n\n空检测目标不等于入口正常或故障：需要核对规则 ID 与检测目标 ID 是否混用、目标是否配置监测。入口引用查询的 revision 必须来自该接口本身，不能沿用探测接口的数字版本。当前证据不足以确认入口健康；后台没有因此自动发起检测、修改入口或扩大用户范围。"

// Only complete no-progress rounds for the two known problematic reads count.
// Reads still execute, so a changed topology/result or corrected query resets
// the consecutive-repeat budget. Pending actions and unrelated work are never
// treated as polling loops. The zero value is private to one user turn.
type entryQueryProgressGuard struct {
	seen           map[string]bool
	repeatedRounds int
}

func (g *entryQueryProgressGuard) observe(calls []ToolCall, outcomes []ToolOutcome) (warn, stop bool) {
	if len(calls) == 0 || len(calls) != len(outcomes) {
		*g = entryQueryProgressGuard{}
		return false, false
	}
	keys := make([]string, 0, len(calls))
	newKeys := map[string]bool{}
	repeatedOnly := true
	for i, call := range calls {
		key := entryQueryNoProgressKey(call, outcomes[i])
		if key == "" {
			*g = entryQueryProgressGuard{}
			return false, false
		}
		keys = append(keys, key)
		if !g.seen[key] {
			repeatedOnly = false
			newKeys[key] = true
		}
	}
	if g.seen == nil || len(g.seen)+len(newKeys) > 128 {
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
	return true, g.repeatedRounds >= 4
}

func entryQueryNoProgressKey(call ToolCall, outcome ToolOutcome) string {
	if outcome.Pending || outcome.ActionID != 0 || !oneOf(call.Function.Name, "entry_probe_targets", "host_references") {
		return ""
	}
	var arguments any
	var evidence any
	if call.Function.Name == "entry_probe_targets" {
		args, err := decodeProbeToolArgs(call)
		if err != nil {
			return ""
		}
		var result struct {
			Status   string `json:"status"`
			Revision *int64 `json:"revision"`
			Targets  struct {
				Items       []json.RawMessage `json:"items"`
				Total       *int64            `json:"total"`
				Limit       *int              `json:"limit"`
				Offset      *int              `json:"offset"`
				Truncated   *bool             `json:"truncated"`
				NextOffset  *int              `json:"next_offset"`
				SizeLimited bool              `json:"size_limited"`
			} `json:"targets"`
		}
		if json.Unmarshal([]byte(outcome.Content), &result) != nil || result.Status != "" || result.Revision == nil {
			return ""
		}
		page := result.Targets
		if page.Items == nil || len(page.Items) != 0 || page.Total == nil || *page.Total != 0 || page.Limit == nil || *page.Limit != args.Limit || page.Offset == nil || *page.Offset != args.Offset || page.Truncated == nil || *page.Truncated || page.NextOffset != nil || page.SizeLimited {
			return ""
		}
		arguments = args
		// Probe heartbeats change even when this exact filter keeps yielding
		// no targets. They are not evidence of progress on the user's query.
		evidence = *result.Revision
	} else {
		var result struct {
			Status string `json:"status"`
			Code   string `json:"code"`
		}
		if json.Unmarshal([]byte(outcome.Content), &result) != nil || result.Status != "error" || !oneOf(result.Code, "invalid_arguments", "conflict") {
			return ""
		}
		// Invalid revision values are precisely what must be recognized here;
		// canonicalize JSON without using the successful-call validator. Keep
		// large numeric values exact and ignore call IDs and object key order.
		var args map[string]any
		d := json.NewDecoder(strings.NewReader(call.Function.Arguments))
		d.UseNumber()
		if d.Decode(&args) != nil || args == nil || d.Decode(new(any)) != io.EOF {
			return ""
		}
		arguments, evidence = args, result.Code
	}
	encoded, err := json.Marshal([]any{call.Function.Name, arguments, evidence})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func entryQueryCorrectionContext(messages []providerMessage, enabled bool) []providerMessage {
	out := make([]providerMessage, 0, len(messages)+1)
	for _, message := range messages {
		if message.contextKind != "entry_query_correction" {
			out = append(out, message)
		}
	}
	if enabled {
		// Fixed server-authored policy only, never promote tool data. System
		// messages remain protected by compactProviderContext independently
		// of whether the repetitive tool batches have left the context window.
		out = append(out, providerMessage{Role: "system", contextKind: "entry_query_correction", Content: entryQueryCorrectionPrompt})
	}
	return out
}
