import React, { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Collapse, DatePicker, Descriptions, Drawer, Empty, Form, Input, Select, Space, Spin, Table, Tag, Typography } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { apiGet } from '../lib/api';
import './NewAPIConversations.css';

type ConversationFilter = { user_id: string; email: string; keyword: string; model: string; mode?: string; start_at?: number; end_at?: number };
type ConversationRow = {
  id: number; user_id: number; user_email: string; user_deleted: boolean; title: string;
  mode: string; model: string; character_id: number; character_name: string; message_count: number;
  last_message_at: number; created_at: number; updated_at: number;
};
type ConversationPage = { items: ConversationRow[]; total: number; current: number; page_size: number };
type ChatMessage = { id: number; conversation_id: number; role: string; content: string; status: string; created_at: number; updated_at: number };
type MessagePage = { items: ChatMessage[]; has_more: boolean; next_before_id: number };
type CharacterSnapshot = { name?: string; description?: string; personality?: string; scenario?: string; first_mes?: string; mes_example?: string; system_prompt?: string; post_history_instructions?: string; creator_notes?: string; character_book?: { entries?: Array<{ name?: string; content?: string }> } };
type ConversationDetail = {
  conversation: { id: number; title: string; mode: string; model: string; character_id: number; persona: string; parent_id: number; created_at: number; updated_at: number };
  user_id: number; user_email: string; user_deleted: boolean; character_snapshot: CharacterSnapshot | null;
  messages: ChatMessage[]; has_more: boolean; next_before_id: number;
};

const { Text } = Typography;
const emptyFilter: ConversationFilter = { user_id: '', email: '', keyword: '', model: '' };
const emptyPage: ConversationPage = { items: [], total: 0, current: 1, page_size: 20 };
const positive = (value: unknown) => Number.isSafeInteger(value) && Number(value) > 0;
const nonnegative = (value: unknown) => Number.isSafeInteger(value) && Number(value) >= 0;
const formatTime = (value?: number) => positive(value) ? new Date(value * 1000).toLocaleString('zh-CN', { hour12: false }) : '—';
const modeLabel = (value: string) => value === 'character' ? '角色聊天' : value === 'chat' ? '普通聊天' : value || '—';
const roleLabels: Record<string, string> = { user: '用户', assistant: 'AI', system: '系统', tool: '工具' };
const statusLabels: Record<string, string> = { complete: '已完成', streaming: '生成中', partial: '部分完成', failed: '失败', unknown: '待核对' };

export function adminConversationQuery(filter: ConversationFilter, current: number, pageSize: number): Record<string, string | number> {
  if (!positive(current) || current > 100000 || !positive(pageSize) || pageSize > 100) throw new Error('会话分页范围无效');
  const query: Record<string, string | number> = { current, page_size: pageSize };
  const userID = filter.user_id.trim();
  if (userID) {
    if (!/^\d+$/.test(userID) || !positive(Number(userID))) throw new Error('用户 ID 必须是正整数');
    query.user_id = Number(userID);
  }
  for (const [key, max] of [['email', 254], ['keyword', 100], ['model', 200]] as const) {
    const value = filter[key].trim();
    if (Array.from(value).length > max) throw new Error(`${key === 'email' ? '邮箱' : key === 'model' ? '模型名称' : '标题关键词'}过长`);
    if (value) query[key] = value;
  }
  if (filter.mode) {
    if (filter.mode !== 'chat' && filter.mode !== 'character') throw new Error('会话类型无效');
    query.mode = filter.mode;
  }
  for (const key of ['start_at', 'end_at'] as const) {
    if (filter[key] !== undefined) {
      if (!positive(filter[key])) throw new Error('请选择有效的创建时间');
      query[key] = filter[key];
    }
  }
  if (filter.start_at && filter.end_at && filter.start_at >= filter.end_at) throw new Error('结束时间必须晚于开始时间（结束时刻不计入）');
  return query;
}

export function adminConversationPage(value: unknown): ConversationPage {
  const page = value as ConversationPage;
  if (!page || !Array.isArray(page.items) || !nonnegative(page.total) || !positive(page.current) || page.current > 100000 || !positive(page.page_size) || page.page_size > 100 || page.items.length > page.page_size || page.total < page.items.length) throw new Error('会话列表格式无效，请重新查询');
  for (const row of page.items) {
    if (!row || !positive(row.id) || !positive(row.user_id) || typeof row.user_deleted !== 'boolean' || !nonnegative(row.character_id) || !nonnegative(row.message_count) ||
      ['user_email', 'title', 'mode', 'model', 'character_name'].some((key) => typeof row[key] !== 'string') ||
      ['created_at', 'updated_at', 'last_message_at'].some((key) => !nonnegative(row[key]))) throw new Error('会话列表格式无效，请重新查询');
  }
  return page;
}

