import React, { useEffect, useMemo, useRef, useState } from 'react';
import {
  Button,
  Divider,
  InputNumber,
  Space,
  Spin,
  Switch,
  Table,
  Tag,
  Tooltip,
  message,
} from 'antd';
import {
  PlayCircleOutlined,
  ReloadOutlined,
  SaveOutlined,
} from '@ant-design/icons';
import { apiGet, apiJsonPost, apiPut, unwrapData } from '../lib/api';
import './DNSFailoverEntryMonitorTab.css';

const ENTRY_MONITOR_PATH = '/dns-failover/entry-monitors';
const ENTRY_MONITOR_FAILURE_THRESHOLD = 2;

type EntryPolicyAction = 'override' | 'original' | 'hide';

type EntryProbe = Record<string, any> & {
  id: number;
  name: string;
  enabled: boolean;
};

type EntryPolicyMember = Record<string, any> & {
  source_key?: string;
  server_type?: string;
  server_id?: number;
  name?: string;
  host?: string;
  port?: number;
};

type EntryPolicy = Record<string, any> & {
  id: number;
  name: string;
  action: EntryPolicyAction;
  entry_host?: string;
  enabled: boolean;
  members: EntryPolicyMember[];
  targets: any[];
};

type EntryTargetState = Record<string, any> & {
  stale: boolean;
};

type EntryTarget = Record<string, any> & {
  id?: number;
  source_key: string;
  name?: string;
  host: string;
  port: number;
  sort: number;
  states: EntryTargetState[];
};

type EntryMonitorDraft = {
  policy_id: number;
  policy_name: string;
  action: EntryPolicyAction;
  enabled: boolean;
  check_interval_sec: number;
  tcp_timeout_ms: number;
  targets: EntryTarget[];
};

type EntryMonitorPayload = {
  revision?: number;
  items?: any[];
  policies?: any[];
  probes?: any[];
};

type NormalizedEntryMonitorPayload = {
  revision: number;
  policies: EntryPolicy[];
  probes: EntryProbe[];
  drafts: Record<number, EntryMonitorDraft>;
  configuredPolicyIDs: number[];
};

type SavedEntryMonitorConfiguration = {
  policyIDs: number[];
  revision: number;
};

type EntryMonitorTabProps = {
  active: boolean;
};

