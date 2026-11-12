package aiops

import (
	"encoding/json"
	"fmt"
	"strings"
)

type panelRecoveryKind uint8

const (
	panelRecoveryNone panelRecoveryKind = iota
	panelRecoveryPreflight
	panelRecoveryReadOnly
)

const panelPreflightRecoveryInstruction = "该操作在写入前检查阶段失败，未执行写入。本批后续调用已跳过；先查询说明与现状，修正参数或消除明确原因后再提交。禁止原样重复失败请求。"
const panelReadOnlyRecoveryInstruction = "该后台写操作结果未知，可能已部分执行。本轮后续仅允许已审查的只读查询核对现状并总结，禁止再次执行任何写入、SSH、探针、登记或知识保存。可用panel_query查询dns.records、dns.domains、dns.record_lines、entry_rules.list、nodes.list、orders.detail等已审查操作，也可用entry_health、entry_rules_list、entry_probe_result等只读专用工具。仅标注mutating=false不足以证明恢复阶段可用：未审查的新操作、监测目标/订阅解析/证据刷新查询不开放。原操作保持待核对，不能声称全部完成。需要写入时由操作人发送新消息重新规划，不自动重试原操作。"

const historicalActionRecoveryPrompt = "轮次边界：只读恢复限制只对产生结果未知动作的那一轮生效，不是会话永久锁。历史动作摘要中的authorization_turn_id是动作获准执行的轮次（0表示手动审批或旧记录未关联轮次），不是当前轮次。上一轮的unknown状态及其输出中的停止提示，不会自动限制本轮所有写入。操作人发送新消息继续原任务时，应先查询当前状态，区分已经完成、尚未完成和仍无法确认的部分，再仅针对核实后尚未完成的差异重新规划并按本轮权限执行；不能仅因历史动作仍为unknown拒绝查询或整个任务。不得直接重放旧请求、假定旧操作未执行、将旧unknown改成成功，或用SSH绕过本轮新发生的只读限制。若查询不足以确认安全的后续差异，只暂停相关操作并说明缺少哪项事实，不得虚构已经核实。"

// Only server-owned persisted action metadata may select a recovery mode. A
// provider response, HTTP status or error text cannot establish non-execution.
func panelActionRecovery(a Action) panelRecoveryKind {
	if a.Kind != "panel_operation" {
		return panelRecoveryNone
	}
	if a.Status == "unknown" {
		return panelRecoveryReadOnly
	}
	if a.Status != "failed" {
		return panelRecoveryNone
	}
	var marker struct {
		ExecutionState string `json:"execution_state"`
		Code           string `json:"code"`
		Recovery       string `json:"recovery"`
	}
	if json.Unmarshal([]byte(a.Output), &marker) != nil || marker.ExecutionState != "not_executed" || marker.Recovery != "panel_preflight" {
		return panelRecoveryNone
	}
	switch marker.Code {
	case "dns_invalid_arguments", "dns_domain_metadata", "dns_record_line_unavailable", "dns_preflight_failed", "node_invalid_arguments", "ip_pool_invalid_arguments", "entry_order_invalid_arguments", "entry_order_conflict", "entry_monitor_invalid_arguments", "entry_monitor_conflict",
		"entry_retirement_invalid_arguments", "entry_retirement_conflict", "entry_retirement_blocked", "entry_retirement_not_found":
		return panelRecoveryPreflight
	default:
		return panelRecoveryNone
	}
}

// Canonical object ordering prevents a changed reason, whitespace or JSON key
// order from disguising an exact replay. Keep numbers lossless (not float64).
func panelRecoveryOperationKey(call ToolCall) string {
	if call.Function.Name != "propose_panel_operation" {
		return ""
	}
	var args struct {
		Operation  string          `json:"operation"`
		Parameters json.RawMessage `json:"parameters"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args.Operation == "" {
		return ""
	}
	var parameters map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(args.Parameters)))
	decoder.UseNumber()
	if decoder.Decode(&parameters) != nil || parameters == nil {
		return ""
	}
	canonical, err := json.Marshal(parameters)
	if err != nil {
		return ""
	}
	return hashOperation(args.Operation + "\x00" + string(canonical))
}

// Explicit allowlist: new tools are denied until reviewed, including tools
// such as knowledge_save which write directly rather than proposing actions.
func (s *Service) panelRecoveryReadOnlyCall(call ToolCall) bool {
	switch call.Function.Name {
	case "knowledge_search", "knowledge_get",
		"panel_operations_list", "panel_operation_describe", "panel_result_read",
		"panel_status", "user_lookup", "user_detail", "plans_list", "groups_list",
		"orders_list", "nodes_list", "node_detail",
		"entry_rules_list", "entry_simulate", "entry_health", "entry_health_detail", "host_references", "entry_coverage", "backup_ips_list",
		"entry_probe_result", "entry_rule_detail", "entry_split_operation",
		"tickets_list", "tickets_list_global", "ticket_get", "ticket_message_get", "ticket_settings", "servers_list", "ssh_intakes_list":
		return true
	case "panel_query":
		var args struct {
			Operation string `json:"operation"`
		}
		if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
			return false
		}
		operation, err := findPanelOperation(s.getPanelOperations(), args.Operation)
		if err != nil || operation.Mutating || !panelRecoveryReadOnlyOperation(operation.Name) {
			return false
		}
		// Some read-labelled routes refresh stored targets or connect to a
		// remote panel and replace resolver state used by live subscriptions.
		switch operation.Path {
		case "/dns-failover/entry-monitors", "/server/client-entry/fetch", "/server/client-entry/resolve":
			return false
		}
		return true
	default:
		return false
	}
}

// The general directory's Mutating=false is necessary but insufficient:
// some read-labelled operations refresh business state. New operations require
// an explicit review before they can be used after an uncertain mutation.
func panelRecoveryReadOnlyOperation(name string) bool {
	switch name {
	case "dns.config", "dns.domains", "dns.records", "dns.record_types", "dns.record_lines",
		"entry_evidence.delivery_hosts", "entry_evidence.delivery_stats", "entry_evidence.delivery_users", "entry_evidence.delivery_versions",
		"nodes.list", "node_groups.list", "node_routes.list",
		"entry_rules.list", "entry_rules.order", "entry_rules.simulate", "entry_split.users", "entry_collections.list", "entry_monitors.config", "entry_monitors.runs", "ip_pool.list",
		"entry_groups.audit", "entry_groups.retirement",
		"users.detail", "orders.list", "orders.detail", "plans.list", "notices.list", "tickets.list", "config.read":
		return true
	default:
		return false
	}
}

func panelReadOnlyRecoveryContext(messages []providerMessage, actionID int64) []providerMessage {
	return append(messages, providerMessage{Role: "system", Content: fmt.Sprintf("操作 #%d 进入本轮只读恢复阶段。%s", actionID, panelReadOnlyRecoveryInstruction)})
}

func panelReadOnlyRecoveryError(actionID int64) string {
	return fmt.Sprintf("操作 #%d 执行结果仍待核对；本轮已暂停后续写入并保留只读核对结果。请根据现状发送新消息重新规划，不会自动重试原操作", actionID)
}
