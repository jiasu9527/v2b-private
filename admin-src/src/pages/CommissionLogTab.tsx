import React, { useEffect, useRef, useState } from 'react';
import { Alert, Button, Checkbox, DatePicker, Form, Input, InputNumber, Modal, Select, Space, Table, Tag, Typography, message } from 'antd';
import dayjs from 'dayjs';
import { apiJsonPost } from '../lib/api';
import { aiRequestID } from './aiOpsModel';
import { commissionApplyRequest, hasCommissionSelection, type CommissionDraft, type CommissionFilter as Filter } from './commissionLogModel';
import './commissionLog.css';

const statusLabels: Record<number, string> = { 0: '待确认', 1: '发放中', 2: '已发放', 3: '已驳回' };
const amount = (v: any) => `¥${(Number(v || 0) / 100).toFixed(2)}`;
const timestamp = (v: any) => v ? dayjs(Number(v) * 1000).format('YYYY-MM-DD HH:mm:ss') : '-';
type RecordRow = { id: number; invite_user_id: number; user_id: number; trade_no: string; order_amount: number; get_amount: number; created_at: number; commission_status: number | null; deleted_at: number | null; deleted_by: number | null; delete_reason: string };
type Preview = { preview_hash: string; count: number; total_amount: number; expires_at: number; all_records: boolean; action: 'delete' | 'restore'; applied_filters: Filter; sample: RecordRow[] };

