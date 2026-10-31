import React, { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Checkbox, Descriptions, Drawer, Form, Grid, Input, InputNumber, Modal, Select, Space, Spin, Switch, Table, Tabs, Tag, Typography, message } from 'antd';
import { DeleteOutlined, HistoryOutlined, MessageOutlined, PlusOutlined, ReloadOutlined, SendOutlined, StopOutlined } from '@ant-design/icons';
import { apiGet, apiJsonPost, getAdminUserInfo } from '../lib/api';
import { AI_DEFAULT_MODELS, AI_DEFAULT_ACCESS_MODE, AI_DEFAULT_FAILOVER_TIMEOUT, AI_REASONING_OPTIONS, aiFallbackRelayForms, aiPrepareFallbackRelays, aiReasoningDefaults, aiReasoningDefaultLabel, aiSessionReasoning, aiAccessMode, aiCanReadSession, aiOwnsFullAccess, aiMergeSessionAccess, aiActionStatus, aiApplyDraft, aiCanApprove, aiConversationTurns, aiFingerprintMatches, aiLatestTurn, aiToolResultName, aiMergeSnapshot, aiNearBottom, aiParseModels, aiRequestID, aiSessionBusy, aiShouldSendKey, aiVisibleDraft, type AIAccessMode, type AIReasoningChoice, type AIAction, type AIConfig, type AIDraft, type AIFingerprint, type AIServer, type AISession, type AISessionDetail } from './aiOpsModel';
import { aiReadSessionStream } from './aiOpsStream';
import AIOpsMarkdown from './AIOpsMarkdown';
import AIOpsActionSummary, { aiActionNames } from './AIOpsActionSummary';
import AIOpsRelaySettings from './AIOpsRelaySettings';
import AIOpsKnowledge from './AIOpsKnowledge';
import AIOpsTelegram from './AIOpsTelegram';
import './AIOpsPage.css';

const { Text, Paragraph } = Typography;
const initialConfig: AIConfig = { enabled: false, base_url: '', models: AI_DEFAULT_MODELS, has_api_key: false };
const time = (value: number) => value ? new Date(value * 1000).toLocaleString('zh-CN', { hour12: false }) : '—';
const statusLabels: Record<string, string> = { idle: '就绪', running: '处理中', pending: '待确认', succeeded: '已执行', completed: '已完成', failed: '失败', unknown: '结果待核对', rejected: '已拒绝', expired: '已过期', interrupted: '已中断', stopped: '已停止' };
const statusColor = (status: string) => ['succeeded', 'completed'].includes(status) ? 'success' : ['running', 'pending'].includes(status) ? 'processing' : ['failed', 'unknown', 'interrupted'].includes(status) ? 'error' : 'default';
const pretty = (value: unknown) => JSON.stringify(value ?? {}, null, 2);
const responseList = (response: any): any[] => Array.isArray(response?.data) ? response.data : [];
type Confirmation = { kind: 'delete' | 'clear' | 'reset' | 'server-delete'; session?: AISession; server?: AIServer };
type Review = { action: AIAction; decision: 'approve' | 'reject' };

function ActionDetails({ action }: { action: AIAction }) {
  const payload = action.payload || {};
  const command = typeof payload.command === 'string' ? payload.command : '';
  return <Space direction="vertical" size="small" style={{ width: '100%' }}>
    <Descriptions size="small" column={1}>
      <Descriptions.Item label="操作">{aiActionNames[action.kind] || action.kind} · #{action.id}</Descriptions.Item>
      {action.server_label && <Descriptions.Item label="操作对象">{action.server_label}</Descriptions.Item>}
      {action.kind === 'ssh_command' && payload.host != null && <Descriptions.Item label="连接身份"><Text code>{String(payload.username || '')}@{String(payload.host)}:{String(payload.port || 22)}</Text></Descriptions.Item>}
      {action.kind !== 'server_register' && payload.host_key_sha256 != null && <Descriptions.Item label="固定主机指纹"><Text code>{String(payload.host_key_sha256)}</Text></Descriptions.Item>}
      <Descriptions.Item label="原因">{action.reason || '—'}</Descriptions.Item>
      <Descriptions.Item label="有效至">{time(action.expires_at)}</Descriptions.Item>
    </Descriptions>
    <AIOpsActionSummary action={action} />
    {action.kind !== 'server_register' && (command ? <><Text strong>{action.approval_mode === 'full_access' ? '完整执行命令' : '完整命令（逐次确认后执行）'}</Text><pre className="ai-ops-pre ai-ops-command">{command}</pre></> : <><Text strong>{action.kind === 'panel_operation' ? '操作参数（敏感值已隐藏）' : '完整变更参数 / 前后快照'}</Text><pre className="ai-ops-pre">{pretty(action.kind === 'panel_operation' ? payload.parameters_preview : payload)}</pre></>)}
  </Space>;
}

