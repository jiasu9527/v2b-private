import React, { memo, useLayoutEffect, useMemo, useState } from 'react';
import { Alert, Button, Descriptions, Space, Spin, Tag, Typography } from 'antd';
import { MessageOutlined } from '@ant-design/icons';
import { aiActionStatus, aiCanApprove, aiConversationTurns, aiLatestTurn, aiSessionBusy, aiToolResultName, aiVisibleDraft, type AIAction, type AIConversationTurn, type AIMessage, type AISessionDetail } from './aiOpsModel';
import AIOpsMarkdown from './AIOpsMarkdown';
import AIOpsActionSummary, { aiActionNames } from './AIOpsActionSummary';
import { aiServerConnection } from './aiOpsServerIdentity';

const { Text } = Typography;
const fullDate = new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
const shortTime = new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false });
export const aiDisplayTime = (value: number) => value ? fullDate.format(value * 1000) : '—';
const time = aiDisplayTime;
const statusLabels: Record<string, string> = { idle: '就绪', running: '处理中', pending: '待确认', succeeded: '已执行', completed: '已完成', failed: '失败', unknown: '结果待核对', rejected: '已拒绝', expired: '已过期', interrupted: '已中断', stopped: '已停止' };
const statusColor = (status: string) => ['succeeded', 'completed'].includes(status) ? 'success' : ['running', 'pending'].includes(status) ? 'processing' : ['failed', 'unknown', 'interrupted'].includes(status) ? 'error' : 'default';
const pretty = (value: unknown) => JSON.stringify(value ?? {}, null, 2);
export type AIReviewHandler = (action: AIAction, decision: 'approve' | 'reject') => void;

// The explicit approval modal always gets the full command, independently of
// whether the corresponding transcript process has ever been expanded.
export const ActionDetails = memo(function ActionDetails({ action }: { action: AIAction }) {
  const payload = action.payload || {};
  const command = typeof payload.command === 'string' ? payload.command : '';
  return <Space className="ai-ops-action-details" direction="vertical" size="small" style={{ width: '100%' }}>
    <Descriptions size="small" column={1}>
      <Descriptions.Item label="操作">{aiActionNames[action.kind] || action.kind} · #{action.id}</Descriptions.Item>
      {action.server_label && <Descriptions.Item label="操作对象">{action.server_label}</Descriptions.Item>}
      {action.kind === 'ssh_command' && payload.host != null && <Descriptions.Item label="连接身份"><Text code>{aiServerConnection({ username: String(payload.username || ''), host: String(payload.host), port: Number(payload.port || 22) })}</Text></Descriptions.Item>}
      {action.kind !== 'server_register' && payload.host_key_sha256 != null && <Descriptions.Item label="固定主机指纹"><Text code>{String(payload.host_key_sha256)}</Text></Descriptions.Item>}
      <Descriptions.Item label="原因">{action.reason || '—'}</Descriptions.Item>
      <Descriptions.Item label="有效至">{time(action.expires_at)}</Descriptions.Item>
    </Descriptions>
    <AIOpsActionSummary action={action} />
    {action.kind !== 'server_register' && (command ? <><Text strong>{action.approval_mode === 'full_access' ? '完整执行命令' : '完整命令（逐次确认后执行）'}</Text><pre className="ai-ops-pre ai-ops-command">{command}</pre></> : <><Text strong>{action.kind === 'panel_operation' ? '操作参数（敏感值已隐藏）' : '完整变更参数 / 前后快照'}</Text><pre className="ai-ops-pre">{pretty(action.kind === 'panel_operation' ? payload.parameters_preview : payload)}</pre></>)}
  </Space>;
});

const ChatMessage = memo(function ChatMessage({ item, miniApp }: { item: AIMessage; miniApp: boolean }) {
  return <article className={`ai-ops-message ai-ops-message-${item.role}`}><div className="ai-ops-message-meta"><span>{item.role === 'user' ? '操作人' : 'AI'}</span>{miniApp ? <time title={time(item.created_at)} dateTime={item.created_at ? new Date(item.created_at * 1000).toISOString() : undefined}>{item.created_at ? shortTime.format(item.created_at * 1000) : '—'}</time> : <time>{time(item.created_at)}</time>}</div>{item.role === 'assistant' ? <AIOpsMarkdown content={item.content} /> : <pre className="ai-ops-text">{item.content}</pre>}</article>;
});

type ProcessProps = { group: AIConversationTurn; busy: boolean; sessionBusy: boolean; enabled: boolean; now: number; onReview: AIReviewHandler; onLayout: () => void };
const sameItems = <T,>(left: T[], right: T[]) => left === right || (left.length === right.length && left.every((item, index) => item === right[index]));
function sameProcess(previous: ProcessProps, next: ProcessProps) {
  // Draft text/version changes belong to the live reply, not to its tool tree.
  // In particular an open, large process must not rebuild on every token.
  return previous.group.key === next.group.key && previous.group.turn?.status === next.group.turn?.status && previous.group.turn?.error === next.group.turn?.error && sameItems(previous.group.messages, next.group.messages) && sameItems(previous.group.actions, next.group.actions) && previous.busy === next.busy && previous.sessionBusy === next.sessionBusy && previous.enabled === next.enabled && previous.now === next.now && previous.onReview === next.onReview && previous.onLayout === next.onLayout;
}

