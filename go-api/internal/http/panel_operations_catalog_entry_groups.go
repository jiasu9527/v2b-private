package httpapi

import "forest/go-api/internal/aiops"

func panelEntryGroupOperations() []aiops.PanelOperation {
	const base = "/server/client-entry-user-policy/"
	ids := func(description string, required bool) panelSchema {
		s := panelSchema{"type": "array", "items": poID(), "maxItems": 100, "description": description}
		if required {
			s["minItems"] = 1
		}
		return s
	}
	groups := ids("真实固定叶子group_id，不是policy_id、节点ID或非叶子父组；明确选定范围，不能据名称猜ID", true)
	replacements := ids("已经建好且需要保留的新固定叶子ID；服务端计算名单并集，不回传上千用户ID", false)
	hash := panelSchema{"type": "string", "minLength": 64, "maxLength": 64, "description": "预览返回的64字符preview_hash，原样使用"}
	return []aiops.PanelOperation{
		po("entry_groups.audit", "entries", "分页审计固定叶子：分配人数、窗口内实际完整成功下发去重人数、与新组名单的交集和剩余人数；summary是全部选中组用户并集，不能累加各组人数。省略group_ids查看当前叶子；默认近7天，后续页携带返回from/to与revision作为expected_revision。历史无记录不能当从未换IP或无人使用；名单覆盖不等于路由覆盖，清理前仍须retire_preview", "GET", base+"group-audit", false, true, poObj(panelSchema{
			"group_ids": groups, "replacement_group_ids": replacements,
			"from":              panelSchema{"type": "integer", "minimum": 1, "description": "起始Unix秒含开始；与to同时传递，允许分钟级窗口"},
			"to":                panelSchema{"type": "integer", "minimum": 1, "description": "结束Unix秒不含结束；翻页原样使用返回窗口"},
			"current":           panelSchema{"type": "integer", "minimum": 1, "maximum": 10000},
			"page_size":         panelSchema{"type": "integer", "minimum": 1, "maximum": 50},
			"expected_revision": poText("后续页必填首读revision，配置或名单变化则409重查，不能拼接混合版本"),
		})),
		po("entry_groups.retire_preview", "entries", "冻结指定旧叶子的回收预览和备份，不删除规则。返回preview_id/hash、can_execute、人数、空祖先/空规则、IP引用和阻塞原因。保留未选兄弟、其他规则、名单及排序；合集关联等不能安全处理时明确阻塞。默认仅允许新组已接管的用户；用户已明确其余走保底时allow_fallback=true并指定真实fallback_policy_id，服务端验证节点和条件。不能用整条entry_rules.delete代替叶子清理", "POST", base+"retirement/preview", true, true, poObj(panelSchema{
			"group_ids": groups, "replacement_group_ids": replacements,
			"allow_fallback":     poBool("仅用户已授权剩余走保底时为true；沿用会话授权，不需反复询问"),
			"fallback_policy_id": poID(),
			"reason":             panelSchema{"type": "string", "minLength": 1, "maxLength": 500, "description": "清理目的及用户已授权的剩余用户去向"},
		}, "group_ids", "reason")),
		po("entry_groups.retire", "entries", "执行can_execute=true的服务端回收预览，单事务清理选中旧叶子、空祖先/空规则及对应监测，解除IP占用但不删IP库存/健康记录，不改网卡。名单或配置已变化拒绝并重新预览；同幂等键返回原回执。超时或结果未知先用entry_groups.retirement传preview_id核对，不能重复删除或改走SSH。released不等于健康备用，完成后查询IP池状态", "POST", base+"retirement/execute", true, true, poObj(panelSchema{
			"preview_id": poID(), "preview_hash": hash,
			"idempotency_key": panelSchema{"type": "string", "minLength": 1, "maxLength": 128, "description": "本次操作稳定唯一标识，重试保持不变"},
		}, "preview_id", "preview_hash", "idempotency_key")),
		po("entry_groups.retirement", "entries", "只读核对回收预览的持久执行回执。id填写提交前已知的preview_id，不是operation_id；committed=true才表示已提交，previewed/blocked/expired均不能宣称已删除。结果未知时优先用本接口核对", "GET", base+"retirement/receipt", false, true, poObj(panelSchema{"id": poID()}, "id")),
	}
}
