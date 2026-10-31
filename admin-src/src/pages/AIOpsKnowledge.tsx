import React, { useEffect, useRef, useState } from 'react';
import { Alert, Button, Checkbox, Empty, Form, Input, Modal, Pagination, Popconfirm, Select, Space, Spin, Tag, Typography, message } from 'antd';
import { DeleteOutlined, EditOutlined, PlusOutlined, PushpinOutlined, ReloadOutlined } from '@ant-design/icons';
import { apiGet, apiJsonPost } from '../lib/api';
import AIOpsMarkdown from './AIOpsMarkdown';
import { AI_KNOWLEDGE_CATEGORIES, AI_KNOWLEDGE_STATUSES, aiKnowledgeCanRead, aiKnowledgeDetail, aiKnowledgeList, aiKnowledgeSavePayload, aiKnowledgeValues, type AIKnowledgeEntry, type AIKnowledgeValues } from './aiOpsKnowledgeModel';
import './AIOpsKnowledge.css';

const { Text, Paragraph } = Typography;
const timestamp = (value: number) => value ? new Date(value * 1000).toLocaleString('zh-CN', { hour12: false }) : '—';
const categoryName = (value: string) => AI_KNOWLEDGE_CATEGORIES.find((item) => item.value === value)?.label || '其他';
const statusName = (value: string) => AI_KNOWLEDGE_STATUSES.find((item) => item.value === value)?.label || value;
type Filters = { query: string; category: string; status: string; current: number; page_size: number };

function KnowledgeMeta({ entry }: { entry: AIKnowledgeEntry }) {
  return <div className="ai-knowledge-meta"><span>{entry.source_kind === 'ai' ? 'AI 整理' : '人工录入'}</span><span>更新于 {timestamp(entry.updated_at)} · 操作人 #{entry.updated_by || entry.created_by}</span>{entry.source_session_id > 0 && <span>来源会话 #{entry.source_session_id}{entry.source_turn_id > 0 ? ` · 轮次 #${entry.source_turn_id}` : ''}</span>}<span>版本 {entry.version}</span></div>;
}

