import React, { useEffect, useMemo, useState } from 'react';
import { Badge, Button, Card, Dropdown, Modal, Skeleton, Space, Table, Tag, Tooltip, message } from 'antd';
import type { MenuProps } from 'antd';
import { CaretDownOutlined, PlusOutlined, QuestionCircleOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { apiGet, apiPost, getAdminPath } from '../lib/api';
import LegacyFilterDrawer, { FilterButton } from '../components/LegacyFilterDrawer';

const periodText: Record<string, string> = { month_price: '月付', quarter_price: '季付', half_year_price: '半年付', year_price: '年付', two_year_price: '两年付', three_year_price: '三年付', onetime_price: '一次性', reset_price: '流量重置包' };
const orderStatusText: Record<number, string> = { 0: '待支付', 1: '开通中', 2: '已取消', 3: '已完成', 4: '已折抵' };
const commissionStatusText: Record<number, string> = { 0: '待确认', 1: '发放中', 2: '已发放', 3: '已驳回' };
const orderTypeText: Record<number, string> = { 1: '新购', 2: '续费', 3: '变更', 4: '流量包', 9: '充值' };
const statusBadge: any[] = ['error', 'processing', 'default', 'success', 'default'];
const commissionBadge: any[] = ['default', 'processing', 'success', 'error'];

const filterDefs = [
  { key: 'trade_no', title: '订单号', condition: ['模糊', '='] },
  { key: 'status', title: '订单状态', condition: ['='], type: 'select', options: [{ label: '未支付', value: 0 }, { label: '已支付', value: 1 }, { label: '已取消', value: 2 }, { label: '已完成', value: 3 }, { label: '已折抵', value: 4 }] },
  { key: 'commission_status', title: '佣金状态', condition: ['='], type: 'select', options: [{ label: '待确认', value: 0 }, { label: '发放中', value: 1 }, { label: '已发放', value: 2 }, { label: '无效', value: 3 }] },
  { key: 'user_id', title: '用户ID', condition: ['='] },
  { key: 'invite_user_id', title: '邀请人ID', condition: ['=', '!='] },
  { key: 'email', title: '邮箱', condition: ['模糊'] },
  { key: 'callback_no', title: '回调单号', condition: ['模糊'] },
  { key: 'commission_balance', title: '佣金金额', condition: ['>', '<', '=', '!=', '>=', '<='] },
  { key: 'created_at', title: '创建时间', condition: ['>=', '>', '<', '<='], type: 'date' },
];

function defaultFilter() { return { key: 'trade_no', condition: '模糊', value: '' }; }
function initialFilters() {
  const query = new URLSearchParams(location.search);
  if (query.get('commission_pending') === '1') {
    return [
      { key: 'status', condition: '=', value: 3 },
      { key: 'commission_status', condition: '=', value: 0 },
      { key: 'commission_balance', condition: '>', value: '0' },
    ];
  }
  return [];
}
function money(v: any) { return (Number(v || 0) / 100).toFixed(2); }
function time(v: any) { return v ? dayjs(Number(v) * 1000).format('YYYY/MM/DD HH:mm') : '-'; }
function fullTime(v: any) { return v ? dayjs(Number(v) * 1000).format('YYYY-MM-DD HH:mm:ss') : '-'; }
function shortTradeNo(v: any) { const s = String(v || ''); return s.length > 8 ? `${s.slice(0, 3)}...${s.slice(-3)}` : s; }
function pathUrl(path: string) { return `/${getAdminPath()}${path}`; }
function periodLabel(value: any) {
  const key = String(value || '');
  if (!key || key === 'deposit') return '';
  if (Object.prototype.hasOwnProperty.call(periodText, key)) return periodText[key];
  return key;
}

function DetailRow({ label, children, empty = '-' }: { label: string; children: React.ReactNode; empty?: React.ReactNode }) {
  return <div className="legacy-detail-row">
    <div className="legacy-detail-label">{label}</div>
    <div className="legacy-detail-value">{children || empty}</div>
  </div>;
}

function DetailSection({ title, children }: { title: string; children: React.ReactNode }) {
  return <section className="legacy-detail-section">
    <div className="legacy-detail-section-title">{title}</div>
    <div className="legacy-detail-grid">{children}</div>
  </section>;
}

export default function OrderPage() {
  const [rows, setRows] = useState<any[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [filters, setFilters] = useState<any[]>(initialFilters);
  const [page, setPage] = useState({ current: 1, pageSize: 50 });
  const [plans, setPlans] = useState<any[]>([]);
  const [detailOpen, setDetailOpen] = useState(false);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detail, setDetail] = useState<{ order: any; user: any; inviteUser: any } | null>(null);
  const effectiveFilters = useMemo(() => filters.filter((item) => String(item.value ?? '').trim() !== ''), [filters]);

  const load = async (override: any = {}, filterOverride?: any[]) => {
    setLoading(true);
    try {
      const nextPage = { ...page, ...override };
      const activeFilters = filterOverride ?? effectiveFilters;
      const params: any = { current: nextPage.current, pageSize: nextPage.pageSize };
      if (activeFilters.length) params.filter = activeFilters;
      const [res, planRes] = await Promise.all([
        apiGet('/order/fetch', params),
        plans.length ? Promise.resolve({ data: plans }) : apiGet('/plan/fetch').catch(() => ({ data: [] })),
      ]);
      setRows(res.data || []);
      setTotal(res.total || 0);
      setPlans(planRes.data || []);
      setPage(nextPage);
    } catch (e: any) { message.error(e.message || '加载失败'); }
    finally { setLoading(false); }
  };
  useEffect(() => { load(); }, []);

  const act = async (path: string, row: any, extra: any = {}) => { await apiPost(path, { trade_no: row.trade_no, ...extra }, { form: true }); message.success('操作成功'); load(); };
  const updateCommission = async (row: any, status: any) => { await apiPost('/order/update', { trade_no: row.trade_no, commission_status: status }, { form: true }); message.success('已更新'); load(); };
  const jumpUserFilter = (key: string, condition: string, value: any) => {
    if (!value) return;
    history.pushState(null, '', pathUrl(`/user?filter_key=${encodeURIComponent(key)}&condition=${encodeURIComponent(condition)}&value=${encodeURIComponent(String(value))}`));
    window.dispatchEvent(new PopStateEvent('popstate'));
  };
  const openDetail = async (row: any) => {
    setDetailOpen(true);
    setDetailLoading(true);
    setDetail({ order: row, user: row.user || {}, inviteUser: row.invite_user || {} });
    try {
      const res = await apiPost('/order/detail', { id: row.id }, { form: true });
      const order = { ...row, ...(res.data || {}) };
      const [userRes, inviteRes] = await Promise.all([
        order.user_id ? apiGet('/user/getUserInfoById', { id: order.user_id }).catch(() => ({ data: row.user || {} })) : Promise.resolve({ data: row.user || {} }),
        order.invite_user_id ? apiGet('/user/getUserInfoById', { id: order.invite_user_id }).catch(() => ({ data: row.invite_user || {} })) : Promise.resolve({ data: row.invite_user || {} }),
      ]);
      setDetail({ order, user: userRes.data || {}, inviteUser: inviteRes.data || {} });
    } catch (e: any) {
      message.error(e.message || '加载订单详情失败');
    } finally {
      setDetailLoading(false);
    }
  };

  const order = detail?.order || {};
  const detailUser = detail?.user || {};
  const inviteUser = detail?.inviteUser || {};
  const planName = order.plan_name || plans.find((plan) => Number(plan.id) === Number(order.plan_id))?.name || '-';

  const columns: any[] = [
    { title: '# 订单号', dataIndex: 'trade_no', width: 130, render: (v: any, row: any) => <a onClick={() => openDetail(row)}>{shortTradeNo(v)}</a> },
    { title: '类型', dataIndex: 'type', width: 90, render: (v: any) => orderTypeText[Number(v)] || v },
    { title: '订阅计划', dataIndex: 'plan_name', width: 180, render: (v: any) => v || '-' },
    { title: '周期', dataIndex: 'period', align: 'center', width: 110, render: (v: any) => {
      const label = periodLabel(v);
      return label ? <Tag>{label}</Tag> : '';
    } },
    { title: '支付金额', dataIndex: 'total_amount', align: 'right', width: 120, render: money },
    { title: <Tooltip title="标记为[已支付]后将会由系统进行开通后并完成">订单状态 <QuestionCircleOutlined /></Tooltip>, dataIndex: 'status', width: 150, render: (status: any, row: any) => {
      const menu: MenuProps['items'] = [{ key: '1', label: '已支付', onClick: () => act('/order/paid', row) }, { key: '2', label: '取消', onClick: () => act('/order/cancel', row) }];
      return <div><Dropdown disabled={Number(status) !== 0} trigger={['click']} menu={{ items: menu }}><span><Badge status={statusBadge[Number(status)] || 'default'} /> {orderStatusText[Number(status)] || status} {Number(status) === 0 && <a>标记为 <CaretDownOutlined /></a>}</span></Dropdown>{Number(status) === 2 && <a className="ml-2" onClick={() => act('/order/paid', row)}>补单</a>}</div>;
    } },
    { title: '佣金金额', dataIndex: 'commission_balance', align: 'right', width: 120, render: (v: any, row: any) => Number(row.status) === 0 || Number(row.status) === 2 ? '-' : (v ? money(v) : '-') },
    { title: <Tooltip title="标记为[有效]后将会由系统处理后发放到用户并完成">佣金状态 <QuestionCircleOutlined /></Tooltip>, dataIndex: 'commission_status', width: 160, render: (status: any, row: any) => {
      if (Number(row.status) === 0 || Number(row.status) === 2 || !row.commission_balance) return '-';
      const items: MenuProps['items'] = [0, 1, 3].map((value) => ({ key: String(value), label: value === 0 ? '待确认' : value === 1 ? '有效' : '无效', disabled: Number(status) === value, onClick: () => updateCommission(row, value) }));
      if (Number(status) === 2) return <span><Badge status={commissionBadge[Number(status)] || 'default'} /> {commissionStatusText[Number(status)] || status}</span>;
      return <Dropdown trigger={['click']} menu={{ items }}><span><Badge status={commissionBadge[Number(status)] || 'default'} /> {commissionStatusText[Number(status)] || status} <a>标记为 <CaretDownOutlined /></a></span></Dropdown>;
    } },
    { title: '创建时间', dataIndex: 'created_at', align: 'right', width: 160, render: time },
  ];

  return <div className="legacy-page order-page">
    <div className="content-heading">订单管理</div>
    <Card className="block-card" styles={{ body: { padding: 0 } }}>
      <div className="forest-table-action">
        <Space>
          <LegacyFilterDrawer value={filters} keys={filterDefs as any} onOk={(next) => { setFilters(next); load({ current: 1 }, next.filter((item) => String(item.value ?? '').trim() !== '')); }}>
            <FilterButton active={effectiveFilters.length > 0} />
          </LegacyFilterDrawer>
          <Button className="ml-2" icon={<PlusOutlined />}>添加订单</Button>
        </Space>
      </div>
      <Table className="forest-table" rowKey="id" loading={loading} dataSource={rows} columns={columns} pagination={{ total, current: page.current, pageSize: page.pageSize, size: 'small', showSizeChanger: true, pageSizeOptions: [10, 50, 100, 150] }} scroll={{ x: 1200 }} onChange={(pagination: any) => load({ current: pagination.current, pageSize: pagination.pageSize })} />
    </Card>
    <Modal open={detailOpen} title="订单信息" footer={false} width={760} className="order-detail-modal" destroyOnHidden onCancel={() => { setDetailOpen(false); setDetail(null); }}>
      {detailLoading && !order.trade_no ? <Skeleton active paragraph={{ rows: 8 }} /> : <div className="legacy-detail-modal">
        <DetailSection title="基础信息">
          <DetailRow label="邮箱">{detailUser.email ? <a onClick={() => jumpUserFilter('email', '模糊', detailUser.email)}>{detailUser.email}</a> : '-'}</DetailRow>
          <DetailRow label="订单号">{order.trade_no}</DetailRow>
          <DetailRow label="订单周期" empty="">{periodLabel(order.period)}</DetailRow>
          <DetailRow label="订单状态">{orderStatusText[Number(order.status)] || order.status || '-'}</DetailRow>
          <DetailRow label="订阅计划">{planName}</DetailRow>
          <DetailRow label="回调单号">{order.callback_no || '-'}</DetailRow>
        </DetailSection>
        <DetailSection title="金额信息">
          <DetailRow label="支付金额">{money(order.total_amount)}</DetailRow>
          <DetailRow label="余额支付">{money(order.balance_amount)}</DetailRow>
          <DetailRow label="优惠金额">{money(order.discount_amount)}</DetailRow>
          <DetailRow label="退回金额">{money(order.refund_amount)}</DetailRow>
          <DetailRow label="折抵金额">{money(order.surplus_amount)}</DetailRow>
        </DetailSection>
        <DetailSection title="时间信息">
          <DetailRow label="创建时间">{fullTime(order.created_at)}</DetailRow>
          <DetailRow label="更新时间">{fullTime(order.updated_at)}</DetailRow>
        </DetailSection>
        {!!order.invite_user_id && Number(order.status) === 3 && <>
          <DetailSection title="邀请佣金">
            <DetailRow label="邀请人">{inviteUser.email ? <Tooltip title="查看TA邀请的人"><a onClick={() => jumpUserFilter('invite_by_email', '模糊', inviteUser.email)}>{inviteUser.email}</a></Tooltip> : '-'}</DetailRow>
            <DetailRow label="佣金金额">{money(order.commission_balance)}</DetailRow>
            {!!order.actual_commission_balance && <DetailRow label="实际发放">{money(order.actual_commission_balance)}</DetailRow>}
            <DetailRow label="佣金状态">{commissionStatusText[Number(order.commission_status)] || order.commission_status || '-'}</DetailRow>
          </DetailSection>
        </>}
      </div>}
    </Modal>
  </div>;
}
