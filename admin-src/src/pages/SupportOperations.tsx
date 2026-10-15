import React, { useEffect, useRef, useState } from 'react';
import { Alert, Button, Space, Table, Tag, Typography } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { apiGet, supportGet } from '../lib/api';
import { type SupportOperation, supportDate } from './supportTypes';

function stateDescription(value: any) {
  let state = value || {};
  if (typeof state === 'string') {
    try { state = JSON.parse(state); } catch { return '—'; }
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
  const sequence = useRef(0);
  const load = async (current = page) => {
    const ticket = ++sequence.current;
    setLoading(true); setError('');
    try {
      const res = await (administrator ? apiGet('/support/operations', { page: current }) : supportGet('/operations', { page: current }));
      if (ticket !== sequence.current) return;
      setRows(res.data?.items || []); setTotal(Number(res.data?.total || 0)); setPage(current);
    } catch (e: any) {
      if (ticket === sequence.current) { setRows([]); setTotal(0); setError(e.message || '加载操作记录失败'); }
    } finally { if (ticket === sequence.current) setLoading(false); }
  };
  useEffect(() => { load(1); return () => { sequence.current += 1; }; }, [administrator]);
  const columns: any[] = [
    { title: '时间', dataIndex: 'created_at', width: 180, render: supportDate },
    ...(administrator ? [{ title: '操作员工', dataIndex: 'actor_email', width: 200, render: (v: string, r: SupportOperation) => v || `#${r.actor_id}` }] : []),
    { title: '用户', dataIndex: 'target_email', width: 210, render: (v: string, r: SupportOperation) => <>{v || '—'}<div className="support-muted">ID {r.target_user_id}</div></> },
    { title: '线路范围', dataIndex: 'source_policy_id', width: 110, render: (v: number, row: SupportOperation) => row.after_state?.source_name || row.before_state?.source_name || (v > 0 ? `规则 #${v}` : '员工授权') },
    { title: '变更前', dataIndex: 'before_state', width: 190, render: stateDescription },
    { title: '变更后', dataIndex: 'after_state', width: 190, render: stateDescription },
    { title: '原因', dataIndex: 'reason', width: 230 },
  ];
  return <div className="support-panel">
    <Space className="support-toolbar" wrap><Typography.Text type="secondary">仅记录实际变更，不记录页面查看和刷新。{administrator ? '管理员可查看全部员工的操作。' : '这里只显示你自己的操作。'}</Typography.Text><Button icon={<ReloadOutlined aria-hidden />} loading={loading} onClick={() => load()}>刷新</Button></Space>
    {error && <Alert type="error" showIcon message={error} className="support-alert" />}
    <Table rowKey="id" columns={columns} dataSource={rows} loading={loading} scroll={{ x: 1150 }} pagination={{ current: page, pageSize: 20, total, showSizeChanger: false, onChange: (current) => load(current), showTotal: (n) => `共 ${n} 条` }} />
  </div>;
}
