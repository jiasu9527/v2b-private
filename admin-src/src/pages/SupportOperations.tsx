import React, { useEffect, useRef, useState } from 'react';
import { Alert, Button, Modal, Space, Table, Tag, Typography } from 'antd';
import { DeleteOutlined, ReloadOutlined } from '@ant-design/icons';
import { apiGet, apiJsonPost, bytes, supportGet } from '../lib/api';
import { type SupportOperation, supportDate } from './supportTypes';

function operationState(value: any): Record<string, any> {
  if (typeof value === 'string') {
    try { return JSON.parse(value) || {}; } catch { return {}; }
  }
  return value && typeof value === 'object' ? value : {};
}
function operationKind(row: SupportOperation) {
  return row.kind || operationState(row.after_state).kind || operationState(row.before_state).kind || 'entry_override';
}
function stateDescription(value: any, row: SupportOperation) {
  const state = operationState(value);
  const kind = state.kind || operationKind(row);
  if (kind === 'entitlement') {
    return <div><div>套餐：{state.plan_name || (state.plan_id ? `套餐 #${state.plan_id}` : '无套餐')}</div><div className="support-muted">总配额：{bytes(state.transfer_enable)} · 已用：{bytes(Number(state.u || 0) + Number(state.d || 0))}</div><div className="support-muted">有效期：{Number(state.expired_at) > 0 ? supportDate(state.expired_at) : '长期有效'}</div></div>;
  }
  if (kind === 'entry_membership') {
    const groups = Array.isArray(state.membership_groups) ? state.membership_groups : [];
    return <div>{groups.length ? groups.map((group: any) => <div className="support-operation-group" key={group.policy_id}><div>{group.name || `规则 #${group.policy_id}`}{group.enabled === false && <Tag>已停用</Tag>}</div>{group.entry_host && <Typography.Text type="secondary">{group.entry_host}</Typography.Text>}</div>) : <Tag>无邮箱指定组，按默认规则</Tag>}{state.has_legacy_override && <Tag color="orange">含旧版人工指定</Tag>}</div>;
  }
  if (!state.target_key || state.active === false) return <Tag>自动分配</Tag>;
  return <div><div>{state.target_name || state.target_key}</div>{state.entry_host && <Typography.Text type="secondary">{state.entry_host}</Typography.Text>}{Number(state.expires_at) > 0 && <div className="support-muted">到期：{supportDate(state.expires_at)}</div>}</div>;
}