// Native details hide their body visually but still mount all of its React
// children. Mount it only while open; summaries and approval buttons stay live.
const ProcessDetails = memo(function ProcessDetails({ group, busy, sessionBusy, enabled, now, onReview, onLayout }: ProcessProps) {
  const [open, setOpen] = useState(false);
  useLayoutEffect(() => { onLayout(); }, [open, onLayout]);
  const processCount = group.messages.reduce((count, item) => count + (item.tool_calls?.length || 0) + (item.role === 'user' || item.role === 'assistant' ? 0 : 1), group.actions.length);
  const pendingActions = group.actions.filter((action) => aiActionStatus(action, now) === 'pending');
  const processState = pendingActions.length ? 'pending' : group.turn?.status === 'running' || group.actions.some((action) => action.status === 'running') ? 'running' : group.turn?.error || group.actions.some((action) => ['failed', 'unknown', 'interrupted'].includes(action.status)) ? 'failed' : '';
  return processCount > 0 ? <details className="ai-ops-process" onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary>
        <span>处理过程 · {processCount} 项</span>
        {processState && <span className={`ai-ops-process-status is-${processState}`}>{processState === 'failed' ? '有异常' : statusLabels[processState]}</span>}
        {pendingActions.map((action) => <button key={action.id} type="button" className="ai-ops-process-review" aria-label={`审核操作 #${action.id}`} disabled={busy || sessionBusy || !aiCanApprove(action, enabled, now)} onClick={(event) => { event.preventDefault(); event.stopPropagation(); onReview(action, 'approve'); }}>审核操作{pendingActions.length > 1 ? ` #${action.id}` : ''}</button>)}
      </summary>
      {open && <div className="ai-ops-process-content">
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
          {state === 'pending' && <Space wrap><Button type="primary" disabled={busy || sessionBusy || !aiCanApprove(action, enabled, now)} onClick={() => onReview(action, 'approve')}>审核执行</Button><Button danger disabled={busy} onClick={() => onReview(action, 'reject')}>拒绝</Button><Text type="secondary">尚未执行，审核时会展开完整操作。</Text></Space>}
          {state === 'running' && action.kind === 'ssh_command' && <Text type="secondary">SSH 正在执行，最长 {Number(action.payload.timeout_seconds || 120)} 秒；执行输出会实时更新。</Text>}
        </section>; })}
      </div>}
    </details> : null;

}, sameProcess);

type TurnProps = ProcessProps & { miniApp: boolean; draftContent?: string };
const ConversationTurn = memo(function ConversationTurn(props: TurnProps) {
  const { group, miniApp, draftContent } = props;
  const process = <ProcessDetails key="process" {...props} />;
  const content: React.ReactNode[] = [];
  let processAdded = false;
  for (const item of group.messages) {
    if (!processAdded && item.role !== 'user') { content.push(process); processAdded = true; }
    if ((item.role === 'user' || item.role === 'assistant') && item.content.trim()) content.push(<ChatMessage key={item.id} item={item} miniApp={miniApp} />);
  }
  if (!processAdded) content.push(process);
  if (draftContent) content.push(<article key="draft" className="ai-ops-message ai-ops-message-assistant ai-ops-live-draft" aria-label="AI 实时回复"><div className="ai-ops-message-meta">AI · 正在回复</div><AIOpsMarkdown content={draftContent} /><span className="ai-ops-stream-cursor" aria-hidden="true" /></article>);
  return <section className="ai-ops-turn" data-turn-id={group.turnID}>{content}</section>;
}, (previous, next) => sameProcess(previous, next) && previous.miniApp === next.miniApp && previous.draftContent === next.draftContent);

type TranscriptProps = { detail: AISessionDetail | null; selectedID: number; miniApp: boolean; busy: boolean; enabled: boolean; now: number; streamConnected: boolean; onReview: AIReviewHandler; onLayout: () => void };
function AIOpsTranscript({ detail, selectedID, miniApp, busy, enabled, now, streamConnected, onReview, onLayout }: TranscriptProps) {
  // Group only when actual transcript data changes. Snapshots may allocate new
  // group arrays, so row memoization also compares their individual items.
  const conversationTurns = useMemo(() => aiConversationTurns(detail), [detail?.session.id, detail?.messages, detail?.actions, detail?.turns, detail?.messages_truncated]);
  const latestTurn = useMemo(() => aiLatestTurn(detail?.turns || []), [detail?.turns]);
  const visibleDraft = aiVisibleDraft(detail);
  const sessionBusy = aiSessionBusy(detail);
  return <>
    {!detail && !selectedID && <div className="ai-ops-empty"><MessageOutlined /><h3>告诉 AI 需要处理什么</h3><p>例如：调整入口规则的 UA 和用户范围、修改节点连接端口、检测指定入口，或在已登记服务器上安装落地／中转服务。</p></div>}
    {selectedID > 0 && !detail && <Spin />}
    {detail?.messages_truncated && <Text type="secondary">当前显示最近 400 条消息，较早记录仍保留；AI 会自动整理发送给模型的上下文。</Text>}
    {conversationTurns.map((group) => {
      const hasPending = group.actions.some((action) => action.status === 'pending');
      return <ConversationTurn key={group.key} group={group} miniApp={miniApp} draftContent={visibleDraft?.id === group.turnID ? visibleDraft.draft_content : undefined} busy={hasPending && busy} sessionBusy={hasPending && sessionBusy} enabled={!hasPending || enabled} now={hasPending ? now : 0} onReview={onReview} onLayout={onLayout} />;
    })}
    {latestTurn?.error && <Alert type="error" showIcon message={`本轮${statusLabels[latestTurn.status] || latestTurn.status}`} description={latestTurn.error} />}
    {sessionBusy && <Space><Spin size="small" /><Text type="secondary">{streamConnected ? '实时接收中…' : '正在处理，自动同步中…'} 可切换会话，不影响后台处理。</Text></Space>}
  </>;
}
export default memo(AIOpsTranscript);