export function adminMessagePage(value: unknown, conversationID: number, beforeID?: number): MessagePage {
  const page = value as MessagePage;
  if (!page || !Array.isArray(page.items) || page.items.length > 200 || typeof page.has_more !== 'boolean' || !nonnegative(page.next_before_id)) throw new Error('消息分页格式无效，请重新打开会话');
  const seen = new Set<number>();
  for (const item of page.items) {
    if (!item || !positive(item.id) || item.conversation_id !== conversationID || seen.has(item.id) ||
      (beforeID !== undefined && item.id >= beforeID) ||
      ['role', 'content', 'status'].some((key) => typeof item[key] !== 'string') ||
      !nonnegative(item.created_at) || !nonnegative(item.updated_at)) throw new Error('消息归属或顺序无效，请重新打开会话');
    seen.add(item.id);
  }
  const items = [...page.items].sort((a, b) => a.id - b.id);
  if (page.has_more && (!items.length || page.next_before_id !== items[0].id)) throw new Error('历史消息游标无效，请重新打开会话');
  return { items, has_more: page.has_more, next_before_id: page.next_before_id };
}

export function adminConversationDetail(value: unknown, id: number): ConversationDetail {
  const detail = value as ConversationDetail;
  const conversation = detail?.conversation;
  if (!conversation || conversation.id !== id || !positive(detail.user_id) || typeof detail.user_email !== 'string' || typeof detail.user_deleted !== 'boolean' ||
    ['title', 'mode', 'model', 'persona'].some((key) => typeof conversation[key] !== 'string') ||
    ['character_id', 'parent_id', 'created_at', 'updated_at'].some((key) => !nonnegative(conversation[key])) ||
    (detail.character_snapshot !== null && (!detail.character_snapshot || typeof detail.character_snapshot !== 'object' || Array.isArray(detail.character_snapshot)))) throw new Error('会话详情格式或归属无效，请重新打开');
  const page = adminMessagePage({ items: detail.messages, has_more: detail.has_more, next_before_id: detail.next_before_id }, id);
  return { ...detail, messages: page.items };
}

export function mergeAdminMessages(current: ChatMessage[], older: ChatMessage[]): ChatMessage[] {
  const items = new Map<number, ChatMessage>();
  for (const item of [...older, ...current]) items.set(item.id, item);
  return [...items.values()].sort((a, b) => a.id - b.id);
}

function PlainText({ value }: { value: unknown }) {
  return <div className="new-api-conversation-text">{typeof value === 'string' && value ? value : '—'}</div>;
}

function CharacterContext({ detail }: { detail: ConversationDetail }) {
  const snapshot = detail.character_snapshot;
  const fields: Array<[keyof CharacterSnapshot, string]> = [['description', '角色描述'], ['personality', '性格'], ['scenario', '场景'], ['first_mes', '角色开场'], ['mes_example', '对话示例'], ['system_prompt', '角色提示词'], ['post_history_instructions', '后置说明'], ['creator_notes', '作者说明']];
  const entries = Array.isArray(snapshot?.character_book?.entries) ? snapshot.character_book.entries : [];
  return <Collapse size="small" items={[
    { key: 'persona', label: '用户设定', children: <PlainText value={detail.conversation.persona} /> },
    ...(snapshot ? [{ key: 'character', label: `会话角色快照 · ${typeof snapshot.name === 'string' ? snapshot.name : '未命名角色'}`, children: <div className="new-api-conversation-context">
      {fields.filter(([key]) => typeof snapshot[key] === 'string' && snapshot[key]).map(([key, label]) => <section key={key}><Text strong>{label}</Text><PlainText value={snapshot[key]} /></section>)}
      {entries.length > 0 && <section><Text strong>世界书 / 背景知识</Text>{entries.map((entry, index) => <div key={index}><Text type="secondary">{typeof entry?.name === 'string' ? entry.name : `条目 ${index + 1}`}</Text><PlainText value={entry?.content} /></div>)}</section>}
    </div> }] : []),
  ]} />;
}

