import { useEffect, useMemo, useRef, useState } from 'react';
import { Alert, Button, Card, Checkbox, Col, DatePicker, Descriptions, Drawer, Form, Input, InputNumber, Modal, Popconfirm, Row, Select, Space, Spin, Table, Tabs, Tag, Tree, Typography, message } from 'antd';
import type { DataNode } from 'antd/es/tree';
import type { ColumnsType } from 'antd/es/table';
import { BranchesOutlined, CopyOutlined, DownloadOutlined, ReloadOutlined, SearchOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { apiGet, apiJsonPost, getAdminUserInfo, unwrapData } from '../lib/api';
import {
  ENTRY_EVIDENCE_PATH, EVIDENCE_CLASSIFICATIONS, EVIDENCE_CONCLUSIONS, evidenceCaseTree, evidenceDeepLink, evidenceDuration, evidenceFilterError, evidenceFilterKey, evidenceFreezePayload, evidenceQuery,
  evidenceResultLabel, evidenceSelectedIDs, evidenceSplitPayload, evidenceTargetOptions, evidenceTime,
  type EvidenceCandidate, type EvidenceCandidatePage, type EvidenceCase, type EvidenceCaseTreeNode, type EvidenceContextPage, type EvidenceContextUser, type EvidenceDelivery,
  type EvidenceFilters, type EvidencePage, type EvidenceSplitInput, type EvidenceSplitPreview, type EvidenceStatus,
} from './entryEvidence';
import './EntryEvidencePanel.css';

const errorText = (error: unknown) => error instanceof Error ? error.message : '请求失败，请重试';
const emptyPage = <T,>(): EvidencePage<T> => ({ items: [], total: 0, current: 1, page_size: 20 });
const jsonText = (value: unknown) => JSON.stringify(value ?? null, null, 2);
const pagePayload = <T,>(response: unknown): EvidencePage<T> => {
  const payload = unwrapData(response) as EvidencePage<T>;
  if (!payload || !Array.isArray(payload.items) || !Number.isFinite(payload.total)) throw new Error('服务端返回的分页数据无效，请更新后端后重试');
  return payload;
};

export function initialEvidenceFilters(): EvidenceFilters {
  return evidenceDeepLink(location.search, location.hash, Math.floor(Date.now() / 1000));
}

function Notices({ values }: { values?: string[] }) {
  return <>{values?.map((value, index) => <Alert key={index} showIcon type="warning" message={value} />)}</>;
}

function EvidenceJSON({ title, value }: { title: string; value: unknown }) {
  return <details className="entry-evidence-json"><summary>{title}</summary><pre>{jsonText(value)}</pre></details>;
}

function CandidateEvidence({ user }: { user: EvidenceCandidate }) {
  const references = Array.isArray(user.evidence) ? user.evidence : [];
  return <Space direction="vertical" style={{ width: '100%' }}>
    <Typography.Text type="secondary">此用户进入候选的原因：观察窗口内，有完整覆盖且服务端响应完成的相应入口记录。请求与节点证据逐条保留，不代表此用户导致异常。</Typography.Text>
    {user.evidence_truncated && <Alert type="warning" showIcon message={`当前快照展示 ${references.length} / ${user.evidence_total || 0} 条证据引用，不能把未展示的部分视为不存在。`} />}
    <Table size="small" rowKey={(item, index) => `${item.request_id}:${item.server_type}:${item.server_id}:${index}`} dataSource={references} pagination={{ pageSize: 10, size: 'small' }} scroll={{ x: 1250 }} locale={{ emptyText: '此候选未返回证据引用；请核对后端记录范围，不以聚合人数代替完整证据。' }} columns={[
      { title: '完成写入时间', dataIndex: 'completed_at', width: 180, render: evidenceTime },
      { title: '请求标识', dataIndex: 'request_id', width: 230, render: (value: string) => <Typography.Text copyable>{value}</Typography.Text> },
      { title: '实际下发入口', width: 220, render: (_, item) => `${item.host}:${item.port}` },
      { title: '节点', width: 140, render: (_, item) => `${item.server_type} #${item.server_id}` },
      { title: 'UA', dataIndex: 'ua', width: 250, ellipsis: true },
      { title: '下发快照版本', dataIndex: 'rule_revision', width: 230, ellipsis: true, render: (value: string) => <Typography.Text copyable ellipsis={{ tooltip: value }}>{value}</Typography.Text> },
    ]} />
  </Space>;
}

function ProbeEvents({ record }: { record: EvidenceCase }) {
  const events = Array.isArray(record.probe_events) ? record.probe_events : [];
  return <details className="entry-evidence-probe-events">
    <summary>本轮探针事件 · 已归档 {record.probe_events_total ?? events.length} 条</summary>
    <Space direction="vertical" style={{ width: '100%' }}>
      <Typography.Text type="secondary">仅展示本轮 A／B 入口关联的真实掉线／恢复事件，按发生时间倒序，最多最近 100 条。不自动判断被墙或用户责任；没有事件不代表入口正常。</Typography.Text>
      {record.probe_events_gap && <Alert type="warning" showIcon message="探针事件归档存在缺口" description="后台归档间隔曾超过普通检测日志保留期，已清理的事件无法追补；不要将缺少事件解释为检测正常。" />}
      {record.probe_events_truncated && <Typography.Text type="secondary">当前展示 {events.length} / {record.probe_events_total ?? events.length} 条，其余事件仍保留在档案中；未展示不等于不存在。</Typography.Text>}
      <Table size="small" rowKey={(event) => `${event.branch}:${event.event_id}`} dataSource={events} pagination={{ pageSize: 10, size: 'small', showSizeChanger: false }} scroll={{ x: 1080 }} locale={{ emptyText: record.status === 'executed' ? '本轮尚无已归档探针事件；请结合检测启用状态、记录缺口与实际测试判断。' : '执行二分后才归档本轮 A／B 入口事件。' }} columns={[
        { title: '事件时间', dataIndex: 'happened_at', width: 190, render: evidenceTime },
        { title: '分支', dataIndex: 'branch', width: 65 },
        { title: '检测事件', dataIndex: 'event_type', width: 125, render: (value: string) => <Tag color={value === 'down' ? 'orange' : value === 'recovered' ? 'green' : undefined}>{value === 'down' ? '探针掉线' : value === 'recovered' ? '探针恢复' : '其他事件'}</Tag> },
        { title: '事件标识', dataIndex: 'event_id', width: 100 },
        { title: '目标 / 探针', width: 135, render: (_, event) => `#${event.target_id} / #${event.probe_id}` },
        { title: '检测入口', width: 220, render: (_, event) => `${event.host}:${event.port}` },
        { title: '检测时解析 IP', dataIndex: 'resolved_ip', width: 210, render: (value: string) => value || '未记录' },
      ]} />
    </Space>
  </details>;
}

export default function EntryEvidencePanel({ rows, onRulesChanged }: { rows: unknown[]; onRulesChanged: () => void | Promise<void> }) {
  const isAdmin = Number(getAdminUserInfo().is_admin) === 1;
  const options = useMemo(() => evidenceTargetOptions(rows), [rows]);
  const [tab, setTab] = useState('deliveries');
  const [draft, setDraft] = useState<EvidenceFilters>(initialEvidenceFilters);
  const [appliedDeliveries, setAppliedDeliveries] = useState<EvidenceFilters | null>(null);
  const [appliedCandidates, setAppliedCandidates] = useState<EvidenceFilters | null>(null);
  const [status, setStatus] = useState<EvidenceStatus | null>(null);
  const [keepDays, setKeepDays] = useState<number>(30);
  const [deliveries, setDeliveries] = useState<EvidencePage<EvidenceDelivery>>(emptyPage);
  const [candidates, setCandidates] = useState<EvidenceCandidatePage | null>(null);
  const [context, setContext] = useState<EvidenceContextPage | null>(null);
  const [contextLoading, setContextLoading] = useState(false);
  const [contextError, setContextError] = useState('');
  const [selected, setSelected] = useState<React.Key[]>([]);
  const [cases, setCases] = useState<EvidencePage<EvidenceCase>>(emptyPage);
  const [loading, setLoading] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState('');
  const [freezeMode, setFreezeMode] = useState<'selected' | 'all' | null>(null);
  const [caseTitle, setCaseTitle] = useState('');
  const [parentCaseID, setParentCaseID] = useState<number | null>(null);
  const [record, setRecord] = useState<EvidenceCase | null>(null);
  const [caseID, setCaseID] = useState(0);
  const [caseUsers, setCaseUsers] = useState<EvidencePage<EvidenceCandidate>>(emptyPage);
  const [caseError, setCaseError] = useState('');
  const [observation, setObservation] = useState('');
  const [conclusion, setConclusion] = useState('');
  const [pool, setPool] = useState(true);
  const [hostA, setHostA] = useState('');
  const [hostB, setHostB] = useState('');
  const [preview, setPreview] = useState<EvidenceSplitPreview | null>(null);
  const [previewInput, setPreviewInput] = useState<EvidenceSplitInput | null>(null);
  const querySequence = useRef(0), caseSequence = useRef(0), statusSequence = useRef(0);
  const queryRequest = useRef<AbortController | null>(null), caseRequest = useRef<AbortController | null>(null);
  const contextSequence = useRef(0), contextRequest = useRef<AbortController | null>(null), draftCaseID = useRef(0);
  const draftBase = useRef({ revision: 0, observation: '', conclusion: '' });
  const draftValues = useRef({ observation, conclusion }); draftValues.current = { observation, conclusion };
  const busyRef = useRef(false), mounted = useRef(true);
  const applied = tab === 'candidates' ? appliedCandidates : appliedDeliveries;
  const dirty = applied && evidenceFilterKey(draft) !== evidenceFilterKey(applied);
  const candidatesDirty = !appliedCandidates || evidenceFilterKey(draft) !== evidenceFilterKey(appliedCandidates);
  const observationDirty = !!record && (observation !== draftBase.current.observation || conclusion !== draftBase.current.conclusion);
  const freezeAllowed = candidates?.freeze_allowed ?? candidates?.supported ?? false;
  const patch = (values: Partial<EvidenceFilters>) => setDraft((old) => ({ ...old, ...values }));
  const invalidatePreview = () => { setPreview(null); setPreviewInput(null); };
  const mutation = async (name: string, action: () => Promise<void>) => {
    if (busyRef.current) return;
    busyRef.current = true; setBusy(name);
    try { await action(); } catch (error) { if (mounted.current) message.error(errorText(error)); }
    finally { busyRef.current = false; if (mounted.current) setBusy(''); }
  };

  const loadStatus = async () => {
    const sequence = ++statusSequence.current;
    try {
      const result = unwrapData(await apiGet(`${ENTRY_EVIDENCE_PATH}/status`, {}, { cache: 'no-store' })) as EvidenceStatus;
      if (!mounted.current || sequence !== statusSequence.current) return;
      setStatus(result); setKeepDays(result.keep_days);
    } catch (error) { if (mounted.current && sequence === statusSequence.current) setError(errorText(error)); }
  };
  useEffect(() => { mounted.current = true; void loadStatus(); return () => { mounted.current = false; querySequence.current++; caseSequence.current++; statusSequence.current++; contextSequence.current++; queryRequest.current?.abort(); caseRequest.current?.abort(); contextRequest.current?.abort(); }; }, []);

  const loadContext = async (filters: EvidenceFilters, current = 1, pageSize = 20) => {
    const sequence = ++contextSequence.current;
    contextRequest.current?.abort(); const controller = new AbortController(); contextRequest.current = controller;
    setContextLoading(true); setContextError('');
    try {
      const result = pagePayload<EvidenceContextUser>(await apiGet(`${ENTRY_EVIDENCE_PATH}/context`, evidenceQuery(filters, current, pageSize), { cache: 'no-store', signal: controller.signal })) as EvidenceContextPage;
      if (!result.counts || ['window_delivered', 'historical_exposure', 'request_only', 'orphaned'].some((key) => !Number.isFinite(result.counts[key as keyof typeof result.counts]))) throw new Error('背景证据汇总无效，请更新后端后重试');
      if (mounted.current && sequence === contextSequence.current) setContext(result);
    } catch (error) { if (mounted.current && sequence === contextSequence.current) setContextError(errorText(error)); }
    finally { if (mounted.current && sequence === contextSequence.current) setContextLoading(false); }
  };

  const search = async (kind: 'deliveries' | 'candidates', filters = draft, current = 1, pageSize = 20, resetSelection = true) => {
    const invalid = evidenceFilterError(filters, kind === 'candidates');
    if (invalid) { setError(invalid); return; }
    const submitted = { ...filters };
    const sequence = ++querySequence.current;
    queryRequest.current?.abort();
    const controller = new AbortController(); queryRequest.current = controller;
    setLoading(kind); setError('');
    try {
      const response = await apiGet(`${ENTRY_EVIDENCE_PATH}/${kind}`, evidenceQuery(submitted, current, pageSize), { cache: 'no-store', signal: controller.signal });
      if (!mounted.current || sequence !== querySequence.current) return;
      const page = pagePayload<EvidenceCandidate | EvidenceDelivery>(response);
      if (kind === 'deliveries') { setDeliveries(page as EvidencePage<EvidenceDelivery>); setAppliedDeliveries(submitted); }
      else {
        const next = page as EvidenceCandidatePage;
        if (resetSelection || next.candidates_hash !== candidates?.candidates_hash || !appliedCandidates || evidenceFilterKey(submitted) !== evidenceFilterKey(appliedCandidates)) setSelected([]);
        if (current === 1 || !appliedCandidates || evidenceFilterKey(submitted) !== evidenceFilterKey(appliedCandidates)) { setContext(null); void loadContext(submitted); }
        setCandidates(next); setAppliedCandidates(submitted);
      }
      setTab(kind);
    } catch (error) { if (mounted.current && sequence === querySequence.current) setError(errorText(error)); }
    finally { if (mounted.current && sequence === querySequence.current) setLoading(''); }
  };

  const loadCases = async (current = 1, pageSize = 20) => {
    const sequence = ++querySequence.current; setLoading('cases'); setError('');
    try {
      const result = pagePayload<EvidenceCase>(await apiGet(`${ENTRY_EVIDENCE_PATH}/cases`, { current, page_size: pageSize }, { cache: 'no-store' }));
      if (mounted.current && sequence === querySequence.current) setCases(result);
    } catch (error) { if (mounted.current && sequence === querySequence.current) setError(errorText(error)); }
    finally { if (mounted.current && sequence === querySequence.current) setLoading(''); }
  };

  const openCase = async (id: number, current = 1, pageSize = 20, resetDraft = false, discardDraft = false) => {
    if (id !== caseID && observationDirty && !discardDraft) {
      Modal.confirm({ title: '观察结论尚未保存', content: '切换档案会放弃未保存内容，是否继续？', okText: '放弃并切换', cancelText: '继续编辑', onOk: () => openCase(id, current, pageSize, resetDraft, true) }); return;
    }
    const sequence = ++caseSequence.current;
    caseRequest.current?.abort(); const controller = new AbortController(); caseRequest.current = controller;
    setCaseID(id); setCaseError(''); setLoading('case'); invalidatePreview();
    if (id !== caseID) { setRecord(null); setCaseUsers(emptyPage()); }
    try {
      const [detail, users] = await Promise.all([
        apiGet(`${ENTRY_EVIDENCE_PATH}/case`, { case_id: id }, { cache: 'no-store', signal: controller.signal }),
        apiGet(`${ENTRY_EVIDENCE_PATH}/candidates`, { case_id: id, current, page_size: pageSize }, { cache: 'no-store', signal: controller.signal }),
      ]);
      if (!mounted.current || sequence !== caseSequence.current) return;
      const next = unwrapData(detail) as EvidenceCase;
      if (next.id !== id || !(next.revision > 0)) throw new Error('档案返回无效，请重新加载');
      setRecord(next); setCaseUsers(pagePayload<EvidenceCandidate>(users));
      const dirtyDraft = draftValues.current.observation !== draftBase.current.observation || draftValues.current.conclusion !== draftBase.current.conclusion;
      if (resetDraft || draftCaseID.current !== id || !dirtyDraft) {
        setObservation(next.observation || ''); setConclusion(next.conclusion || ''); draftCaseID.current = id;
        draftBase.current = { revision: next.revision, observation: next.observation || '', conclusion: next.conclusion || '' };
      }
    } catch (error) { if (mounted.current && sequence === caseSequence.current) { setRecord((previous) => previous?.id === id ? previous : null); setCaseError(errorText(error)); } }
    finally { if (mounted.current && sequence === caseSequence.current) setLoading(''); }
  };
  const closeCase = (discardDraft = false) => {
    if (busyRef.current) return;
    if (observationDirty && !discardDraft) { Modal.confirm({ title: '观察结论尚未保存', content: '关闭会放弃未保存内容，是否继续？', okText: '放弃并关闭', cancelText: '继续编辑', onOk: () => closeCase(true) }); return; }
    caseSequence.current++; caseRequest.current?.abort(); draftCaseID.current = 0; setCaseID(0); setRecord(null); setCaseError(''); invalidatePreview();
  };

  const freeze = () => void mutation('freeze', async () => {
    if (!appliedCandidates || !candidates || !freezeMode || candidatesDirty) throw new Error('请先提交候选查询，再冻结名单');
    const payload = evidenceFreezePayload(appliedCandidates, candidates.target.revision, candidates.candidates_hash, caseTitle, freezeMode === 'selected' ? evidenceSelectedIDs(selected) : undefined);
    const result = unwrapData(await apiJsonPost(`${ENTRY_EVIDENCE_PATH}/freeze`, { ...payload, ...(parentCaseID ? { parent_case_id: parentCaseID } : {}) })) as EvidenceCase;
    if (!mounted.current) return;
    setFreezeMode(null); message.success('已冻结排查名单，不会封禁用户'); await openCase(result.id);
  });
  const prepareSplit = () => void mutation('preview', async () => {
    if (!record) return;
    invalidatePreview();
    const sequence = caseSequence.current;
    const input = evidenceSplitPayload(record, pool, hostA, hostB);
    const result = unwrapData(await apiJsonPost(`${ENTRY_EVIDENCE_PATH}/split-preview`, input)) as EvidenceSplitPreview;
    if (!mounted.current || sequence !== caseSequence.current) return;
    if (result.case_id !== record.id || result.revision !== record.revision || !Array.isArray(result.groups)) throw new Error('预览版本不一致，请重新打开档案');
    setPreview(result); setPreviewInput(input);
  });
  const executeSplit = () => void mutation('execute', async () => {
    if (!record || !preview?.supported || !previewInput || preview.revision !== record.revision) throw new Error('请重新生成可执行的二分预览');
    const input = previewInput;
    invalidatePreview();
    const result = unwrapData(await apiJsonPost(`${ENTRY_EVIDENCE_PATH}/split-execute`, input)) as EvidenceCase;
    if (!mounted.current) return;
    message.success('二分规则已写入；用户需重新拉取后才可能收到新入口');
    await openCase(result.id); await onRulesChanged();
  });
  const saveObservation = () => void mutation('observation', async () => {
    if (!record) return;
    await apiJsonPost(`${ENTRY_EVIDENCE_PATH}/case-update`, { case_id: record.id, revision: draftBase.current.revision, observation, conclusion });
    if (mounted.current) { message.success('观察记录已保存'); await openCase(record.id, caseUsers.current, caseUsers.page_size, true); }
  });
  const exportUsers = (copy = false) => void mutation('export', async () => {
    if (!record) return;
    const id = record.id, version = record.revision, emails = new Set<string>();
    let current = 1, collected = 0;
    for (;;) {
      const result = pagePayload<EvidenceCandidate>(await apiGet(`${ENTRY_EVIDENCE_PATH}/candidates`, { case_id: id, current, page_size: 100 }, { cache: 'no-store' }));
      for (const user of result.items) if (user.selected && user.email) emails.add(user.email.trim());
      collected += result.items.length;
      if (collected >= result.total) break;
      if (!result.items.length || current >= 10000) throw new Error('名单分页未完整返回，已停止导出');
      current++;
    }
    const fresh = unwrapData(await apiGet(`${ENTRY_EVIDENCE_PATH}/case`, { case_id: id }, { cache: 'no-store' })) as EvidenceCase;
    if (fresh.revision !== version) throw new Error('档案版本已变化，请重新加载后导出');
    const text = [...emails].sort().join('\n');
    if (copy) { await navigator.clipboard.writeText(text); message.success(`已复制 ${emails.size} 个邮箱`); }
    else { const url = URL.createObjectURL(new Blob([text], { type: 'text/plain;charset=utf-8' })); const link = document.createElement('a'); link.href = url; link.download = `entry-investigation-${id}-users.txt`; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000); }
  });
  const continueBranch = (branch: 'A' | 'B', discardDraft = false) => {
    if (!record?.execution || busyRef.current) return;
    if (observationDirty && !discardDraft) { Modal.confirm({ title: '观察结论尚未保存', content: '继续下一轮会放弃未保存内容，是否继续？', okText: '放弃并继续', cancelText: '继续编辑', onOk: () => continueBranch(branch, true) }); return; }
    const groupID = Number(record.execution[branch === 'A' ? 'group_a_id' : 'group_b_id']);
    if (!Number.isSafeInteger(groupID) || groupID <= 0) { message.error('执行结果缺少子组标识，请刷新档案'); return; }
    const now = Math.floor(Date.now() / 1000);
    setParentCaseID(record.id);
    setDraft({ target: `split_group:${groupID}`, host: String(record.execution[branch === 'A' ? 'entry_host_a' : 'entry_host_b'] || ''), port: record.query.port, from: Math.max(record.query.to, Number(record.execution.executed_at) || now - 3600), to: now, ua: record.query.ua || '', account: '' });
    setCandidates(null); setContext(null); contextSequence.current++; contextRequest.current?.abort(); setAppliedCandidates(null); setSelected([]); setTab('candidates'); closeCase(true);
    message.info(`已选择 ${branch} 子组作为下一轮，请核对实际入口及观察窗口后查询`);
  };

  const candidateColumns: ColumnsType<EvidenceCandidate> = [
    { title: '用户', width: 240, render: (_, user) => <><Typography.Text strong>#{user.user_id}</Typography.Text><br /><Typography.Text copyable>{user.email || '邮箱不可用'}</Typography.Text></> },
    { title: '证据分类', width: 180, render: (_, user) => <><Tag>{user.classification || (user.branch ? user.selected ? '已选中' : '未选中' : '窗口内完成写入')}</Tag>{user.branch && <Tag color="blue">{user.branch} 组</Tag>}</> },
    { title: '依据 / 原因', width: 260, render: (_, user) => user.reasons?.join('；') || '观察窗口内有相应入口记录；不代表异常由此用户造成' },
    { title: '请求数', dataIndex: 'request_count', width: 90 },
    { title: '首次 / 最后完成写入', width: 200, render: (_, user) => <>{evidenceTime(user.first_completed_at)}<br />{evidenceTime(user.last_completed_at)}</> },
    { title: 'UA', width: 260, render: (_, user) => <Typography.Text ellipsis={{ tooltip: user.uas?.join('\n') }}>{user.uas?.join('；') || '—'}</Typography.Text> },
    { title: '关联节点', width: 160, render: (_, user) => user.nodes?.map((node) => `${node.server_type} #${node.server_id}`).join('；') || '—' },
  ];
  const deliveryColumns: ColumnsType<EvidenceDelivery> = [
    { title: '记录完成时间', dataIndex: 'completed_at', width: 180, render: evidenceTime },
    { title: '用户', width: 200, render: (_, item) => <>{item.email_at_event || `#${item.user_id}`}<br /><Typography.Text type="secondary">#{item.user_id}</Typography.Text></> },
    { title: '结果 / 证据覆盖', width: 210, render: (_, item) => <Tag color={item.status >= 200 && item.status < 300 && item.response_complete && item.coverage === 'complete' ? 'green' : !item.response_complete ? 'red' : 'orange'}>{evidenceResultLabel(item)}</Tag> },
    { title: '实际下发节点', width: 150, render: (_, item) => `${item.node_count} 个 · ${item.transport}` },
    { title: '请求 UA', dataIndex: 'ua', width: 260, ellipsis: true },
    { title: '请求标识', width: 240, render: (_, item) => <Typography.Text copyable>{item.id}</Typography.Text> },
  ];

  const caseTreeData = (nodes: EvidenceCaseTreeNode[]): DataNode[] => nodes.map((node) => ({
    key: node.record.id,
    title: <Space wrap><Typography.Text>#{node.record.id} {node.record.title || node.record.target?.name}</Typography.Text><Tag>{node.record.status === 'executed' ? '已执行' : '已冻结'}</Tag>{node.unloadedParentID && <Button type="link" size="small" onClick={(event) => { event.stopPropagation(); void openCase(node.unloadedParentID!); }}>父轮次 #{node.unloadedParentID} 未在本页</Button>}</Space>,
    children: caseTreeData(node.children),
  }));
  const contextualEvidence = candidates && <details className="entry-evidence-context">
    <summary>历史暴露与证据缺口{context ? ` · 历史暴露 ${context.counts.historical_exposure} 人 · 仅请求 ${context.counts.request_only} 人` : contextLoading ? ' · 正在查询' : ''}</summary>
    <Space direction="vertical" style={{ width: '100%' }}>
      <Typography.Text type="secondary">此表仅作背景说明，不会加入勾选或冻结名单。历史暴露不等于异常期间再次获取；仅请求分类仅含带有目标节点关联但不完整的记录，不能证明获得此入口；完全无法关联入口的请求只作缺口提示。旧订阅日志不能补成历史下发证据。</Typography.Text>
      {contextError && <Alert type="warning" showIcon message={`背景证据查询未完成：${contextError}`} action={<Button onClick={() => appliedCandidates && void loadContext(appliedCandidates)}>重试</Button>} />}
      {context && <><Notices values={context.warnings} /><Space wrap><Tag>窗口内完整下发 {context.counts.window_delivered} 人</Tag><Tag>窗口前历史暴露 {context.counts.historical_exposure} 人</Tag><Tag>仅请求记录 {context.counts.request_only} 人</Tag><Tag>账号已不存在 {context.counts.orphaned} 人（交叉统计）</Tag></Space></>}
      <Table<EvidenceContextUser> size="small" rowKey="user_id" loading={contextLoading} dataSource={context?.items || []} scroll={{ x: 960 }} columns={[
        { title: '用户', width: 260, render: (_, user) => <><Typography.Text copyable>{user.email || `#${user.user_id}`}</Typography.Text><br /><Typography.Text type="secondary">#{user.user_id}{user.orphaned ? ' · 账号已不存在' : ''}</Typography.Text></> },
        { title: '证据分类', width: 220, dataIndex: 'classification', render: (value: EvidenceContextUser['classification']) => EVIDENCE_CLASSIFICATIONS[value] || value },
        { title: '首次 / 最后记录', width: 220, render: (_, user) => <>{evidenceTime(user.first_at)}<br />{evidenceTime(user.last_at)}</> },
        { title: '请求数', dataIndex: 'request_count', width: 100 },
      ]} pagination={context ? { current: context.current, pageSize: context.page_size, total: context.total, showSizeChanger: true, onChange: (page, size) => { if (appliedCandidates) void loadContext(appliedCandidates, page, size); } } : false} locale={{ emptyText: '此范围暂无背景证据；不代表不存在历史暴露或客户端缓存。' }} />
    </Space>
  </details>;

  const filters = <Form layout="vertical" className="entry-evidence-filters" onFinish={() => void search(tab === 'candidates' ? 'candidates' : 'deliveries')}>
    <Row gutter={[16, 0]}>
      <Col xs={24} lg={12}><Form.Item label="入口规则 / 固定子组"><Select allowClear showSearch optionFilterProp="label" value={draft.target || undefined} options={options} placeholder="历史查询可不限；冻结需指定规则" onChange={(target) => { const option = options.find((item) => item.value === target); patch({ target: target || '', ...(option?.host ? { host: option.host } : {}) }); }} /></Form.Item></Col>
      <Col xs={16} lg={8}><Form.Item label="下发入口域名 / IP"><Input allowClear value={draft.host} onChange={(event) => patch({ host: event.target.value })} placeholder="实际下发地址；留空不限" /></Form.Item></Col>
      <Col xs={8} lg={4}><Form.Item label="端口"><InputNumber min={1} max={65535} precision={0} value={draft.port} placeholder="不限" onChange={(port) => patch({ port: port ?? undefined })} /></Form.Item></Col>
      <Col xs={24} lg={12}><Form.Item label="记录完成时间范围（本地时区，开始含、结束不含）"><DatePicker.RangePicker showTime format="YYYY-MM-DD HH:mm:ss" value={[dayjs.unix(draft.from), dayjs.unix(draft.to)]} allowClear={false} onChange={(range) => { if (range?.[0] && range[1]) patch({ from: range[0].unix(), to: range[1].unix() }); }} presets={[{ label: '最近 1 小时', value: () => [dayjs().subtract(1, 'hour'), dayjs()] }, { label: '最近 24 小时', value: () => [dayjs().subtract(1, 'day'), dayjs()] }]} /></Form.Item></Col>
      <Col xs={24} lg={6}><Form.Item label="请求 UA"><Input value={draft.ua} allowClear onChange={(event) => patch({ ua: event.target.value })} placeholder="忽略大小写包含" /></Form.Item></Col>
      <Col xs={24} lg={6}><Form.Item label="用户 ID / 完整邮箱"><Input value={draft.account} allowClear onChange={(event) => patch({ account: event.target.value })} placeholder="留空不限" /></Form.Item></Col>
    </Row>
    <Space wrap><Button type="primary" htmlType="submit" loading={loading === tab} icon={<SearchOutlined />}>查询{tab === 'candidates' ? '异常候选' : '历史下发'}</Button><Button onClick={() => { setTab('candidates'); void search('candidates'); }}>筛选候选名单</Button></Space>
  </Form>;

  return <div className="entry-evidence-panel">
    <Alert showIcon type="info" message="历史下发证据与当前规则模拟分开记录" description="服务端写入完成不证明客户端已经收到、导入或实际连接。异常候选仅用于分组排查，不能据此判断用户恶意；冻结名单不会封禁账号。" />
    <div className="entry-evidence-coverage"><Typography.Text type="secondary">{status ? `记录${status.enabled ? '已启用' : '未启用'} · 保留 ${status.keep_days} 天 · 覆盖 ${evidenceTime(status.available_from)} 至 ${evidenceTime(status.available_to)}${status.write_failures ? ` · 记录失败 ${status.write_failures} 次` : ''}` : '正在读取证据记录状态…'}</Typography.Text><Button size="small" type="text" icon={<ReloadOutlined />} onClick={() => void loadStatus()}>刷新</Button></div>
    <Notices values={status?.warnings} />{parentCaseID && <Alert type="info" message={`正在准备父轮次 #${parentCaseID} 的后续排查`} action={<Button type="link" onClick={() => setParentCaseID(null)}>改为独立档案</Button>} />}
    {error && <Alert type="error" showIcon message={error} description="未重新成功查询前，下方仍是上次结果，不会作为新的筛选结果使用。" />}
    <Tabs activeKey={tab} onChange={(value) => { querySequence.current++; queryRequest.current?.abort(); setLoading(''); setTab(value); if (value === 'cases') void loadCases(); }} items={[
      { key: 'deliveries', label: '历史下发', children: <>{filters}{dirty && <Alert type="warning" message="条件已修改，请重新查询；翻页仍使用上次提交条件。" />}<Notices values={deliveries.warnings} /><Table<EvidenceDelivery> size="small" rowKey={(item) => item.id} loading={loading === 'deliveries'} dataSource={deliveries.items} columns={deliveryColumns} scroll={{ x: 1300 }} expandable={{ expandedRowRender: (item) => <Space direction="vertical" style={{ width: '100%' }}><Typography.Text type="secondary">请求发起 {evidenceTime(item.requested_at)} · HTTP {item.status} · {item.bytes_written} 字节 · {item.coverage_reason || '完整度以 coverage 字段为准'}{item.write_error ? ` · 写入错误：${item.write_error}` : ''}</Typography.Text><Table size="small" rowKey={(_, index) => String(index)} dataSource={item.nodes || []} pagination={false} scroll={{ x: 1000 }} columns={[{ title: '节点', render: (_, node) => `${node.server_type} #${node.server_id}`, width: 130 }, { title: '规则 / 子组', render: (_, node) => `${node.policy_id || '默认'} / ${node.group_id || '—'}`, width: 130 }, { title: '配置入口', dataIndex: 'source_host', width: 210 }, { title: '实际写入地址', render: (_, node) => `${node.delivered_host}:${node.port}`, width: 220 }, { title: '服务端解析', dataIndex: 'resolved', render: (value) => value ? '是' : '否；客户端解析未知', width: 180 }, { title: '来源', dataIndex: 'source', width: 130 }]} /><EvidenceJSON title="不可变下发记录（版本仅为下发快照）" value={item} /></Space> }} pagination={{ current: deliveries.current, pageSize: deliveries.page_size, total: deliveries.total, showSizeChanger: true, onChange: (page, size) => { if (appliedDeliveries) void search('deliveries', appliedDeliveries, page, size); } }} locale={{ emptyText: applied ? '当前范围暂无下发证据；不代表用户未拉取或没有使用节点。' : '选择观察窗口并查询，不自动扫描全部历史。' }} /></> },
      { key: 'candidates', label: '异常候选', children: <>{filters}{dirty && <Alert type="warning" message="条件已修改，重新查询前不能冻结名单。" />}{candidates && <><Notices values={candidates.warnings} /><Alert type={candidates.supported ? 'info' : 'warning'} message={candidates.supported ? `去重候选 ${candidates.total} 人 · 当前规则快照 ${candidates.target.name}` : candidates.unsupported_reason || '当前目标不支持冻结二分'} description="候选由同一观察窗口及同一组筛选条件生成；选中数是用户去重数，不是请求次数。" /><Space wrap className="entry-evidence-actions"><Button disabled={!!busy || !!candidatesDirty || loading === 'candidates' || selected.length < 1 || !freezeAllowed} onClick={() => { setCaseTitle(''); setFreezeMode('selected'); }}>冻结选中 {selected.length} 人</Button><Button disabled={!!busy || !!candidatesDirty || loading === 'candidates' || !candidates.total || !freezeAllowed} onClick={() => { setCaseTitle(''); setFreezeMode('all'); }}>冻结全部匹配 {candidates.total} 人</Button></Space></>}<Table<EvidenceCandidate> size="small" rowKey="user_id" loading={loading === 'candidates'} dataSource={candidates?.items || []} columns={candidateColumns} scroll={{ x: 1400 }} expandable={{ expandedRowRender: (user) => <CandidateEvidence user={user} /> }} rowSelection={{ selectedRowKeys: selected, preserveSelectedRowKeys: true, onChange: setSelected, getCheckboxProps: () => ({ disabled: !!candidatesDirty || !!busy }) }} pagination={candidates ? { current: candidates.current, pageSize: candidates.page_size, total: candidates.total, showSizeChanger: true, onChange: (page, size) => { if (appliedCandidates) void search('candidates', appliedCandidates, page, size, false); } } : false} locale={{ emptyText: '先指定入口及观察窗口，查询候选名单。' }} />{contextualEvidence}</> },
      { key: 'cases', label: '轮次档案', children: <><Button icon={<ReloadOutlined />} onClick={() => void loadCases()}>刷新档案</Button><details className="entry-evidence-case-tree"><summary>本页轮次关系树</summary><Typography.Paragraph type="secondary">仅展示当前页已加载档案，不是完整排查树。跨页父轮次可点击打开；删除的历史档案可能无法再读取。</Typography.Paragraph><Tree key={cases.items.map((item) => item.id).join(',')} showLine defaultExpandAll treeData={caseTreeData(evidenceCaseTree(cases.items))} onSelect={(keys) => { if (keys.length) void openCase(Number(keys[0])); }} /></details><Table<EvidenceCase> size="small" rowKey="id" loading={loading === 'cases'} dataSource={cases.items} scroll={{ x: 1050 }} columns={[
        { title: '档案', width: 250, render: (_, item) => <Button type="link" onClick={() => void openCase(item.id)}>#{item.id} {item.title || item.target?.name}</Button> },
        { title: '状态', dataIndex: 'status', width: 100, render: (value) => <Tag color={value === 'executed' ? 'green' : 'blue'}>{value === 'executed' ? '已执行' : '已冻结'}</Tag> },
        { title: '选中 / 候选 / 留原入口', width: 190, render: (_, item) => `${item.selected_count} / ${item.total_candidates} / ${item.residual_count}` },
        { title: '创建时间', dataIndex: 'created_at', width: 180, render: evidenceTime },
        { title: '父轮次', dataIndex: 'parent_case_id', width: 100, render: (id) => id ? <Button type="link" onClick={() => void openCase(id)}>#{id}</Button> : '—' },
        { title: '操作', width: 160, render: (_, item) => <Space><Button size="small" onClick={() => void openCase(item.id)}>查看</Button>{isAdmin && <Popconfirm title="删除此排查档案及保存的名单证据？" description="不会回滚已执行的入口规则。" onConfirm={() => mutation('delete', async () => { await apiJsonPost(`${ENTRY_EVIDENCE_PATH}/case-delete`, { case_id: item.id }); await loadCases(); })}><Button size="small" danger disabled={!!busy}>删除</Button></Popconfirm>}</Space> },
      ]} pagination={{ current: cases.current, pageSize: cases.page_size, total: cases.total, showSizeChanger: true, onChange: (page, size) => void loadCases(page, size) }} /></> },
      ...(isAdmin ? [{ key: 'settings', label: '记录保留', children: <Card size="small" title="历史证据保留设置"><Space direction="vertical"><Typography.Text type="secondary">仅管理员可调整保留期及删除档案。已冻结的证据快照与实时记录分开保留，具体清理范围以服务端设置为准。</Typography.Text><Space><InputNumber aria-label="证据保留天数" min={1} max={365} precision={0} value={keepDays} onChange={(value) => setKeepDays(value || 30)} addonAfter="天" /><Button type="primary" loading={busy === 'settings'} disabled={!status || !!busy} onClick={() => void mutation('settings', async () => { await apiJsonPost(`${ENTRY_EVIDENCE_PATH}/settings`, { keep_days: keepDays }); await loadStatus(); message.success('保留设置已保存'); })}>保存</Button></Space></Space></Card> }] : []),
    ]} />

    <Modal title="冻结排查名单" open={!!freezeMode} onCancel={() => { if (!busy) setFreezeMode(null); }} confirmLoading={busy === 'freeze'} onOk={freeze} okText="保存名单快照" okButtonProps={{ disabled: !!candidatesDirty || !!busy }}>
      <Alert showIcon type="info" message={`保存${freezeMode === 'all' ? '全部匹配' : '已选中'}的 ${freezeMode === 'all' ? candidates?.total || 0 : selected.length} 位用户`} description="此操作只保存证据与名单，尚不改变入口规则。规则或候选名单发生变化时需要重新查询，避免冻结过时结果。" />
      <Form layout="vertical"><Form.Item label="档案名称（可选）"><Input maxLength={100} value={caseTitle} onChange={(event) => setCaseTitle(event.target.value)} placeholder="例如：香港入口 54101 晚高峰排查" /></Form.Item></Form>
    </Modal>

    <Drawer title={record ? `排查档案 #${record.id} · ${record.title || record.target.name}` : '读取排查档案'} open={caseID > 0} onClose={() => closeCase()} width="min(1100px, 100vw)" rootClassName="entry-evidence-drawer" destroyOnHidden>
      {caseError && <Alert type="error" showIcon message={caseError} action={<Button onClick={() => void openCase(caseID)}>重试</Button>} />}
      <Spin spinning={loading === 'case'}>{record && <Space direction="vertical" size="middle" style={{ width: '100%' }}>
        {record.execution?.monitoring_status && <Alert showIcon type={record.execution.monitoring_status === 'inherited' ? 'info' : 'warning'} message={`检测配置：${({ inherited: '已继承', unconfigured: '来源未启用检测', deferred: '尚未完成继承' } as Record<string, string>)[String(record.execution.monitoring_status)] || String(record.execution.monitoring_status)}`} description={String(record.execution.monitoring_warning || '检测配置状态不代表探针已返回健康结果，请到入口检测核对。')} />}
        <Descriptions bordered size="small" column={{ xs: 1, sm: 2, md: 3 }} items={[
          { key: 'target', label: '来源入口', children: record.target.name }, { key: 'state', label: '档案状态', children: record.status === 'executed' ? '已执行二分' : '名单已冻结' }, { key: 'revision', label: '档案版本', children: record.revision }, { key: 'parent', label: '父轮次', children: record.parent_case_id ? <Button type="link" onClick={() => void openCase(record.parent_case_id!)}>#{record.parent_case_id}</Button> : '独立档案' },
          { key: 'window', label: '证据窗口', children: `${evidenceTime(record.query.from)} ～ ${evidenceTime(record.query.to)}` }, { key: 'selected', label: '选中 / 候选', children: `${record.selected_count} / ${record.total_candidates}` }, { key: 'residual', label: '保留原入口', children: record.residual_count },
        ]} />
        <Tabs items={[
          { key: 'users', label: '冻结名单', children: <><Space wrap className="entry-evidence-actions"><Button icon={<CopyOutlined />} loading={busy === 'export'} disabled={!!busy} onClick={() => exportUsers(true)}>复制选中邮箱</Button><Button icon={<DownloadOutlined />} disabled={!!busy} onClick={() => exportUsers()}>导出选中邮箱 TXT</Button><Typography.Text type="secondary">包含全部候选，标记选中及 A／B／R 分支；不随新请求自动增减。</Typography.Text></Space><Table<EvidenceCandidate> size="small" rowKey="user_id" dataSource={caseUsers.items} columns={candidateColumns} scroll={{ x: 1400 }} expandable={{ expandedRowRender: (user) => <CandidateEvidence user={user} /> }} pagination={{ current: caseUsers.current, pageSize: caseUsers.page_size, total: caseUsers.total, showSizeChanger: true, onChange: (page, size) => void openCase(record.id, page, size) }} /></> },
          { key: 'evidence', label: '不可变证据', children: <Space direction="vertical" style={{ width: '100%' }}><EvidenceJSON title="冻结查询条件及名单摘要" value={{ query: record.query, candidates_hash: record.candidates_hash, source_users_revision: record.source_users_revision, source_user_count: record.source_user_count }} /><EvidenceJSON title="节点及 UA 条件快照" value={{ members: record.target.members, conditions: record.target.conditions, revision: record.target.revision }} /><EvidenceJSON title="冻结时探针快照" value={record.probe_snapshot} /><EvidenceJSON title="执行结果" value={record.execution} /></Space> },
          { key: 'split', label: '二分预览 / 执行', children: <Space direction="vertical" size="middle" style={{ width: '100%' }}>
            <Alert type="warning" showIcon message="只分配明确选中的冻结名单，未选中用户必须保留可解释的去向" description="保留原有节点与 UA 条件。普通规则采用名单覆盖层，原动态规则继续处理其他用户；固定叶子分为 A／B，必要时增加 R 组保留原入口。配置写入成功不代表客户端已收到。" />
            {record.status === 'executed' ? <><Alert type="success" message="本轮已执行，不会重复创建分组；请结合后续下发与探针证据观察。" /><Space wrap><Button disabled={!!busy} onClick={() => continueBranch('A')}>继续排查 A 组</Button><Button disabled={!!busy} onClick={() => continueBranch('B')}>继续排查 B 组</Button></Space></> : <>
              <Checkbox checked={pool} disabled={!!busy} onChange={(event) => { setPool(event.target.checked); invalidatePreview(); }}>从健康备用 IP 池分配</Checkbox>
              {!pool && <Row gutter={16}><Col xs={24} md={12}><Input aria-label="A 组入口" value={hostA} disabled={!!busy} placeholder="A 组入口域名或 IP" onChange={(event) => { setHostA(event.target.value); invalidatePreview(); }} /></Col><Col xs={24} md={12}><Input aria-label="B 组入口" value={hostB} disabled={!!busy} placeholder="B 组入口域名或 IP" onChange={(event) => { setHostB(event.target.value); invalidatePreview(); }} /></Col></Row>}
              <Button icon={<BranchesOutlined />} loading={busy === 'preview'} disabled={!!busy || record.selected_count < 2} onClick={prepareSplit}>生成二分预览</Button>
              {preview && <><Notices values={preview.warnings} />{!preview.supported && <Alert type="warning" message={preview.unsupported_reason || '当前快照不能执行'} />}<Table size="small" rowKey="branch" pagination={false} dataSource={preview.groups} scroll={{ x: 550 }} columns={[{ title: '分支', dataIndex: 'branch', width: 70 }, { title: '人数', dataIndex: 'user_count', width: 90 }, { title: '入口去向', dataIndex: 'entry_host' }, { title: '影响', dataIndex: 'unchanged', render: (value) => value ? '保留原入口' : '下次拉取使用新入口' }]} /><EvidenceJSON title="继承的节点和 UA 条件" value={{ members: preview.members, conditions: preview.conditions }} /><Popconfirm title="确认按以上冻结名单执行二分？" description="将修改真实入口分配；不会触碰未选中的其他用户。后端会重新校验规则版本和备用 IP 占用。" onConfirm={executeSplit}><Button type="primary" disabled={!preview.supported || !!busy} loading={busy === 'execute'}>确认执行二分</Button></Popconfirm></>}
            </>}
          </Space> },
          { key: 'observation', label: '观察与结论', children: <><Space direction="vertical" size="middle" style={{ width: '100%', marginBottom: 20 }}>
            <Alert type="info" showIcon message="下发覆盖人数不等于实际连接人数" description="尚无记录不代表没有拉取或获得入口；记录失败、超出保留期和客户端缓存都可能形成证据缺口。观察时长从规则写入生效时间计算，不代表各客户端使用时长。" />
            {record.observation_warning && <Alert type="warning" showIcon message={record.observation_warning} />}
            <Button icon={<ReloadOutlined />} disabled={!!busy || loading === 'case'} onClick={() => void openCase(record.id)}>刷新各分支下发覆盖</Button>
            <Table size="small" rowKey="branch" dataSource={record.observations || []} pagination={false} scroll={{ x: 1300 }} locale={{ emptyText: record.status === 'executed' ? '尚未取得分支观察数据，请刷新后核对。' : '执行二分后开始累计分支下发证据。' }} columns={[
              { title: '分支', dataIndex: 'branch', width: 70 }, { title: '入口', dataIndex: 'entry_host', width: 200 },
              { title: '固定人数', dataIndex: 'fixed_users', width: 100 }, { title: '已记录服务端下发人数', dataIndex: 'acquired_users', width: 170 },
              { title: '尚无记录人数', dataIndex: 'no_recorded_delivery', width: 130 }, { title: '观察时长', dataIndex: 'observed_seconds', width: 170, render: evidenceDuration },
              { title: '首次 / 最后记录', width: 200, render: (_, item) => <>{evidenceTime(item.first_delivery_at)}<br />{evidenceTime(item.last_delivery_at)}</> },
              { title: '覆盖 / 刷新', width: 200, render: (_, item) => <>{item.coverage_gap ? <Tag color="orange">存在保留期缺口</Tag> : <Tag>已保留区间</Tag>}<br />{evidenceTime(item.refreshed_at)}</> },
            ]} />
            <ProbeEvents record={record} />
          </Space><Form layout="vertical"><Form.Item label="观察记录"><Input.TextArea rows={6} maxLength={8000} showCount value={observation} disabled={!!busy} onChange={(event) => setObservation(event.target.value)} placeholder="记录检测任务、观察窗口、各分支表现；区分已确认和待验证事实" /></Form.Item><Form.Item label="观察分类（人工确认）"><Select aria-label="观察分类" allowClear placeholder="选择已有证据支持的分类" value={EVIDENCE_CONCLUSIONS.find((value) => conclusion === value || conclusion.startsWith(`${value}：`))} options={EVIDENCE_CONCLUSIONS.map((value) => ({ label: value, value }))} disabled={!!busy} onChange={(value) => setConclusion((previous) => { const category = EVIDENCE_CONCLUSIONS.find((item) => previous === item || previous.startsWith(`${item}：`)); const detail = category ? previous.slice(category.length).replace(/^：/, '') : previous; return value ? `${value}：${detail}` : detail; })} /></Form.Item><Form.Item label="结论说明"><Input.TextArea rows={3} maxLength={2000} value={conclusion} disabled={!!busy} onChange={(event) => setConclusion(event.target.value)} placeholder="异常与用户存在相关性不代表恶意，请注明证据不足或其他可能因素" /></Form.Item>{observationDirty && <Typography.Paragraph type="warning">有未保存内容；刷新覆盖或翻名单页不会清空草稿。{record.revision !== draftBase.current.revision ? '档案版本已变化，保存将校验原版本；请先复制草稿并重新打开档案核对。' : ''}</Typography.Paragraph>}<Button type="primary" loading={busy === 'observation'} disabled={!!busy} onClick={saveObservation}>保存观察结论</Button></Form></> },
        ]} />
      </Space>}</Spin>
    </Drawer>
  </div>;
}