export default function SupportOperations({ administrator = false }: { administrator?: boolean }) {
  const [rows, setRows] = useState<SupportOperation[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [latestId, setLatestId] = useState(0);
  const [clearFence, setClearFence] = useState<number | null>(null);
  const [clearing, setClearing] = useState(false);
  const [clearError, setClearError] = useState('');
  const sequence = useRef(0);
  const clearSequence = useRef(0);
  const clearingRef = useRef(false);
  const load = async (current = page, afterClear = false) => {
    if (clearingRef.current && !afterClear) return;
    const ticket = ++sequence.current;
    setLoading(true); setError('');
    if (!afterClear) setNotice('');
    try {
      const res = await (administrator ? apiGet('/support/operations', { page: current }) : supportGet('/operations', { page: current }));
      if (ticket !== sequence.current) return;
      const fence = Number(res.data?.latest_id);
      setRows(res.data?.items || []); setTotal(Number(res.data?.total || 0)); setPage(current);
      setLatestId(Number.isSafeInteger(fence) && fence > 0 ? fence : 0);
    } catch (e: any) {
      if (ticket === sequence.current) {
        setLatestId(0);
        setError(`${afterClear ? '日志已清空，但刷新失败：' : ''}${e.message || '加载操作记录失败'}`);
      }
    } finally { if (ticket === sequence.current) setLoading(false); }
  };
  useEffect(() => {
    setRows([]); setTotal(0); setLatestId(0); setClearFence(null); setClearError(''); setClearing(false); clearingRef.current = false;
    load(1);
    return () => { sequence.current += 1; clearSequence.current += 1; };
  }, [administrator]);
  const clearLogs = async () => {
    if (!administrator || clearFence === null || clearingRef.current) return;
    const ticket = ++clearSequence.current;
    clearingRef.current = true;
    sequence.current += 1;
    setLoading(false); setClearing(true); setClearError(''); setError(''); setNotice('');
    try {
      const res = await apiJsonPost('/support/operations/clear', { through_id: clearFence, confirm: true });
      if (ticket !== clearSequence.current) return;
      const deleted = Number(res.data?.deleted);
      setClearFence(null); setRows([]); setTotal(0); setLatestId(0); setPage(1);
      setNotice(Number.isSafeInteger(deleted) && deleted >= 0 ? `已清空 ${deleted} 条员工操作记录，新产生的记录会继续保留。` : '员工操作记录已清空，新产生的记录会继续保留。');
      await load(1, true);
    } catch (e: any) {
      if (ticket === clearSequence.current) setClearError(e.message || '清空操作记录失败，请重试');
    } finally {
      if (ticket === clearSequence.current) { clearingRef.current = false; setClearing(false); }
    }
  };
  const columns: any[] = [
    { title: '时间', dataIndex: 'created_at', width: 180, render: supportDate },
    ...(administrator ? [{ title: '操作员工', dataIndex: 'actor_email', width: 200, render: (v: string, r: SupportOperation) => v || `#${r.actor_id}` }] : []),
    { title: '用户', dataIndex: 'target_email', width: 210, render: (v: string, r: SupportOperation) => <>{v || '—'}<div className="support-muted">ID {r.target_user_id}</div></> },
    { title: '操作类型', width: 150, render: (_: unknown, row: SupportOperation) => operationKind(row) === 'entitlement' ? <Tag color="blue">套餐 / 流量 / 有效期</Tag> : operationKind(row) === 'entry_membership' ? <Tag color="cyan">邮箱指定入口组</Tag> : <><Tag>旧版入口指定</Tag><div className="support-muted">{operationState(row.after_state).source_name || operationState(row.before_state).source_name || (row.source_policy_id > 0 ? `规则 #${row.source_policy_id}` : '入口操作')}</div></> },
    { title: '变更前', dataIndex: 'before_state', width: 250, render: stateDescription },
    { title: '变更后', dataIndex: 'after_state', width: 250, render: stateDescription },
    { title: '原因', dataIndex: 'reason', width: 230 },
  ];
  return <div className="support-panel">
    <Space className="support-toolbar" wrap><Typography.Text type="secondary">仅记录实际变更，不记录页面查看和刷新。{administrator ? '管理员可查看全部员工的操作。' : '这里只显示你自己的操作。'}</Typography.Text><Button icon={<ReloadOutlined aria-hidden />} loading={loading} disabled={clearing} onClick={() => load()}>刷新</Button>{administrator && <Button danger icon={<DeleteOutlined aria-hidden />} disabled={loading || clearing || rows.length === 0 || latestId <= 0} onClick={() => { setClearFence(latestId); setClearError(''); }}>清空日志</Button>}</Space>
    {notice && <Alert type="success" showIcon message={notice} className="support-alert" />}
    {error && <Alert type="error" showIcon message={error} className="support-alert" />}
    <Table rowKey="id" columns={columns} dataSource={rows} loading={loading || clearing} scroll={{ x: administrator ? 1450 : 1250 }} pagination={{ current: page, pageSize: 20, total, disabled: clearing, showSizeChanger: false, onChange: (current) => load(current), showTotal: (n) => `共 ${n} 条` }} />
    {administrator && <Modal title="清空员工操作日志" open={clearFence !== null} okText="确认清空" cancelText="取消" okButtonProps={{ danger: true }} cancelButtonProps={{ disabled: clearing }} confirmLoading={clearing} closable={!clearing} maskClosable={!clearing} keyboard={!clearing} onOk={clearLogs} onCancel={() => { if (!clearingRef.current) { setClearFence(null); setClearError(''); } }}>
      <Typography.Paragraph>将清空所有员工截至当前快照的操作记录，包含其他分页，且不可恢复。</Typography.Paragraph>
      <Typography.Paragraph>仅删除操作日志，不会修改用户的套餐、流量、有效期或入口分组；当前快照之后新增的日志不会删除。</Typography.Paragraph>
      {clearError && <Alert type="error" showIcon message={clearError} description="可以重试，仍按本次确认时的快照范围清空，不会扩大删除范围。" />}
    </Modal>}

  </div>;
}
