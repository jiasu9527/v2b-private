package httpapi

import "forest/go-api/internal/aiops"

func panelCommissionOperations() []aiops.PanelOperation {
	out := []aiops.PanelOperation{}
	add := func(name, description, path string, mutating bool, props panelSchema, required ...string) {
		out = append(out, po(name, "commissions", description, "POST", "/commission-log/"+path, mutating, false, poObj(props, required...)))
	}
	filter := poObj(panelSchema{
		"id": poID(), "ids": panelSchema{"type": "array", "items": poID(), "minItems": 1, "maxItems": 10000, "uniqueItems": true},
		"invite_user_id": poID(), "user_id": poID(), "trade_no": poText("完整订单号，精确匹配"),
		"created_from":      panelSchema{"type": "integer", "minimum": 0, "description": "流水创建Unix秒，含下界"},
		"created_to":        panelSchema{"type": "integer", "minimum": 0, "description": "流水创建Unix秒，不含上界"},
		"commission_status": panelSchema{"type": "integer", "enum": []int{0, 1, 2, 3}, "description": "关联订单状态：0待确认、1发放中、2已发放、3已驳回；0不代表无佣金"},
		"visibility":        poEnum("展示状态，不是结算状态：active正常/deleted已隐藏/all全部；默认active", "active", "deleted", "all"),
	})
	add("commission_log.list", "独立返佣流水分页，严格AND筛选，返回total与total_amount（get_amount合计分）、applied_filters；余额不能由历史流水推算。状态来自当前关联订单，记录隐藏不改变资金。", "list", false, panelSchema{"filter": filter, "current": panelSchema{"type": "integer", "minimum": 1}, "page_size": panelSchema{"type": "integer", "minimum": 1, "maximum": 100}})
	add("commission_log.preview", "必须先预览再执行：冻结本次ID及原记录，返回条数、合计分、10分钟preview_hash。delete=隐藏明细，restore=恢复。既不改余额也不改订单佣金状态；空目标筛选需confirm_all=true，最多10000条，超出须缩小范围。此查询会保存预览但不变更业务记录。", "preview", false, panelSchema{"filter": filter, "action": poEnum("操作", "delete", "restore"), "reason": panelSchema{"type": "string", "minLength": 1, "maxLength": 500}, "confirm_all": poBool("无ID/邀请人/用户/订单/时间/佣金状态筛选时，确认全范围；仅visibility不算限定目标")}, "filter", "action", "reason")
	apply := panelSchema{"preview_hash": poText("必须使用同一操作人刚取得的真实preview_hash，不得猜测"), "idempotency_key": poText("同一执行请求的稳定唯一键，网络重试必须复用；不同内容不得复用"), "confirm": poBool("核对预览条数和金额后明确true"), "confirm_all": poBool("全范围预览执行时再次true")}
	single := poMerge(apply, panelSchema{"id": poID()})
	add("commission_log.delete", "按ID软隐藏单条返佣明细，必须先对该ID做delete预览；保留金额及审计，可恢复，不改余额、不阻止未结算订单发佣。", "delete", true, single, "id", "preview_hash", "idempotency_key", "confirm")
	add("commission_log.delete_many", "仅隐藏preview_hash冻结的返佣记录，原子校验和幂等执行；不接受执行时新筛选，不物理删除、不扣余额。不得用orders.commission代替。", "delete-many", true, apply, "preview_hash", "idempotency_key", "confirm")
	add("commission_log.restore", "恢复restore预览冻结的已隐藏明细；不重新发佣、不修改余额或订单状态。", "restore", true, apply, "preview_hash", "idempotency_key", "confirm")
	return out
}
