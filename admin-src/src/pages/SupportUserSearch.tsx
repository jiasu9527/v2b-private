import React, { useRef, useState } from 'react';
import { Alert, Button, Card, Collapse, Descriptions, Empty, Form, Input, Modal, Select, Space, Tag, Typography, message } from 'antd';
import { SearchOutlined, UndoOutlined } from '@ant-design/icons';
import { bytes, supportGet, supportPost } from '../lib/api';
import { type SupportScope, type SupportUserResult, supportDate } from './supportTypes';

const overrideStatus: Record<string, string> = {
  active: '人工指定生效中', expired: '人工指定已到期', invalid: '目标失效，已恢复自动',
  disabled: '目标已停用，已恢复自动', incompatible: '目标不兼容，已恢复自动',
  inactive: '人工指定未生效', target_missing: '目标已删除，已恢复自动',
  restored: '已恢复自动分配', target_unavailable: '目标已停用或删除，已恢复自动',
  source_not_applicable: '当前条件下规则未命中，指定不生效',
};
function uuid() {
  if (typeof crypto.randomUUID === 'function') return crypto.randomUUID();
  const value = crypto.getRandomValues(new Uint8Array(16));
  value[6] = (value[6] & 15) | 64; value[8] = (value[8] & 63) | 128;
  const hex = Array.from(value, (v) => v.toString(16).padStart(2, '0')).join('');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

type ChangeOptions = { scope: SupportScope; target: string; restore: boolean; reason: string; duration: number };
function ScopeEditor({ scope, busy, canRestore, change }: { scope: SupportScope; busy: boolean; canRestore?: boolean; change: (options: ChangeOptions) => void }) {
  const [target, setTarget] = useState('');
  const [duration, setDuration] = useState(0);
  const [reason, setReason] = useState('');
  const override = scope.override;
  const effective = override?.active && override.status === 'active' && scope.applicable !== false;
  const current = effective ? override.entry_host || '目标入口暂不可用' : scope.automatic_entry_host || '原始节点入口 / 无入口替换';
  return <Card className="support-scope" title={<Space wrap><span>{scope.name || `入口规则 #${scope.source_policy_id}`}</span><Typography.Text type="secondary">规则 #{scope.source_policy_id}</Typography.Text></Space>}>
    <div className="support-scope-current"><div><div className="support-muted">自动分配入口</div><Typography.Text code>{scope.automatic_entry_host || '原始节点入口 / 无入口替换'}</Typography.Text></div><div><div className="support-muted">当前生效入口</div><Typography.Text code>{current}</Typography.Text>{override && <div style={{ marginTop: 8 }}><Tag color={effective ? 'blue' : override.status === 'restored' ? undefined : 'orange'}>{effective ? '人工指定生效中' : overrideStatus[override.status] || '人工指定已失效，按自动规则处理'}</Tag><span>{override.target_name || override.target_key}</span><div className="support-muted">{Number(override.expires_at) > 0 ? `指定到期：${supportDate(override.expires_at)}` : '永久指定，直到人工恢复或目标失效'}</div></div>}</div></div>
    {scope.nodes?.length > 0 && <Typography.Paragraph><span className="support-muted">影响节点：</span>{scope.nodes.map((node) => <Tag key={`${node.type}:${node.id}`}>{node.name || `${node.type} #${node.id}`}</Tag>)}</Typography.Paragraph>}
    {scope.applicable === false && <Alert type="warning" showIcon className="support-alert" message="当前用户 / UA 条件未命中此规则，不能新增指定；已有记录仅供查看或恢复自动分配。" />}
    <div className="support-scope-form">
      <Form.Item label="切换目标"><Select aria-label={`${scope.name}切换目标`} value={target || undefined} placeholder={scope.targets?.length ? '选择授权的兼容入口组' : '暂无已授权的兼容入口'} disabled={busy || !scope.targets?.length} showSearch optionFilterProp="label" onChange={setTarget} options={(scope.targets || []).map((item) => ({ value: item.key, label: `${item.name} · ${item.entry_host}` }))} /></Form.Item>
      <Form.Item label="有效期"><Select aria-label={`${scope.name}有效期`} value={duration} disabled={busy} onChange={setDuration} options={[{ value: 0, label: '永久（直到恢复自动）' }, { value: 3600, label: '1 小时' }, { value: 86400, label: '24 小时' }, { value: 604800, label: '7 天' }]} /></Form.Item>
      <Form.Item className="support-full-width" label="操作原因" required><Input.TextArea aria-label={`${scope.name}操作原因`} placeholder="填写本次排障或切换原因（必填）" maxLength={500} showCount autoSize={{ minRows: 2, maxRows: 4 }} disabled={busy} value={reason} onChange={(event) => setReason(event.target.value)} /></Form.Item>
      <Space className="support-full-width" wrap><Button type="primary" disabled={busy || scope.applicable === false || !target || !reason.trim()} onClick={() => change({ scope, target, restore: false, reason, duration })}>指定入口</Button><Button icon={<UndoOutlined aria-hidden />} disabled={busy || canRestore === false || !override?.active || !reason.trim()} onClick={() => change({ scope, target: '', restore: true, reason, duration: 0 })}>恢复自动分配</Button><Typography.Text type="secondary">只影响此用户的本线路范围，不修改分组规则、套餐或节点权限。</Typography.Text></Space>
    </div>
  </Card>;
}

export default function SupportUserSearch() {
  const [query, setQuery] = useState('');
  const [ua, setUA] = useState('');
  const [result, setResult] = useState<SupportUserResult | null>(null);
  const [resolvedQuery, setResolvedQuery] = useState('');
  const [resolvedUA, setResolvedUA] = useState('');
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const sequence = useRef(0);
  const savingRef = useRef(false);
  const pendingChange = useRef<{ key: string; body: any } | null>(null);
  const clearResult = () => { sequence.current += 1; setResult(null); setError(''); };
  const search = async () => {
    if (savingRef.current || loading) return;
    const value = query.trim();
    if (!/^[1-9]\d*$/.test(value) && !/^[^\s@]+@[^\s@]+$/.test(value)) return message.warning('请输入完整邮箱或完整的十进制用户 ID，不支持模糊搜索。');
    const ticket = ++sequence.current;
    setResult(null); setError(''); setLoading(true); pendingChange.current = null;
    try {
      const res = await supportGet('/user', { query: value, ua: ua.trim() });
      if (ticket !== sequence.current) return;
      setResult(res.data || { found: false, scopes: [] }); setResolvedQuery(value); setResolvedUA(ua.trim());
    } catch (e: any) { if (ticket === sequence.current) setError(e.message || '查询失败'); }
    finally { if (ticket === sequence.current) setLoading(false); }
  };
  const change = ({ scope, target, restore, reason, duration }: ChangeOptions) => {
    if (!result?.user || savingRef.current) return;
    const targetInfo = scope.targets.find((entry) => entry.key === target);
    const key = JSON.stringify([result.user.id, scope.source_policy_id, target, restore, reason.trim(), duration, scope.version, resolvedUA]);
    if (!pendingChange.current || pendingChange.current.key !== key) {
      pendingChange.current = { key, body: { user_id: result.user.id, source_policy_id: scope.source_policy_id, target_key: target, restore, reason: reason.trim(), expires_at: !restore && duration > 0 ? Math.floor(Date.now() / 1000) + duration : 0, version: scope.version, request_id: uuid(), ua: resolvedUA } };
    }
    const body = pendingChange.current.body;
    Modal.confirm({
      title: restore ? '恢复该用户的自动入口分配？' : '确认指定该用户入口？',
      content: <><Typography.Paragraph>用户：{result.user.email}（ID {result.user.id}）</Typography.Paragraph><Typography.Paragraph>范围：{scope.name}</Typography.Paragraph><Typography.Paragraph>{restore ? '撤销人工指定，重新按现有规则分配。' : `目标：${targetInfo?.name || target} · ${targetInfo?.entry_host || ''}`}</Typography.Paragraph><Typography.Paragraph>不更改套餐权益。保存后用户需要更新订阅，已连接的客户端不会被强制切换。</Typography.Paragraph></>,
      okText: restore ? '恢复自动分配' : '确认切换', cancelText: '取消', maskClosable: false,
      onOk: async () => {
        if (savingRef.current) throw new Error('正在处理另一笔操作');
        savingRef.current = true; setSaving(true); setError('');
        try {
          const res = await supportPost('/entry', body);
          setResult(res.data || null); pendingChange.current = null;
          message.success(restore ? '已恢复自动分配，请让用户更新订阅' : '已保存指定入口，请让用户更新订阅');
        } catch (e: any) {
          const text = e.message || '操作失败，请重新查询确认当前状态';
          setResult(null); setError(`${text}。请重新精准查询，避免使用过期的用户状态。`);
          message.error(text); throw e;
        } finally { savingRef.current = false; setSaving(false); }
      },
    });
  };
  return <div className="support-panel">
    <Alert type="info" showIcon className="support-alert" message="只支持完整邮箱或用户 ID 精准查询，不提供用户列表、模糊搜索和导出。" description="员工只可指定获授权的兼容入口；规则条件不修改，套餐、流量、有效期和节点权限不变。" />
    <Card><Form layout="vertical" onFinish={search}><Form.Item label="精准查询用户" required><Space.Compact style={{ width: '100%' }}><Input aria-label="完整邮箱或用户ID" placeholder="完整邮箱或完整用户 ID" maxLength={254} autoComplete="off" value={query} disabled={loading || saving} onChange={(event) => { setQuery(event.target.value); clearResult(); }} /><Button type="primary" htmlType="submit" icon={<SearchOutlined aria-hidden />} loading={loading} disabled={saving || !query.trim()}>精准查询</Button></Space.Compact></Form.Item><Collapse size="small" ghost items={[{ key: 'ua', label: '高级：按客户端 UA 预览匹配规则', children: <><Input aria-label="客户端UA" placeholder="可选，输入用户订阅客户端的完整 User-Agent" maxLength={1024} autoComplete="off" value={ua} disabled={loading || saving} onChange={(event) => { setUA(event.target.value); clearResult(); }} /><Typography.Paragraph type="secondary" style={{ marginTop: 8, marginBottom: 0 }}>入口规则可能按 UA 匹配。留空使用默认预览；填写后只预览该 UA 下的线路范围，不修改用户客户端或规则条件。</Typography.Paragraph></> }]} /></Form></Card>
    {error && <Alert type="error" showIcon className="support-alert" style={{ marginTop: 16 }} message={error} />}
    {result && !result.found && <div className="support-empty" style={{ marginTop: 16 }}><Empty description="未找到可操作用户，请检查完整邮箱或用户 ID。" /></div>}
    {result?.found && result.user && <>
      <Card className="support-user-summary"><Descriptions title="用户信息" column={{ xs: 1, sm: 2, lg: 4 }} items={[
        { key: 'id', label: '用户 ID', children: result.user.id }, { key: 'email', label: '邮箱', children: result.user.email },
        { key: 'plan', label: '套餐', children: result.user.plan_name || '无套餐' }, { key: 'expire', label: '有效期至', children: result.user.expired_at ? supportDate(result.user.expired_at) : '无到期时间' },
        { key: 'remain', label: '剩余流量', children: bytes(result.user.remaining_bytes) },
      ]} /><Typography.Text type="secondary">本次查询：{resolvedQuery}{resolvedUA ? ` · UA：${resolvedUA}` : ' · 默认 UA 预览'}</Typography.Text></Card>
      <Alert type="warning" showIcon className="support-alert" message="不同线路可能使用不同入口，请确认要调整的线路范围。保存后让用户更新订阅。" />
      {(result.scopes || []).map((scope) => <ScopeEditor key={`${result.user.id}:${scope.source_policy_id}:${scope.version}`} scope={scope} busy={saving || loading} canRestore={result.can_restore} change={change} />)}
      {!result.scopes?.length && <div className="support-empty"><Empty description="该用户当前没有可由你调整的入口范围。" /></div>}
    </>}
  </div>;
}