export default function CommissionLogTab() {
  const [form] = Form.useForm();
  const [filter, setFilter] = useState<Filter>({ visibility: 'active' });
  const [data, setData] = useState<RecordRow[]>([]);
  const [total, setTotal] = useState(0);
  const [totalAmount, setTotalAmount] = useState(0);
  const [page, setPage] = useState({ current: 1, pageSize: 50 });
  const [loading, setLoading] = useState(false);
  const [selected, setSelected] = useState<React.Key[]>([]);
  const [draft, setDraft] = useState<CommissionDraft | null>(null);
  const [reason, setReason] = useState('');
  const [allConfirmed, setAllConfirmed] = useState(false);
  const [finalAllConfirmed, setFinalAllConfirmed] = useState(false);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [idempotencyKey, setIdempotencyKey] = useState('');
  const [busy, setBusy] = useState(false);
  const sequence = useRef(0);
  const load = async (nextFilter = filter, nextPage = page) => {
    const seq = ++sequence.current;
    setLoading(true);
    try {
      const res = await apiJsonPost('/commission-log/list', { filter: nextFilter, current: nextPage.current, page_size: nextPage.pageSize });
      if (seq !== sequence.current) return;
      setData(res.data || []); setTotal(res.total || 0); setTotalAmount(res.total_amount || 0);
      setFilter(res.applied_filters || nextFilter); setPage(nextPage); setSelected([]);
    } catch (error: any) { if (seq === sequence.current) message.error(error.message || '返佣记录查询失败'); }
    finally { if (seq === sequence.current) setLoading(false); }
  };
  useEffect(() => { void load(); return () => { sequence.current++; }; }, []);
  const search = async () => {
    const values = await form.validateFields();
    const next: Filter = { visibility: values.visibility || 'active' };
    for (const key of ['invite_user_id', 'user_id', 'commission_status']) if (values[key] !== undefined && values[key] !== null) (next as any)[key] = values[key];
    if (values.trade_no?.trim()) next.trade_no = values.trade_no.trim();
    if (values.range?.[0] && values.range?.[1]) { next.created_from = values.range[0].unix(); next.created_to = values.range[1].unix(); }
    await load(next, { ...page, current: 1 });
  };
  const open = (action: 'delete' | 'restore', target?: RecordRow, useSelection = false) => {
    const next: Filter = target ? { id: target.id } : useSelection ? { ids: selected.map(Number) } : { ...filter };
    next.visibility = action === 'delete' ? 'active' : 'deleted';
    setDraft({ action, filter: next, singleID: action === 'delete' ? target?.id : undefined });
    setReason(''); setAllConfirmed(false); setFinalAllConfirmed(false); setPreview(null); setIdempotencyKey('');
  };
  const createPreview = async () => {
    if (!draft || !reason.trim()) { message.warning('请填写操作原因'); return; }
    setBusy(true);
    try {
      const res = await apiJsonPost('/commission-log/preview', { filter: draft.filter, action: draft.action, reason: reason.trim(), confirm_all: allConfirmed });
      setPreview(res.data);
      setIdempotencyKey(aiRequestID());
    } catch (error: any) { message.error(error.message || '预览失败'); }
    finally { setBusy(false); }
  };
  const apply = async () => {
    if (!draft || !preview || !idempotencyKey) return;
    setBusy(true);
    try {
      const request = commissionApplyRequest(draft, preview, idempotencyKey, finalAllConfirmed);
      const result = await apiJsonPost(request.path, request.body);
      if (!result.data?.committed) throw new Error('未收到成功回执，请保留当前预览核对');
      message.success(`${draft.action === 'delete' ? '已隐藏' : '已恢复'} ${result.data.count} 条，余额未变`);
      setDraft(null); setPreview(null); await load();
    } catch (error: any) { message.error(error.message || '操作结果待核对；重试请保留本次预览与幂等键'); }
    finally { setBusy(false); }
  };
  const columns: any[] = [
    { title: '流水 ID', dataIndex: 'id', width: 95 },
    { title: '邀请人 ID', dataIndex: 'invite_user_id', width: 110 },
    { title: '用户 ID', dataIndex: 'user_id', width: 100 },
    { title: '订单号', dataIndex: 'trade_no', width: 235, render: (v: string) => <Typography.Text copyable>{v}</Typography.Text> },
    { title: '订单金额', dataIndex: 'order_amount', width: 110, render: amount },
    { title: '返佣金额', dataIndex: 'get_amount', width: 110, render: amount },
    { title: '订单佣金状态', dataIndex: 'commission_status', width: 130, render: (v: number | null) => v === null ? '订单已不存在' : statusLabels[v] || String(v) },
    { title: '记录状态', dataIndex: 'deleted_at', width: 100, render: (v: number | null) => <Tag color={v ? 'default' : 'green'}>{v ? '已隐藏' : '正常'}</Tag> },
    { title: '创建时间', dataIndex: 'created_at', width: 170, render: timestamp },
    { title: '清理记录', key: 'audit', width: 230, render: (_: any, row: RecordRow) => row.deleted_at ? <><div>#{row.deleted_by} · {timestamp(row.deleted_at)}</div><Typography.Text style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{row.delete_reason}</Typography.Text></> : '-' },
    { title: '操作', key: 'actions', width: 85, fixed: 'right', render: (_: any, row: RecordRow) => <Button type="link" danger={!row.deleted_at} onClick={() => open(row.deleted_at ? 'restore' : 'delete', row)}>{row.deleted_at ? '恢复' : '隐藏'}</Button> },
  ];
  return <div className="commission-log-panel" style={{ padding: 16 }}>
    <Alert type="info" showIcon message="隐藏仅清理用户邀请页面的明细，可恢复；原始金额与审计保留，不扣余额、不重发佣金，也不修改订单佣金状态。" style={{ marginBottom: 16, background: '#f6f8fa', borderColor: '#dde2e7' }} />
    <Form className="commission-log-filters" form={form} layout="inline" initialValues={{ visibility: 'active' }} onFinish={search} style={{ gap: 8, marginBottom: 16 }}>
      <Form.Item name="invite_user_id" label="邀请人 ID"><InputNumber min={1} precision={0} /></Form.Item>
      <Form.Item name="user_id" label="用户 ID"><InputNumber min={1} precision={0} /></Form.Item>
      <Form.Item name="trade_no" label="订单号"><Input allowClear placeholder="完整订单号" /></Form.Item>
      <Form.Item name="commission_status" label="佣金状态"><Select allowClear style={{ width: 125 }} options={Object.entries(statusLabels).map(([value, label]) => ({ value: Number(value), label }))} /></Form.Item>
      <Form.Item name="visibility" label="记录状态"><Select style={{ width: 110 }} options={[{ value: 'active', label: '正常' }, { value: 'deleted', label: '已隐藏' }, { value: 'all', label: '全部' }]} /></Form.Item>
      <Form.Item name="range" label="创建时间"><DatePicker.RangePicker showTime placeholder={['开始（含）', '截止（不含）']} /></Form.Item>
      <Button htmlType="submit" type="primary" loading={loading}>查询</Button>
      <Button onClick={() => { form.resetFields(); void load({ visibility: 'active' }, { ...page, current: 1 }); }}>重置</Button>
    </Form>
    <Space wrap style={{ marginBottom: 12 }}>
      <Typography.Text>筛选合计 {total} 条 · {amount(totalAmount)}（历史流水，非可提现余额）</Typography.Text>
      <Button danger disabled={!selected.length || loading} onClick={() => open('delete', undefined, true)}>隐藏所选</Button>
      <Button disabled={!selected.length || loading} onClick={() => open('restore', undefined, true)}>恢复所选</Button>
      <Button danger disabled={loading} onClick={() => open('delete')}>预览隐藏筛选结果</Button>
      <Button disabled={loading} onClick={() => open('restore')}>预览恢复筛选结果</Button>
    </Space>
    <Table<RecordRow> rowKey="id" loading={loading} dataSource={data} columns={columns} rowSelection={{ selectedRowKeys: selected, onChange: setSelected }} scroll={{ x: 1500 }} pagination={{ ...page, total, showSizeChanger: true, pageSizeOptions: [20, 50, 100] }} onChange={(p) => void load(filter, { current: p.current || 1, pageSize: p.pageSize || 50 })} />
    <Modal title={draft?.action === 'restore' ? '恢复返佣记录' : '隐藏返佣记录（可恢复）'} open={!!draft} onCancel={() => { if (!busy) { setDraft(null); setPreview(null); } }} footer={null} closable={!busy} maskClosable={!busy} width={800}>
      {!preview ? <Space direction="vertical" style={{ width: '100%' }}>
        <Typography.Text>先预览，核对命中条数、金额和样本后再执行。不会修改任何用户余额。</Typography.Text>
        <pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{JSON.stringify(draft?.filter, null, 2)}</pre>
        <Input.TextArea value={reason} onChange={(e) => setReason(e.target.value)} placeholder="操作原因（必填）" maxLength={500} rows={3} />
        {draft && !hasCommissionSelection(draft.filter) && <Checkbox checked={allConfirmed} onChange={(e) => setAllConfirmed(e.target.checked)}>确认预览全站{draft.action === 'delete' ? '正常' : '已隐藏'}返佣明细</Checkbox>}
        <Button type="primary" loading={busy} disabled={!reason.trim() || (!!draft && !hasCommissionSelection(draft.filter) && !allConfirmed)} onClick={createPreview}>生成预览，不执行清理</Button>
      </Space> : <Space direction="vertical" style={{ width: '100%' }}>
        <Alert type="warning" showIcon message={`本次${preview.action === 'delete' ? '隐藏' : '恢复'} ${preview.count} 条，原金额合计 ${amount(preview.total_amount)}；余额不变`} description={`名单已冻结，新增记录不会被顺带处理。预览有效至 ${timestamp(preview.expires_at)}。下方最多显示 20 条样本。`} />
        <pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{JSON.stringify(preview.applied_filters, null, 2)}</pre>
        <Table size="small" rowKey="id" pagination={false} dataSource={preview.sample} columns={columns.slice(0, 6)} scroll={{ x: 760 }} />
        {preview.all_records && <Checkbox checked={finalAllConfirmed} onChange={(e) => setFinalAllConfirmed(e.target.checked)}>再次确认处理本次预览的全站范围，已核对条数和金额</Checkbox>}
        <Space><Button onClick={() => { setPreview(null); setFinalAllConfirmed(false); }} disabled={busy}>返回重新预览</Button><Button danger={preview.action === 'delete'} type="primary" loading={busy} disabled={preview.all_records && !finalAllConfirmed} onClick={apply}>确认{preview.action === 'delete' ? '隐藏' : '恢复'}</Button></Space>
        <Typography.Text type="secondary">请求失败后再次点击使用相同幂等键，不会重复处理；若提示记录变化或预览过期，请重新预览。</Typography.Text>
      </Space>}
    </Modal>
  </div>;
}