export default function AIOpsPage() {
  const [tab, setTab] = useState('chat');
  const [historyOpen, setHistoryOpen] = useState(false);
  const historyNewButton = useRef<HTMLButtonElement>(null);
  const screens = Grid.useBreakpoint();
  const compact = screens.lg === false;
  const [config, setConfig] = useState<AIConfig>(initialConfig);
  const [sessions, setSessions] = useState<AISession[]>([]);
  const [servers, setServers] = useState<AIServer[]>([]);
  const [selectedID, setSelectedID] = useState(0);
  const selectedRef = useRef(0);
  const selectionSequence = useRef(0);
  const [detail, setDetail] = useState<AISessionDetail | null>(null);
  const [model, setModel] = useState(AI_DEFAULT_MODELS[0]);
  const [reasoningEffort, setReasoningEffort] = useState<AIReasoningChoice>('default');
  const reasoningEdited = useRef(false);
  const [newAccessMode, setNewAccessMode] = useState<AIAccessMode>(AI_DEFAULT_ACCESS_MODE);
  const actor = getAdminUserInfo();
  const actorID = Number(actor.id || 0);
  const isAdmin = Number(actor.is_admin) === 1;
  const reasoningChoice: AIReasoningChoice = isAdmin ? reasoningEffort : 'default';
  const [draft, setDraft] = useState('');
  const [loaded, setLoaded] = useState(false);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState('');
  const busyRef = useRef(false);
  const [error, setError] = useState('');
  const [now, setNow] = useState(Date.now());
  const [configForm] = Form.useForm();
  const configuredModelsText = Form.useWatch('models_text', { form: configForm, preserve: true });
  const [serverForm] = Form.useForm();
  const [serverOpen, setServerOpen] = useState(false);
  const [editing, setEditing] = useState<AIServer | null>(null);
  const [fingerprint, setFingerprint] = useState<AIFingerprint | null>(null);
  const [fingerprintLoading, setFingerprintLoading] = useState(false);
  const [review, setReview] = useState<Review | null>(null);
  const [confirmedOutput, setConfirmedOutput] = useState(false);
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null);
  const [resetText, setResetText] = useState('');
  const [sendRetry, setSendRetry] = useState<{ sessionID: number; model: string; content: string; requestID: string; accessMode: AIAccessMode; accessVersion?: number; reasoningEffort: AIReasoningChoice } | null>(null);
  const [streamConnected, setStreamConnected] = useState(false);
  const alive = useRef(true);
  const detailSequence = useRef(0);
  const serverListSequence = useRef(0);
  const refreshedRegistrations = useRef(new Set<string>());
  const fingerprintSequence = useRef(0);
  const pollRunning = useRef(false);
  const messageViewport = useRef<HTMLDivElement>(null);
  const streamActive = useRef(false);
  const followOutput = useRef(true);
  const composing = useRef(false);
  const compositionEndedAt = useRef(0);
  useEffect(() => { if (!compact) setHistoryOpen(false); }, [compact]);

  const applySnapshot = (value: AISessionDetail) => {
    if (!alive.current || selectedRef.current !== value.session.id || !aiCanReadSession(value.session, actorID, isAdmin)) return;
    if (!reasoningEdited.current) setReasoningEffort(aiSessionReasoning(value.session));
    setDetail((previous) => aiMergeSnapshot(previous, { ...value, turns: value.turns || [], actions: value.actions || [] }));
    setSessions((items) => items.map((item) => item.id === value.session.id ? aiMergeSessionAccess(item, value.session) : item));
  };

  const fail = (e: any, fallback: string) => { if (alive.current && !e?.errorFields) message.error(e?.message || fallback); };
  const begin = (name: string) => { if (busyRef.current) return false; busyRef.current = true; setBusy(name); return true; };
  const finish = () => { busyRef.current = false; if (alive.current) setBusy(''); };
  const loadDetail = async (id: number) => {
    if (!id) return;
    const sequence = ++detailSequence.current;
    const response = await apiGet('/ai-ops/session', { id }, { cache: 'no-store' });
    if (!alive.current || selectedRef.current !== id || sequence !== detailSequence.current) return;
    const value = response?.data;
    if (!value?.session || !Array.isArray(value.messages)) throw new Error('会话数据格式不正确');
    if (!aiCanReadSession(value.session, actorID, isAdmin)) throw new Error('只能查看本人创建的会话');
    applySnapshot(value);
  };
  const loadServers = async (selection?: { id: number; sequence: number }) => {
    const sequence = ++serverListSequence.current;
    const response = await apiGet('/ai-ops/servers', {}, { cache: 'no-store' });
    if (!alive.current || sequence !== serverListSequence.current) return;
    if (selection && (selectedRef.current !== selection.id || selectionSequence.current !== selection.sequence)) return;
    setServers(responseList(response));
  };
  const changeTab = (next: string) => {
    if (next === 'settings' && !isAdmin) return;
    setTab(next);
    if (next === 'servers') void loadServers().catch((e) => fail(e, '读取服务器列表失败'));
  };
  const loadLists = async () => {
    const [sessionResult] = await Promise.all([apiGet('/ai-ops/sessions', {}, { cache: 'no-store' }), loadServers()]);
    if (!alive.current) return;
    setSessions((items) => responseList(sessionResult).filter((session) => aiCanReadSession(session, actorID, isAdmin)).map((session) => aiMergeSessionAccess(items.find((item) => item.id === session.id), session)));
  };
  const applyConfig = (next: AIConfig) => {
    if (!next || !Array.isArray(next.models)) throw new Error('AI 配置数据格式不正确');
    setConfig(next);
    if (isAdmin) configForm.setFieldsValue({ ...next, reasoning_efforts: aiReasoningDefaults(next.models, next.reasoning_efforts), models_text: next.models.join('\n'), api_key: '', clear_api_key: false, failover_enabled: !!next.failover_enabled, failover_timeout_seconds: next.failover_timeout_seconds ?? AI_DEFAULT_FAILOVER_TIMEOUT, fallback_relays: aiFallbackRelayForms(next.fallback_relays) });
    setModel((current) => next.models.includes(current) ? current : next.models[0] || AI_DEFAULT_MODELS[0]);
  };
  const load = async (refreshConfig = true) => {
    setLoading(true);
    try {
      await loadLists();
      if (refreshConfig) {
        const response = await apiGet('/ai-ops/config', {}, { cache: 'no-store' });
        if (!alive.current) return;
        applyConfig(response?.data as AIConfig);
      }
      if (selectedRef.current) await loadDetail(selectedRef.current);
      if (alive.current) { setLoaded(true); setError(''); }
    } catch (e: any) { if (alive.current) setError(e?.message || '读取 AI 运维失败'); }
    finally { if (alive.current) setLoading(false); }
  };
  useEffect(() => {
    alive.current = true; void load();
    const tick = window.setInterval(() => setNow(Date.now()), 1000);
    const timer = window.setInterval(async () => {
      if (!selectedRef.current || streamActive.current || pollRunning.current || document.visibilityState === 'hidden') return;
      pollRunning.current = true;
      try { await loadDetail(selectedRef.current); } catch { /* Keep the last snapshot; explicit refresh shows errors. */ }
      finally { pollRunning.current = false; }
    }, 5000);
    return () => { alive.current = false; detailSequence.current += 1; serverListSequence.current += 1; fingerprintSequence.current += 1; window.clearInterval(tick); window.clearInterval(timer); };
  }, []);
  useEffect(() => {
    if (!detail || detail.session.id !== selectedRef.current) return;
    const completed = detail.actions.filter((action) => action.kind === 'server_register' && action.status === 'succeeded' && action.session_id === detail.session.id && !refreshedRegistrations.current.has(`${action.session_id}:${action.id}`));
    if (!completed.length) return;
    completed.forEach((action) => refreshedRegistrations.current.add(`${action.session_id}:${action.id}`));
    const selection = { id: detail.session.id, sequence: selectionSequence.current };
    void loadServers(selection).catch((e) => { if (selectedRef.current === selection.id && selectionSequence.current === selection.sequence) fail(e, '服务器已登记，列表刷新失败，请点击刷新'); });
  }, [detail?.session.id, detail?.actions]);
  const streamWanted = tab === 'chat' && selectedID > 0 && (aiSessionBusy(detail) || sessions.some((item) => item.id === selectedID && item.status === 'running'));
  useEffect(() => {
    if (!streamWanted) return;
    let stopped = false;
    let controller: AbortController | null = null;
    let timer: number | undefined;
    const resetConnection = () => { streamActive.current = false; if (!stopped && alive.current) setStreamConnected(false); };
    const connect = async () => {
      if (stopped || document.visibilityState === 'hidden' || controller) return;
      controller = new AbortController();
      const current = controller;
      let delay = 1000;
      try {
        await aiReadSessionStream(selectedID, current.signal, ({ event, data }) => {
          if (stopped || current.signal.aborted || selectedRef.current !== selectedID) return;
          streamActive.current = true; setStreamConnected(true);
          if (event === 'snapshot') {
            const value = data as AISessionDetail;
            if (value?.session?.id !== selectedID || !Array.isArray(value.messages) || !Array.isArray(value.turns)) return;
            detailSequence.current += 1;
            applySnapshot(value);
          } else if (event === 'draft') {
            const value = data as AIDraft;
            if (value?.session_id !== selectedID) return;
            detailSequence.current += 1;
            setDetail((previous) => aiApplyDraft(previous, value));
          } else if (event === 'invalidate') {
            void loadDetail(selectedID).catch(() => undefined);
          }
        });
      } catch { delay = 5000; /* Old backends or proxies fall back to normal polling. */ }
      finally {
        if (controller === current) controller = null;
        resetConnection();
        if (!stopped) timer = window.setTimeout(() => void connect(), delay);
      }
    };
    const visibility = () => {
      if (document.visibilityState === 'hidden') { window.clearTimeout(timer); controller?.abort(); resetConnection(); }
      else { window.clearTimeout(timer); void connect(); }
    };
    document.addEventListener('visibilitychange', visibility);
    void connect();
    return () => { stopped = true; window.clearTimeout(timer); controller?.abort(); streamActive.current = false; setStreamConnected(false); document.removeEventListener('visibilitychange', visibility); };
  }, [selectedID, streamWanted]);
  const visibleDraft = aiVisibleDraft(detail);
  useEffect(() => {
    // Follow only while already at the bottom; reading earlier messages must
    // not be interrupted by a streamed token or another administrator's action.
    const pane = messageViewport.current;
    if (pane && followOutput.current) pane.scrollTop = pane.scrollHeight;
  }, [detail?.messages.length, detail?.actions.length, visibleDraft?.draft_content]);
  const selectSession = (session: AISession) => {
    if (!aiCanReadSession(session, actorID, isAdmin)) return;
    reasoningEdited.current = false; setReasoningEffort(aiSessionReasoning(session));
    followOutput.current = true;
    selectionSequence.current += 1; selectedRef.current = session.id; setSelectedID(session.id); setDetail(null); setDraft(''); setSendRetry(null); setNewAccessMode(AI_DEFAULT_ACCESS_MODE); setModel(session.model); setTab('chat');
    void loadDetail(session.id).catch((e) => fail(e, '读取会话失败'));
  };
  const newSession = () => { followOutput.current = true; reasoningEdited.current = false; setReasoningEffort('default'); selectionSequence.current += 1; selectedRef.current = 0; detailSequence.current += 1; setSelectedID(0); setDetail(null); setNewAccessMode(AI_DEFAULT_ACCESS_MODE); setDraft(''); setSendRetry(null); setTab('chat'); };
  const accessMode = selectedID ? aiAccessMode(detail?.session) : newAccessMode;
  const foreignFullAccess = !!detail && aiAccessMode(detail.session) === 'full' && !aiOwnsFullAccess(detail.session, actorID);
  const changeAccess = async (mode: AIAccessMode) => {
    if (mode !== 'standard' && mode !== 'full') return;
    if (!selectedRef.current) { setNewAccessMode(mode); setSendRetry(null); return; }
    if (!detail || !begin('access')) return;
    const id = selectedRef.current;
    const selection = selectionSequence.current;
    try {
      const response = await apiJsonPost('/ai-ops/session/access', { id, mode, expected_version: detail.session.access_version || 0 });
      const session = response?.data as AISession;
      if (!session || session.id !== id || session.access_mode !== mode || !Number.isSafeInteger(session.access_version)) throw new Error('权限变更状态未确认，请刷新后核对');
      if (!alive.current) return;
      setSessions((items) => items.map((item) => item.id === id ? aiMergeSessionAccess(item, session) : item));
      if (selectedRef.current === id && selectionSequence.current === selection) {
        setDetail((previous) => previous?.session.id === id ? { ...previous, session: aiMergeSessionAccess(previous.session, session) } : previous);
        setSendRetry(null);
        await loadDetail(id);
      }
      message.success(mode === 'full' ? '本会话已启用完全访问' : '已撤回完全访问，后续操作逐次确认');
    } catch (e) { fail(e, '更改权限失败，请刷新核对'); }
    finally { finish(); }
  };
  const stop = async () => {
    const id = selectedRef.current;
    if (!id || !begin('stop')) return;
    try {
      await apiJsonPost('/ai-ops/chat/stop', { id });
      if (!alive.current) return;
      if (selectedRef.current === id) await loadDetail(id);
      await loadLists();
      message.success('已请求停止；已执行的修改不会自动回滚');
    } catch (e) { fail(e, '停止请求失败，请刷新核对执行状态'); }
    finally { finish(); }
  };
  const send = async (contentOverride?: string) => {
    const content = contentOverride ?? draft;
    if (!content.trim() || !config.enabled || !config.has_api_key || (selectedRef.current > 0 && !detail) || aiSessionBusy(detail) || foreignFullAccess || !begin('send')) return;
    followOutput.current = true;
    const originalID = selectedRef.current;
    const selection = selectionSequence.current;
    const accessVersion = detail?.session.access_version;
    const retry = sendRetry && sendRetry.sessionID === originalID && sendRetry.model === model && sendRetry.content === content && sendRetry.accessMode === accessMode && sendRetry.accessVersion === accessVersion && sendRetry.reasoningEffort === reasoningChoice ? sendRetry : { sessionID: originalID, model, content, requestID: aiRequestID(), accessMode, accessVersion, reasoningEffort: reasoningChoice };
    setSendRetry(retry);
    try {
      const response = await apiJsonPost('/ai-ops/chat/send', { session_id: originalID, model, content, request_id: retry.requestID, access_mode: retry.accessMode, access_version: retry.accessVersion, reasoning_effort: retry.reasoningEffort });
      if (!alive.current) return;
      const session = response?.data?.session as AISession;
      if (!session?.id) throw new Error('发送状态未确认，请刷新后重试同一消息');
      setSendRetry(null);
      if (selectedRef.current === originalID && selectionSequence.current === selection) { reasoningEdited.current = false; setReasoningEffort(aiSessionReasoning(session)); selectedRef.current = session.id; setSelectedID(session.id); setDraft(''); setTab('chat'); await loadDetail(session.id); }
      await loadLists();
    } catch (e) { fail(e, '发送失败，可刷新后重试同一消息'); }
    finally { finish(); }
  };
  const saveConfig = async () => {
    if (!isAdmin || !begin('config')) return;
    try {
      const values = await configForm.validateFields();
      if (config.has_api_key && String(values.base_url || '').trim() !== config.base_url && !values.api_key && !values.clear_api_key) throw new Error('修改中转地址后必须重新输入 API Key 或明确清除旧 Key，不能自动沿用');
      const models = aiParseModels(values.models_text || '');
      const fallbackRelays = aiPrepareFallbackRelays(values.fallback_relays, config.fallback_relays, models);
      const response = await apiJsonPost('/ai-ops/config/save', { enabled: !!values.enabled, base_url: String(values.base_url || '').trim(), models, reasoning_efforts: aiReasoningDefaults(models, values.reasoning_efforts), api_key: values.api_key || '', clear_api_key: !!values.clear_api_key, failover_enabled: !!values.failover_enabled, failover_timeout_seconds: values.failover_timeout_seconds ?? AI_DEFAULT_FAILOVER_TIMEOUT, fallback_relays: fallbackRelays });
      if (!alive.current) return;
      configForm.setFieldsValue({ api_key: '', clear_api_key: false, fallback_relays: fallbackRelays.map((relay) => ({ ...relay, api_key: '', clear_api_key: false })) });
      applyConfig(response?.data as AIConfig);
      message.success('AI 配置已保存'); await load(false);
    } catch (e) { fail(e, '保存配置失败'); }
    finally { finish(); }
  };
  const openServer = (server: AIServer | null) => {
    fingerprintSequence.current += 1; setFingerprintLoading(false); setFingerprint(null); setEditing(server); serverForm.resetFields();
    serverForm.setFieldsValue({ name: server?.name || '', host: server?.host || '', port: server?.port || 22, username: server?.username || 'root', host_key_sha256: server?.host_key_sha256 || '', enabled: server?.enabled ?? true, password: '', private_key: '', key_passphrase: '', clear_password: false, clear_private_key: false, clear_key_passphrase: false });
    setServerOpen(true);
  };
  const closeServer = () => { if (busy) return; fingerprintSequence.current += 1; setFingerprintLoading(false); setFingerprint(null); setServerOpen(false); serverForm.resetFields(); setEditing(null); };
  const probeFingerprint = async () => {
    const sequence = ++fingerprintSequence.current;
    try {
      const values = await serverForm.validateFields(['host', 'port']); setFingerprintLoading(true); setFingerprint(null);
      const response = await apiJsonPost('/ai-ops/server/fingerprint', { host: String(values.host).trim(), port: Number(values.port) });
      if (!alive.current || sequence !== fingerprintSequence.current) return;
      const observed = response?.data as AIFingerprint;
      if (!aiFingerprintMatches(observed, serverForm.getFieldValue('host'), serverForm.getFieldValue('port'))) return;
      setFingerprint(observed);
    } catch (e) { if (sequence === fingerprintSequence.current) fail(e, '读取主机指纹失败'); }
    finally { if (alive.current && sequence === fingerprintSequence.current) setFingerprintLoading(false); }
  };
  const saveServer = async () => {
    if (!begin('server')) return;
    try {
      const values = await serverForm.validateFields();
      const identityChanged = editing && (editing.host !== String(values.host).trim() || editing.port !== Number(values.port) || editing.username !== String(values.username).trim() || editing.host_key_sha256 !== String(values.host_key_sha256).trim());
      if (identityChanged && !values.password && !values.private_key) throw new Error('连接身份已变更，请重新输入密码或私钥；旧凭据不会沿用');
      await apiJsonPost('/ai-ops/server/save', { id: editing?.id || 0, version: editing?.version || 0, ...values, host: String(values.host).trim(), username: String(values.username).trim(), port: Number(values.port), enabled: !!values.enabled });
      if (!alive.current) return;
      fingerprintSequence.current += 1; setServerOpen(false); setFingerprint(null); serverForm.resetFields(); setEditing(null); message.success('服务器已保存'); await loadLists();
    } catch (e) { fail(e, '保存服务器失败'); }
    finally { finish(); }
  };
  const approve = async () => {
    if (!review || !begin('review')) return;
    try {
      if (review.decision === 'approve' && (!confirmedOutput || !aiCanApprove(review.action, config.enabled))) throw new Error('请核对操作与输出风险；过期操作不能执行');
      await apiJsonPost(`/ai-ops/action/${review.decision}`, { session_id: review.action.session_id, id: review.action.id });
      if (!alive.current) return;
      const sessionID = review.action.session_id; setReview(null); setConfirmedOutput(false); message.success(review.decision === 'approve' ? '已确认，请等待执行结果' : '已拒绝此操作');
      if (selectedRef.current === sessionID) await loadDetail(sessionID);
      await loadLists();
    } catch (e) { fail(e, '处理操作失败，请刷新核对，勿重复执行'); }
    finally { finish(); }
  };
  const remove = async () => {
    if (!confirmation || (confirmation.kind === 'reset' && resetText !== 'RESET_AI_DATA') || !begin('remove')) return;
    try {
      if ((confirmation.kind === 'delete' || confirmation.kind === 'clear' || confirmation.kind === 'reset') && !isAdmin) throw new Error('仅管理员可删除会话或清理数据');
      if (confirmation.kind === 'delete') await apiJsonPost('/ai-ops/session/delete', { id: confirmation.session!.id, confirm: true });
      if (confirmation.kind === 'clear') await apiJsonPost('/ai-ops/session/clear', { confirm: true });
      if (confirmation.kind === 'reset') await apiJsonPost('/ai-ops/reset', { confirm: true, confirm_text: 'RESET_AI_DATA' });
      if (confirmation.kind === 'server-delete') await apiJsonPost('/ai-ops/server/delete', { id: confirmation.server!.id, version: confirmation.server!.version, confirm: true });
      if (!alive.current) return;
      if (confirmation.kind !== 'server-delete' && (confirmation.kind !== 'delete' || confirmation.session!.id === selectedRef.current)) { newSession(); setTab(tab); }
      setConfirmation(null); setResetText(''); message.success('已完成清理'); await load(confirmation.kind === 'reset');
    } catch (e) { fail(e, '清理失败'); }
    finally { finish(); }
  };
  const checkServer = (server: AIServer) => { newSession(); setDraft(`请检查已登记服务器 #${server.id}（${server.name}）的状态，仅使用固定只读诊断，不执行其他命令。`); };
  const deployServer = (server: AIServer) => { newSession(); setDraft(`我要在已登记服务器 #${server.id}（${server.name}，${server.host}）上部署或调整节点／中转服务。先检查系统、现有服务和端口，再按我补充的安装脚本与目标配置生成完整非交互命令，按本会话选择的权限模式执行。安装任务请设置足够的执行时间，完成后检查服务与连通性。\n\n部署需求：`); };
  const conversationTurns = aiConversationTurns(detail);
  const latestTurn = aiLatestTurn(detail?.turns || []);
  const unavailable = !config.enabled || !config.has_api_key;
  const sessionHistory = <div className="ai-ops-session-history">
    <Space wrap><Button ref={historyNewButton} icon={<PlusOutlined />} onClick={() => { newSession(); setHistoryOpen(false); }}>新对话</Button>{isAdmin && <Button danger type="text" disabled={!sessions.length || !!busy} onClick={() => setConfirmation({ kind: 'clear' })}>清空全部会话</Button>}</Space>
    <Paragraph type="secondary">{isAdmin ? '全部操作人的会话' : '我的会话'} · 最近 200 个</Paragraph>
    <div className="ai-ops-session-list">{sessions.map((session) => <div className={`ai-ops-session ${selectedID === session.id ? 'is-active' : ''}`} key={session.id}><button onClick={() => { selectSession(session); setHistoryOpen(false); }}><strong>{session.source === 'telegram' && <Tag color="blue">TG</Tag>}{session.title || `会话 #${session.id}`}</strong><span>{session.model}</span><span>{time(session.updated_at)} · 操作人 #{session.created_by} {session.status === 'running' ? '· 处理中' : ''}</span></button>{isAdmin && <Button type="text" danger aria-label={`删除会话 ${session.id}`} icon={<DeleteOutlined />} disabled={!!busy} onClick={() => setConfirmation({ kind: 'delete', session })} />}</div>)}</div>
    {!sessions.length && <Text type="secondary">暂无历史会话</Text>}
  </div>;
  const chat = <div className="ai-ops-chat-layout">
    {!compact && <aside className="ai-ops-sessions">{sessionHistory}</aside>}
    <section className="ai-ops-conversation">
      <div className="ai-ops-conversation-head">
        <div className="ai-ops-conversation-title">{detail?.session.source === 'telegram' && <Tag color="blue">TG</Tag>}<Text strong ellipsis>{detail?.session.title || (selectedID ? '读取会话中…' : '新的运维对话')}</Text>{compact && <div className="ai-ops-mobile-actions"><Button type="text" size="small" aria-label="历史会话" icon={<HistoryOutlined />} onClick={() => setHistoryOpen(true)}>历史</Button><Button type="text" size="small" icon={<PlusOutlined />} onClick={newSession}>新对话</Button></div>}</div>
        <div className={`ai-ops-conversation-controls${isAdmin ? '' : ' ai-ops-conversation-controls-staff'}`}><Select className="ai-ops-model-select" aria-label="选择 AI 模型" value={model} onChange={setModel} disabled={!!busy || aiSessionBusy(detail)} options={config.models.map((value) => ({ value, label: value }))} />{isAdmin && <Select<AIReasoningChoice> className="ai-ops-reasoning-select" aria-label="推理强度" value={reasoningChoice} onChange={(value) => { reasoningEdited.current = true; setReasoningEffort(value); }} disabled={!!busy || aiSessionBusy(detail) || (selectedID > 0 && !detail)} options={[{ value: 'default', label: aiReasoningDefaultLabel(config, model) }, ...AI_REASONING_OPTIONS]} />}<Select<AIAccessMode> className="ai-ops-access-select" aria-label="AI 访问权限" value={accessMode} onChange={(mode) => void changeAccess(mode)} loading={busy === 'access'} disabled={!!busy || (selectedID > 0 && !detail)} options={[{ value: 'standard', label: '逐次确认' }, { value: 'full', label: '完全访问', disabled: aiSessionBusy(detail) && accessMode !== 'full' }]} /></div>
      </div>
      {foreignFullAccess && <div className="ai-ops-access-owner"><Text type="secondary">操作人 #{detail?.session.full_access_by || '未知'} 的授权不可沿用；请切换权限或由你启用。</Text><Button size="small" disabled={!!busy || aiSessionBusy(detail)} onClick={() => void changeAccess('full')}>由我启用完全访问</Button></div>}
      <div className="ai-ops-messages" ref={messageViewport} onScroll={() => { const pane = messageViewport.current; if (pane) followOutput.current = aiNearBottom(pane.scrollTop, pane.clientHeight, pane.scrollHeight); }}>
        {!detail && !selectedID && <div className="ai-ops-empty"><MessageOutlined /><h3>告诉 AI 需要处理什么</h3><p>例如：调整入口规则的 UA 和用户范围、修改节点连接端口、检测指定入口，或在已登记服务器上安装落地／中转服务。</p></div>}
        {selectedID > 0 && !detail && <Spin />}
        {detail?.messages_truncated && <Text type="secondary">当前显示最近 400 条消息，较早记录仍保留；AI 会自动整理发送给模型的上下文。</Text>}
        {conversationTurns.map((group) => {
          const processCount = group.messages.reduce((count, item) => count + (item.tool_calls?.length || 0) + (item.role === 'user' || item.role === 'assistant' ? 0 : 1), group.actions.length);
          const pendingActions = group.actions.filter((action) => aiActionStatus(action, now) === 'pending');
          const processState = pendingActions.length ? 'pending' : group.turn?.status === 'running' || group.actions.some((action) => action.status === 'running') ? 'running' : group.turn?.error || group.actions.some((action) => ['failed', 'unknown', 'interrupted'].includes(action.status)) ? 'failed' : '';
          const process = processCount > 0 ? <details className="ai-ops-process" key="process">
              <summary>
                <span>处理过程 · {processCount} 项</span>
                {processState && <span className={`ai-ops-process-status is-${processState}`}>{processState === 'failed' ? '有异常' : statusLabels[processState]}</span>}
                {pendingActions.map((action) => <button key={action.id} type="button" className="ai-ops-process-review" aria-label={`审核操作 #${action.id}`} disabled={!!busy || aiSessionBusy(detail) || !aiCanApprove(action, config.enabled, now)} onClick={(event) => { event.preventDefault(); event.stopPropagation(); setConfirmedOutput(false); setReview({ action, decision: 'approve' }); }}>审核操作{pendingActions.length > 1 ? ` #${action.id}` : ''}</button>)}
              </summary>
              <div className="ai-ops-process-content">
                {group.messages.map((item, index) => {
                  const toolName = aiToolResultName(group.messages, index);
                  return <React.Fragment key={item.id}>
                    {item.tool_calls?.map((call) => <section key={call.id} className="ai-ops-process-item ai-ops-tool-call"><div className="ai-ops-process-item-head"><Text strong>工具调用 · {call.function.name}</Text><Text type="secondary">{time(item.created_at)}</Text></div><pre className="ai-ops-pre">{call.function.arguments}</pre></section>)}
                    {item.role !== 'user' && item.role !== 'assistant' && <section className="ai-ops-process-item ai-ops-tool-result"><div className="ai-ops-process-item-head"><Text strong>{item.role === 'tool' ? `工具结果${toolName ? ` · ${toolName}` : ''}` : '系统详情'}</Text><Text type="secondary">{time(item.created_at)}</Text></div><pre className="ai-ops-pre">{item.content}</pre></section>}
                  </React.Fragment>;
                })}
                {group.actions.map((action) => { const state = aiActionStatus(action, now); return <section key={action.id} className="ai-ops-process-item ai-ops-action">
                  <div className="ai-ops-process-item-head"><Text strong>{action.approval_mode === 'full_access' ? '执行记录' : '操作确认'} #{action.id}</Text><Tag color={statusColor(state)}>{statusLabels[state] || state}</Tag>{(action.approved_by ?? 0) > 0 && <Text type="secondary">{action.approval_mode === 'full_access' ? '完全访问授权：' : '人工批准：'}操作人 #{action.approved_by}</Text>}{(action.rejected_by ?? 0) > 0 && <Text type="secondary">人工拒绝：操作人 #{action.rejected_by}</Text>}</div>
                  <ActionDetails action={action} />
                  {action.output && <><Text strong>执行输出（会进入模型上下文）</Text><pre className="ai-ops-pre">{action.output}</pre></>}
                  {action.error && <Alert type="error" showIcon message={action.error} />}
                  {state === 'pending' && <Space wrap><Button type="primary" disabled={!!busy || aiSessionBusy(detail) || !aiCanApprove(action, config.enabled, now)} onClick={() => { setConfirmedOutput(false); setReview({ action, decision: 'approve' }); }}>审核执行</Button><Button danger disabled={!!busy} onClick={() => { setConfirmedOutput(false); setReview({ action, decision: 'reject' }); }}>拒绝</Button><Text type="secondary">尚未执行，审核时会展开完整操作。</Text></Space>}
                  {state === 'running' && action.kind === 'ssh_command' && <Text type="secondary">SSH 正在执行，最长 {Number(action.payload.timeout_seconds || 120)} 秒；执行输出会实时更新。</Text>}
                </section>; })}
              </div>
            </details> : null;
          const content: React.ReactNode[] = [];
          let processAdded = false;
          for (const item of group.messages) {
            if (!processAdded && item.role !== 'user') { if (process) content.push(process); processAdded = true; }
            if ((item.role === 'user' || item.role === 'assistant') && item.content.trim()) content.push(<article key={item.id} className={`ai-ops-message ai-ops-message-${item.role}`}><div className="ai-ops-message-meta"><span>{item.role === 'user' ? '操作人' : 'AI'}</span><time>{time(item.created_at)}</time></div>{item.role === 'assistant' ? <AIOpsMarkdown content={item.content} /> : <pre className="ai-ops-text">{item.content}</pre>}</article>);
          }
          if (!processAdded && process) content.push(process);
          if (visibleDraft?.id === group.turnID) content.push(<article key="draft" className="ai-ops-message ai-ops-message-assistant ai-ops-live-draft" aria-label="AI 实时回复"><div className="ai-ops-message-meta">AI · 正在回复</div><AIOpsMarkdown content={visibleDraft.draft_content || ''} /><span className="ai-ops-stream-cursor" aria-hidden="true" /></article>);
          return <section className="ai-ops-turn" key={group.key} data-turn-id={group.turnID}>{content}</section>;
        })}
        {latestTurn?.error && <Alert type="error" showIcon message={`本轮${statusLabels[latestTurn.status] || latestTurn.status}`} description={latestTurn.error} />}
        {aiSessionBusy(detail) && <Space><Spin size="small" /><Text type="secondary">{streamConnected ? '实时接收中…' : '正在处理，自动同步中…'} 可切换会话，不影响后台处理。</Text></Space>}
      </div>
      <div className="ai-ops-composer"><Input.TextArea aria-label="运维消息" value={draft} onChange={(e) => setDraft(e.target.value)} onCompositionStart={() => { composing.current = true; }} onCompositionEnd={() => { composing.current = false; compositionEndedAt.current = Date.now(); }} onKeyDown={(event) => {
        if (!aiShouldSendKey({ key: event.key, shiftKey: event.shiftKey, ctrlKey: event.ctrlKey, altKey: event.altKey, metaKey: event.metaKey, repeat: event.repeat, isComposing: event.nativeEvent.isComposing, keyCode: event.keyCode }, composing.current || Date.now() - compositionEndedAt.current < 50)) return;
        event.preventDefault(); void send();
      }} placeholder={compact ? '描述问题或操作需求…' : '描述问题或操作需求；Enter 发送，Shift+Enter 换行'} autoSize={{ minRows: compact ? 2 : 3, maxRows: compact ? 4 : 8 }} maxLength={12000} disabled={busy === 'send' || aiSessionBusy(detail)} /><div className="ai-ops-composer-actions"><div className="ai-ops-send-controls"><Button type="primary" icon={<SendOutlined />} loading={busy === 'send'} disabled={!draft.trim() || unavailable || !!busy || aiSessionBusy(detail) || foreignFullAccess || (selectedID > 0 && !detail)} onClick={() => void send()}>发送</Button>{aiSessionBusy(detail) && <Button danger icon={<StopOutlined />} loading={busy === 'stop'} disabled={!!busy && busy !== 'stop'} onClick={() => void stop()}>停止</Button>}</div><Text className="ai-ops-composer-hint" type="secondary">{unavailable ? isAdmin ? '请先在设置中启用 AI 并保存中转 API Key。' : '请联系管理员启用 AI 运维。' : compact ? 'Enter 发送 · Shift+Enter 换行' : 'Enter 发送 · Shift+Enter 换行 · 工具详情默认折叠'}</Text></div></div>
    </section>
    <Drawer title="历史会话" aria-label="历史会话" placement="left" rootClassName="ai-ops-history-drawer" width="min(360px, 90vw)" open={compact && historyOpen} onClose={() => setHistoryOpen(false)} destroyOnHidden>{compact && sessionHistory}</Drawer>
  </div>;
  const serverPanel = <Space direction="vertical" size="middle" style={{ width: '100%' }}><Alert type="info" showIcon message="服务器凭据仅保存到后端，不发给模型" description="可通过聊天粘贴连接资料，也可在此表单登记。表单需填写已核对的 SSH 主机指纹；聊天未提供指纹时，会记录首次连接指纹，后续连接必须匹配。逐次确认模式下，每条服务器命令和部署操作均须批准；在对话窗口选择完全访问后，AI 可按当前任务连续执行。" /><Button type="primary" icon={<PlusOutlined />} disabled={!!busy} onClick={() => openServer(null)}>登记服务器</Button><Table<AIServer> rowKey="id" size="small" dataSource={servers} scroll={{ x: 1100 }} columns={[
    { title: '名称', dataIndex: 'name', width: 160, render: (name, server) => <><strong>{name}</strong><div>#{server.id}</div></> },
    { title: '连接', width: 240, render: (_, server) => <Text code>{server.username}@{server.host}:{server.port}</Text> },
    { title: '主机指纹', dataIndex: 'host_key_sha256', width: 300, render: (value) => <span className="ai-ops-wrap">{value}</span> },
    { title: '凭据 / 状态', width: 170, render: (_, server) => <Space size={4} wrap>{server.has_password && <Tag>密码已保存</Tag>}{server.has_private_key && <Tag>私钥已保存</Tag>}<Tag color={server.enabled ? 'success' : 'default'}>{server.enabled ? '启用' : '停用'}</Tag></Space> },
    { title: '操作', fixed: 'right', width: 330, render: (_, server) => <Space size={0} wrap><Button type="link" disabled={!!busy} onClick={() => openServer(server)}>编辑</Button><Button type="link" disabled={!!busy || !server.enabled || unavailable} onClick={() => checkServer(server)}>在聊天中检查</Button><Button type="link" disabled={!!busy || !server.enabled || unavailable} onClick={() => deployServer(server)}>部署／配置</Button><Button type="link" danger disabled={!!busy} onClick={() => setConfirmation({ kind: 'server-delete', server })}>删除</Button></Space> },
  ]} pagination={{ pageSize: 10 }} locale={{ emptyText: '暂无登记的服务器' }} /></Space>;
  const settings = isAdmin ? <Space direction="vertical" size="middle" style={{ width: '100%' }}><Card title="模型中转设置" size="small"><Form form={configForm} layout="vertical" disabled={!!busy} onFinish={saveConfig}>
    <Form.Item name="enabled" label="启用 AI 运维" valuePropName="checked"><Switch /></Form.Item>
    <Form.Item name="base_url" label="中转 Base URL" dependencies={['enabled']} rules={[({ getFieldValue }) => ({ required: !!getFieldValue('enabled'), whitespace: !!getFieldValue('enabled'), message: '请输入中转 Base URL' })]} extra="使用兼容 OpenAI Chat Completions 的 API 基址。修改中转地址后必须重新输入 API Key 或明确清除旧 Key，不会自动沿用旧凭据。"><Input placeholder="https://relay.example.com/v1" autoComplete="off" /></Form.Item>
    <Form.Item name="api_key" label={`API Key · ${config.has_api_key ? '已保存（留空保持不变）' : '未配置'}`} extra="保存后不会回显。不要放在 URL 中或写进聊天。"><Input.Password autoComplete="new-password" placeholder={config.has_api_key ? '留空保留已有 API Key' : '输入 API Key'} /></Form.Item>
    <Form.Item name="clear_api_key" valuePropName="checked"><Checkbox>清除已有 API Key</Checkbox></Form.Item>
    <Form.Item name="models_text" label="模型 ID（每行一个）" rules={[{ validator: (_, value) => aiParseModels(value || '').length ? Promise.resolve() : Promise.reject(new Error('至少填写一个模型 ID')) }]} extra="默认提供四个模型 ID；可以按中转支持的实际 ID 修改，不会自动替换模型。"><Input.TextArea rows={4} /></Form.Item>
    <div className="ai-ops-reasoning-settings"><Text strong>各模型默认推理强度</Text><Paragraph type="secondary">具体等级取决于中转站和模型支持情况；不支持时请选择“中转默认”，请求中不传推理参数。</Paragraph><div className="ai-ops-reasoning-grid">{aiParseModels(configuredModelsText ?? config.models.join('\n')).map((id) => <Form.Item key={id} name={['reasoning_efforts', id]} label={id} initialValue="auto"><Select aria-label={`${id} 默认推理强度`} options={AI_REASONING_OPTIONS} /></Form.Item>)}</div></div>
    <AIOpsRelaySettings config={config} form={configForm} models={aiParseModels(configuredModelsText ?? config.models.join('\n'))} busy={!!busy} />
    <Button type="primary" htmlType="submit" loading={busy === 'config'}>保存设置</Button>
  </Form></Card><Card title="数据清理" size="small"><Paragraph>清空会话只删除聊天、消息及相关操作记录，保留中转配置、服务器凭据和共享知识。重置全部 AI 数据会同时清除上述全部数据并停止进行中的 AI 任务；已经在远程服务器启动的命令不能保证撤回或回滚。有远程操作正在执行时，删除、清空和重置会被拒绝，需等操作完成或进入“结果待核对”状态后再处理。</Paragraph><Space wrap><Button danger disabled={!!busy} onClick={() => setConfirmation({ kind: 'clear' })}>清空全部会话</Button><Button danger type="primary" disabled={!!busy} onClick={() => { setResetText(''); setConfirmation({ kind: 'reset' }); }}>重置全部 AI 数据</Button></Space></Card></Space> : null;
  const titles = { delete: '删除当前会话', clear: '清空全部会话', reset: '重置全部 AI 数据', 'server-delete': '删除服务器配置' };
  const pageStatus = <Space wrap><Tag color={config.enabled && config.has_api_key ? 'success' : 'default'}>{!loaded ? '加载中' : config.enabled ? config.has_api_key ? '已启用' : '缺少 API Key' : '未启用'}</Tag><Button icon={<ReloadOutlined />} loading={loading} disabled={!!busy} onClick={() => void load()}>刷新</Button></Space>;
  return <div className={`legacy-page ai-ops-page${tab === 'chat' ? ' ai-ops-page-chat' : ''}`}><div className="content-heading">AI 运维</div><div className="ai-ops-page-body"><div className="ai-ops-page-stack">{(compact || tab !== 'chat') && pageStatus}{error && <Alert type="error" showIcon message="读取失败" description={error} />}<Tabs className="ai-ops-tabs" tabBarExtraContent={!compact && tab === 'chat' ? { left: <Text strong className="ai-ops-desktop-title">AI 运维</Text>, right: pageStatus } : undefined} activeKey={tab} onChange={changeTab} items={[{ key: 'chat', label: '运维对话', children: chat }, { key: 'servers', label: '服务器', children: serverPanel }, { key: 'knowledge', label: '共享知识', children: <AIOpsKnowledge active={tab === 'knowledge'} isAdmin={isAdmin} actorID={actorID} /> }, { key: 'telegram', label: 'Telegram', children: <AIOpsTelegram active={tab === 'telegram'} isAdmin={isAdmin} actorID={actorID} /> }, ...(isAdmin ? [{ key: 'settings', label: '设置', children: settings }] : [])]} /></div></div>
    <Modal title={editing ? `编辑服务器 · ${editing.name}` : '登记服务器'} open={serverOpen} onCancel={closeServer} onOk={saveServer} confirmLoading={busy === 'server'} okText="保存服务器" cancelButtonProps={{ disabled: !!busy }} closable={!busy} maskClosable={!busy} destroyOnHidden width={720}>
      <Alert type="warning" showIcon message="凭据与连接身份绑定" description="修改地址、端口、用户名或主机指纹后，必须重新输入密码或私钥；旧的密码、私钥和口令都不会沿用。" style={{ marginBottom: 16 }} />
      <Form form={serverForm} layout="vertical" disabled={!!busy} onValuesChange={(changed) => { if ('host' in changed || 'port' in changed) { fingerprintSequence.current += 1; setFingerprint(null); setFingerprintLoading(false); } }}>
        <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true }]}><Input maxLength={100} /></Form.Item>
        <Space align="start" wrap><Form.Item name="host" label="服务器地址" rules={[{ required: true, whitespace: true }]}><Input placeholder="IP 或主机名，不带协议" /></Form.Item><Form.Item name="port" label="SSH 端口" rules={[{ required: true }]}><InputNumber min={1} max={65535} precision={0} /></Form.Item><Form.Item name="username" label="用户名" rules={[{ required: true, whitespace: true }]}><Input autoComplete="off" /></Form.Item></Space>
        <Form.Item name="host_key_sha256" label="已核对的 SSH 主机指纹" rules={[{ required: true, whitespace: true, message: '请核对并填写主机指纹' }]} extra="建议通过服务器控制台或可信渠道核对。获取指纹不会自动填入或修改这里。"><Input placeholder="SHA256:..." /></Form.Item>
        <Button loading={fingerprintLoading} disabled={!!busy} onClick={() => void probeFingerprint()}>获取主机指纹（仅观察）</Button>
        {fingerprint && <Alert style={{ marginTop: 12, marginBottom: 12 }} type="warning" showIcon message="观察到的指纹，不代表已可信" description={<><div>{fingerprint.host}:{fingerprint.port} · {fingerprint.key_type}</div><Text code copyable={{ text: fingerprint.fingerprint }}>{fingerprint.fingerprint}</Text><div>请独立核对后，手动复制到上方指纹字段；与旧值不同不要直接覆盖。</div></>} />}
        <Form.Item name="password" label={`SSH 密码${editing?.has_password ? '（已保存，留空保留）' : ''}`}><Input.Password autoComplete="new-password" /></Form.Item><Form.Item name="clear_password" valuePropName="checked"><Checkbox>清除已存密码</Checkbox></Form.Item>
        <Form.Item name="private_key" label={`SSH 私钥${editing?.has_private_key ? '（已保存，留空保留）' : ''}`}><Input.TextArea rows={4} autoComplete="off" placeholder="可用私钥代替密码；不要填入聊天" /></Form.Item><Form.Item name="clear_private_key" valuePropName="checked"><Checkbox>清除已存私钥</Checkbox></Form.Item>
        <Form.Item name="key_passphrase" label={`私钥口令${editing?.has_key_passphrase ? '（已保存，留空保留）' : ''}`}><Input.Password autoComplete="new-password" /></Form.Item><Form.Item name="clear_key_passphrase" valuePropName="checked"><Checkbox>清除已存私钥口令</Checkbox></Form.Item><Form.Item name="enabled" label="启用" valuePropName="checked"><Switch /></Form.Item>
      </Form>
    </Modal>
    <Modal title={review?.decision === 'approve' ? '审核并执行操作' : '拒绝操作'} open={!!review} onCancel={() => { if (!busy) { setReview(null); setConfirmedOutput(false); } }} onOk={approve} confirmLoading={busy === 'review'} okText={review?.decision === 'approve' ? '确认执行以上操作' : '确认拒绝'} okButtonProps={{ danger: review?.decision === 'approve', disabled: !review || aiActionStatus(review.action, now) !== 'pending' || (review.decision === 'approve' && (!confirmedOutput || !aiCanApprove(review.action, config.enabled, now))) }} cancelButtonProps={{ disabled: !!busy }} closable={!busy} maskClosable={!busy} destroyOnHidden width={820}>
      {review && <Space direction="vertical" size="middle" style={{ width: '100%' }}><Alert type="warning" showIcon message="只批准这一次操作，不授予后续命令自动执行权限" description="请完整核对目标、命令和配置变化；输出会进入对话并发往中转模型，确认无密钥或私人数据。已经启动的远程命令不保证能撤销、终止或回滚。" /><ActionDetails action={review.action} />{review.decision === 'approve' && <Checkbox checked={confirmedOutput} onChange={(e) => setConfirmedOutput(e.target.checked)}>我已核对操作对象、完整修改内容及影响范围，确认执行本次操作</Checkbox>}</Space>}
    </Modal>
    <Modal title={confirmation ? titles[confirmation.kind] : ''} open={!!confirmation} onCancel={() => { if (!busy) { setConfirmation(null); setResetText(''); } }} afterClose={() => { if (historyOpen) historyNewButton.current?.focus(); }} onOk={remove} confirmLoading={busy === 'remove'} okText="确认删除" okButtonProps={{ danger: true, disabled: confirmation?.kind === 'reset' && resetText !== 'RESET_AI_DATA' }} cancelButtonProps={{ disabled: !!busy }} closable={!busy} maskClosable={!busy} destroyOnHidden>
      {confirmation && confirmation.kind !== 'server-delete' && <Alert type="warning" showIcon message="有远程操作正在执行时不能删除或重置" description="请等待操作完成或进入“结果待核对”状态后重试。清理记录不能撤销已经执行的命令。" style={{ marginBottom: 12 }} />}
      {confirmation?.kind === 'delete' && <Paragraph>删除「{confirmation.session?.title}」的聊天、消息和操作记录？删除后不可查看。已启动的远程命令不保证撤销或回滚。</Paragraph>}
      {confirmation?.kind === 'clear' && <Paragraph>将清空全部操作人的会话、消息和相关操作记录，保留中转配置、服务器凭据和共享知识。进行中的 AI 任务会停止；已启动的远程命令不保证撤销或回滚。</Paragraph>}
      {confirmation?.kind === 'server-delete' && <Paragraph>删除「{confirmation.server?.name}」及其 SSH 凭据？不会删除面板节点，也不会卸载服务器上已部署的服务；已启动的命令不保证停止。</Paragraph>}
      {confirmation?.kind === 'reset' && <><Alert type="error" showIcon message="将删除全部 AI 数据" description="包括中转配置及 API Key、全部服务器 SSH 凭据、全部操作人的会话与操作记录，以及全部共享知识。会停止进行中的 AI 任务，但已在远程启动的命令不保证停止或回滚。" /><Paragraph style={{ marginTop: 12 }}>请输入 <Text code>RESET_AI_DATA</Text> 确认：</Paragraph><Input aria-label="重置确认文本" value={resetText} onChange={(e) => setResetText(e.target.value)} autoComplete="off" /></>}
    </Modal>
  </div>;
}
