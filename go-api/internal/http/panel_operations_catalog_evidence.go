package httpapi

import "forest/go-api/internal/aiops"

func panelEntryEvidenceOperations() []aiops.PanelOperation {
	const base = "/server/client-entry-user-policy/investigations/"
	query := poFields("kind target_id:i host port:i from:i to:i ua account current:i page_size:i")
	query["kind"] = poEnum("来源规则种类；先读取真实目标，不猜测ID", "policy", "split_group")
	query["from"] = poInt("时间窗口开始Unix秒，包含开始")
	query["to"] = poInt("时间窗口结束Unix秒，不含结束")
	query["account"] = poText("可选用户ID或完整邮箱精确匹配，省略不限制用户")
	query["ua"] = poText("可选UA包含文本，与其他条件匹配同一条实际下发请求")
	page := poFields("current:i page_size:i")
	split := poFields("case_id:i revision:i use_backup_ip_pool:b entry_host_a entry_host_b")
	out := []aiops.PanelOperation{}
	add := func(name, description, method, path string, write, adminOnly bool, fields panelSchema, required ...string) {
		out = append(out, po("entry_evidence."+name, "entries", description, method, base+path, write, adminOnly, poObj(fields, required...)))
	}
	add("status", "读取实际下发证据覆盖时间、保留天数和写入失败计数。仅启用后的证据可追溯；放行200不是下发，查不到不是未获得入口", "GET", "status", false, false, panelSchema{})
	add("deliveries", "分页查询历史实际下发请求及节点白名单元数据；按历史快照筛选不是当前规则重算。response_complete仅证明服务端写入完成，不证明客户端接收或恶意行为；域名不等于客户端解析后的IP", "GET", "deliveries", false, false, poMerge(query, poFields("policy_id:i group_id:i user_id:i complete_only:b")))
	add("candidates", "预览历史下发候选时必须填kind/target_id/host/port/from/to，返回规则revision和candidates_hash。读取已冻结名单则只填case_id及分页/可选selected，不填实时筛选；冻结不封禁用户。全部页核对后才能冻结，不能把当前匹配当历史证据", "GET", "candidates", false, false, poMerge(query, poFields("case_id:i selected:b")))
	add("freeze", "按已核对预览创建不可变排查档案和候选名单，需最新revision和candidates_hash；selected_user_ids省略选全部符合证据用户，填写则仅选所列。不是封禁或调整入口；parent_case_id关联上一轮", "POST", "freeze", true, false, poMerge(query, poFields("revision expected_candidates_hash selected_user_ids:ids title parent_case_id:i")), "kind", "target_id", "host", "port", "from", "to", "revision", "expected_candidates_hash")
	add("cases", "分页查询排查档案及轮次关联、观察结论；名单快照独立保留，不受普通日志滚动清理影响", "GET", "cases", false, false, page)
	add("context", "按真实下发记录分层查看窗口内已下发、窗口前历史暴露、仅有不完整拉取及已删除账号。仅有拉取不能关联该入口，不自动进入冻结名单；缺日志不是无暴露", "GET", "context", false, false, query, "kind", "target_id", "host", "port", "from", "to")
	add("case", "读取指定排查档案、冻结规则和探针证据、二分执行结果；未执行不能称完成，异常不等于恶意行为", "GET", "case", false, false, poFields("case_id:i"), "case_id")
	add("split_preview", "预览冻结候选二分的A/B及未选用户R去向，保留UA和节点范围。必须先预览确认supported及warnings，再使用同一参数执行；并发变化必须重查，不能自行换候选或入口", "POST", "split-preview", false, false, split, "case_id", "revision", "use_backup_ip_pool")
	add("split_execute", "执行已冻结且预览核对的候选二分；源规则/名单/排序改变会拒绝，未选用户保留原入口。use_backup_ip_pool原子领取健康未占用地址；手填需不同有效入口。每档案仅执行一次，网络失败先读case核对，不盲目重试", "POST", "split-execute", true, false, split, "case_id", "revision", "use_backup_ip_pool")
	add("case_update", "保存排查观察状态和人工结论，需最新档案revision；记录事实和缺口，不用单次TCP失败断言内鬼或被墙", "POST", "case-update", true, false, poFields("case_id:i revision:i observation conclusion"), "case_id", "revision", "observation")
	add("case_delete", "仅管理员删除指定排查档案和冻结证据，不回滚已执行入口变更；先确认档案和影响，员工不能删除", "POST", "case-delete", true, true, poFields("case_id:i"), "case_id")
	add("settings", "仅管理员设置实际下发普通证据保留天数1至365，缩短将由后台清理旧普通记录，已冻结档案不受影响", "POST", "settings", true, true, poFields("keep_days:i"), "keep_days")
	return out
}
