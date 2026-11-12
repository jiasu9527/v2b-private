package httpapi

import "forest/go-api/internal/aiops"

func panelEntryInvestigationOperations() []aiops.PanelOperation {
	const base = "/server/client-entry-user-policy/investigations/"
	caseID := panelSchema{"case_id": poID()}
	versioned := poMerge(caseID, panelSchema{"revision": poMerge(poID(), panelSchema{"description": "排查任务当前revision；不是规则revision或档案revision"})})
	blockedAt := panelSchema{"type": "integer", "minimum": 1, "description": "已核实的首次封锁时间Unix秒；可取用户聊天明确提供的时间或核对的事件记录，缺失不猜测，不把TCP失败直接判为封锁"}
	note := panelSchema{"type": "string", "minLength": 1, "maxLength": 2000, "description": "核实依据和范围；可如实记录操作人在聊天中确认的事实，无需再去页面勾选，不填写凭据或直接认定用户责任"}
	start := poMerge(versioned, panelSchema{
		"revision":            poMerge(poID(), panelSchema{"description": "待启动的冻结证据档案当前revision；先读entry_evidence.case"}),
		"blocked_at":          blockedAt,
		"note":                note,
		"min_observe_seconds": panelSchema{"type": "integer", "minimum": 300, "maximum": 604800, "description": "最少观察秒数，省略默认24小时；时间到不代表用户可排除"},
		"max_rounds":          panelSchema{"type": "integer", "minimum": 1, "maximum": 30, "description": "最大轮数，省略默认10轮；达到后转人工复核"},
		"stop_users":          panelSchema{"type": "integer", "minimum": 2, "maximum": 100, "description": "候选缩至该人数时停止继续二分并人工复核，省略默认8人"},
	})
	confirm := poMerge(versioned, panelSchema{
		"branch":         poEnum("实际执行分支，只能A或B", "A", "B"),
		"blocked_at":     blockedAt,
		"note":           note,
		"control_status": poEnum("人工核对未下发对照入口的状态；unknown不等于健康", "healthy", "abnormal", "unknown"),
	})
	page := panelSchema{
		"current":   panelSchema{"type": "integer", "minimum": 1, "maximum": 10000},
		"page_size": panelSchema{"type": "integer", "minimum": 1, "maximum": 100},
	}
	return []aiops.PanelOperation{
		po("entry_evidence.investigation_start", "entries", "从已有冻结档案启动入口排查；用户可在聊天确认被墙及范围，AI代办freeze/start，不需用户打开页面。沿用当前会话权限，完全访问自动执行，逐次确认仍用操作卡。持久保存封锁时间、依据及观察设置；本操作不改入口。先读case核对实际IP、窗口和人数，blocked_at须等于query.to；不能用TCP失败冒充确认。普通日志滚动不删除冻结名单", "POST", base+"investigation-start", true, false, poObj(start, "case_id", "revision", "blocked_at", "note")),
		po("entry_evidence.investigation", "entries", "只读读取持久排查进度、A/B实际下发/未记录人数、证据缺口和下一步建议；checked_at为报告更新时间，读取不会触发检测或换入口。缺日志不等于没拿到，异常不等于某用户造成；恢复中断任务先读此接口", "GET", base+"investigation", false, false, poObj(caseID, "case_id")),
		po("entry_evidence.investigation_list", "entries", "只读分页列出持久排查任务与摘要，关闭聊天或切模型不会丢失任务；不自动执行下一轮入口变更", "GET", base+"investigation-list", false, false, poObj(page)),
		po("entry_evidence.investigation_refresh", "entries", "刷新排查证据与进度报告，仅保存观察结果，不发起网络探测、不换入口、不二分。状态需人工确认或证据不足时不得推断内鬼或强行推进", "POST", base+"investigation-refresh", true, false, poObj(caseID, "case_id")),
		po("entry_evidence.investigation_confirm", "entries", "记录操作人已在聊天或页面明确核实的A/B封锁及对照状态，使用任务revision，沿用当前会话权限，无需重复页面确认；仅记事实不改入口。不知道对照状态就用unknown，不能编造健康。两边异常、对照异常或日志缺口时保留不确定性，不能自行挑一边归责", "POST", base+"investigation-confirm", true, false, poObj(confirm, "case_id", "revision", "branch", "blocked_at", "note", "control_status")),
		po("entry_evidence.investigation_next", "entries", "仅在ready_next时依据服务端记录冻结下一轮证据名单，返回新case_id，不立即改入口。AI随后读新case、split_preview核对再split_execute；完全访问模式在已授权排查范围内连续办理，逐次确认模式沿用操作卡，无需用户去页面填表或重复上传名单。剩余用户与其他分支保留观察，不认定无嫌疑", "POST", base+"investigation-next", true, false, poObj(versioned, "case_id", "revision")),
	}
}