function list(value: any): any[] {
  if (Array.isArray(value)) return value;
  if (typeof value !== 'string' || !value.trim()) return [];
  try {
    const parsed = JSON.parse(value);
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

function numberValue(value: any, fallback = 0) {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : fallback;
}

function boolValue(value: any, fallback = false) {
  if (value === undefined || value === null || value === '') return fallback;
  if (typeof value === 'boolean') return value;
  const normalized = String(value).trim().toLowerCase();
  return normalized !== '0' && normalized !== 'false' && normalized !== 'off';
}

function uniqueIDs(values: any[]) {
  return Array.from(new Set(values.map((value) => numberValue(value)).filter((value) => value > 0)));
}

function formatTime(value: any) {
  if (value === undefined || value === null || value === '') return '—';
  if (typeof value === 'string' && !/^\d+(?:\.\d+)?$/.test(value.trim())) {
    const parsed = new Date(value);
    return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString();
  }
  const timestamp = numberValue(value);
  if (!timestamp) return '—';
  const milliseconds = timestamp < 1_000_000_000_000 ? timestamp * 1000 : timestamp;
  return new Date(milliseconds).toLocaleString();
}

function normalizeAction(value: any): EntryPolicyAction {
  if (value === 'original' || value === 'hide') return value;
  return 'override';
}

function memberSourceKey(member: EntryPolicyMember, index: number) {
  const explicit = String(member.source_key || '').trim();
  if (explicit) return explicit;
  const serverType = String(member.server_type || member.type || '').trim().toLowerCase();
  const serverID = numberValue(member.server_id ?? member.id);
  if (serverType && serverID > 0) return `${serverType}:${serverID}`;
  return `member:${index + 1}`;
}

function memberHost(member: EntryPolicyMember) {
  return String(
    member.host
    || member.server_host
    || member.address
    || member.entry_host
    || member.dns_value
    || '',
  ).trim();
}

function normalizePolicy(raw: any): EntryPolicy {
  return {
    ...raw,
    id: numberValue(raw?.id),
    name: String(raw?.name || '').trim() || `规则 #${numberValue(raw?.id)}`,
    action: normalizeAction(raw?.action),
    entry_host: String(raw?.entry_host || '').trim(),
    enabled: boolValue(raw?.enabled, true),
    members: list(raw?.members),
    targets: list(raw?.targets),
  };
}

function derivedPolicyTargets(policy: EntryPolicy): EntryTarget[] {
  if (policy.action === 'hide') return [];
  if (policy.targets.length) {
    return policy.targets.map((target, index) => ({
      ...target,
      source_key: String(target?.source_key || `target:${index + 1}`).trim(),
      host: String(target?.host || '').trim(),
      port: numberValue(target?.port ?? target?.suggested_port, 443),
      sort: numberValue(target?.sort, index),
      states: [],
    })).filter((target) => target.source_key && target.host);
  }
  if (policy.action === 'override') {
    const host = String(policy.entry_host || '').trim();
    return host ? [{
      source_key: 'entry_host',
      host,
      port: 443,
      sort: 0,
      states: [],
    }] : [];
  }
  return policy.members.map((member, index) => ({
    source_key: memberSourceKey(member, index),
    host: memberHost(member),
    port: numberValue(member.port || member.server_port, 443),
    sort: index,
    states: [],
  })).filter((target) => target.host);
}

function normalizeTarget(raw: any, index: number): EntryTarget {
  return {
    ...raw,
    id: numberValue(raw?.id) || undefined,
    source_key: String(raw?.source_key || `target:${numberValue(raw?.id) || index + 1}`).trim(),
    host: String(raw?.host || '').trim(),
    port: numberValue(raw?.port, 443),
    sort: numberValue(raw?.sort, index),
    states: list(raw?.states).map((state) => ({
      ...state,
      stale: boolValue(state?.stale),
    })),
  };
}

function normalizeDraft(policy: EntryPolicy, item?: any): EntryMonitorDraft {
  const configuredTargets = list(item?.targets).map(normalizeTarget);
  return {
    policy_id: policy.id,
    policy_name: String(item?.policy_name || policy.name),
    action: normalizeAction(item?.action || policy.action),
    enabled: boolValue(item?.enabled, true),
    check_interval_sec: numberValue(item?.check_interval_sec, 30),
    tcp_timeout_ms: numberValue(item?.tcp_timeout_ms, 3000),
    targets: (configuredTargets.length ? configuredTargets : derivedPolicyTargets(policy))
      .sort((left, right) => left.sort - right.sort),
  };
}

function mergeLiveState(current: Record<number, EntryMonitorDraft>, incoming: Record<number, EntryMonitorDraft>) {
  const result = { ...current };
  Object.entries(incoming).forEach(([rawPolicyID, next]) => {
    const policyID = Number(rawPolicyID);
    const existing = current[policyID];
    if (!existing) {
      result[policyID] = next;
      return;
    }
    const incomingBySource = new Map(next.targets.map((target) => [target.source_key, target]));
    const existingSources = new Set(existing.targets.map((target) => target.source_key));
    result[policyID] = {
      ...existing,
      policy_name: next.policy_name,
      action: next.action,
      targets: [
        ...existing.targets.map((target) => {
          const live = incomingBySource.get(target.source_key);
          return live ? {
            ...target,
            id: live.id,
            host: live.host || target.host,
            sort: live.sort,
            states: live.states,
          } : target;
        }),
        ...next.targets.filter((target) => !existingSources.has(target.source_key)),
      ].sort((left, right) => left.sort - right.sort),
    };
  });
  return result;
}

function isPolicySelectable(policy: EntryPolicy) {
  return policy.enabled && policy.action !== 'hide' && derivedPolicyTargets(policy).length > 0;
}

function normalizeConfigurationPayload(raw: EntryMonitorPayload): NormalizedEntryMonitorPayload {
  const policies = list(raw.policies).map(normalizePolicy).filter((policy) => policy.id > 0);
  const items = list(raw.items);
  const itemByPolicyID = new Map(items.map((item) => [numberValue(item?.policy_id), item]));
  const drafts: Record<number, EntryMonitorDraft> = {};
  policies.forEach((policy) => {
    drafts[policy.id] = normalizeDraft(policy, itemByPolicyID.get(policy.id));
  });
  const selectablePolicyIDs = new Set(policies.filter(isPolicySelectable).map((policy) => policy.id));
  return {
    revision: numberValue(raw.revision),
    policies,
    probes: list(raw.probes).map((probe) => ({
      ...probe,
      id: numberValue(probe?.id),
      name: String(probe?.name || '').trim() || `探针 #${numberValue(probe?.id)}`,
      enabled: boolValue(probe?.enabled),
    })).filter((probe) => probe.id > 0),
    drafts,
    configuredPolicyIDs: uniqueIDs(items.map((item) => item?.policy_id))
      .filter((policyID) => selectablePolicyIDs.has(policyID)),
  };
}

function actionTag(action: EntryPolicyAction) {
  if (action === 'hide') return <Tag>隐藏规则</Tag>;
  if (action === 'original') return <Tag color="blue">节点原地址</Tag>;
  return <Tag color="cyan">独立入口</Tag>;
}

function stateSuccess(state: EntryTargetState) {
  const value = state.last_success ?? state.success;
  if (value === undefined || value === null || value === '') return undefined;
  return boolValue(value);
}

function stateLatency(state: EntryTargetState) {
  const value = state.last_latency_ms ?? state.latency_ms;
  return value === undefined || value === null || value === '' ? undefined : numberValue(value);
}

function stateIP(state: EntryTargetState) {
  return String(state.last_resolved_ip || state.resolved_ip || '').trim();
}

function stateError(state: EntryTargetState) {
  return String(state.last_error || state.error || '').trim();
}

function stateTime(state: EntryTargetState) {
  return state.last_reported_at ?? state.reported_at ?? state.checked_at ?? state.updated_at ?? state.created_at;
}

function runStatusTag(run: any) {
  if (run?.success !== undefined && run?.success !== null) {
    return boolValue(run.success) ? <Tag color="success">成功</Tag> : <Tag color="error">失败</Tag>;
  }
  const status = String(run?.status || run?.outcome || '').trim().toLowerCase();
  const label: Record<string, string> = {
    queued: '等待中',
    pending: '等待中',
    running: '检测中',
    completed: '已完成',
    succeeded: '已完成',
    success: '已完成',
    partial: '部分失败',
    failed: '失败',
    error: '失败',
    timeout: '已超时',
  };
  const color = ['completed', 'succeeded', 'success'].includes(status)
    ? 'success'
    : ['failed', 'error', 'timeout'].includes(status)
      ? 'error'
      : ['queued', 'pending', 'running'].includes(status)
        ? 'processing'
        : 'default';
  return <Tag color={color}>{label[status] || status || '—'}</Tag>;
}

export default function DNSFailoverEntryMonitorTab({ active }: EntryMonitorTabProps) {
  const [policies, setPolicies] = useState<EntryPolicy[]>([]);
  const [probes, setProbes] = useState<EntryProbe[]>([]);
  const [drafts, setDrafts] = useState<Record<number, EntryMonitorDraft>>({});
  const [selectedPolicyIDs, setSelectedPolicyIDs] = useState<number[]>([]);
  const [expandedPolicyIDs, setExpandedPolicyIDs] = useState<number[]>([]);
  const [runs, setRuns] = useState<any[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [running, setRunning] = useState(false);

  const policiesRef = useRef<EntryPolicy[]>([]);
  const draftsRef = useRef<Record<number, EntryMonitorDraft>>({});
  const selectedPolicyIDsRef = useRef<number[]>([]);
  const loadedRef = useRef(false);
  const dirtyRef = useRef(false);
  const savingRef = useRef(false);
  const runningRef = useRef(false);
  const revisionRef = useRef(0);
  const editVersionRef = useRef(0);
  const configurationRequestSequenceRef = useRef(0);
  const runsRequestSequenceRef = useRef(0);
  const loadingRequestSequenceRef = useRef(0);

  const policyByID = useMemo(
    () => new Map(policies.map((policy) => [policy.id, policy])),
    [policies],
  );
  const probeByID = useMemo(
    () => new Map(probes.map((probe) => [probe.id, probe])),
    [probes],
  );

  const fetchConfiguration = async (quiet = false) => {
    const requestSequence = ++configurationRequestSequenceRef.current;
    const requestEditVersion = editVersionRef.current;
    try {
      const payload = (unwrapData(await apiGet(ENTRY_MONITOR_PATH)) || {}) as EntryMonitorPayload;
      if (requestSequence !== configurationRequestSequenceRef.current) return false;
      const next = normalizeConfigurationPayload(payload);
      const preserveEdits = dirtyRef.current || editVersionRef.current !== requestEditVersion;

      policiesRef.current = next.policies;
      setPolicies(next.policies);
      setProbes(next.probes);
      if (preserveEdits) {
        const mergedDrafts = mergeLiveState(draftsRef.current, next.drafts);
        draftsRef.current = mergedDrafts;
        setDrafts(mergedDrafts);
      } else {
        draftsRef.current = next.drafts;
        selectedPolicyIDsRef.current = next.configuredPolicyIDs;
        revisionRef.current = next.revision;
        dirtyRef.current = false;
        setDrafts(next.drafts);
        setSelectedPolicyIDs(next.configuredPolicyIDs);
        setDirty(false);
      }
      loadedRef.current = true;
      setLoaded(true);
      return true;
    } catch (error: any) {
      if (requestSequence !== configurationRequestSequenceRef.current) return false;
      if (!quiet) message.error(error?.message || '加载用户入口检测失败');
      throw error;
    }
  };

  const fetchRuns = async (quiet = false) => {
    const requestSequence = ++runsRequestSequenceRef.current;
    try {
      const payload = unwrapData(await apiGet(`${ENTRY_MONITOR_PATH}/runs`, { limit: 20 })) || {};
      if (requestSequence !== runsRequestSequenceRef.current) return false;
      setRuns(Array.isArray(payload) ? payload : list(payload.items));
      return true;
    } catch (error: any) {
      if (requestSequence !== runsRequestSequenceRef.current) return false;
      if (!quiet) message.error(error?.message || '加载近期检测失败');
      throw error;
    }
  };

  const refresh = async (quiet = false) => {
    const loadingRequestSequence = quiet ? 0 : ++loadingRequestSequenceRef.current;
    if (!quiet) setLoading(true);
    try {
      await Promise.all([
        fetchConfiguration(quiet),
        fetchRuns(quiet),
      ]);
    } catch {
      // Individual loaders surface non-background errors.
    } finally {
      if (!quiet && loadingRequestSequence === loadingRequestSequenceRef.current) setLoading(false);
    }
  };

  useEffect(() => {
    if (!active || loaded) return;
    void refresh();
  }, [active, loaded]);

  useEffect(() => {
    if (!active || !loaded) return;
    const timer = window.setInterval(() => {
      void refresh(true);
    }, 10000);
    return () => window.clearInterval(timer);
  }, [active, loaded]);

  const markEdited = () => {
    editVersionRef.current += 1;
    dirtyRef.current = true;
    setDirty(true);
  };

  const updateDraft = (policyID: number, patch: Partial<EntryMonitorDraft>) => {
    const currentDraft = draftsRef.current[policyID];
    if (!currentDraft) return;
    const nextDrafts = {
      ...draftsRef.current,
      [policyID]: { ...currentDraft, ...patch },
    };
    draftsRef.current = nextDrafts;
    setDrafts(nextDrafts);
    markEdited();
  };

  const updateTargetPort = (policyID: number, sourceKey: string, port: number | null) => {
    const draft = draftsRef.current[policyID];
    if (!draft) return;
    updateDraft(policyID, {
      targets: draft.targets.map((target) => target.source_key === sourceKey
        ? { ...target, port: numberValue(port) }
        : target),
    });
  };

  const selectPolicies = (keys: React.Key[]) => {
    const selectablePolicyIDs = new Set(policiesRef.current.filter(isPolicySelectable).map((policy) => policy.id));
    const next = uniqueIDs(keys as any[]).filter((policyID) => selectablePolicyIDs.has(policyID));
    selectedPolicyIDsRef.current = next;
    setSelectedPolicyIDs(next);
    // Selection and expansion are intentionally independent. Removing an
    // expanded rule from monitoring must not unmount its configuration table
    // and make the scroll container jump; rows only open or close via the
    // table's expand control.
    markEdited();
  };

  const selectPolicy = (policyID: number, selected: boolean) => {
    const current = selectedPolicyIDsRef.current;
    selectPolicies(selected
      ? [...current, policyID]
      : current.filter((currentPolicyID) => currentPolicyID !== policyID));
  };

  const validate = (
    policyIDs: number[],
    monitorDrafts: Record<number, EntryMonitorDraft>,
  ) => {
    const currentPolicyByID = new Map(policiesRef.current.map((policy) => [policy.id, policy]));
    for (const policyID of policyIDs) {
      const draft = monitorDrafts[policyID];
      const policy = currentPolicyByID.get(policyID);
      if (!draft || !policy || !isPolicySelectable(policy)) return '所选入口规则已经失效，请刷新后重试';
      if (!draft.targets.length) return `${policy.name} 没有可检测地址`;
      if (draft.check_interval_sec < 5 || draft.check_interval_sec > 3600) return `${policy.name} 的检测间隔必须在 5-3600 秒之间`;
      if (draft.tcp_timeout_ms < 100 || draft.tcp_timeout_ms > 60000) return `${policy.name} 的 TCP 超时必须在 100-60000 毫秒之间`;
      if (draft.targets.some((target) => !target.source_key || !target.host)) return `${policy.name} 包含无效检测地址`;
      if (draft.targets.some((target) => target.port < 1 || target.port > 65535)) return `${policy.name} 的 TCP 端口必须在 1-65535 之间`;
    }
    return '';
  };

  const saveMonitors = async (silent = false): Promise<SavedEntryMonitorConfiguration | false> => {
    if (savingRef.current) return false;
    const saveEditVersion = editVersionRef.current;
    const saveRevision = revisionRef.current;
    const savePolicyIDs = [...selectedPolicyIDsRef.current];
    const saveDrafts = draftsRef.current;
    const validationError = validate(savePolicyIDs, saveDrafts);
    if (validationError) {
      message.error(validationError);
      return false;
    }
    const enabledPolicyIDs = savePolicyIDs.filter((policyID) => saveDrafts[policyID]?.enabled);
    const items = savePolicyIDs.map((policyID) => {
      const draft = saveDrafts[policyID];
      return {
        policy_id: policyID,
        enabled: draft.enabled,
        check_interval_sec: draft.check_interval_sec,
        tcp_timeout_ms: draft.tcp_timeout_ms,
        targets: draft.targets.map((target) => ({
          source_key: target.source_key,
          port: target.port,
        })),
      };
    });

    savingRef.current = true;
    setSaving(true);
    configurationRequestSequenceRef.current += 1;
    try {
      const payload = (unwrapData(await apiPut(ENTRY_MONITOR_PATH, {
        revision: saveRevision,
        items,
      })) || {}) as EntryMonitorPayload;
      configurationRequestSequenceRef.current += 1;
      const next = normalizeConfigurationPayload(payload);
      const hasNewEdits = editVersionRef.current !== saveEditVersion;

      policiesRef.current = next.policies;
      revisionRef.current = next.revision;
      setPolicies(next.policies);
      setProbes(next.probes);
      if (hasNewEdits) {
        const mergedDrafts = mergeLiveState(draftsRef.current, next.drafts);
        draftsRef.current = mergedDrafts;
        dirtyRef.current = true;
        setDrafts(mergedDrafts);
        setDirty(true);
      } else {
        draftsRef.current = next.drafts;
        selectedPolicyIDsRef.current = next.configuredPolicyIDs;
        dirtyRef.current = false;
        setDrafts(next.drafts);
        setSelectedPolicyIDs(next.configuredPolicyIDs);
        setDirty(false);
      }
      loadedRef.current = true;
      setLoaded(true);
      if (!silent) {
        message.success(hasNewEdits ? '本次配置已保存，仍有新修改未保存' : '用户入口检测配置已保存');
      }
      return { policyIDs: enabledPolicyIDs, revision: next.revision };
    } catch (error: any) {
      message.error(error?.message || '保存用户入口检测失败');
      return false;
    } finally {
      savingRef.current = false;
      setSaving(false);
    }
  };

  const runNow = async () => {
    if (runningRef.current || savingRef.current) return;
    const hasEnabledPolicy = selectedPolicyIDsRef.current
      .some((policyID) => draftsRef.current[policyID]?.enabled);
    if (!hasEnabledPolicy) {
      message.warning('请先选择并启用要检测的用户入口规则');
      return;
    }
    runningRef.current = true;
    setRunning(true);
    try {
      const savedConfiguration = await saveMonitors(true);
      if (!savedConfiguration) return;
      const payload = unwrapData(await apiJsonPost(`${ENTRY_MONITOR_PATH}/run`, {
        policy_ids: savedConfiguration.policyIDs,
      })) || {};
      const runID = payload.run_id ?? payload.id;
      message.success(runID ? `检测任务 #${runID} 已提交` : '检测任务已提交');
      await fetchRuns(true);
    } catch (error: any) {
      message.error(error?.message || '提交检测任务失败');
    } finally {
      runningRef.current = false;
      setRunning(false);
    }
  };

  const policyTargets = (policy: EntryPolicy) => drafts[policy.id]?.targets || derivedPolicyTargets(policy);

  const policyColumns: any[] = [
    {
      title: '入口规则',
      key: 'policy',
      width: 250,
      render: (_: any, policy: EntryPolicy) => <div className="dns-entry-policy-name">
        <strong>{policy.name}</strong>
        <div className="dns-failover-sub">#{policy.id}</div>
      </div>,
    },
    {
      title: '规则动作',
      dataIndex: 'action',
      width: 140,
      render: (action: EntryPolicyAction, policy: EntryPolicy) => <Space size={4} wrap>
        {actionTag(action)}
        {!policy.enabled && <Tag>规则已停用</Tag>}
      </Space>,
    },
    {
      title: '检测地址',
      key: 'targets',
      width: 360,
      render: (_: any, policy: EntryPolicy) => {
        if (policy.action === 'hide') return <span className="dns-failover-sub">隐藏规则不可检测</span>;
        const targets = policyTargets(policy);
        if (!targets.length) return <span className="text-danger">没有可检测地址</span>;
        if (policy.action === 'override') return <span className="dns-entry-host">{targets[0].host}</span>;
        return <details className="dns-entry-policy-details">
          <summary>{targets.length} 个节点原地址</summary>
          <div className="dns-entry-policy-hosts">
            {targets.map((target) => <span className="dns-entry-host" key={target.source_key}>
              {target.name ? `${target.name} · ` : ''}{target.host}
            </span>)}
          </div>
        </details>;
      },
    },
    {
      title: '监控状态',
      key: 'configured',
      width: 160,
      render: (_: any, policy: EntryPolicy) => {
        if (!selectedPolicyIDs.includes(policy.id)) return <Tag>未纳入</Tag>;
        const draft = drafts[policy.id];
        return <Tag color={draft?.enabled ? 'success' : 'default'}>
          {draft?.enabled ? '检测中' : '已暂停'}
        </Tag>;
      },
    },
  ];

  const targetColumns = (policyID: number, disabled = false): any[] => [
    {
      title: '地址',
      key: 'host',
      width: 290,
      render: (_: any, target: EntryTarget) => <div>
        <strong>{target.name || '检测地址'}</strong>
        <div className="dns-entry-host">{target.host || '—'}</div>
        <div className="dns-failover-sub">{target.source_key}</div>
      </div>,
    },
    {
      title: 'TCP 端口',
      dataIndex: 'port',
      width: 140,
      render: (port: number, target: EntryTarget) => <InputNumber
        min={1}
        max={65535}
        precision={0}
        value={port}
        disabled={disabled}
        aria-label={`${target.name || target.host || '检测地址'} TCP 端口`}
        onChange={(value) => updateTargetPort(policyID, target.source_key, value)}
      />,
    },
    {
      title: '最近检测',
      key: 'states',
      width: 680,
      render: (_: any, target: EntryTarget) => target.states.length
        ? <div className="dns-entry-state-list">
          {target.states.map((state, index) => {
            const probeID = numberValue(state.probe_id);
            const probe = probeByID.get(probeID);
            const success = stateSuccess(state);
            const failureStreak = numberValue(state.consecutive_failure);
            const awaitingFailureConfirmation = success !== false
              && failureStreak > 0
              && failureStreak < ENTRY_MONITOR_FAILURE_THRESHOLD;
            const latency = stateLatency(state);
            const ip = stateIP(state);
            const error = stateError(state);
            return <div className="dns-entry-state" key={`${probeID || 'probe'}-${index}`}>
              <Tooltip title={probe?.public_ip || ''}>
                <span className="dns-entry-state-probe">{probe?.name || state.probe_name || (probeID ? `探针 #${probeID}` : '未知探针')}</span>
              </Tooltip>
              {state.stale
                ? <Tag color="warning">已过期</Tag>
                : awaitingFailureConfirmation
                  ? <Tag color="warning">待复核 {failureStreak}/{ENTRY_MONITOR_FAILURE_THRESHOLD}</Tag>
                : success === true
                  ? <Tag color="success">成功</Tag>
                  : success === false
                    ? <Tag color="error">失败</Tag>
                    : <Tag>无数据</Tag>}
              <span>{latency === undefined || state.stale ? '—' : `${latency} ms`}</span>
              <span className="dns-entry-state-result">
                {ip || '—'}
                {error && <span className="text-danger">{error}</span>}
              </span>
              <span className="dns-entry-state-time">{formatTime(stateTime(state))}</span>
            </div>;
          })}
        </div>
        : <span className="dns-failover-sub">尚无检测记录</span>,
    },
  ];

  const renderPolicyConfiguration = (policy: EntryPolicy) => {
    const draft = drafts[policy.id];
    if (!draft) return null;
    const selected = selectedPolicyIDs.includes(policy.id);
    const enabledProbeCount = probes.filter((probe) => probe.enabled).length;
    return <div className="dns-entry-config-body">
        <div className="dns-entry-monitor-fields">
          <div className="dns-entry-monitor-field">
            <span>纳入监控</span>
            <Switch
              checked={selected}
              checkedChildren="已纳入"
              unCheckedChildren="未纳入"
              aria-label={`${policy.name} 纳入监控`}
              onChange={(checked) => selectPolicy(policy.id, checked)}
            />
          </div>
          <div className="dns-entry-monitor-field">
            <span>启用检测</span>
            <Switch
              checked={draft.enabled}
              disabled={!selected}
              checkedChildren="启用"
              unCheckedChildren="暂停"
              aria-label={`${policy.name} 启用检测`}
              onChange={(enabled) => updateDraft(policy.id, { enabled })}
            />
          </div>
          <div className="dns-entry-monitor-field">
            <span>检测间隔</span>
            <InputNumber
              min={5}
              max={3600}
              precision={0}
              addonAfter="秒"
              value={draft.check_interval_sec}
              disabled={!selected}
              aria-label={`${policy.name} 检测间隔`}
              onChange={(value) => updateDraft(policy.id, { check_interval_sec: numberValue(value) })}
            />
          </div>
          <div className="dns-entry-monitor-field">
            <span>TCP 超时</span>
            <InputNumber
              min={100}
              max={60000}
              precision={0}
              addonAfter="ms"
              value={draft.tcp_timeout_ms}
              disabled={!selected}
              aria-label={`${policy.name} TCP 超时`}
              onChange={(value) => updateDraft(policy.id, { tcp_timeout_ms: numberValue(value) })}
            />
          </div>
          <div className="dns-entry-monitor-field dns-entry-monitor-field--probe-scope">
            <span>检测探针</span>
            {enabledProbeCount > 0
              ? <Tag color="blue">全部启用探针 · {enabledProbeCount}</Tag>
              : <Tag color="error">暂无启用探针</Tag>}
          </div>
        </div>
        <Table
          className="dns-entry-target-table"
          rowKey={(target) => target.source_key}
          size="small"
          pagination={false}
          dataSource={draft.targets}
          columns={targetColumns(policy.id, !selected)}
          scroll={{ x: 1110 }}
          locale={{ emptyText: '没有可检测地址' }}
        />
      </div>;
  };

  const runColumns: any[] = [
    {
      title: '任务',
      key: 'run',
      width: 150,
      render: (_: any, run: any) => <div>
        <strong>#{run.run_id ?? run.id ?? '—'}</strong>
        <div className="dns-failover-sub">{runStatusTag(run)}</div>
      </div>,
    },
    {
      title: '入口规则',
      key: 'policy',
      width: 270,
      render: (_: any, run: any) => {
        const policyIDs = uniqueIDs([
          ...list(run.policy_ids),
          ...(run.policy_id ? [run.policy_id] : []),
        ]);
        const policyNames = policyIDs.map((policyID) => policyByID.get(policyID)?.name || `#${policyID}`);
        return policyNames.join('、') || run.policy_name || '—';
      },
    },
    {
      title: '进度',
      key: 'progress',
      width: 130,
      render: (_: any, run: any) => `${numberValue(run.received_results)} / ${numberValue(run.expected_results)}`,
    },
    {
      title: '结果',
      key: 'result',
      width: 220,
      render: (_: any, run: any) => {
        const results = list(run.results);
        const successCount = results.filter((result) => boolValue(result?.success)).length;
        const failureCount = results.length - successCount;
        const resultPrefix = boolValue(run.results_truncated) ? '展示' : '';
        return results.length ? <Space size={4} wrap>
          <Tag color="success">{resultPrefix}成功 {successCount}</Tag>
          {failureCount > 0 && <Tag color="error">{resultPrefix}失败 {failureCount}</Tag>}
        </Space> : <span className="dns-failover-sub">等待结果</span>;
      },
    },
    {
      title: '时间',
      key: 'time',
      width: 180,
      render: (_: any, run: any) => <div>
        <span>{formatTime(run.started_at ?? run.created_at)}</span>
        {run.completed_at && <div className="dns-failover-sub">完成 {formatTime(run.completed_at)}</div>}
      </div>,
    },
  ];

  const runResultColumns: any[] = [
    {
      title: '目标',
      key: 'target',
      width: 280,
      render: (_: any, result: any) => <div>
        <strong>{result.target_name || (result.target_id ? `目标 #${result.target_id}` : '—')}</strong>
        <div className="dns-failover-sub dns-entry-host">
          {result.host ? `${result.host}${result.port ? `:${result.port}` : ''}` : '—'}
        </div>
      </div>,
    },
    {
      title: '探针',
      key: 'probe',
      width: 170,
      render: (_: any, result: any) => result.probe_name
        || probeByID.get(numberValue(result.probe_id))?.name
        || (result.probe_id ? `#${result.probe_id}` : '—'),
    },
    {
      title: '状态',
      dataIndex: 'success',
      width: 90,
      render: (success: any) => boolValue(success)
        ? <Tag color="success">成功</Tag>
        : <Tag color="error">失败</Tag>,
    },
    {
      title: '延迟',
      dataIndex: 'latency_ms',
      width: 100,
      render: (latency: any) => latency == null ? '—' : `${numberValue(latency)} ms`,
    },
    {
      title: '解析 IP / 错误',
      key: 'detail',
      width: 280,
      render: (_: any, result: any) => <div>
        <span>{result.resolved_ip || '—'}</span>
        {result.error && <div className="text-danger dns-entry-run-error">{result.error}</div>}
      </div>,
    },
    {
      title: '时间',
      dataIndex: 'reported_at',
      width: 180,
      render: formatTime,
    },
  ];

  return <Spin spinning={loading}>
    <div className="dns-entry-monitor-tab">
      <div className="forest-table-action dns-entry-toolbar">
        <Space wrap>
          <Button
            type="primary"
            icon={<SaveOutlined />}
            loading={saving}
            disabled={!dirty || running}
            onClick={() => void saveMonitors()}
          >
            保存配置
          </Button>
          <Button
            icon={<PlayCircleOutlined />}
            loading={running}
            disabled={saving || !selectedPolicyIDs.some((policyID) => drafts[policyID]?.enabled)}
            onClick={() => void runNow()}
          >
            立即检测
          </Button>
          <Button icon={<ReloadOutlined />} onClick={() => void refresh()}>
            刷新
          </Button>
        </Space>
        <span className="dns-entry-save-state">{dirty ? '有未保存修改' : '配置已同步'}</span>
      </div>

      <Table
        className="dns-entry-policy-table"
        rowKey="id"
        size="small"
        pagination={false}
        dataSource={policies}
        columns={policyColumns}
        scroll={{ x: 1010 }}
        rowSelection={{
          columnTitle: '纳入监控',
          columnWidth: 100,
          selectedRowKeys: selectedPolicyIDs,
          onChange: selectPolicies,
          getCheckboxProps: (policy: EntryPolicy) => ({
            disabled: !isPolicySelectable(policy),
          }),
        }}
        expandable={{
          expandedRowKeys: expandedPolicyIDs,
          onExpandedRowsChange: (keys) => setExpandedPolicyIDs(uniqueIDs(keys as any[])),
          rowExpandable: isPolicySelectable,
          expandedRowRender: renderPolicyConfiguration,
        }}
        locale={{ emptyText: loaded ? '暂无用户入口规则' : '正在加载' }}
      />

      <Divider orientation="left">近期检测</Divider>
      <Table
        className="dns-entry-runs-table"
        rowKey={(run, index) => `${run.run_id ?? run.id ?? 'run'}-${run.target_id ?? run.source_key ?? index}-${run.probe_id ?? ''}`}
        size="small"
        pagination={false}
        dataSource={runs}
        columns={runColumns}
        scroll={{ x: 1030 }}
        expandable={{
          rowExpandable: (run) => list(run.results).length > 0,
          expandedRowRender: (run) => <>
            {boolValue(run.results_truncated) && <div className="dns-failover-sub mb-2">
              仅展示前 200 条，完整统计看进度
            </div>}
            <Table
              className="dns-entry-run-results"
              rowKey={(result, index) => `${result.id ?? result.target_id ?? index}-${result.probe_id ?? ''}`}
              size="small"
              pagination={false}
              dataSource={list(run.results)}
              columns={runResultColumns}
              scroll={{ x: 1100 }}
            />
          </>,
        }}
        locale={{ emptyText: '暂无检测任务' }}
      />
    </div>
  </Spin>;
}
