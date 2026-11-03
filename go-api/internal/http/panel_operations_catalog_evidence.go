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
	delivery := poFields("policy_id:i group_id:i collection_id:i provider_id:i server_type server_id:i host port from:i to:i ua account transport rule_revision version current:i page_size:i")
	delivery["server_id"] = map[string]any{"type": "integer", "description": "实际下发节点ID；附加节点可为负数，必须读取真实ID，0不作为有效查询ID"}
	delivery["port"] = poText("实际下发端口或端口范围字符串，精确匹配；如54101或2000-2009")
	delivery["from"], delivery["to"], delivery["ua"], delivery["account"] = query["from"], query["to"], query["ua"], query["account"]
	delivery["rule_revision"] = poText("当时订阅规则快照的64位小写hex；不是当前规则revision，先从delivery_versions取得")
	delivery["version"] = poText("delivery_versions返回的32位订阅快照标识；精确匹配规则/叶子/合集/provider/host/port等全部维度，不是独立配置生效轮次")
	delivery["transport"] = poEnum("可选订阅响应路径；不是UA客户端名称", "subscription", "server_json", "app_clash", "forest_runtime", "forest_provider")
	// Use the validator's supported constraints. Exact lowercase-hex syntax
	// and cross-field time/page relationships remain enforced by the handler.
	delivery["current"] = panelSchema{"type": "integer", "minimum": 1, "maximum": 10000, "description": "页码从1开始；(current-1)*page_size不得超过10000，超过时缩小时间或目标范围"}
	delivery["page_size"] = panelSchema{"type": "integer", "minimum": 1, "maximum": 100, "description": "每页1至100条，默认20"}
	delivery["server_type"] = poEnum("实际下发节点协议，省略表示不限", "shadowsocks", "vmess", "vless", "trojan", "tuic", "hysteria", "hysteria2", "anytls", "v2node", "juicity", "mieru", "socks", "http")
	for _, field := range []string{"policy_id", "group_id", "collection_id", "provider_id"} {
		delivery[field] = poID()
	}
	for _, field := range []string{"from", "to"} {
		delivery[field] = poMerge(delivery[field].(panelSchema), panelSchema{"minimum": 0})
	}
	for field, length := range map[string]int{"rule_revision": 64, "version": 32} {
		delivery[field] = poMerge(delivery[field].(panelSchema), panelSchema{"minLength": length, "maxLength": length})
	}
	for field, length := range map[string]int{"host": 255, "port": 64, "ua": 1024, "account": 254} {
		delivery[field] = poMerge(delivery[field].(panelSchema), panelSchema{"maxLength": length})
	}
	add("delivery_hosts", "分页读取保留期内实际下发过的入口地址及去重用户人数，可用于历史拉取IP选择；跨规则节点端口取并集，排除不完整和失败响应；不是当前入口规则列表", "GET", "delivery-hosts", false, false, delivery)
	add("delivery_stats", "统计保留期内实际下发过目标入口的去重用户数、去重请求数及首次/最近下发时间；同一订阅多个节点与合集成员取并集，不累计相加。只计完整覆盖、服务端写入完整的2xx请求；全部筛选匹配同一请求和同一节点。不使用当前规则反推历史，服务端返回不证明客户端导入或连接。首次记录不是入口启用时间，需精确时间时由已知变更记录给from/to", "GET", "delivery-stats", false, false, delivery)
	add("delivery_users", "分页读取真实订阅下发的去重用户ID、历史邮箱、次数及首次/最近记录；不会关联当前用户或当前规则，已删除用户保留历史身份。支持version精确快照、UA和半开时间范围。仅保留期内完整2xx记录，不能把缺日志视为未获得入口；每页最多100，按返回current/page_size/total翻页", "GET", "delivery-users", false, false, delivery)
	add("delivery_versions", "分页列出实际订阅下发的规则/固定叶子/合集/provider与host/port快照及各自去重人数。返回version供精确钻取；rule_revision为空表示旧版本未知，collection_id=0可能缺历史归属。快照不是配置生效轮次，相同快照复用不能保证区分轮次，首次记录不等于启用时间。各版本人数有重叠不能相加", "GET", "delivery-versions", false, false, delivery)
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
