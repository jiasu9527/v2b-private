package httpapi

import "forest/go-api/internal/admin"

func panelMonitorSaveSchema(incremental bool) panelSchema {
	if !incremental {
		schema := poReflect(admin.ClientEntryMonitorSaveRequest{})
		schema["required"] = []string{"revision", "items"}
		return schema
	}
	bounded := func(min, max int, description string) panelSchema {
		return panelSchema{"type": "integer", "minimum": min, "maximum": max, "description": description}
	}
	target := poObj(panelSchema{
		"source_key":         panelSchema{"type": "string", "minLength": 1, "maxLength": 255, "description": "entry_monitors.config返回的完整source_key，不是节点ID或探测target_id"},
		"port":               bounded(1, 65535, "实际入口检测端口；保留已有值"),
		"auto_split_enabled": poBool("保留原自动二分开关；新监测默认false，只有明确要求才开启"),
	}, "source_key", "port", "auto_split_enabled")
	item := poObj(panelSchema{
		"policy_id": poID(), "enabled": poBool("是否启用该规则的监测"),
		"check_interval_sec": bounded(5, 3600, "检测间隔秒数，新建通常30"),
		"tcp_timeout_ms":     bounded(100, 60000, "TCP超时毫秒，新建通常5000"),
		"failure_threshold":  bounded(2, 10, "连续失败确认次数，新建通常3"),
		"success_threshold":  bounded(1, 10, "连续成功确认次数，新建通常2"),
		"targets":            poArray(target),
	})
	items := poArray(item)
	item["required"] = []string{"policy_id", "enabled", "check_interval_sec", "tcp_timeout_ms", "failure_threshold", "success_threshold", "targets"}
	items["minItems"], items["maxItems"] = 1, 20
	item["properties"].(panelSchema)["targets"].(panelSchema)["minItems"] = 1
	return poObj(panelSchema{"revision": panelSchema{"type": "integer", "minimum": 1, "description": "最新监测配置revision；不是入口排序revision或探测目标版本"}, "items": items}, "revision", "items")
}