export default function NewAPIConversations() {
  const [filter, setFilter] = useState<ConversationFilter>({ ...emptyFilter });
  const [applied, setApplied] = useState<ConversationFilter>({ ...emptyFilter });
  const [page, setPage] = useState<ConversationPage>({ ...emptyPage });
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [selectedID, setSelectedID] = useState<number>();
  const [detail, setDetail] = useState<ConversationDetail>();
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState('');
  const [olderLoading, setOlderLoading] = useState(false);
  const [olderError, setOlderError] = useState('');
  const mounted = useRef(true);
  const requests = useRef({ list: 0, detail: 0, older: 0 });
  const controllers = useRef<{ list?: AbortController; detail?: AbortController; older?: AbortController }>({});
  const activeID = useRef<number>();
  const olderBusy = useRef(false);
  const transcript = useRef<HTMLDivElement>(null);
  const scrollRestore = useRef<{ id: number; height?: number; top?: number }>();

  const closeDetail = () => {
    ++requests.current.detail; ++requests.current.older;
    controllers.current.detail?.abort(); controllers.current.older?.abort();
    activeID.current = undefined; olderBusy.current = false; scrollRestore.current = undefined;
    setSelectedID(undefined); setDetail(undefined); setDetailError(''); setOlderError(''); setDetailLoading(false); setOlderLoading(false);
  };

  const loadList = async (query = applied, current = page.current, pageSize = page.page_size) => {
    const sequence = ++requests.current.list;
    controllers.current.list?.abort();
    closeDetail();
    let params: Record<string, string | number>;
    try { params = adminConversationQuery(query, current, pageSize); }
    catch (failure: any) { setPage({ ...emptyPage }); setLoading(false); setError(failure.message); return; }
    const controller = new AbortController(); controllers.current.list = controller;
    setLoading(true); setError(''); setPage({ items: [], current, page_size: pageSize, total: 0 });
    try {
      const response = await apiGet('/new-api/conversations', params, { cache: 'no-store', signal: controller.signal });
      const next = adminConversationPage(response?.data);
      if (next.current !== current || next.page_size !== pageSize) throw new Error('会话列表页码与查询不一致，请重新查询');
      if (mounted.current && sequence === requests.current.list) { setPage(next); setApplied({ ...query }); }
    } catch (failure: any) { if (mounted.current && sequence === requests.current.list) setError(failure?.message || '读取用户聊天列表失败'); }
    finally { if (mounted.current && sequence === requests.current.list) setLoading(false); }
  };

  const openDetail = async (id: number) => {
    if (!positive(id)) return;
    closeDetail();
    const sequence = ++requests.current.detail;
    const controller = new AbortController(); controllers.current.detail = controller;
    activeID.current = id; setSelectedID(id); setDetailLoading(true);
    try {
      const response = await apiGet('/new-api/conversations/detail', { id }, { cache: 'no-store', signal: controller.signal });
      const next = adminConversationDetail(response?.data, id);
      if (mounted.current && sequence === requests.current.detail && activeID.current === id) {
        scrollRestore.current = { id }; setDetail(next);
      }
    } catch (failure: any) { if (mounted.current && sequence === requests.current.detail && activeID.current === id) setDetailError(failure?.message || '读取聊天详情失败'); }
    finally { if (mounted.current && sequence === requests.current.detail && activeID.current === id) setDetailLoading(false); }
  };

  const loadOlder = async () => {
    if (!detail || !detail.has_more || olderBusy.current || activeID.current !== detail.conversation.id) return;
    const id = detail.conversation.id, beforeID = detail.next_before_id;
    const sequence = ++requests.current.older, detailSequence = requests.current.detail;
    const controller = new AbortController(); controllers.current.older = controller;
    olderBusy.current = true; setOlderLoading(true); setOlderError('');
    try {
      const response = await apiGet('/new-api/conversations/messages', { id, before_id: beforeID, page_size: 100 }, { cache: 'no-store', signal: controller.signal });
      const next = adminMessagePage(response?.data, id, beforeID);
      if (mounted.current && sequence === requests.current.older && detailSequence === requests.current.detail && activeID.current === id) {
        scrollRestore.current = { id, height: transcript.current?.scrollHeight || 0, top: transcript.current?.scrollTop || 0 };
        setDetail((current) => current?.conversation.id === id ? { ...current, messages: mergeAdminMessages(current.messages, next.items), has_more: next.has_more, next_before_id: next.next_before_id } : current);
      }
    } catch (failure: any) { if (mounted.current && sequence === requests.current.older && detailSequence === requests.current.detail && activeID.current === id) setOlderError(failure?.message || '读取较早消息失败'); }
    finally {
      if (mounted.current && sequence === requests.current.older && detailSequence === requests.current.detail && activeID.current === id) { olderBusy.current = false; setOlderLoading(false); }
    }
  };

  useEffect(() => {
    mounted.current = true;
    void loadList({ ...emptyFilter }, 1, 20);
    return () => {
      mounted.current = false;
      ++requests.current.list; ++requests.current.detail; ++requests.current.older;
      for (const controller of Object.values(controllers.current)) controller?.abort();
      activeID.current = undefined; scrollRestore.current = undefined;
    };
  }, []);

  useLayoutEffect(() => {
    const restore = scrollRestore.current, element = transcript.current;
    if (!restore || !element || restore.id !== detail?.conversation.id) return;
    element.scrollTop = restore.height === undefined ? element.scrollHeight : (restore.top || 0) + Math.max(0, element.scrollHeight - restore.height);
    scrollRestore.current = undefined;
  }, [detail?.messages]);

  const patch = (next: Partial<ConversationFilter>) => setFilter((current) => ({ ...current, ...next }));
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || '本地时区';
  return <div className="new-api-conversations">
    <Text type="secondary">只读查看 Forest 用户的 AI 聊天记录；不调用模型、不扣余额。这里只展示当前仍保留的会话。</Text>
    <Card size="small"><Form layout="vertical" onFinish={() => void loadList(filter, 1)}>
      <div className="new-api-conversation-filters">
        <Form.Item label="用户 ID"><Input aria-label="聊天用户 ID" value={filter.user_id} inputMode="numeric" placeholder="精确用户 ID" allowClear onChange={(event) => patch({ user_id: event.target.value })} /></Form.Item>
        <Form.Item label="用户邮箱"><Input aria-label="聊天用户邮箱" value={filter.email} placeholder="完整邮箱，精确匹配" maxLength={254} allowClear onChange={(event) => patch({ email: event.target.value })} /></Form.Item>
        <Form.Item label="会话标题"><Input aria-label="聊天标题关键词" value={filter.keyword} placeholder="标题包含关键词" maxLength={100} allowClear onChange={(event) => patch({ keyword: event.target.value })} /></Form.Item>
        <Form.Item label="模型"><Input aria-label="聊天模型" value={filter.model} placeholder="完整模型名称" maxLength={200} allowClear onChange={(event) => patch({ model: event.target.value })} /></Form.Item>
        <Form.Item label="会话类型"><Select aria-label="聊天类型" value={filter.mode} placeholder="全部类型" allowClear options={[{ value: 'chat', label: '普通聊天' }, { value: 'character', label: '角色聊天' }]} onChange={(mode) => patch({ mode })} /></Form.Item>
        <Form.Item label="创建开始时间（含）"><DatePicker aria-label="聊天创建开始时间" value={filter.start_at ? dayjs.unix(filter.start_at) : null} format="YYYY-MM-DD HH:mm" showTime={{ format: 'HH:mm' }} placeholder="开始时间" onChange={(value) => patch({ start_at: value?.startOf('minute').unix() })} /></Form.Item>
        <Form.Item label="创建结束时间（不含）"><DatePicker aria-label="聊天创建结束时间" value={filter.end_at ? dayjs.unix(filter.end_at) : null} format="YYYY-MM-DD HH:mm" showTime={{ format: 'HH:mm' }} placeholder="结束时间" onChange={(value) => patch({ end_at: value?.startOf('minute').unix() })} /></Form.Item>
      </div>
      <Space wrap><Button type="primary" htmlType="submit" loading={loading}>查询聊天</Button><Button onClick={() => { setFilter({ ...emptyFilter }); void loadList({ ...emptyFilter }, 1); }}>重置</Button><Button icon={<ReloadOutlined />} onClick={() => void loadList()} loading={loading}>刷新当前结果</Button><Text type="secondary">时间精确到分钟 · {timezone}</Text></Space>
    </Form></Card>
    {JSON.stringify(filter) !== JSON.stringify(applied) && <Text type="secondary">筛选条件尚未应用；查询后生效，翻页使用上次成功查询的条件。</Text>}
    {error && <Alert type="error" showIcon message={error} action={<Button size="small" onClick={() => void loadList(filter, 1)}>重新查询</Button>} />}
    <Table<ConversationRow> size="small" rowKey="id" loading={loading} dataSource={page.items} scroll={{ x: 1320 }} locale={{ emptyText: loading ? '正在读取聊天列表…' : error ? '查询失败，未显示旧结果' : '没有符合条件的聊天会话' }} columns={[
      { title: '用户', key: 'user', width: 210, render: (_, row) => <div><div>#{row.user_id} {row.user_deleted && <Tag>账号已删除</Tag>}</div><span className="new-api-conversation-wrap">{row.user_email || '邮箱不可用'}</span></div> },
      { title: '会话', key: 'title', width: 260, render: (_, row) => <div><div className="new-api-conversation-wrap">{row.title || '未命名会话'}</div><Text type="secondary">#{row.id} · {modeLabel(row.mode)}{row.character_name ? ` · ${row.character_name}` : ''}</Text></div> },
      { title: '模型', dataIndex: 'model', width: 190, render: (value) => <span className="new-api-conversation-wrap">{value || '—'}</span> },
      { title: '消息数', dataIndex: 'message_count', width: 90 },
      { title: '创建时间', dataIndex: 'created_at', width: 190, render: formatTime },
      { title: '最近消息', dataIndex: 'last_message_at', width: 190, render: formatTime },
      { title: '操作', key: 'action', width: 100, fixed: 'right', render: (_, row) => <Button size="small" onClick={() => void openDetail(row.id)}>查看聊天</Button> },
    ]} pagination={{ current: page.current, pageSize: page.page_size, total: page.total, showSizeChanger: true, pageSizeOptions: [20, 50, 100], showTotal: (total) => `共 ${total} 个会话`, onChange: (current, size) => void loadList(applied, current, size) }} />
    <Drawer title={`用户聊天${selectedID ? ` #${selectedID}` : ''}`} open={selectedID !== undefined} onClose={closeDetail} width="min(880px, 100vw)" rootClassName="new-api-conversation-drawer" destroyOnHidden>
      {detailLoading && <div className="new-api-conversation-loading"><Spin tip="正在读取聊天记录…"><div style={{ minHeight: 80 }} /></Spin></div>}
      {detailError && <Alert type="error" showIcon message={detailError} action={<Button size="small" onClick={() => selectedID && void openDetail(selectedID)}>重新读取</Button>} />}
      {detail && <div className="new-api-conversation-detail">
        <Descriptions bordered size="small" column={{ xs: 1, sm: 2 }}>
          <Descriptions.Item label="会话标题" span={2}><PlainText value={detail.conversation.title || '未命名会话'} /></Descriptions.Item>
          <Descriptions.Item label="用户"><span className="new-api-conversation-wrap">#{detail.user_id} · {detail.user_email || '邮箱不可用'} {detail.user_deleted && <Tag>账号已删除</Tag>}</span></Descriptions.Item>
          <Descriptions.Item label="类型">{modeLabel(detail.conversation.mode)}</Descriptions.Item>
          <Descriptions.Item label="模型"><span className="new-api-conversation-wrap">{detail.conversation.model || '—'}</span></Descriptions.Item>
          <Descriptions.Item label="角色">{typeof detail.character_snapshot?.name === 'string' ? detail.character_snapshot.name : '无角色快照'}{detail.conversation.character_id > 0 ? ` · #${detail.conversation.character_id}` : ''}</Descriptions.Item>
          <Descriptions.Item label="创建时间">{formatTime(detail.conversation.created_at)}</Descriptions.Item>
          <Descriptions.Item label="更新时间">{formatTime(detail.conversation.updated_at)}</Descriptions.Item>
          {detail.conversation.parent_id > 0 && <Descriptions.Item label="分支来源">会话 #{detail.conversation.parent_id}</Descriptions.Item>}
        </Descriptions>
        <CharacterContext detail={detail} />
        <Space wrap><Button onClick={() => void loadOlder()} disabled={!detail.has_more} loading={olderLoading}>加载更早消息</Button><Text type="secondary">已加载 {detail.messages.length} 条 · 旧 → 新{!detail.has_more ? ' · 已到最早记录' : ''}</Text></Space>
        {olderError && <Alert type="error" showIcon message={olderError} />}
        <div ref={transcript} className="new-api-conversation-messages" tabIndex={0} aria-label="聊天消息记录">
          {!detail.messages.length && <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="此会话尚无消息记录" />}
          {detail.messages.map((item) => <article key={item.id} className={`new-api-conversation-message${item.role === 'user' ? ' is-user' : ''}`}>
            <div className="new-api-conversation-message-meta"><Text strong>{roleLabels[item.role] || item.role}</Text><Tag color={item.status === 'failed' || item.status === 'unknown' ? 'warning' : undefined}>{statusLabels[item.status] || item.status}</Tag><Text type="secondary">#{item.id} · {formatTime(item.created_at)}</Text></div>
            <PlainText value={item.content || '（此条消息没有文本内容）'} />
          </article>)}
        </div>
      </div>}
    </Drawer>
  </div>;
}
