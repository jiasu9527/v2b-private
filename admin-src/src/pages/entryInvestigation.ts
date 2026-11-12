import type { EvidenceCase } from './entryEvidence';

export type InvestigationBranchName = 'A' | 'B';
export type InvestigationControlStatus = 'healthy' | 'abnormal' | 'unknown';
export type EntryInvestigationBranch = {
  branch: InvestigationBranchName; group_id: number; host: string; assigned_users: number;
  acquired_users: number; unrecorded_users: number; pre_failure_users: number;
  health: string; last_reported_at: number; blocked_at?: number;
};
export type EntryInvestigationFailure = {
  branch: InvestigationBranchName; blocked_at: number; confirmed_at: number; confirmed_by: number;
  note: string; control_status: InvestigationControlStatus;
};
export type EntryInvestigation = {
  case_id: number; root_case_id: number; round: number; revision: number; created_by: number;
  created_at: number; updated_at: number; next_case_id?: number;
  settings: { case_id: number; revision: number; blocked_at: number; note: string; min_observe_seconds: number; max_rounds: number; stop_users: number };
  failures: EntryInvestigationFailure[];
  report: {
    state: string; reason: string; checked_at: number; executed_at?: number; observed_seconds: number;
    branches: EntryInvestigationBranch[]; warnings: string[]; blockers: string[]; evidence_risks?: string[];
    next_query?: EvidenceCase['query']; requires_confirmation: boolean;
  };
};

export const INVESTIGATION_STATES: Record<string, { label: string; color?: string }> = {
  awaiting_split_confirmation: { label: '等待确认分流', color: 'blue' },
  awaiting_block_confirmation: { label: '等待核实封锁', color: 'orange' },
  review_candidates: { label: '小名单待人工复核', color: 'purple' },
  continued: { label: '下一轮已冻结', color: 'blue' },
  awaiting_split: { label: '等待确认分流', color: 'blue' },
  awaiting_execution: { label: '等待确认分流', color: 'blue' },
  waiting_split: { label: '等待确认分流', color: 'blue' },
  waiting_delivery: { label: '等待实际下发', color: 'blue' },
  observing: { label: '观察中', color: 'blue' },
  waiting_observation: { label: '观察中', color: 'blue' },
  ready_next: { label: '可预备下一轮', color: 'green' },
  needs_review: { label: '待人工复核', color: 'orange' },
  manual_review: { label: '待人工复核', color: 'orange' },
  evidence_gap: { label: '证据不足', color: 'orange' },
  insufficient_evidence: { label: '证据不足', color: 'orange' },
  both_failed: { label: '两组异常', color: 'orange' },
  both_blocked: { label: '两组异常', color: 'orange' },
  control_abnormal: { label: '对照异常', color: 'orange' },
  small_cohort: { label: '少量候选，待复核', color: 'purple' },
  round_limit: { label: '达到轮次上限', color: 'orange' },
  next_prepared: { label: '下一轮已冻结', color: 'blue' },
  next_ready: { label: '下一轮已冻结', color: 'blue' },
};
export const INVESTIGATION_HEALTH: Record<string, string> = {
  archived: '已归档的检测状态',
  healthy: '近期探针正常', unhealthy: '探针异常', down: '探针异常', failed: '探针异常',
  degraded: '探针状态不一致', unknown: '缺少有效检测', stale: '检测已过期',
  unmonitored: '未配置监测', no_probe: '无可用探针', probe_offline: '探针离线',
  unconfigured: '未配置监测', insufficient: '检测不足', checking: '等待检测',
};
export const INVESTIGATION_CONTROL: Record<InvestigationControlStatus, string> = {
  healthy: '对照入口正常（已人工核对）', abnormal: '对照入口也异常', unknown: '未设置或尚未核对',
};
export const INVESTIGATION_NOT_FOUND = '排查档案或目标不存在';

export function parseEntryInvestigation(value: unknown, caseID?: number): EntryInvestigation {
  const task = value as EntryInvestigation;
  if (!task || !Number.isSafeInteger(task.case_id) || task.case_id <= 0 ||
      (caseID !== undefined && task.case_id !== caseID) || !Number.isSafeInteger(task.revision) || task.revision <= 0 ||
      !task.report || typeof task.report.state !== 'string' || !task.settings ||
      !Array.isArray(task.report.branches) || !Array.isArray(task.report.warnings) || !Array.isArray(task.report.blockers) ||
      !Array.isArray(task.failures)) throw new Error('排查任务返回无效，请刷新核对，不要重复提交写入');
  return task;
}

export function investigationCanPrepareNext(task: EntryInvestigation | null): boolean {
  return !!task && task.report.state === 'ready_next' && task.report.blockers.length === 0 &&
    !!task.report.next_query && !(task.next_case_id > 0);
}

export function investigationStartPayload(record: EvidenceCase, form: {
  blockedAt: number; note: string; hours: number; maxRounds: number; stopUsers: number; confirmed: boolean;
}, now = Math.floor(Date.now() / 1000)) {
  if (!form.confirmed) throw new Error('请先确认已核实入口被封锁，不是仅凭单次探针失败');
  if (record.status !== 'frozen' || record.parent_case_id > 0 || !(record.id > 0) || !(record.revision > 0)) throw new Error('请从已冻结、尚未执行的档案开启排查');
  if (!Number.isSafeInteger(form.blockedAt) || form.blockedAt <= record.query.from || form.blockedAt > now || record.query.to !== form.blockedAt) throw new Error('首次异常时间须与冻结候选窗口结束时间相同；时间不符请重新冻结名单');
  const note = form.note.trim();
  if (!note || note.includes('\0') || [...note].length > 2000) throw new Error('请填写 1～2000 字的确认依据，不能包含空字符');
  if (!Number.isInteger(form.hours) || form.hours < 1 || form.hours > 168) throw new Error('最短观察时间须为 1～168 小时');
  if (!Number.isInteger(form.maxRounds) || form.maxRounds < 1 || form.maxRounds > 30) throw new Error('最大轮数须为 1～30');
  if (!Number.isInteger(form.stopUsers) || form.stopUsers < 2 || form.stopUsers > 8) throw new Error('人工复核人数须为 2～8');
  return { case_id: record.id, revision: record.revision, blocked_at: form.blockedAt, note, min_observe_seconds: form.hours * 3600, max_rounds: form.maxRounds, stop_users: form.stopUsers };
}

export function investigationConfirmPayload(task: EntryInvestigation, form: {
  branch: InvestigationBranchName; blockedAt: number; note: string; controlStatus: InvestigationControlStatus; confirmed: boolean;
}, now = Math.floor(Date.now() / 1000)) {
  if (!form.confirmed) throw new Error('请确认已人工核实该分支入口被封锁');
  if (!['A', 'B'].includes(form.branch) || !task.report.branches.some((item) => item.branch === form.branch)) throw new Error('分支已变化，请刷新任务');
  if (!task.report.executed_at || !Number.isSafeInteger(form.blockedAt) || form.blockedAt <= task.report.executed_at || form.blockedAt > now) throw new Error('首次异常时间须在本轮分流开始之后，且不能在未来');
  const note = form.note.trim();
  if (!note || note.includes('\0') || [...note].length > 2000) throw new Error('请填写 1～2000 字的确认依据，不能包含空字符');
  if (!Object.prototype.hasOwnProperty.call(INVESTIGATION_CONTROL, form.controlStatus)) throw new Error('请选择实际核对的对照状态');
  return { case_id: task.case_id, revision: task.revision, branch: form.branch, blocked_at: form.blockedAt, note, control_status: form.controlStatus };
}
