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
	for _, protocol := range []string{"shadowsocks", "vmess", "vless", "trojan", "tuic", "hysteria", "anytls", "v2node"} {
		nodeSort[protocol] = panelSchema{"type": "object", "additionalProperties": poInt("排序值"), "description": "节点ID字符串到排序值的映射"}
		prefix := "/server/" + protocol
		fields := panelNodeFields(protocol)
		add("nodes."+protocol+".save", "nodes", "创建或完整保存"+protocol+"节点；有id时先查询当前设置并保留未修改字段。group_id/route_id是ID字符串数组，port可为端口范围字符串；其他连接参数按后台协议校验。", "POST", prefix+"/save", true, false, fields, "name", "group_id", "host", "port", "server_port", "rate")
		add("nodes."+protocol+".update", "nodes", "修改节点显示状态或入口归属", "POST", prefix+"/update", true, false, poFields("id:i show:i client_entry_only:i entry_group_id:i"), "id")
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
	condition := poObj(panelSchema{"field": poEnum("规则字段", "user_id", "registration_days", "plan_id", "email", "ua"), "operator": poEnum("数值eq/gte/lte/gt/lt/in/between；邮箱in；UA contains_any/excludes_any/empty/not_empty", "eq", "gte", "lte", "gt", "lt", "in", "between", "contains_any", "excludes_any", "empty", "not_empty"), "value": panelSchema{"description": "单个匹配值，保持既有规则JSON类型"}, "values": poArray(panelSchema{}), "min": panelSchema{"description": "范围下界，沿用条件数据类型"}, "max": panelSchema{"description": "范围上界，沿用条件数据类型"}}, "field", "operator")
	add("entry_rules.save", "entries", "新建或完整保存入口分配规则；先查现有规则的conditions结构，保留未修改条件；members为生效节点", "POST", base+"/save", true, false, poMerge(poFields("id:i name action entry_host resolve_entry_host:i extra_nodes_position enabled:i remarks"), panelSchema{"conditions": poArray(condition), "members": members, "extra_nodes": poArray(poText("附加节点订阅链接；先读取现有格式"))}), "name", "conditions")
	add("entry_rules.enabled", "entries", "启用或停用指定入口规则，enabled=0或1", "POST", base+"/enabled", true, false, poFields("id:i enabled:i"), "id", "enabled")
	add("entry_rules.sort", "entries", "排序入口规则或二分组，items逐项kind/id", "POST", base+"/sort", true, false, panelSchema{"items": poArray(poObj(poFields("kind id:i"), "kind", "id"))}, "items")
	add("entry_rules.delete", "entries", "删除入口分配规则", "POST", base+"/drop", true, false, panelSchema{"id": poID()}, "id")
	add("entry_split.preview", "entries", "按最近minutes分钟订阅拉取用户预览二分", "GET", base+"/split-preview", false, false, poFields("minutes:i"))
	add("entry_split.create", "entries", "按最近订阅用户新建二分规则，填写两边入口地址", "POST", base+"/split-create", true, false, poMerge(poFields("name minutes:i entry_host_a entry_host_b resolve_entry_host:i enabled:i remarks"), panelSchema{"members": members}), "name", "minutes", "entry_host_a", "entry_host_b")
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
		case "dns.domains":
			fields = poMerge(poSmallPage(), poFields("keyword type"))
		case "dns.records":
			fields = poMerge(poSmallPage(), poFields("domain domain_id:i keyword subdomain record_type record_line"))
		}
		add(x.name, "dns", x.desc, "GET", x.path, false, false, fields)
	}
	dnsAuth := poFields("secret_id secret_key edition auth_type api_token verify:b clear:b")
	add("dns.config_save", "dns", "保存DNSPod连接设置；凭据只写不回显", "POST", "/dns/config/save", true, false, dnsAuth)
	add("dns.config_test", "dns", "测试已保存或提交的DNSPod连接凭据", "POST", "/dns/config/test", false, false, poFields("secret_id secret_key edition auth_type api_token"))
	add("dns.record_save", "dns", "创建或更新DNS记录；更新传record_id，value为目标地址", "POST", "/dns/record/save", true, false, poFields("record_id:i domain_id:i ttl:i mx:i weight:i domain record_type value domain_grade sub_domain record_line record_line_id"), "domain", "record_type", "value")
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
	addSchema("entry_monitors.save", "entries", "完整保存入口检测设置，必须用最新revision并保留全部items；targets.auto_split_enabled控制指定入口自动二分", "PUT", em, true, false, poReflect(admin.ClientEntryMonitorSaveRequest{}))
	add("entry_monitors.run", "entries", "发起指定入口检测，省略policy_ids或空数组检测全部", "POST", em+"/run", true, false, poFields("policy_ids:ids"))
	add("entry_monitors.runs", "entries", "读取入口检测执行记录", "GET", em+"/runs", false, false, poFields("limit:i"))
	add("entry_monitors.clear_runs", "entries", "管理员清空入口检测记录", "DELETE", em+"/runs", true, true, panelSchema{})
	add("ip_pool.list", "entries", "读取备用IP池、健康状态与占用", "GET", em+"/backup-ips", false, false, panelSchema{})
	addSchema("ip_pool.create", "entries", "新增IP池资源；单个直接传字段，批量传items，每项port应明确为实际入口端口", "POST", em+"/backup-ips", true, false, poReflect(admin.ClientEntryBackupIPCreateRequest{}))
	add("ip_pool.update", "entries", "修改IP池条目", "PUT", em+"/backup-ips/{id}", true, false, poMerge(poReflect(admin.ClientEntryBackupIPSaveRequest{})["properties"].(panelSchema), panelSchema{"id": poID()}), "id")
	add("ip_pool.delete", "entries", "删除IP池条目，后台校验占用", "DELETE", em+"/backup-ips/{id}", true, false, panelSchema{"id": poID()}, "id")
	add("ip_pool.refresh", "entries", "主动刷新指定IP池检测，ids为空按后台全部刷新", "POST", em+"/backup-ips/refresh", true, false, poFields("ids:ids"))
	addSchema("ip_pool.delete_many", "entries", "按scope和ids批量删除IP池资源，已占用条目由后台跳过", "POST", em+"/backup-ips/delete", true, false, poReflect(admin.ClientEntryBackupIPBulkDeleteRequest{}))
	return out
}

func panelNodeFields(protocol string) panelSchema {
	p := poFields("id:i group_id:a route_id:a name parent_id:i host port server_port:i tags:a rate:n show:i client_entry_only:i sort:i")
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
	for _, k := range []string{"networkSettings", "tlsSettings", "ruleSettings", "dnsSettings", "network_settings", "tls_settings", "encryption_settings", "obfs_settings", "ddns_settings"} {
		if _, ok := p[k]; ok {
			p[k] = panelSchema{"type": "object", "additionalProperties": true, "description": "协议连接设置，先读节点现有对象或node_detail字段说明；后台按协议校验"}
		}
	}
	return p
}
