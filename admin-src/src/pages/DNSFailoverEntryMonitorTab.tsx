import React, { useEffect, useMemo, useRef, useState } from 'react';
import {
  Button,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Space,
  Spin,
  Switch,
  Table,
  Tabs,
  Tag,
  Tooltip,
  message,
} from 'antd';
import {
  CopyOutlined,
  DeleteOutlined,
  EditOutlined,
  PlusOutlined,
  PlayCircleOutlined,
  ReloadOutlined,
  SaveOutlined,
} from '@ant-design/icons';
import { apiDelete, apiGet, apiJsonPost, apiPut, unwrapData } from '../lib/api';
import './DNSFailoverEntryMonitorTab.css';

const ENTRY_MONITOR_PATH = '/dns-failover/entry-monitors';
const BACKUP_IP_PATH = `${ENTRY_MONITOR_PATH}/backup-ips`;
const DEFAULT_ENTRY_MONITOR_TIMEOUT_MS = 5000;
const DEFAULT_ENTRY_MONITOR_FAILURE_THRESHOLD = 3;
const DEFAULT_ENTRY_MONITOR_SUCCESS_THRESHOLD = 2;
const BACKUP_IP_SUCCESS_THRESHOLD = 2;
const DEFAULT_BACKUP_IP_PORT = 54101;

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
  auto_split_enabled: boolean;
  states: EntryTargetState[];
};

type EntryMonitorDraft = {
  policy_id: number;
  policy_name: string;
  action: EntryPolicyAction;
  enabled: boolean;
  check_interval_sec: number;
  tcp_timeout_ms: number;
  failure_threshold: number;
  success_threshold: number;
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

type BackupIPState = EntryTargetState & {
  probe_id?: number;
  probe_name?: string;
};

type BackupIP = Record<string, any> & {
  id: number;
  name: string;
  ip: string;
  port: number;
  enabled: boolean;
  status: string;
  used: boolean;
  used_by: any[];
  quarantine_until?: any;
  states: BackupIPState[];
};

type BackupIPInput = {
  name: string;
  ip: string;
  port: number;
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

function timestampMilliseconds(value: any) {
  if (value === undefined || value === null || value === '') return 0;
  if (typeof value === 'string' && !/^\d+(?:\.\d+)?$/.test(value.trim())) {
    const parsed = new Date(value).getTime();
    return Number.isNaN(parsed) ? 0 : parsed;
  }
  const timestamp = numberValue(value);
  return timestamp > 0 && timestamp < 1_000_000_000_000 ? timestamp * 1000 : timestamp;
}

function normalizeBackupIP(raw: any, index: number): BackupIP {
  const usage = raw?.used_by ?? raw?.usages ?? raw?.assignments ?? raw?.usage;
  const usedBy = Array.isArray(usage)
    ? usage
    : usage && typeof usage === 'object'
      ? [usage]
      : [];
  const states = list(raw?.states ?? raw?.probe_states ?? raw?.results).map((state) => ({
    ...state,
    probe_id: numberValue(state?.probe_id) || undefined,
    probe_name: String(state?.probe_name || '').trim() || undefined,
    stale: boolValue(state?.stale),
  }));
  const ip = String(raw?.ip || raw?.address || raw?.host || '').trim().replace(/^\[|\]$/g, '');
  return {
    ...raw,
    id: numberValue(raw?.id) || -(index + 1),
    name: String(raw?.name || '').trim() || ip || `备用 IP #${index + 1}`,
    ip,
    port: numberValue(raw?.port, DEFAULT_BACKUP_IP_PORT),
    enabled: boolValue(raw?.enabled, true),
    status: String(raw?.status || raw?.availability_status || raw?.health_status || '').trim().toLowerCase(),
    used: boolValue(raw?.used ?? raw?.in_use, false) || usedBy.length > 0,
    used_by: usedBy,
    quarantine_until: raw?.quarantine_until,
    states,
  };
}

function isIPv4Literal(value: string) {
  const parts = value.split('.');
  return parts.length === 4 && parts.every((part) => /^\d{1,3}$/.test(part)
    && Number(part) <= 255
    && (part === '0' || !part.startsWith('0')))
    && value !== '0.0.0.0';
}

function isIPv6Literal(value: string) {
  if (!value.includes(':') || !/^[0-9a-f:.]+$/i.test(value)) return false;
  try {
    // URL performs the browser's IPv6 parser without issuing a network request.
    const hostname = new URL(`http://[${value}]/`).hostname;
    return hostname.length > 2 && hostname !== '[::]' && hostname !== '::';
  } catch {
    return false;
  }
}

function normalizeIPLiteral(value: string) {
  const ip = value.trim().replace(/^\[|\]$/g, '');
  return isIPv4Literal(ip) || isIPv6Literal(ip) ? ip : '';
}

function parseBackupEndpoint(value: string, defaultPort: number) {
  const endpoint = value.trim();
  const bracketed = endpoint.match(/^\[([^\]]+)\](?::(\d+))?$/);
  if (bracketed) {
    return { ip: normalizeIPLiteral(bracketed[1]), port: numberValue(bracketed[2], defaultPort) };
  }
  const colonCount = (endpoint.match(/:/g) || []).length;
  if (colonCount === 1) {
    const separator = endpoint.lastIndexOf(':');
    const portText = endpoint.slice(separator + 1);
    if (/^\d+$/.test(portText)) {
      return {
        ip: normalizeIPLiteral(endpoint.slice(0, separator)),
        port: numberValue(portText),
      };
    }
  }
  return { ip: normalizeIPLiteral(endpoint), port: defaultPort };
}

function parseBackupIPLines(value: string, defaultPort: number) {
  const items: BackupIPInput[] = [];
  const errors: string[] = [];
  const seen = new Set<string>();
  value.split(/\r?\n/).forEach((rawLine, index) => {
    const line = rawLine.trim();
    if (!line) return;
    const parts = line.split(/[,，]/).map((part) => part.trim());
    let name = '';
    let endpoint = line;
    let requestedPort = defaultPort;
    if (parts.length === 3) {
      [name, endpoint] = parts;
      requestedPort = numberValue(parts[2]);
    } else if (parts.length === 2) {
      if (/^\d+$/.test(parts[1]) && normalizeIPLiteral(parts[0])) {
        endpoint = parts[0];
        requestedPort = numberValue(parts[1]);
      } else {
        [name, endpoint] = parts;
      }
    } else if (parts.length > 3) {
      errors.push(`第 ${index + 1} 行格式无效`);
      return;
    }
    const parsed = parseBackupEndpoint(endpoint, requestedPort);
    if (!parsed.ip) {
      errors.push(`第 ${index + 1} 行不是有效的 IPv4/IPv6 地址`);
      return;
    }
    if (parsed.port < 1 || parsed.port > 65535) {
      errors.push(`第 ${index + 1} 行端口必须在 1-65535 之间`);
      return;
    }
    const key = parsed.ip.toLowerCase();
    if (seen.has(key)) {
      errors.push(`第 ${index + 1} 行与本次其他地址重复`);
      return;
    }
    seen.add(key);
    items.push({ name: name || parsed.ip, ip: parsed.ip, port: parsed.port });
  });
  return { items, errors };
}

function formatBackupEndpoint(ip: string, port: number) {
  return ip.includes(':') ? `[${ip}]:${port}` : `${ip}:${port}`;
}

async function copyText(value: string) {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value);
      return;
    } catch {
      // Fall through for non-HTTPS panels or browsers that deny Clipboard API access.
    }
  }
  const textarea = document.createElement('textarea');
  textarea.value = value;
  textarea.style.position = 'fixed';
  textarea.style.opacity = '0';
  let copied = false;
  try {
    document.body.appendChild(textarea);
    textarea.focus();
    textarea.select();
    copied = document.execCommand('copy');
  } finally {
    textarea.remove();
  }
  if (!copied) throw new Error('copy failed');
}

