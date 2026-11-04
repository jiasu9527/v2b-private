package httpapi

import (
	"forest/go-api/internal/admin"
	"forest/go-api/internal/aiops"
)

func panelNetworkOperations() []aiops.PanelOperation {
	out := []aiops.PanelOperation{}
	add := func(name, category, desc, method, path string, write, adminOnly bool, p panelSchema, req ...string) {
		out = append(out, po(name, category, desc, method, path, write, adminOnly, poObj(p, req...)))
	}
	addSchema := func(name, category, desc, method, path string, write, adminOnly bool, schema panelSchema) {
		out = append(out, po(name, category, desc, method, path, write, adminOnly, schema))
	}
	add("nodes.list", "nodes", "列出所有协议节点及运行状态，默认隐藏安装命令与凭据。明确部署V2node时可传include_install_command=true，必须同时传node_id；此时仍返回节点列表结构，但只含该ID的V2node及其install_command，其他凭据仍隐藏；目标不存在返回404", "GET", "/server/manage/getNodes", false, false, panelSchema{"node_id": poID(), "include_install_command": poBool("默认false；仅明确部署V2node需要时设true，必须同时指定node_id，取得该节点后台生成的完整安装命令")})
	nodeSort := panelSchema{}
	nodeRequired := map[string][]string{
		"shadowsocks": {"cipher"},
		"vmess":       {"tls", "network"},
		"vless":       {"tls", "network"},
		"trojan":      {"network"},
		"tuic":        {"insecure", "disable_sni", "zero_rtt_handshake"},
		"hysteria":    {"version", "insecure"},
		"anytls":      {"insecure"},
		"v2node":      {"protocol", "tls", "network", "disable_sni", "zero_rtt_handshake"},
	}
	for _, protocol := range []string{"shadowsocks", "vmess", "vless", "trojan", "tuic", "hysteria", "anytls", "v2node"} {
		nodeSort[protocol] = panelSchema{"type": "object", "additionalProperties": poInt("排序值"), "description": "节点ID字符串到排序值的映射"}
		prefix := "/server/" + protocol
		fields := panelNodeFields(protocol)
		required := append([]string{"name", "group_id", "host", "port", "server_port", "rate"}, nodeRequired[protocol]...)
		description := "创建或完整保存" + protocol + "节点；有id时先查询当前设置并保留未修改字段。group_id/route_id是ID字符串数组，port可为端口范围字符串；其他连接参数按后台协议校验。"
		if protocol == "v2node" {
			description += "protocol是V2node使用的底层协议，先查询目标或参考模板节点，不能由V2node名称猜测；disable_sni和zero_rtt_handshake必须从该模板或用户明确设置取得，不能遗漏或擅自填默认值。"
		}
		add("nodes."+protocol+".save", "nodes", description, "POST", prefix+"/save", true, false, fields, required...)
		add("nodes."+protocol+".update", "nodes", "修改节点显示状态或入口归属；show=0完全不下发；show=1且client_entry_only=1时仅匹配入口放行规则的有权限用户获得订阅节点，不改变节点端鉴权", "POST", prefix+"/update", true, false, poFields("id:i show:i client_entry_only:i entry_group_id:i"), "id")
		add("nodes."+protocol+".copy", "nodes", "复制现有节点配置", "POST", prefix+"/copy", true, false, panelSchema{"id": poID()}, "id")
		add("nodes."+protocol+".delete", "nodes", "删除指定协议节点", "POST", prefix+"/drop", true, false, panelSchema{"id": poID()}, "id")
	}
	add("nodes.sort", "nodes", "按协议分别设置节点排序；示例vless:{\"12\":1}", "POST", "/server/manage/sort", true, false, nodeSort)
	add("nodes.replace_host", "nodes", "将节点中匹配old_host的地址批量改为new_host", "POST", "/server/manage/updateHost", true, false, poFields("old_host new_host"), "old_host", "new_host")
	add("node_groups.list", "nodes", "列出节点权限组或指定group_id详情", "GET", "/server/group/fetch", false, false, poFields("group_id:i"))
	add("node_groups.save", "nodes", "新增或修改节点权限组", "POST", "/server/group/save", true, false, poFields("id:i name"), "name")
	add("node_groups.delete", "nodes", "删除节点权限组", "POST", "/server/group/drop", true, false, panelSchema{"id": poID()}, "id")
	add("node_routes.list", "nodes", "列出节点路由规则", "GET", "/server/route/fetch", false, false, panelSchema{})
	add("node_routes.save", "nodes", "新增或保存路由规则；match为匹配字符串数组，action/action_value沿用后台规则类型", "POST", "/server/route/save", true, false, poFields("id:i remarks match:a action action_value"), "match", "action")
	add("node_routes.delete", "nodes", "删除节点路由规则", "POST", "/server/route/drop", true, false, panelSchema{"id": poID()}, "id")
	member := poObj(poFields("server_type server_id:i sort:i"), "server_type", "server_id")
	members := poArray(member)
	add("client_entries.list", "entries", "查询客户端入口组，可按id精确读取", "GET", "/server/client-entry/fetch", false, false, poFields("id:i"))
	add("client_entries.resolve", "entries", "解析指定客户端入口组的远端成员和连接", "POST", "/server/client-entry/resolve", false, false, panelSchema{"id": poID()}, "id")
	add("client_entries.save", "entries", "新增或完整保存客户端入口组；先查当前设置，保留未改字段；远端SSH凭据仅用于所指定入口源", "POST", "/server/client-entry/save", true, false, poMerge(poFields("id:i code name display_name remarks strategy action action_value hide_member_nodes:b show:i remote_enabled:b remote_host remote_ssh_port:i remote_ssh_user remote_ssh_password remote_group_ref remote_exclude_names:a remote_refresh_sec:i match:a"), panelSchema{"members": members, "ips": poArray(poObj(poFields("ip sort:i"), "ip"))}), "code", "name")
	add("client_entries.delete", "entries", "删除客户端入口组", "POST", "/server/client-entry/drop", true, false, panelSchema{"id": poID()}, "id")
	base := "/server/client-entry-user-policy"
	add("entry_rules.list", "entries", "查询全部入口分配规则、条件、二分组和成员", "GET", base+"/fetch", false, false, panelSchema{})
	add("entry_rules.simulate", "entries", "按邮箱及UA模拟当前命中的入口规则；member可指定协议:节点ID", "GET", base+"/simulate", false, false, poFields("email ua member"), "email")
	condition := poObj(panelSchema{"field": poEnum("规则字段", "user_id", "registration_days", "plan_id", "email", "ua"), "operator": poEnum("数值eq等于/gte大于等于/lte小于等于/gt大于/lt小于/in指定值/between含两端；用户ID十万以内可用lte value=100000；邮箱in；UA contains_any/excludes_any/empty/not_empty", "eq", "gte", "lte", "gt", "lt", "in", "between", "contains_any", "excludes_any", "empty", "not_empty"), "value": panelSchema{"description": "eq/gt/gte/lt/lte填写单个整数；between填写min/max，in填写values"}, "values": poArray(panelSchema{}), "min": panelSchema{"description": "范围下界，含边界"}, "max": panelSchema{"description": "范围上界，含边界"}}, "field", "operator")
	add("entry_rules.save", "entries", "新建或完整保存入口分配规则；新建无条件或把非空条件清空时必须confirm_match_all=true，匹配范围仅限所选节点的合资格用户；先查现有规则，保留未修改条件；conditions是AND，members仅含目标节点，逐节点首条命中生效。ID≤100000用一条lte或between范围条件；新节点client_entry_only=1配original/override白名单即可限制订阅下发，不必再建反向hide；同时检查目标是否被其他规则放行。original沿用原地址，override替换入口，hide不下发；不改变节点端鉴权", "POST", base+"/save", true, false, poMerge(poFields("id:i name action entry_host resolve_entry_host:i extra_nodes_position enabled:i remarks confirm_match_all:b"), panelSchema{"conditions": poArray(condition), "members": members, "extra_nodes": poArray(poText("附加节点订阅链接；先读取现有格式"))}), "name", "conditions")
	add("entry_rules.enabled", "entries", "启用或停用指定入口规则，enabled=0或1", "POST", base+"/enabled", true, false, poFields("id:i enabled:i"), "id", "enabled")
	add("entry_rules.sort", "entries", "排序入口规则或二分组，items逐项kind/id", "POST", base+"/sort", true, false, panelSchema{"items": poArray(poObj(poFields("kind id:i"), "kind", "id"))}, "items")
	add("entry_rules.delete", "entries", "删除入口分配规则", "POST", base+"/drop", true, false, panelSchema{"id": poID()}, "id")
	historyFields := panelSchema{
		"history_host": poText("历史实际下发的入口IP/host；指定时跨原规则、分组、合集、UA、节点和端口取用户并集，不是请求来源IP"),
		"history_from": poInt("历史查询开始Unix秒，包含开始；与history_to一起原样传递"),
		"history_to":   poInt("历史查询结束Unix秒，不含结束；创建时必须使用预览返回值"),
		"minutes":      poInt("仅未指定history_host时表示近期拉取窗口；不能用任意订阅拉取代替目标IP实际下发"),
	}
	add("entry_split.preview", "entries", "预览新建固定名单：指定history_host及history_from/history_to可按历史实际下发IP跨来源去重，返回现存user_count及snapshot_hash/from/to；只计完整成功响应。必须读取预览，不能沿用旧人数或猜hash；历史已删除账号不加入新组。未指定history_host才是最近minutes分钟任意订阅拉取模式", "GET", base+"/split-preview", false, false, historyFields)
	add("entry_split.create", "entries", "与后台历史拉取IP页面相同：按历史实际下发IP名单新建独立固定组或平均二分，不要求原来源是单规则，来源属于合集也可选人。历史模式必须传预览返回的history_host/history_from=from/history_to=to/snapshot_hash，省略minutes；名单变化返回冲突需重查，不复制上千个ID。history_single_group=false或省略创建A/B，true只建一个组并省略entry_host_b。members必须明确选择且仅作用于这些共同节点，继承原端口；新规则不自动继承各来源UA或用户/节点差异，不能宣称已保留，无法表达时先说明。新规则默认追加，需核对排序并保留特殊置顶规则，不先删除/重建/移出旧规则或合集；创建成功后再按需排序并模拟验证。历史模式不是永久快照ID接口，只支持时间窗口加hash校验。非历史模式才传minutes", "POST", base+"/split-create", true, false, poMerge(historyFields, poFields("name entry_host_a entry_host_b resolve_entry_host:i enabled:i remarks snapshot_hash history_single_group:b"), panelSchema{"members": members}), "name", "members", "entry_host_a")

	split := poFields("policy_id:i group_id:i entry_host_a entry_host_b use_backup_ip_pool:b")
	convert := poMerge(split)
	delete(convert, "group_id")
	add("entry_split.convert", "entries", "把现有规则转成二分，use_backup_ip_pool=true从IP池自动填充入口；支持ID范围加UA", "POST", base+"/split-convert", true, false, convert, "policy_id")
	add("entry_split.group", "entries", "继续二分已有二分组，支持IP池自动分配", "POST", base+"/split-group", true, false, split, "policy_id", "group_id")
	add("entry_split.update_group", "entries", "修改二分组名称/入口/成员/启用状态", "POST", base+"/split-group-host", true, false, poMerge(poFields("policy_id:i group_id:i name entry_host resolve_entry_host:i enabled:i remarks"), panelSchema{"members": members}), "policy_id", "group_id")
	add("entry_split.users", "entries", "分页列出二分组用户邮箱", "GET", base+"/split-group-users", false, false, poMerge(poSmallPage(), poFields("policy_id:i group_id:i search")), "policy_id", "group_id")
	add("entry_split.sort", "entries", "调整二分组顺序，ids为完整目标顺序", "POST", base+"/split-group-sort", true, false, poFields("policy_id:i ids:ids"), "policy_id", "ids")
	add("entry_split.root", "entries", "将指定二分组移回根分组", "POST", base+"/split-group-root", true, false, poFields("policy_id:i group_id:i"), "policy_id", "group_id")
	add("entry_collections.list", "entries", "列出入口合集及版本", "GET", base+"/collection/fetch", false, false, panelSchema{})
	addSchema("entry_collections.save", "entries", "创建或保存入口合集；编辑需传当前version，items.kind=policy或split_group", "POST", base+"/collection/save", true, false, poReflect(admin.ClientEntryCollectionSaveRequest{}))
	add("entry_collections.delete", "entries", "删除入口合集，需传当前version", "POST", base+"/collection/drop", true, false, poFields("id:i version:i"), "id", "version")
	add("entry_collections.remove", "entries", "从合集移除一个成员，需传当前version", "POST", base+"/collection/remove", true, false, poMerge(poFields("id:i version:i"), panelSchema{"item": poReflect(admin.ClientEntryCollectionItem{})}), "id", "version", "item")
	for _, x := range []struct{ name, path, desc string }{{"dns.config", "/dns/config", "查询DNSPod配置状态，密钥不回显"}, {"dns.record_types", "/dns/record/types", "查询支持的DNS记录类型"}, {"dns.record_lines", "/dns/record/lines", "查询DNS解析线路"}, {"dns.domains", "/dns/domain/list", "查询DNS域名"}, {"dns.records", "/dns/record/list", "查询域名DNS记录"}} {
		fields := panelSchema{}
		switch x.name {
		case "dns.record_types":
			fields = poFields("domain_grade")
		case "dns.record_lines":
			fields = poFields("domain domain_id:i domain_grade record_type")
			fields["domain_grade"] = poText("域名实际套餐等级，对应 dns.domains 的 Grade；省略时后端只读查询补齐，不能猜测免费套餐")
			x.desc += "；domain使用完整准确域名，domain_id对应dns.domains的DomainId，domain_grade对应Grade；返回的线路名称与ID应成对使用"
		case "dns.domains":
			fields = poMerge(poSmallPage(), poFields("keyword type"))
		case "dns.records":
			fields = poMerge(poSmallPage(), poFields("domain domain_id:i keyword subdomain record_type record_line"))
			x.desc += "；domain使用完整准确域名，domain_id对应dns.domains的DomainId，省略时后端只读查询补齐；新建前及结果未知后先按subdomain查询核对"
		}
		add(x.name, "dns", x.desc, "GET", x.path, false, false, fields)
	}
	dnsAuth := poFields("secret_id secret_key edition auth_type api_token verify:b clear:b")
	add("dns.config_save", "dns", "保存DNSPod连接设置；凭据只写不回显", "POST", "/dns/config/save", true, false, dnsAuth)
	add("dns.config_test", "dns", "测试已保存或提交的DNSPod连接凭据", "POST", "/dns/config/test", false, false, poFields("secret_id secret_key edition auth_type api_token"))
	dnsRecord := poFields("record_id:i domain_id:i ttl:i mx:i weight:i domain record_type value domain_grade sub_domain record_line record_line_id")
	dnsRecord["domain_id"] = poInt("dns.domains返回的DomainId，必须与domain精确匹配；省略时后端只读补齐")
	dnsRecord["domain_grade"] = poText("dns.domains返回的真实Grade；缺省由后端查询，付费Global等线路不能使用猜测的免费等级")
	dnsRecord["record_line"] = poText("dns.record_lines返回的实际线路名称，与record_line_id成对使用；不要用不存在的线路代替用户要求")
	dnsRecord["record_line_id"] = poText("dns.record_lines返回的对应LineId，不能跨域名或套餐猜测")
	add("dns.record_save", "dns", "创建或更新DNS记录；更新传record_id，value为目标地址。先查询域名DomainId/Grade、实际可用线路及现有记录，再创建；后端核实域名并补齐元数据。明确未执行的前置错误可查询后修正参数；结果未知时只读核对，不要重复创建或继续修改依赖该记录的入口", "POST", "/dns/record/save", true, false, dnsRecord, "domain", "record_type", "value")
	add("dns.record_delete", "dns", "删除指定DNS记录", "POST", "/dns/record/delete", true, false, poFields("record_id:i domain_id:i domain"), "record_id")
	add("dns.record_status", "dns", "启用或停用DNS记录，status沿用DNSPod ENABLE/DISABLE", "POST", "/dns/record/status", true, false, poFields("record_id:i domain_id:i domain status"), "record_id", "status")
	df := "/dns-failover"
	add("dns_failover.settings", "dns_failover", "读取探针API地址设置", "GET", df+"/settings", false, false, panelSchema{})
	add("dns_failover.save_settings", "dns_failover", "修改探针连接面板的API地址", "PUT", df+"/settings", true, false, poFields("dns_probe_api_url"), "dns_probe_api_url")
	add("dns_failover.probes", "dns_failover", "列出探针，凭据不回显", "GET", df+"/probes", false, false, panelSchema{})
	add("dns_failover.create_probe", "dns_failover", "登记探针；结果包含本次新探针的secret和install_command，可继续在指定SSH服务器安装；仅在明确部署需求时创建，普通探针列表不回显凭据", "POST", df+"/probes", true, false, poFields("name"), "name")
	add("dns_failover.delete_probe", "dns_failover", "删除探针", "DELETE", df+"/probes/{id}", true, false, panelSchema{"id": poID()}, "id")
	add("dns_failover.enable_probe", "dns_failover", "启停探针", "PATCH", df+"/probes/{id}/enabled", true, false, poFields("id:i enabled:b"), "id", "enabled")
	add("dns_failover.revoke_probe", "dns_failover", "撤销探针可用状态", "PATCH", df+"/probes/{id}/revoke", true, false, panelSchema{"id": poID()}, "id")
	add("dns_failover.rules", "dns_failover", "列出DNS故障切换规则", "GET", df+"/rules", false, false, panelSchema{})
	rule := poReflect(dnsFailoverRuleInput{})
	addSchema("dns_failover.create_rule", "dns_failover", "创建DNS故障切换规则，targets是候选地址，probe_ids为检测探针", "POST", df+"/rules", true, false, rule)
	add("dns_failover.rule", "dns_failover", "读取指定DNS故障切换规则详情", "GET", df+"/rules/{id}", false, false, panelSchema{"id": poID()}, "id")
	add("dns_failover.update_rule", "dns_failover", "完整保存DNS故障切换规则；先查询并保留未改值", "PUT", df+"/rules/{id}", true, false, poMerge(rule["properties"].(panelSchema), panelSchema{"id": poID()}), "id")
	add("dns_failover.delete_rule", "dns_failover", "删除DNS故障切换规则", "DELETE", df+"/rules/{id}", true, false, panelSchema{"id": poID()}, "id")
	add("dns_failover.rule_status", "dns_failover", "查询DNS故障切换规则的实时状态", "GET", df+"/rules/{id}/status", false, false, panelSchema{"id": poID()}, "id")
	add("dns_failover.enable_rule", "dns_failover", "启停DNS故障切换规则", "PATCH", df+"/rules/{id}/enabled", true, false, poFields("id:i enabled:b"), "id", "enabled")
	add("dns_failover.switch", "dns_failover", "手动切换DNS规则到指定目标", "POST", df+"/rules/{id}/manual-switch", true, false, poFields("id:i target_id:i"), "id", "target_id")
	add("dns_failover.events", "dns_failover", "分页查询DNS故障切换事件", "GET", df+"/events", false, false, poMerge(poSmallPage(), poFields("group:i event_type")))
	add("dns_failover.logs", "dns_failover", "分页查询DNS检测日志", "GET", df+"/logs", false, false, poMerge(poSmallPage(), poFields("group:i probe_id:i target_id:i stage level outcome")))
	em := df + "/entry-monitors"
	add("entry_monitors.list", "entries", "查询入口检测规则和当前revision", "GET", em, false, false, panelSchema{})
	addSchema("entry_monitors.save", "entries", "完整保存入口检测设置，必须用最新revision并保留全部items；targets.auto_split_enabled控制指定入口自动二分。仅返回事务提交回执、revision及保存/删除数量，不回传全量快照；保存数量含未变化项，不是差异计数", "PUT", em+"/save-receipt", true, false, poReflect(admin.ClientEntryMonitorSaveRequest{}))
	add("entry_monitors.run", "entries", "发起指定入口检测，省略policy_ids或空数组检测全部", "POST", em+"/run", true, false, poFields("policy_ids:ids"))
	add("entry_monitors.runs", "entries", "读取入口检测执行记录", "GET", em+"/runs", false, false, poFields("limit:i"))
	add("entry_monitors.clear_runs", "entries", "管理员清空入口检测记录", "DELETE", em+"/runs", true, true, panelSchema{})
	add("ip_pool.list", "entries", "读取备用IP池、健康状态与占用", "GET", em+"/backup-ips", false, false, panelSchema{})
	addSchema("ip_pool.create", "entries", "新增IP池资源；单个直接传字段，批量传items，每项port应明确为实际入口端口", "POST", em+"/backup-ips", true, false, poReflect(admin.ClientEntryBackupIPCreateRequest{}))
	add("ip_pool.update", "entries", "修改IP池条目", "PUT", em+"/backup-ips/{id}", true, false, poMerge(poReflect(admin.ClientEntryBackupIPSaveRequest{})["properties"].(panelSchema), panelSchema{"id": poID()}), "id")
	add("ip_pool.delete", "entries", "删除IP池条目，后台校验占用", "DELETE", em+"/backup-ips/{id}", true, false, panelSchema{"id": poID()}, "id")
	add("ip_pool.refresh", "entries", "主动刷新指定IP池检测，ids为空按后台全部刷新", "POST", em+"/backup-ips/refresh", true, false, poFields("ids:ids"))
	add("ip_pool.delete_many", "entries", "批量删除IP池条目，不修改服务器网卡或独立监控。scope必填：selected删除指定ID，必须传非空ids（最多500）；faulty删除全部已确认unhealthy的条目，all删除全部，两者均不得传非空ids。只替换指定一批时必须用selected，不得扩大为faulty/all；后台跳过仍被入口规则占用的条目，核对deleted/skipped_in_use/skipped_not_found。参数校验未通过时未执行删除，可查询后修正参数，不得原样重试。", "POST", em+"/backup-ips/delete", true, false, panelSchema{
		"scope": poEnum("删除范围，必须明确选择；指定替换名单用selected", "selected", "faulty", "all"),
		"ids":   panelSchema{"type": "array", "items": poID(), "maxItems": 500, "description": "scope=selected时必须传1至500个真实IP池ID；不是IP字符串、节点ID或监控ID。faulty/all省略。"},
	}, "scope")
	return out
}