export default function AIOpsKnowledge({ active, isAdmin, actorID, miniBackHandler }: { active: boolean; isAdmin: boolean; actorID: number; miniBackHandler?: React.MutableRefObject<(() => boolean) | null> }) {
  const [query, setQuery] = useState('');
  const [filters, setFilters] = useState<Filters>({ query: '', category: '', status: '', current: 1, page_size: 20 });
  const [entries, setEntries] = useState<AIKnowledgeEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [mode, setMode] = useState<'view' | 'edit' | 'create' | null>(null);
  const [entry, setEntry] = useState<AIKnowledgeEntry | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState('');
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');
  const [deleting, setDeleting] = useState<AIKnowledgeEntry | null>(null);
  const [deleteError, setDeleteError] = useState('');
  const [form] = Form.useForm<AIKnowledgeValues>();
  const alive = useRef(true), listSequence = useRef(0), detailSequence = useRef(0), mutation = useRef(false);
  useEffect(() => { alive.current = true; return () => { alive.current = false; listSequence.current += 1; detailSequence.current += 1; }; }, []);
  const load = async () => {
    if (!active) return;
    const sequence = ++listSequence.current;
    setLoading(true);
    try {
      const response = await apiGet('/ai-ops/knowledge', filters, { cache: 'no-store' });
      if (!alive.current || sequence !== listSequence.current) return;
      const result = aiKnowledgeList(response?.data);
      setEntries(result.data.filter((item) => aiKnowledgeCanRead(item, actorID, isAdmin))); setTotal(result.total); setError('');
    } catch (cause: any) { if (alive.current && sequence === listSequence.current) setError(cause?.message || '读取共享知识失败'); }
    finally { if (alive.current && sequence === listSequence.current) setLoading(false); }
  };
  useEffect(() => { if (active) void load(); else listSequence.current += 1; }, [active, filters]);
  const read = async (id: number, nextMode: 'view' | 'edit' = 'view') => {
    if (nextMode === 'edit' && !isAdmin) return;
    const sequence = ++detailSequence.current;
    setMode(nextMode); setDetailLoading(true); setDetailError(''); setSaveError('');
    try {
      const response = await apiGet('/ai-ops/knowledge/detail', { id }, { cache: 'no-store' });
      if (!alive.current || sequence !== detailSequence.current) return;
      const detail = aiKnowledgeDetail(response?.data);
      if (detail.id !== id || !aiKnowledgeCanRead(detail, actorID, isAdmin)) throw new Error('无法查看这条资料');
      setEntry(detail); form.setFieldsValue(aiKnowledgeValues(detail));
    } catch (cause: any) { if (alive.current && sequence === detailSequence.current) setDetailError(cause?.message || '读取知识详情失败'); }
    finally { if (alive.current && sequence === detailSequence.current) setDetailLoading(false); }
  };
  const open = (item: AIKnowledgeEntry, nextMode: 'view' | 'edit' = 'view') => { setEntry(item); void read(item.id, nextMode); };
  const create = () => { detailSequence.current += 1; setEntry(null); setDetailLoading(false); setDetailError(''); setSaveError(''); form.resetFields(); form.setFieldsValue(aiKnowledgeValues()); setMode('create'); };
  const close = () => { if (mutation.current) return; detailSequence.current += 1; setMode(null); setEntry(null); setDetailError(''); setSaveError(''); form.resetFields(); };
  useEffect(() => {
    if (!miniBackHandler || !active) return;
    const goBack = () => {
      if (!mode && !deleting) return false;
      if (saving || mutation.current) return true;
      if (deleting) { setDeleting(null); setDeleteError(''); }
      else close();
      return true;
    };
    miniBackHandler.current = goBack;
    return () => { if (miniBackHandler.current === goBack) miniBackHandler.current = null; };
  }, [miniBackHandler, active, mode, deleting, saving]);
  const save = async () => {
    if (mutation.current || !mode || mode === 'view' || mode === 'edit' && (!isAdmin || !entry)) return;
    mutation.current = true; setSaving(true); setSaveError('');
    try {
      const values = await form.validateFields();
      const payload = aiKnowledgeSavePayload(values, mode === 'edit' ? entry : null, isAdmin);
      const response = await apiJsonPost('/ai-ops/knowledge/save', payload);
      if (!alive.current) return;
      const saved = aiKnowledgeDetail(response?.data);
      setEntry(saved); setMode('view'); form.setFieldsValue(aiKnowledgeValues(saved)); message.success(saved.status === 'active' ? '共享知识已发布' : saved.status === 'disabled' ? '共享知识已停用' : '草稿已保存'); await load();
    } catch (cause: any) { if (alive.current && !cause?.errorFields) setSaveError(`${cause?.message || '保存失败'}${entry ? '。请刷新最新内容后再保存；当前输入已保留，不会自动覆盖其他人的修改。' : ''}`); }
    finally { mutation.current = false; if (alive.current) setSaving(false); }
  };
  const remove = async () => {
    if (!isAdmin || !deleting || mutation.current) return;
    mutation.current = true; setSaving(true); setDeleteError('');
    try {
      await apiJsonPost('/ai-ops/knowledge/delete', { id: deleting.id, expected_version: deleting.version, confirm: true });
      if (!alive.current) return;
      if (entry?.id === deleting.id) { setMode(null); setEntry(null); }
      setDeleting(null); message.success('共享知识已删除');
      if (entries.length === 1 && filters.current > 1) setFilters((current) => ({ ...current, current: current.current - 1 })); else await load();
    } catch (cause: any) { if (alive.current) setDeleteError(`${cause?.message || '删除失败'}。请关闭此窗口并刷新列表后重试。`); }
    finally { mutation.current = false; if (alive.current) setSaving(false); }
  };
  const edit = mode === 'edit' || mode === 'create';
  const statuses = isAdmin ? AI_KNOWLEDGE_STATUSES : AI_KNOWLEDGE_STATUSES.filter((item) => item.value !== 'disabled').map((item) => item.value === 'draft' ? { ...item, label: '我的草稿' } : item);
  return <div className="ai-knowledge">
    <Paragraph className="ai-knowledge-note" type="secondary">{isAdmin ? '已发布资料供所有运维人员与 AI 使用；管理员可维护，员工可提交草稿。' : '可阅读已发布资料和自己的草稿；提交后由管理员发布。'} 不存放密码、密钥或订阅 Token。</Paragraph>
    <div className="ai-knowledge-toolbar">
      <Input.Search aria-label="搜索共享知识" placeholder="搜索标题、摘要或正文" value={query} allowClear maxLength={200} onChange={(event) => setQuery(event.target.value)} onSearch={(value) => setFilters((current) => ({ ...current, query: value.trim(), current: 1 }))} />
      <Select aria-label="知识类别" value={filters.category} options={[{ value: '', label: '全部类别' }, ...AI_KNOWLEDGE_CATEGORIES]} onChange={(category) => setFilters((current) => ({ ...current, category, current: 1 }))} />
      <Select aria-label="知识状态" value={filters.status} options={[{ value: '', label: isAdmin ? '全部状态' : '全部可见资料' }, ...statuses]} onChange={(status) => setFilters((current) => ({ ...current, status, current: 1 }))} />
      <Space><Button icon={<ReloadOutlined />} aria-label="刷新共享知识" loading={loading} disabled={saving} onClick={() => void load()} /><Button type="primary" icon={<PlusOutlined />} disabled={saving} onClick={create}>{isAdmin ? '新建资料' : '提交草稿'}</Button></Space>
    </div>
    {error && <Alert type="error" showIcon message={error} />}
    <Spin spinning={loading}><div className="ai-knowledge-list">{entries.map((item) => <article className="ai-knowledge-row" key={item.id}>
      <div className="ai-knowledge-row-main"><div className="ai-knowledge-title"><button onClick={() => open(item)}>{item.title}</button>{item.pinned && <Tag icon={<PushpinOutlined />}>常用</Tag>}<Tag>{categoryName(item.category)}</Tag><Tag color={item.status === 'active' ? 'success' : item.status === 'draft' ? 'processing' : 'default'}>{statusName(item.status)}</Tag></div>{item.summary && <p className="ai-knowledge-summary">{item.summary}</p>}<KnowledgeMeta entry={item} /></div>
      <Space size={0} className="ai-knowledge-row-actions"><Button type="link" onClick={() => open(item)}>阅读</Button>{isAdmin && <><Button type="link" icon={<EditOutlined />} onClick={() => open(item, 'edit')}>编辑</Button><Button type="text" danger icon={<DeleteOutlined />} aria-label={`删除资料 ${item.id}`} disabled={saving} onClick={() => { setDeleting(item); setDeleteError(''); }} /></>}</Space>
    </article>)}{!entries.length && !loading && !error && <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无符合条件的共享知识" />}</div></Spin>
    <Pagination className="ai-knowledge-pagination" current={filters.current} pageSize={filters.page_size} total={total} showSizeChanger pageSizeOptions={[20, 50]} showTotal={(count) => `共 ${count} 条资料`} onChange={(current, page_size) => setFilters((previous) => ({ ...previous, current: page_size !== previous.page_size ? 1 : current, page_size }))} />
    <Modal title={mode === 'create' ? isAdmin ? '新建共享知识' : '提交知识草稿' : edit ? '编辑共享知识' : entry?.title || '知识详情'} open={!!mode} onCancel={close} width={920} rootClassName="ai-knowledge-modal" destroyOnHidden maskClosable={!saving} closable={!saving} footer={edit ? <Space wrap>{mode === 'edit' && entry && <Popconfirm title="刷新会替换未保存的输入，继续？" onConfirm={() => void read(entry.id, 'edit')} okText="刷新" cancelText="保留输入"><Button disabled={saving || detailLoading}>刷新最新内容</Button></Popconfirm>}<Button disabled={saving} onClick={close}>取消</Button><Button type="primary" loading={saving} disabled={detailLoading || !!detailError} onClick={() => void save()}>{isAdmin ? '保存资料' : '提交草稿'}</Button></Space> : <Space>{isAdmin && entry && !detailLoading && !detailError && <Button icon={<EditOutlined />} onClick={() => { form.setFieldsValue(aiKnowledgeValues(entry)); setMode('edit'); }}>编辑／发布</Button>}<Button onClick={close}>关闭</Button></Space>}>
      {detailLoading ? <div className="ai-knowledge-loading"><Spin /></div> : detailError ? <Alert type="error" showIcon message={detailError} action={entry && <Button size="small" onClick={() => void read(entry.id, edit ? 'edit' : 'view')}>重试</Button>} /> : edit ? <>
        {saveError && <Alert type="error" showIcon message={saveError} className="ai-knowledge-save-error" />}
        <Form form={form} layout="vertical" disabled={saving}>
          <Form.Item name="title" label="标题" rules={[{ required: true, whitespace: true, message: '请输入标题' }, { max: 120 }]}><Input maxLength={120} /></Form.Item>
          <div className="ai-knowledge-form-options"><Form.Item name="category" label="类别" rules={[{ required: true }]}><Select options={[...AI_KNOWLEDGE_CATEGORIES]} /></Form.Item>{isAdmin && <><Form.Item name="status" label="发布状态" rules={[{ required: true }]}><Select options={AI_KNOWLEDGE_STATUSES} /></Form.Item><Form.Item name="pinned" label="常用资料" valuePropName="checked"><Checkbox>置顶常用</Checkbox></Form.Item></>}</div>
          {!isAdmin && <Paragraph type="secondary">本次只提交草稿，发布后其他员工和 AI 才会使用。</Paragraph>}
          <Form.Item name="summary" label="摘要" rules={[{ max: 500 }]}><Input.TextArea maxLength={500} showCount autoSize={{ minRows: 2, maxRows: 4 }} /></Form.Item>
          <Form.Item name="content" label="正文（支持 Markdown）" rules={[{ required: true, whitespace: true, message: '请输入正文' }, { max: 12000 }]}><Input.TextArea className="ai-knowledge-editor" maxLength={12000} showCount autoSize={{ minRows: 12, maxRows: 24 }} /></Form.Item>
        </Form>
      </> : entry && <div className="ai-knowledge-reading"><div className="ai-knowledge-reading-tags"><Tag>{categoryName(entry.category)}</Tag><Tag>{statusName(entry.status)}</Tag>{entry.pinned && <Tag icon={<PushpinOutlined />}>常用</Tag>}</div><KnowledgeMeta entry={entry} />{entry.source_note && <Paragraph className="ai-knowledge-source" type="secondary">来源说明：{entry.source_note}</Paragraph>}{entry.summary && <Paragraph className="ai-knowledge-detail-summary">{entry.summary}</Paragraph>}<AIOpsMarkdown content={entry.content || ''} /></div>}
    </Modal>
    <Modal title="删除共享知识" open={!!deleting} onCancel={() => { if (!saving) { setDeleting(null); setDeleteError(''); } }} onOk={() => void remove()} okText="确认删除" okButtonProps={{ danger: true }} cancelButtonProps={{ disabled: saving }} confirmLoading={saving} closable={!saving} maskClosable={!saving}>
      <Paragraph>删除「{deleting?.title}」后，运维人员和 AI 将无法再读取这条资料。</Paragraph>{deleteError && <Alert type="error" showIcon message={deleteError} />}
    </Modal>
  </div>;
}