function isBackupIPAvailable(item: BackupIP) {
  return !item.used && item.enabled && (
    boolValue(item.available)
    || ['available', 'healthy', 'ready', 'success', 'online'].includes(item.status)
  );
}

function backupStatusMeta(item: BackupIP) {
  if (!item.enabled) return { label: '已禁用', color: 'default' };
  if (item.used) return { label: '使用中', color: 'blue' };
  const quarantined = boolValue(item.quarantined)
    || item.status === 'quarantined'
    || timestampMilliseconds(item.quarantine_until) > Date.now();
  if (quarantined) return { label: '隔离', color: 'warning' };
  if (['available', 'healthy', 'ready', 'success', 'online'].includes(item.status)) {
    return { label: '可用', color: 'success' };
  }
  if (['failed', 'failure', 'unhealthy', 'offline', 'error'].includes(item.status)) {
    return { label: '故障', color: 'error' };
  }
  if (item.status === 'no_probe') return { label: '无启用探针', color: 'warning' };
  if (item.status === 'probe_offline') return { label: '探针离线', color: 'warning' };
  return { label: '测活中', color: 'processing' };
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
      auto_split_enabled: boolValue(target?.auto_split_enabled),
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
      auto_split_enabled: false,
      states: [],
    }] : [];
  }
  return policy.members.map((member, index) => ({
    source_key: memberSourceKey(member, index),
    host: memberHost(member),
    port: numberValue(member.port || member.server_port, 443),
    sort: index,
    auto_split_enabled: false,
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
    auto_split_enabled: boolValue(raw?.auto_split_enabled),
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
    tcp_timeout_ms: numberValue(item?.tcp_timeout_ms, DEFAULT_ENTRY_MONITOR_TIMEOUT_MS),
    failure_threshold: numberValue(
      item?.failure_threshold ?? DEFAULT_ENTRY_MONITOR_FAILURE_THRESHOLD,
      DEFAULT_ENTRY_MONITOR_FAILURE_THRESHOLD,
    ),
    success_threshold: numberValue(
      item?.success_threshold ?? DEFAULT_ENTRY_MONITOR_SUCCESS_THRESHOLD,
      DEFAULT_ENTRY_MONITOR_SUCCESS_THRESHOLD,
    ),
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

function isFixedSplitLeafTarget(target: EntryTarget) {
  return /^policy:\d+:split-group:\d+$/.test(String(target.source_key || '').trim());
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
  const [clearingRuns, setClearingRuns] = useState(false);
  const [backupIPs, setBackupIPs] = useState<BackupIP[]>([]);
  const [backupLoading, setBackupLoading] = useState(false);
  const [backupBatchOpen, setBackupBatchOpen] = useState(false);
  const [backupBatchText, setBackupBatchText] = useState('');
  const [backupDefaultPort, setBackupDefaultPort] = useState(DEFAULT_BACKUP_IP_PORT);
  const [backupBatchSaving, setBackupBatchSaving] = useState(false);
  const [editingBackupIP, setEditingBackupIP] = useState<BackupIPInput & { id: number } | null>(null);
  const [backupActionID, setBackupActionID] = useState<number | null>(null);
  const [selectedBackupIPIDs, setSelectedBackupIPIDs] = useState<number[]>([]);
  const [backupBatchDeleting, setBackupBatchDeleting] = useState(false);

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
  const backupRequestSequenceRef = useRef(0);
  const loadingRequestSequenceRef = useRef(0);

  const policyByID = useMemo(
    () => new Map(policies.map((policy) => [policy.id, policy])),
    [policies],
  );
  const probeByID = useMemo(
    () => new Map(probes.map((probe) => [probe.id, probe])),
    [probes],
  );
  const enabledProbes = useMemo(
    () => probes.filter((probe) => probe.enabled),
    [probes],
  );
  const backupBatchPreview = useMemo(
    () => parseBackupIPLines(backupBatchText, backupDefaultPort),
    [backupBatchText, backupDefaultPort],
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

  const fetchBackupIPs = async (quiet = false) => {
    const requestSequence = ++backupRequestSequenceRef.current;
    if (!quiet) setBackupLoading(true);
    try {
      const payload = unwrapData(await apiGet(BACKUP_IP_PATH)) || {};
      if (requestSequence !== backupRequestSequenceRef.current) return false;
      const rawItems = Array.isArray(payload)
        ? payload
        : list(payload.items ?? payload.backup_ips ?? payload.data);
      const nextItems = rawItems.map(normalizeBackupIP).filter((item) => item.id > 0 && item.ip);
      setBackupIPs(nextItems);
      setSelectedBackupIPIDs((current) => current.filter((id) => nextItems.some((item) => item.id === id && !item.used)));
      return true;
    } catch (error: any) {
      if (requestSequence !== backupRequestSequenceRef.current) return false;
      if (!quiet) message.error(error?.message || '加载备用 IP 池失败');
      throw error;
    } finally {
      if (!quiet && requestSequence === backupRequestSequenceRef.current) setBackupLoading(false);
    }
  };

  const refresh = async (quiet = false) => {
    const loadingRequestSequence = quiet ? 0 : ++loadingRequestSequenceRef.current;
    if (!quiet) setLoading(true);
    try {
      await Promise.all([
        fetchConfiguration(quiet),
        fetchRuns(quiet),
        fetchBackupIPs(quiet),
      ]);
    } catch {
      // Individual loaders surface non-background errors.
    } finally {
      if (!quiet && loadingRequestSequence === loadingRequestSequenceRef.current) setLoading(false);
    }
  };

  const clearRuns = async () => {
    if (clearingRuns) return;
    setClearingRuns(true);
    // Invalidate an older refresh so it cannot put deleted rows back into the
    // table after the DELETE request has completed.
    runsRequestSequenceRef.current += 1;
    try {
      const payload = unwrapData(await apiDelete(`${ENTRY_MONITOR_PATH}/runs`)) || {};
      const deleted = numberValue(payload.deleted);
      setRuns((current) => current.filter((run) => String(run.status || '').toLowerCase() === 'running'));
      message.success(deleted > 0 ? `已清理 ${deleted} 条检测记录` : '没有可清理的检测记录');
      try {
        await fetchRuns(true);
      } catch {
        // The records are already cleared; the regular refresh will retry.
      }
    } catch (error: any) {
      message.error(error?.message || '清理近期检测失败');
    } finally {
      setClearingRuns(false);
    }
  };

  const createBackupIPs = async () => {
    if (backupBatchSaving) return;
    const parsed = parseBackupIPLines(backupBatchText, backupDefaultPort);
    if (parsed.errors.length) {
      const extra = parsed.errors.length > 3 ? `；另有 ${parsed.errors.length - 3} 行错误` : '';
      message.error(`${parsed.errors.slice(0, 3).join('；')}${extra}`);
      return;
    }
    if (!parsed.items.length) {
      message.warning('请先输入至少一个备用 IP');
      return;
    }
    setBackupBatchSaving(true);
    try {
      const payload = unwrapData(await apiJsonPost(BACKUP_IP_PATH, {
        items: parsed.items.map((item) => ({ ...item, enabled: true })),
      })) || {};
      const createdItems = list(payload.items);
      const created = createdItems.length || parsed.items.length;
      message.success(`已添加 ${created} 个备用 IP，等待全部启用探针测活`);
      setBackupBatchOpen(false);
      setBackupBatchText('');
      try {
        await fetchBackupIPs(true);
      } catch {
        // Creation has committed; the regular poll will retry the list refresh.
      }
    } catch (error: any) {
      message.error(error?.message || '批量添加备用 IP 失败');
    } finally {
      setBackupBatchSaving(false);
    }
  };

  const updateBackupIP = async (item: BackupIP, patch: Partial<BackupIPInput & { enabled: boolean }>) => {
    if (backupActionID !== null) return false;
    const nextIP = normalizeIPLiteral(String(patch.ip ?? item.ip));
    const nextPort = numberValue(patch.port ?? item.port);
    if (!nextIP) {
      message.error('请输入有效的 IPv4/IPv6 地址');
      return false;
    }
    if (nextPort < 1 || nextPort > 65535) {
      message.error('TCP 端口必须在 1-65535 之间');
      return false;
    }
    setBackupActionID(item.id);
    try {
      await apiPut(`${BACKUP_IP_PATH}/${item.id}`, {
        name: String(patch.name ?? item.name).trim() || nextIP,
        ip: nextIP,
        port: nextPort,
        enabled: patch.enabled ?? item.enabled,
        check_interval_sec: numberValue(item.check_interval_sec, 30),
        tcp_timeout_ms: numberValue(item.tcp_timeout_ms, 3000),
        sort: numberValue(item.sort),
      });
      message.success('备用 IP 已更新');
      try {
        await fetchBackupIPs(true);
      } catch {
        // The update already succeeded; do not present a refresh failure as a save failure.
      }
      return true;
    } catch (error: any) {
      message.error(error?.message || '更新备用 IP 失败');
      return false;
    } finally {
      setBackupActionID(null);
    }
  };

  const saveEditingBackupIP = async () => {
    const draft = editingBackupIP;
    if (!draft) return;
    const item = backupIPs.find((candidate) => candidate.id === draft.id);
    if (!item) {
      message.error('备用 IP 已不存在，请刷新后重试');
      setEditingBackupIP(null);
      return;
    }
    const saved = await updateBackupIP(item, draft);
    if (saved) setEditingBackupIP(null);
  };

  const deleteBackupIP = async (item: BackupIP) => {
    if (backupActionID !== null) return;
    setBackupActionID(item.id);
    try {
      await apiDelete(`${BACKUP_IP_PATH}/${item.id}`);
      message.success('备用 IP 已删除');
      setBackupIPs((current) => current.filter((candidate) => candidate.id !== item.id));
      setSelectedBackupIPIDs((current) => current.filter((id) => id !== item.id));
      try {
        await fetchBackupIPs(true);
      } catch {
        // Deletion has committed; the regular poll will retry the list refresh.
      }
    } catch (error: any) {
      message.error(error?.message || '删除备用 IP 失败');
    } finally {
      setBackupActionID(null);
    }
  };

  const deleteBackupIPs = async (scope: 'selected' | 'faulty' | 'all') => {
    if (backupBatchDeleting || backupActionID !== null) return;
    const ids = scope === 'selected' ? uniqueIDs(selectedBackupIPIDs) : undefined;
    if (scope === 'selected' && !ids?.length) {
      message.warning('请先选择要删除的备用 IP');
      return;
    }
    setBackupBatchDeleting(true);
    try {
      const payload = unwrapData(await apiJsonPost(`${BACKUP_IP_PATH}/delete`, {
        scope,
        ...(ids ? { ids } : {}),
      })) || {};
      const deleted = numberValue(payload.deleted);
      const skippedInUse = numberValue(payload.skipped_in_use);
      const skippedNotFound = numberValue(payload.skipped_not_found);
      const summary = [
        `已删除 ${deleted} 个备用 IP`,
        skippedInUse > 0 ? `使用中的 ${skippedInUse} 个已保留` : '',
        skippedNotFound > 0 ? `不存在或已被删除的 ${skippedNotFound} 个已跳过` : '',
      ].filter(Boolean).join('，');
      message.success(summary);
      setSelectedBackupIPIDs([]);
      try {
        await fetchBackupIPs(true);
      } catch {
        // Deletion has committed; the regular poll will retry the list refresh.
      }
    } catch (error: any) {
      message.error(error?.message || '批量删除备用 IP 失败');
    } finally {
      setBackupBatchDeleting(false);
    }
  };

  const copyAvailableBackupIPs = async () => {
    const seen = new Set<string>();
    const ips = backupIPs
      .filter(isBackupIPAvailable)
      .map((item) => item.ip.trim())
      .filter((ip) => {
        const key = ip.toLowerCase();
        if (!ip || seen.has(key)) return false;
        seen.add(key);
        return true;
      });
    if (!ips.length) {
      message.warning('当前没有健康且未使用的备用 IP');
      return;
    }
    try {
      await copyText(ips.join('\n'));
      message.success(`已复制 ${ips.length} 个可用 IP`);
    } catch {
      message.error('复制失败，请稍后重试');
    }
  };

  const refreshBackupIP = async (item: BackupIP) => {
    if (backupActionID !== null) return;
    setBackupActionID(item.id);
    try {
      await apiJsonPost(`${BACKUP_IP_PATH}/refresh`, { ids: [item.id] });
      message.success(`${item.name} 已提交全部探针重新测活`);
      try {
        await fetchBackupIPs(true);
      } catch {
        // The refresh task was accepted; the regular poll will retry status loading.
      }
    } catch (error: any) {
      message.error(error?.message || '提交重新测活失败');
    } finally {
      setBackupActionID(null);
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

  const updateTargetAutoSplit = (policyID: number, sourceKey: string, enabled: boolean) => {
    const draft = draftsRef.current[policyID];
    if (!draft) return;
    updateDraft(policyID, {
      targets: draft.targets.map((target) => target.source_key === sourceKey
        ? { ...target, auto_split_enabled: enabled }
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
      if (!Number.isInteger(draft.failure_threshold) || draft.failure_threshold < 2 || draft.failure_threshold > 10) {
        return `${policy.name} 的故障确认次数必须在 2-10 次之间`;
      }
      if (!Number.isInteger(draft.success_threshold) || draft.success_threshold < 1 || draft.success_threshold > 10) {
        return `${policy.name} 的恢复确认次数必须在 1-10 次之间`;
      }
      if (draft.targets.some((target) => !target.source_key || !target.host)) return `${policy.name} 包含无效检测地址`;
      if (draft.targets.some((target) => target.port < 1 || target.port > 65535)) return `${policy.name} 的 TCP 端口必须在 1-65535 之间`;
      if (draft.targets.some((target) => target.auto_split_enabled && !isFixedSplitLeafTarget(target))) {
        return `${policy.name} 只有固定二分叶子入口可以开启自动二分`;
      }
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
        failure_threshold: draft.failure_threshold,
        success_threshold: draft.success_threshold,
        targets: draft.targets.map((target) => ({
          source_key: target.source_key,
          port: target.port,
          auto_split_enabled: target.auto_split_enabled,
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
        if (policy.action === 'override' && targets.length === 1) return <span className="dns-entry-host">{targets[0].host}</span>;
        return <details className="dns-entry-policy-details">
          <summary>{policy.action === 'override' ? `${targets.length} 个分组入口` : `${targets.length} 个节点原地址`}</summary>
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

  const targetColumns = (policyID: number, disabled = false): any[] => {
    const failureThreshold = drafts[policyID]?.failure_threshold
      || DEFAULT_ENTRY_MONITOR_FAILURE_THRESHOLD;
    const successThreshold = drafts[policyID]?.success_threshold
      || DEFAULT_ENTRY_MONITOR_SUCCESS_THRESHOLD;
    return [
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
      title: '自动二分',
      key: 'auto_split_enabled',
      width: 190,
      render: (_: any, target: EntryTarget) => {
        const supported = isFixedSplitLeafTarget(target);
        return <Tooltip title={supported
          ? `全部在线探针连续达到 ${failureThreshold} 次失败后才确认入口故障，并自动从备用池领取两个健康且未占用的 IP，将固定名单继续二分。`
          : '仅固定二分叶子入口支持自动二分'}>
          <span>
            <Switch
              size="small"
              checked={target.auto_split_enabled}
              disabled={disabled || !supported}
              checkedChildren="已开启"
              unCheckedChildren="已关闭"
              aria-label={`${target.name || target.host || '检测地址'} 自动二分`}
              onChange={(enabled) => updateTargetAutoSplit(policyID, target.source_key, enabled)}
            />
          </span>
        </Tooltip>;
      },
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
            const successStreak = numberValue(state.consecutive_success);
            const awaitingFailureConfirmation = success !== false
              && failureStreak > 0
              && failureStreak < failureThreshold;
            const awaitingRecoveryConfirmation = success === false
              && successStreak > 0
              && successStreak < successThreshold;
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
                  ? <Tag color="warning">待复核 {failureStreak}/{failureThreshold}</Tag>
                  : awaitingRecoveryConfirmation
                    ? <Tag color="processing">恢复复核 {successStreak}/{successThreshold}</Tag>
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
  };

  const backupProbeRows = (item: BackupIP) => {
    const statesByProbeID = new Map(item.states.map((state) => [numberValue(state.probe_id), state]));
    if (enabledProbes.length) {
      return enabledProbes.map((probe) => ({ probe, state: statesByProbeID.get(probe.id) }));
    }
    return item.states.map((state) => ({
      probe: probeByID.get(numberValue(state.probe_id)),
      state,
    }));
  };

  const backupUsageLabel = (usage: any) => {
    const policyName = String(usage?.policy_name || '').trim()
      || (numberValue(usage?.policy_id) ? `规则 #${numberValue(usage.policy_id)}` : '入口规则');
    const groupName = String(usage?.split_group_name || usage?.group_name || '').trim();
    return groupName ? `${policyName} / ${groupName}` : policyName;
  };

  const renderBackupProbeStates = (item: BackupIP) => {
    const rows = backupProbeRows(item);
    if (!rows.length) return <div className="dns-entry-backup-empty-probes">暂无启用探针，备用 IP 不会进入可用队列。</div>;
    return <div className="dns-entry-backup-probes">
      <div className="dns-entry-backup-probe-head">
        全部启用探针逐一测活；每个在线探针连续成功 {BACKUP_IP_SUCCESS_THRESHOLD} 次后，这个 IP 才可参与自动分配。
      </div>
      <div className="dns-entry-state-list">
        {rows.map(({ probe, state }, index) => {
          const probeID = numberValue(state?.probe_id ?? probe?.id);
          const probeOnline = state ? boolValue(state.probe_online, true) : undefined;
          const success = state ? stateSuccess(state) : undefined;
          const successStreak = numberValue(state?.consecutive_success);
          const latency = state ? stateLatency(state) : undefined;
          const error = state ? stateError(state) : '';
          const hasResult = !!state && !!timestampMilliseconds(stateTime(state));
          const pendingSuccess = success === true && successStreak < BACKUP_IP_SUCCESS_THRESHOLD;
          return <div className="dns-entry-state dns-entry-backup-probe-row" key={`${probeID || 'probe'}-${index}`}>
            <Tooltip title={probe?.public_ip || ''}>
              <span className="dns-entry-state-probe">
                {probe?.name || state?.probe_name || (probeID ? `探针 #${probeID}` : '未知探针')}
              </span>
            </Tooltip>
            {probeOnline === false
              ? <Tag color="warning">探针离线</Tag>
              : !hasResult
                ? <Tag>无数据</Tag>
                : state.stale
                  ? <Tag color="warning">已过期</Tag>
                  : pendingSuccess
                    ? <Tag color="processing">确认中 {successStreak}/{BACKUP_IP_SUCCESS_THRESHOLD}</Tag>
                    : success === true
                      ? <Tag color="success">成功</Tag>
                      : success === false
                        ? <Tag color="error">失败</Tag>
                        : <Tag>无数据</Tag>}
            <span>{latency === undefined || state?.stale ? '—' : `${latency} ms`}</span>
            <span className="dns-entry-state-result">{error ? <span className="text-danger">{error}</span> : '—'}</span>
            <span className="dns-entry-state-time">{hasResult ? formatTime(stateTime(state)) : '—'}</span>
          </div>;
        })}
      </div>
    </div>;
  };

  const backupColumns: any[] = [
    {
      title: '备用 IP',
      key: 'endpoint',
      width: 290,
      render: (_: any, item: BackupIP) => <div className="dns-entry-backup-address">
        <strong title={item.name}>{item.name}</strong>
        <div className="dns-entry-host">{formatBackupEndpoint(item.ip, item.port)}</div>
      </div>,
    },
    {
      title: '启用',
      dataIndex: 'enabled',
      width: 92,
      render: (enabled: boolean, item: BackupIP) => <Switch
        size="small"
        checked={enabled}
        loading={backupActionID === item.id}
        disabled={backupActionID !== null && backupActionID !== item.id}
        checkedChildren="启用"
        unCheckedChildren="停用"
        aria-label={`${item.name} 启用备用 IP 测活`}
        onChange={(checked) => void updateBackupIP(item, { enabled: checked })}
      />,
    },
    {
      title: '池状态',
      key: 'status',
      width: 220,
      render: (_: any, item: BackupIP) => {
        const status = backupStatusMeta(item);
        const enabledCount = numberValue(item.enabled_probe_count, backupProbeRows(item).length);
        const onlineCount = numberValue(item.online_probe_count, enabledCount);
        const healthyCount = numberValue(
          item.healthy_probe_count,
          item.states.filter((state) => !state.stale
            && stateSuccess(state) === true
            && numberValue(state.consecutive_success) >= BACKUP_IP_SUCCESS_THRESHOLD).length,
        );
        return <div className="dns-entry-backup-status">
          <Space size={4} wrap>
            <Tag color={status.color}>{status.label}</Tag>
            {enabledCount > 0 && <Tag>{healthyCount}/{enabledCount} 探针健康</Tag>}
          </Space>
          {onlineCount < enabledCount && <div className="dns-failover-sub">在线探针 {onlineCount}/{enabledCount}</div>}
          {timestampMilliseconds(item.quarantine_until) > Date.now() && <div className="dns-failover-sub">
            隔离至 {formatTime(item.quarantine_until)}
          </div>}
          {enabledCount > 0 && <div className="dns-failover-sub">展开查看逐探针结果</div>}
        </div>;
      },
    },
    {
      title: '占用入口',
      key: 'usage',
      width: 300,
      render: (_: any, item: BackupIP) => item.used_by.length
        ? <div className="dns-entry-backup-usages">
          {item.used_by.map((usage, index) => <Tag color="blue" key={`${usage?.kind || 'usage'}-${usage?.policy_id || index}-${usage?.split_group_id || ''}`}>
            {backupUsageLabel(usage)}
          </Tag>)}
        </div>
        : item.used
          ? <Tag color="blue">已被入口规则占用</Tag>
          : <span className="dns-failover-sub">未占用</span>,
    },
    {
      title: '最近测活',
      key: 'last_checked_at',
      width: 180,
      render: (_: any, item: BackupIP) => {
        const latest = item.states.reduce((current, state) => {
          const timestamp = timestampMilliseconds(stateTime(state));
          return timestamp > current ? timestamp : current;
        }, 0);
        return latest ? formatTime(latest) : '—';
      },
    },
    {
      title: '操作',
      key: 'actions',
      fixed: 'right',
      width: 252,
      render: (_: any, item: BackupIP) => <Space size={4} wrap={false}>
        <Button
          type="link"
          size="small"
          icon={<EditOutlined />}
          disabled={backupActionID !== null}
          onClick={() => setEditingBackupIP({ id: item.id, name: item.name, ip: item.ip, port: item.port })}
        >
          编辑
        </Button>
        <Tooltip title={item.enabled ? '' : '请先启用这个备用 IP'}>
          <Button
            type="link"
            size="small"
            icon={<ReloadOutlined />}
            loading={backupActionID === item.id}
            disabled={!item.enabled || (backupActionID !== null && backupActionID !== item.id)}
            onClick={() => void refreshBackupIP(item)}
          >
            重新测活
          </Button>
        </Tooltip>
        <Tooltip title={item.used ? '这个 IP 正被入口规则使用，请先更换对应入口' : ''}>
          <span>
            <Popconfirm
              title={`确认删除 ${item.name}？`}
              description="删除后将同时清除全部探针测活记录。"
              okText="删除"
              cancelText="取消"
              okButtonProps={{ danger: true }}
              disabled={item.used}
              onConfirm={() => deleteBackupIP(item)}
            >
              <Button
                type="link"
                size="small"
                danger
                icon={<DeleteOutlined />}
                disabled={item.used || backupActionID !== null}
              >
                删除
              </Button>
            </Popconfirm>
          </span>
        </Tooltip>
      </Space>,
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
          <div className="dns-entry-monitor-field">
            <span>故障确认次数</span>
            <InputNumber
              min={2}
              max={10}
              precision={0}
              addonAfter="次"
              value={draft.failure_threshold}
              disabled={!selected}
              aria-label={`${policy.name} 故障确认次数`}
              onChange={(value) => updateDraft(policy.id, { failure_threshold: numberValue(value) })}
            />
          </div>
          <div className="dns-entry-monitor-field">
            <span>恢复确认次数</span>
            <InputNumber
              min={1}
              max={10}
              precision={0}
              addonAfter="次"
              value={draft.success_threshold}
              disabled={!selected}
              aria-label={`${policy.name} 恢复确认次数`}
              onChange={(value) => updateDraft(policy.id, { success_threshold: numberValue(value) })}
            />
          </div>
          <div className="dns-entry-monitor-field dns-entry-monitor-field--probe-scope">
            <span>检测探针</span>
            {enabledProbeCount > 0
              ? <Tag color="blue">全部启用探针 · {enabledProbeCount}</Tag>
              : <Tag color="error">暂无启用探针</Tag>}
          </div>
        </div>
        <div className="dns-failover-sub">
          TCP 超时默认 5000 ms；更高延迟线路可调整到 8000 ms。故障需连续失败 {draft.failure_threshold} 次、恢复需连续成功 {draft.success_threshold} 次才会通知，可减少网络抖动误报。
        </div>
        <Table
          className="dns-entry-target-table"
          rowKey={(target) => target.source_key}
          size="small"
          pagination={false}
          dataSource={draft.targets}
          columns={targetColumns(policy.id, !selected)}
          scroll={{ x: 1300 }}
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
      <Tabs
        className="dns-entry-section-tabs"
        items={[
          {
            key: 'monitors',
            label: `入口监控 (${selectedPolicyIDs.length}/${policies.length})`,
            children: <>
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
            </>,
          },
          {
            key: 'backup-ips',
            label: `备用 IP 池 (${backupIPs.length})`,
            children: <>
      <div className="dns-entry-backup-toolbar">
        <div className="dns-entry-backup-intro">
          <strong>全部启用探针同时测活</strong>
          <span>连续测通且未被入口规则使用的 IP 才会标记为可用；自动二分会从可用队列原子分配。</span>
          <Space size={4} wrap>
            <Tag>共 {backupIPs.length} 个</Tag>
            <Tag color="success">可用 {backupIPs.filter(isBackupIPAvailable).length}</Tag>
            <Tag color="blue">使用中 {backupIPs.filter((item) => item.used).length}</Tag>
            <Tag color="warning">隔离 {backupIPs.filter((item) => backupStatusMeta(item).label === '隔离').length}</Tag>
          </Space>
        </div>
        <Space className="dns-entry-backup-actions" wrap>
          <Button
            type="primary"
            icon={<PlusOutlined />}
            disabled={backupBatchDeleting}
            onClick={() => setBackupBatchOpen(true)}
          >
            批量添加
          </Button>
          <Button
            icon={<CopyOutlined />}
            disabled={!backupIPs.some(isBackupIPAvailable)}
            onClick={() => void copyAvailableBackupIPs()}
          >
            复制可用 IP
          </Button>
          <Popconfirm
            title={`确认删除所选的 ${selectedBackupIPIDs.length} 个备用 IP？`}
            description="删除后将同时清除对应的探针测活记录；使用中的 IP 会自动保留。"
            okText="删除所选"
            cancelText="取消"
            okButtonProps={{ danger: true }}
            disabled={!selectedBackupIPIDs.length}
            onConfirm={() => void deleteBackupIPs('selected')}
          >
            <Button
              danger
              icon={<DeleteOutlined />}
              disabled={!selectedBackupIPIDs.length || backupActionID !== null}
              loading={backupBatchDeleting}
            >
              删除所选{selectedBackupIPIDs.length ? ` (${selectedBackupIPIDs.length})` : ''}
            </Button>
          </Popconfirm>
          <Popconfirm
            title="确认删除全部故障 IP？"
            description="仅删除状态为故障且未被入口使用的 IP；使用中的 IP 会自动保留。"
            okText="删除故障 IP"
            cancelText="取消"
            okButtonProps={{ danger: true }}
            disabled={!backupIPs.some((item) => item.status === 'unhealthy' && !item.used)}
            onConfirm={() => void deleteBackupIPs('faulty')}
          >
            <Button
              danger
              icon={<DeleteOutlined />}
              disabled={!backupIPs.some((item) => item.status === 'unhealthy' && !item.used) || backupActionID !== null}
              loading={backupBatchDeleting}
            >
              删除故障 IP
            </Button>
          </Popconfirm>
          <Popconfirm
            title="确认删除池内全部未使用 IP？"
            description="此操作会清空全部未占用备用 IP 及其测活记录；正在使用的 IP 会自动保留。"
            okText="删除全部"
            cancelText="取消"
            okButtonProps={{ danger: true }}
            disabled={!backupIPs.some((item) => !item.used)}
            onConfirm={() => void deleteBackupIPs('all')}
          >
            <Button
              danger
              icon={<DeleteOutlined />}
              disabled={!backupIPs.some((item) => !item.used) || backupActionID !== null}
              loading={backupBatchDeleting}
            >
              删除全部
            </Button>
          </Popconfirm>
        </Space>
      </div>
      <Table
        className="dns-entry-backup-table"
        rowKey="id"
        size="small"
        loading={backupLoading}
        dataSource={backupIPs}
        columns={backupColumns}
        rowSelection={{
          selectedRowKeys: selectedBackupIPIDs,
          onChange: (keys) => setSelectedBackupIPIDs(uniqueIDs(keys as any[])),
          getCheckboxProps: (item: BackupIP) => ({
            disabled: item.used || backupBatchDeleting || backupActionID !== null,
          }),
        }}
        scroll={{ x: 1334 }}
        pagination={backupIPs.length > 20 ? {
          defaultPageSize: 20,
          showSizeChanger: true,
          pageSizeOptions: [20, 50, 100],
          showTotal: (total) => `共 ${total} 个备用 IP`,
        } : false}
        expandable={{
          expandedRowRender: renderBackupProbeStates,
          rowExpandable: () => true,
        }}
        locale={{ emptyText: '暂无备用 IP，批量添加后将自动下发给全部启用探针测活' }}
      />
            </>,
          },
          {
            key: 'runs',
            label: `近期检测 (${runs.length})`,
            children: <>
        <div className="forest-table-action dns-entry-runs-toolbar">
          <span className="dns-failover-sub">检测记录保留最近的任务和逐探针结果</span>
          <Popconfirm
            title="确认清理近期检测记录？"
            description="仅删除已经结束的检测任务及结果，不影响正在执行的任务、监控配置和告警状态。"
            okText="清理"
            cancelText="取消"
            okButtonProps={{ danger: true }}
            onConfirm={() => clearRuns()}
          >
            <Button
              type="link"
              size="small"
              danger
              icon={<DeleteOutlined />}
              loading={clearingRuns}
            >
              手动清理
            </Button>
          </Popconfirm>
        </div>
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
            </>,
          },
        ]}
      />

      <Modal
        title="批量添加备用 IP"
        open={backupBatchOpen}
        width={680}
        okText={`添加${backupBatchPreview.items.length ? ` ${backupBatchPreview.items.length} 个` : ''}`}
        cancelText="取消"
        confirmLoading={backupBatchSaving}
        okButtonProps={{ disabled: !backupBatchPreview.items.length || backupBatchPreview.errors.length > 0 }}
        closable={!backupBatchSaving}
        maskClosable={!backupBatchSaving}
        destroyOnHidden
        onOk={() => void createBackupIPs()}
        onCancel={() => {
          if (!backupBatchSaving) setBackupBatchOpen(false);
        }}
      >
        <div className="dns-entry-backup-batch-form">
          <div className="dns-entry-backup-batch-help">
            一行一个，支持 <code>名称,IP,端口</code>、<code>IP:端口</code>。IPv6 带端口请写成 <code>[IPv6]:端口</code>；省略端口时使用下方默认值。
          </div>
          <label className="dns-entry-backup-default-port">
            <span>默认 TCP 端口</span>
            <InputNumber
              min={1}
              max={65535}
              precision={0}
              value={backupDefaultPort}
              onChange={(value) => setBackupDefaultPort(numberValue(value, DEFAULT_BACKUP_IP_PORT))}
            />
          </label>
          <Input.TextArea
            rows={10}
            value={backupBatchText}
            placeholder={'香港备用,1.1.1.1,54101\n8.8.8.8:54101\n[2001:db8::1]:54101'}
            onChange={(event) => setBackupBatchText(event.target.value)}
          />
          <div className={backupBatchPreview.errors.length ? 'dns-entry-backup-preview dns-entry-backup-preview--error' : 'dns-entry-backup-preview'}>
            {backupBatchPreview.errors.length
              ? backupBatchPreview.errors.slice(0, 3).join('；')
              : backupBatchPreview.items.length
                ? `已识别 ${backupBatchPreview.items.length} 个地址`
                : '等待输入地址'}
          </div>
        </div>
      </Modal>

      <Modal
        title="编辑备用 IP"
        open={!!editingBackupIP}
        width={520}
        okText="保存"
        cancelText="取消"
        confirmLoading={!!editingBackupIP && backupActionID === editingBackupIP.id}
        closable={backupActionID === null}
        maskClosable={backupActionID === null}
        destroyOnHidden
        onOk={() => void saveEditingBackupIP()}
        onCancel={() => {
          if (backupActionID === null) setEditingBackupIP(null);
        }}
      >
        {editingBackupIP && <div className="dns-entry-backup-edit-form">
          <label>
            <span>名称</span>
            <Input
              maxLength={255}
              value={editingBackupIP.name}
              placeholder="用于识别这个备用 IP"
              onChange={(event) => setEditingBackupIP({ ...editingBackupIP, name: event.target.value })}
            />
          </label>
          <label>
            <span>IP 地址</span>
            <Input
              value={editingBackupIP.ip}
              placeholder="IPv4 或 IPv6，不要填写域名"
              disabled={backupIPs.some((item) => item.id === editingBackupIP.id && item.used)}
              onChange={(event) => setEditingBackupIP({ ...editingBackupIP, ip: event.target.value })}
            />
            {backupIPs.some((item) => item.id === editingBackupIP.id && item.used)
              && <span className="dns-failover-sub">使用中的 IP 不能更换地址，可继续修改名称和检测端口。</span>}
          </label>
          <label>
            <span>TCP 端口</span>
            <InputNumber
              min={1}
              max={65535}
              precision={0}
              value={editingBackupIP.port}
              onChange={(value) => setEditingBackupIP({ ...editingBackupIP, port: numberValue(value) })}
            />
          </label>
        </div>}
      </Modal>
    </div>
  </Spin>;
}