func panelNodeFields(protocol string) panelSchema {
	p := poFields("id:i group_id:a route_id:a name parent_id:i host port server_port:i tags:a rate:n show:i client_entry_only:i sort:i")
	p["show"] = poInt("0完全不下发；1开启订阅可见，仍需满足权限组及入口规则")
	p["client_entry_only"] = poInt("1仅入口分配可见：默认不下发，命中original/override才下发；0允许默认下发；不改变节点端鉴权")
	extra := map[string]string{
		"vmess":       "tls:i network rules:a networkSettings tlsSettings ruleSettings dnsSettings",
		"trojan":      "network network_settings allow_insecure:i server_name",
		"shadowsocks": "cipher obfs obfs_settings",
		"tuic":        "server_name insecure:i disable_sni:i udp_relay_mode zero_rtt_handshake:i congestion_control",
		"hysteria":    "version:i up_mbps:i down_mbps:i obfs obfs_password server_name insecure:i",
		"vless":       "tls:i tls_settings flow network network_settings encryption encryption_settings",
		"anytls":      "server_name insecure:i padding_scheme",
		"v2node":      "listen_ip send_through protocol tls:i tls_settings flow network network_settings encryption encryption_settings disable_sni:i udp_relay_mode zero_rtt_handshake:i congestion_control cipher up_mbps:i down_mbps:i obfs obfs_password padding_scheme ddns_settings",
	}
	if protocol == "vless" {
		p["port"] = poInt("连接端口")
	} else {
		p["port"] = panelSchema{"type": []string{"string", "integer"}, "description": "连接端口或范围"}
	}
	p["rate"] = panelSchema{"type": []string{"number", "string"}, "description": "流量倍率"}
	for _, key := range []string{"group_id", "route_id"} {
		p[key] = poArray(panelSchema{"type": []string{"string", "integer"}})
	}
	for k, v := range poFields(extra[protocol]) {
		p[k] = v
	}
	if protocol == "v2node" {
		p["protocol"] = poText("V2node使用的底层协议；先查询目标或参考模板节点取得，不能猜测")
		p["disable_sni"] = poInt("必填，0或1；从目标或参考模板节点，或用户明确设置取得，不能擅自填默认值")
		p["zero_rtt_handshake"] = poInt("必填，0或1；从目标或参考模板节点，或用户明确设置取得，不能擅自填默认值")
	}
	for _, k := range []string{"networkSettings", "tlsSettings", "ruleSettings", "dnsSettings", "network_settings", "tls_settings", "encryption_settings", "obfs_settings", "ddns_settings"} {
		if _, ok := p[k]; ok {
			p[k] = panelSchema{"type": "object", "additionalProperties": true, "description": "协议连接设置，先读节点现有对象或node_detail字段说明；后台按协议校验"}
		}
	}
	return p
}
