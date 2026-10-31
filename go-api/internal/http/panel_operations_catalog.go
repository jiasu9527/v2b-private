package httpapi

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"

	"forest/go-api/internal/aiops"
)

// Every operation names one fixed backend route. HTTP verbs alone are not an
// authorization boundary: several legacy handlers accept more than one verb.
func panelOperationCatalog() []aiops.PanelOperation {
	out := append(panelCoreOperations(), panelNetworkOperations()...)
	out = append(out, panelBusinessOperations()...)
	out = append(out, panelEntryEvidenceOperations()...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

type panelSchema map[string]any

func poText(description string) panelSchema {
	return panelSchema{"type": "string", "description": description}
}
func poInt(description string) panelSchema {
	return panelSchema{"type": "integer", "description": description}
}
func poNullable(schema panelSchema) panelSchema {
	if kind, ok := schema["type"].(string); ok {
		schema["type"] = []string{kind, "null"}
	}
	return schema
}
func poBool(description string) panelSchema {
	return panelSchema{"type": "boolean", "description": description}
}
func poID() panelSchema {
	return panelSchema{"type": "integer", "minimum": 1, "description": "记录ID；先查询取得真实ID"}
}
func poEnum(description string, values ...string) panelSchema {
	return panelSchema{"type": "string", "description": description, "enum": values}
}
func poArray(item any) panelSchema { return panelSchema{"type": "array", "items": item} }
func poObj(props panelSchema, required ...string) panelSchema {
	if required == nil {
		required = []string{}
	}
	return panelSchema{"type": "object", "properties": props, "additionalProperties": false, "required": required}
}
func poFields(spec string) panelSchema {
	p := panelSchema{}
	for _, field := range strings.Fields(spec) {
		pair := strings.SplitN(field, ":", 2)
		name, kind := pair[0], "s"
		if len(pair) > 1 {
			kind = pair[1]
		}
		switch kind {
		case "i":
			p[name] = poInt(name)
		case "b":
			p[name] = poBool(name)
		case "a":
			p[name] = poArray(poText(name))
		case "ids":
			p[name] = poArray(poID())
		case "n":
			p[name] = panelSchema{"type": "number", "description": name}
		default:
			p[name] = poText(name)
		}
	}
	return p
}
func poMerge(parts ...panelSchema) panelSchema {
	p := panelSchema{}
	for _, part := range parts {
		for k, v := range part {
			p[k] = v
		}
	}
	return p
}
func poReflect(value any) panelSchema { return poReflectType(reflect.TypeOf(value)) }
func poReflectType(t reflect.Type) panelSchema {
	if t.Kind() == reflect.Pointer {
		schema := poReflectType(t.Elem())
		if kind, ok := schema["type"].(string); ok {
			schema["type"] = []string{kind, "null"}
		}
		return schema
	}
	switch t.Kind() {
	case reflect.Struct:
		p := panelSchema{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue
			}
			tag := strings.Split(f.Tag.Get("json"), ",")[0]
			if tag == "-" {
				continue
			}
			if f.Anonymous && tag == "" {
				for k, v := range poReflectType(f.Type)["properties"].(panelSchema) {
					p[k] = v
				}
				continue
			}
			if tag == "" {
				tag = f.Name
			}
			p[tag] = poReflectType(f.Type)
		}
		return poObj(p)
	case reflect.Slice, reflect.Array:
		return poArray(poReflectType(t.Elem()))
	case reflect.Map:
		return panelSchema{"type": "object", "additionalProperties": poReflectType(t.Elem())}
	case reflect.Bool:
		return poBool("")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint64:
		return poInt("")
	case reflect.Float32, reflect.Float64:
		return panelSchema{"type": "number"}
	default:
		return poText("")
	}
}
func po(name, category, description, method, path string, mutating, adminOnly bool, schema panelSchema) aiops.PanelOperation {
	raw, _ := json.Marshal(schema)
	return aiops.PanelOperation{Name: name, Category: category, Description: description, Method: method, Path: path, Mutating: mutating, AdminOnly: adminOnly, Parameters: raw}
}
func poFilters() panelSchema {
	return poArray(poObj(panelSchema{"key": poText("后台筛选字段，如id/email/plan_id/status；不是SQL"), "condition": poEnum("筛选关系，具体字段支持范围由后台校验", "=", ">", "<", ">=", "<=", "!=", "模糊"), "value": poText("筛选值，数字也用字符串")}, "key", "condition", "value"))
}
func poPage() panelSchema {
	return panelSchema{"current": panelSchema{"type": "integer", "minimum": 1, "description": "页码，从1开始"}, "pageSize": panelSchema{"type": "integer", "minimum": 1, "maximum": 100, "description": "每页数量，最多100"}, "sort": poText("后台支持的排序字段"), "sort_type": poEnum("排序方向", "ASC", "DESC"), "filter": poFilters()}
}
func poSmallPage() panelSchema {
	return panelSchema{"current": panelSchema{"type": "integer", "minimum": 1}, "page_size": panelSchema{"type": "integer", "minimum": 1, "maximum": 100}}
}

func panelCoreOperations() []aiops.PanelOperation {
	out := []aiops.PanelOperation{}
	add := func(name, category, desc, method, path string, write, admin bool, p panelSchema, req ...string) {
		out = append(out, po(name, category, desc, method, path, write, admin, poObj(p, req...)))
	}
	for _, x := range []struct{ name, path, desc string }{
		{"system.status", "/system/getSystemStatus", "查询后台服务状态"}, {"system.queue_stats", "/system/getQueueStats", "查询队列统计"}, {"system.queue_workload", "/system/getQueueWorkload", "查询队列工作负载"}, {"system.queue_masters", "/system/getQueueMasters", "查询队列主进程（兼容接口）"},
		{"stats.overview", "/stat/getOverride", "查询仪表盘概况"}, {"stats.orders", "/stat/getOrder", "查询订单汇总"}, {"stats.server_last_rank", "/stat/getServerLastRank", "查询昨日节点排行"}, {"stats.server_today_rank", "/stat/getServerTodayRank", "查询今日节点排行"}, {"stats.invite_last_rank", "/stat/getInviteLastRank", "查询昨日邀请排行"}, {"stats.invite_today_rank", "/stat/getInviteTodayRank", "查询今日邀请排行"}, {"stats.user_last_rank", "/stat/getUserLastRank", "查询昨日用户排行"}, {"stats.user_today_rank", "/stat/getUserTodayRank", "查询今日用户排行"}, {"stats.online", "/stat/online", "查询在线人数和国家省市分布"},
	} {
		add(x.name, strings.Split(x.name, ".")[0], x.desc, "GET", x.path, false, false, panelSchema{})
	}
	add("system.logs", "system", "分页查询系统日志", "GET", "/system/getSystemLog", false, false, poFields("current:i page_size:i level"))
	for _, x := range []struct{ name, path, desc string }{{"stats.period", "/stat/getStat", "查询指定时间范围统计"}, {"stats.ranking", "/stat/getRanking", "查询指定时间范围排行"}, {"stats.records", "/stat/getStatRecord", "查询统计记录"}} {
		add(x.name, "stats", x.desc+"；start_at/end_at为Unix秒；ranking.type可为server_traffic_rank/user_consumption_rank/invite_rank；records.type可为paid_total/commission_total/register_count", "GET", x.path, false, false, poFields("start_at:i end_at:i type limit:i"))
	}
	add("stats.user", "stats", "分页查询单用户流量统计", "GET", "/stat/getStatUser", false, false, poMerge(poPage(), panelSchema{"user_id": poID()}), "user_id")
	add("stats.online_users", "stats", "查询在线地区里的账号；city范围须同时传province_code和city，未知城市可传空字符串", "GET", "/stat/online/users", false, false, poFields("scope country_code province_code city reason search page:i page_size:i"))
	add("users.list", "users", "组合筛选所有用户，支持ID范围、邮箱和套餐；不传filter即全站，按current翻页", "GET", "/user/fetch", false, false, poPage())
	add("users.detail", "users", "按ID读取用户完整业务资料及邀请关系", "GET", "/user/getUserInfoById", false, false, panelSchema{"id": poID()}, "id")
	userFields := poFields("id:i email banned:i is_admin:i is_staff:i password transfer_enable:i device_limit:i expired_at:i commission_rate:i discount:i speed_limit:i u:i d:i balance:i commission_type:i commission_balance:i plan_id:i remarks invite_user_email invite_code")
	for _, key := range []string{"device_limit", "expired_at", "commission_rate", "discount", "speed_limit", "plan_id", "remarks"} {
		userFields[key] = poNullable(userFields[key].(panelSchema))
	}
	add("users.update", "users", "修改用户资料：email、banned、is_admin、is_staff是后台保存必填字段，先读取详情并保留未更改值；金额单位分、流量单位字节。普通用户业务设置可员工操作；角色变更和管理员账号管理须管理员。", "POST", "/user/update", true, false, userFields, "id", "email", "banned", "is_admin", "is_staff")
	add("users.inviter", "users", "修改或清空邀请人，invite_user_email传完整邮箱，空串解除绑定", "POST", "/user/setInviteUser", true, false, poFields("id:i invite_user_email"), "id", "invite_user_email")
	add("users.generate", "users", "创建用户；单个传email_prefix/email_suffix，批量传generate_count和email_suffix；plan_id和expired_at可选", "POST", "/user/generate", true, false, poFields("email_prefix email_suffix password plan_id:i expired_at:i generate_count:i"), "email_suffix")
	add("users.export", "users", "按筛选导出用户CSV，敏感登录与订阅凭据不进入模型上下文", "POST", "/user/dumpCSV", false, false, panelSchema{"filter": poFilters()})
	add("users.send_mail", "users", "按filter群发邮件；省略filter就是全站。仅在用户明确要求发送时执行，沿用后台发送间隔。", "POST", "/user/sendMail", true, false, poMerge(poPage(), poFields("subject content")), "subject", "content")
	for _, x := range []struct{ name, path, desc string }{{"users.ban", "/user/ban", "批量封禁"}, {"users.unban", "/user/unban", "批量解封"}, {"users.delete_many", "/user/allDel", "按筛选删除用户，删除不可恢复"}} {
		add(x.name, "users", x.desc+"；filter必须明确目标，空数组代表全部用户须操作人明确要求", "POST", x.path, true, false, panelSchema{"filter": poFilters()}, "filter")
	}
	add("users.reset_secret", "users", "重置用户订阅Token与UUID；旧订阅失效", "POST", "/user/resetSecret", true, false, panelSchema{"id": poID()}, "id")
	add("users.delete", "users", "删除指定用户", "POST", "/user/delUser", true, false, panelSchema{"id": poID()}, "id")
	add("orders.list", "orders", "分页查询订单，filter支持用户ID比较等，is_commission=1查询佣金订单", "GET", "/order/fetch", false, false, poMerge(poPage(), poFields("is_commission:i")))
	add("orders.detail", "orders", "查询订单详细资料、佣金记录及折抵关联订单", "POST", "/order/detail", false, false, panelSchema{"id": poID()}, "id")
	add("orders.commission", "orders", "更新订单佣金状态；不修改支付通道配置", "POST", "/order/update", true, false, poFields("trade_no commission_status:i"), "trade_no", "commission_status")
	for _, x := range []struct{ name, path, desc string }{{"orders.paid", "/order/paid", "补单：将订单标记已支付并执行套餐交付"}, {"orders.cancel", "/order/cancel", "取消未完成订单"}, {"orders.refund", "/order/refund", "退款处理，沿用后台退款语义"}} {
		add(x.name, "orders", x.desc, "POST", x.path, true, false, poFields("trade_no"), "trade_no")
	}
	add("orders.assign", "orders", "为用户创建并分配套餐订单；金额total_amount单位分", "POST", "/order/assign", true, false, poFields("email plan_id:i total_amount:i period"), "email", "plan_id", "total_amount", "period")
	add("tickets.list", "tickets", "全站工单分页，无须用户ID；status=0未关闭，可按email/status/reply_status筛选；id可查询指定工单。长消息优先使用专用ticket_get/ticket_message_get工具。", "GET", "/ticket/fetch", false, false, poMerge(poPage(), poFields("id:i status:i email"), panelSchema{"reply_status": poArray(poInt("0待回复，1已回复"))}))
	add("tickets.reply", "tickets", "回复指定工单，消息发送给用户，沿用邮件提醒", "POST", "/ticket/reply", true, false, poFields("id:i message"), "id", "message")
	add("tickets.close", "tickets", "关闭指定工单", "POST", "/ticket/close", true, false, panelSchema{"id": poID()}, "id")
	add("support.operations", "support", "查看员工操作记录；管理员可查看全部", "GET", "/support/operations", false, true, poFields("page:i"))
	add("support.clear_operations", "support", "管理员清理through_id及以前的员工操作日志，必须confirm=true", "POST", "/support/operations/clear", true, true, poFields("through_id:i confirm:b"), "through_id", "confirm")
	return out
}
