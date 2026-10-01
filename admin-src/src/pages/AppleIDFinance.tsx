import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, DatePicker, Empty, Select, Skeleton, Space, Table, Tag, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { ReloadOutlined, SearchOutlined } from '@ant-design/icons';
import dayjs, { Dayjs } from 'dayjs';
import * as echarts from 'echarts';
import { apiGet, money } from '../lib/api';
import AppleIDOrderDetail, { AppleIDOrderStatus } from './AppleIDOrderDetail';
import './AppleIDPage.css';
import './AppleIDFinance.css';

type Filters = { start_date?: string; end_date?: string; product_id?: number };
type Page = { current: number; pageSize: number };
type Totals = { paid_count: number; paid_total: number; refund_count: number; refund_total: number; net_total: number };
type Daily = Totals & { date: string };
type Stats = { start_date: string; end_date: string; timezone: string; summary: Totals; daily: Daily[] };
type Transaction = {
  id: string; order_id: number; trade_no: string; user_email: string;
  product_id: number; product_name: string; event_type: 'payment' | 'refund';
  amount: number; occurred_at: number; occurred_at_local?: string; status: number; payment_id?: number; callback_no?: string;
};
const totalFields: (keyof Totals)[] = ['paid_count', 'paid_total', 'refund_count', 'refund_total', 'net_total'];
const validTotals = (totals: Totals) => totals && totalFields.every((key) => Number.isFinite(totals[key]));

function FinanceChart({ daily }: { daily: Daily[] }) {
  const element = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!element.current) return;
    const chart = echarts.init(element.current, undefined, { renderer: 'svg' });
    chart.setOption({
      animation: false,
      color: ['#252525', '#b46b35', '#628c78'],
      aria: { enabled: true, label: { description: '所选日期每日收款、确认退款及净流水，单位为元。' } },
      tooltip: { trigger: 'axis', valueFormatter: (value: number) => `¥${Number(value).toFixed(2)}` },
      legend: { top: 4, left: 'center', data: ['收款', '确认退款', '净流水'] },
      grid: { top: 54, left: 12, right: 18, bottom: 20, containLabel: true },
      xAxis: { type: 'category', boundaryGap: false, data: daily.map((row) => row.date), axisLabel: { color: '#666', formatter: (value: string) => value.slice(5), hideOverlap: true }, axisLine: { lineStyle: { color: '#d9d9d9' } } },
      yAxis: { type: 'value', name: '元', nameTextStyle: { color: '#777' }, splitLine: { lineStyle: { color: '#efefef' } } },
      series: ([['收款', 'paid_total'], ['确认退款', 'refund_total'], ['净流水', 'net_total']] as const).map(([name, key]) => ({
        name, type: 'line', smooth: false, showSymbol: daily.length <= 7, symbolSize: 5,
        lineStyle: { width: 2, type: key === 'refund_total' ? 'dashed' : 'solid' },
        data: daily.map((row) => row[key] / 100), emphasis: { focus: 'series' },
      })),
    });
    const resize = new ResizeObserver(() => chart.resize());
    resize.observe(element.current);
    return () => { resize.disconnect(); chart.dispose(); };
  }, [daily]);
  return <div ref={element} className="apple-finance-chart" role="img" aria-label="每日收款、确认退款及净流水趋势图，单位元" />;
}

function eventTime(timestamp: number, timezone?: string) {
  if (!timestamp) return '-';
  try {
    return new Intl.DateTimeFormat('zh-CN', {
      timeZone: timezone, year: 'numeric', month: '2-digit', day: '2-digit',
      hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
    }).format(new Date(timestamp * 1000));
  } catch {
    return new Date(timestamp * 1000).toISOString().replace('T', ' ').replace('.000Z', ' UTC');
  }
}

export default function AppleIDFinance() {
  const [stats, setStats] = useState<Stats | null>(null);
  const [rows, setRows] = useState<Transaction[]>([]);
  const [total, setTotal] = useState(0);
  const [filters, setFilters] = useState<Filters>({});
  const [page, setPage] = useState<Page>({ current: 1, pageSize: 20 });
  const [range, setRange] = useState<[Dayjs, Dayjs] | null>(null);
  const [productID, setProductID] = useState<number>();
  const [products, setProducts] = useState<{ id: number; name: string }[]>([]);
  const [serverToday, setServerToday] = useState('');
  const [statsLoading, setStatsLoading] = useState(true);
  const [tableLoading, setTableLoading] = useState(true);
  const [productsLoading, setProductsLoading] = useState(false);
  const [statsError, setStatsError] = useState('');
  const [tableError, setTableError] = useState('');
  const [productsError, setProductsError] = useState('');
  const [validationError, setValidationError] = useState('');
  const [selectedID, setSelectedID] = useState<number | null>(null);
  const alive = useRef(true);
  const statsRequest = useRef(0);
  const tableRequest = useRef(0);
  const productsRequest = useRef(0);

  const loadTransactions = async (nextFilters: Filters, nextPage: Page) => {
    const token = ++tableRequest.current;
    setPage(nextPage);
    setTableLoading(true);
    setTableError('');
    setRows([]);
    setTotal(0);
    try {
      const res = await apiGet('/apple-id/finance/transactions', { ...nextFilters, current: nextPage.current, page_size: nextPage.pageSize });
      if (!alive.current || token !== tableRequest.current) return;
      if (!Array.isArray(res?.data) || !Number.isFinite(Number(res.total))) throw new Error('流水响应异常，请重试');
      setRows(res.data);
      setTotal(Number(res.total));
    } catch (e: any) {
      if (alive.current && token === tableRequest.current) setTableError(e.message || '流水加载失败，请重试');
    } finally {
      if (alive.current && token === tableRequest.current) setTableLoading(false);
    }
  };

  const loadAll = async (nextFilters: Filters = filters, nextPage: Page = page) => {
    const token = ++statsRequest.current;
    tableRequest.current += 1;
    setFilters(nextFilters);
    setPage(nextPage);
    setStatsLoading(true);
    setTableLoading(true);
    setStatsError('');
    setTableError('');
    setStats(null);
    setRows([]);
    setTotal(0);
    try {
      const res = await apiGet('/apple-id/finance/stats', nextFilters);
      if (!alive.current || token !== statsRequest.current) return;
      const data = res?.data as Stats | undefined;
      if (!data?.start_date || !data.end_date || !validTotals(data.summary) || !Array.isArray(data.daily) || !data.daily.every(validTotals)) throw new Error('统计响应异常，请重试');
      const effectiveFilters = { ...nextFilters, start_date: data.start_date, end_date: data.end_date };
      setStats(data);
      setFilters(effectiveFilters);
      if (!nextFilters.start_date) {
        setRange([dayjs(data.start_date), dayjs(data.end_date)]);
        setServerToday(data.end_date);
      }
      setStatsLoading(false);
      await loadTransactions(effectiveFilters, nextPage);
    } catch (e: any) {
      if (!alive.current || token !== statsRequest.current) return;
      setStatsError(e.message || '统计加载失败，请重试');
      setTableError('统计日期未确认，暂未加载流水。请重试统计。');
      setTableLoading(false);
    } finally {
      if (alive.current && token === statsRequest.current) setStatsLoading(false);
    }
  };

  const loadProducts = async () => {
    const token = ++productsRequest.current;
    setProductsLoading(true);
    setProductsError('');
    try {
      const res = await apiGet('/apple-id/product/fetch');
      if (!alive.current || token !== productsRequest.current) return;
      if (!Array.isArray(res?.data)) throw new Error('商品响应异常');
      setProducts(res.data);
    } catch (e: any) {
      if (alive.current && token === productsRequest.current) setProductsError(e.message || '商品筛选项加载失败');
    } finally {
      if (alive.current && token === productsRequest.current) setProductsLoading(false);
    }
  };

  useEffect(() => {
    alive.current = true;
    loadAll({}, { current: 1, pageSize: 20 });
    loadProducts();
    return () => { alive.current = false; statsRequest.current += 1; tableRequest.current += 1; productsRequest.current += 1; };
  }, []);

  const query = (nextRange = range) => {
    if (nextRange && (nextRange[1].diff(nextRange[0], 'day') < 0 || nextRange[1].diff(nextRange[0], 'day') >= 366)) {
      setValidationError('请选择不超过 366 天的日期范围');
      return;
    }
    setValidationError('');
    loadAll({ product_id: productID, ...(nextRange ? { start_date: nextRange[0].format('YYYY-MM-DD'), end_date: nextRange[1].format('YYYY-MM-DD') } : {}) }, { ...page, current: 1 });
  };
  const selectRecent = (days: number) => {
    if (!serverToday) return;
    const nextRange: [Dayjs, Dayjs] = [dayjs(serverToday).subtract(days - 1, 'day'), dayjs(serverToday)];
    setRange(nextRange);
    query(nextRange);
  };

  const columns: ColumnsType<Transaction> = [
    { title: '发生时间', dataIndex: 'occurred_at', width: 215, render: (value, row) => row.occurred_at_local || eventTime(value, stats?.timezone) },
    { title: '类型', dataIndex: 'event_type', width: 110, render: (value) => <Tag color={value === 'payment' ? 'success' : 'default'}>{value === 'payment' ? '收款' : '确认退款'}</Tag> },
    { title: '金额', dataIndex: 'amount', width: 135, render: (value, row) => <span className={`apple-finance-amount ${row.event_type === 'refund' ? 'is-refund' : ''}`}>{row.event_type === 'payment' ? '+' : '−'}{money(value)}</span> },
    { title: '订单号', dataIndex: 'trade_no', width: 280, render: (value, row) => <Button className="apple-finance-order-link" type="link" onClick={() => setSelectedID(row.order_id)}>{value}</Button> },
    { title: '购买用户', dataIndex: 'user_email', width: 220, render: (value) => value || '用户已删除' },
    { title: '商品', dataIndex: 'product_name', width: 220, render: (value, row) => <div>{value}<div className="text-muted">商品 #{row.product_id}</div></div> },
    { title: '当前订单状态', dataIndex: 'status', width: 180, render: (value) => <AppleIDOrderStatus status={value} /> },
    { title: '支付流水', dataIndex: 'callback_no', width: 250, render: (value, row) => <div>{value || '-'}{row.payment_id && <div className="text-muted">支付通道 #{row.payment_id}</div>}</div> },
    { title: '操作', width: 85, fixed: 'right', render: (_, row) => <Button type="link" onClick={() => setSelectedID(row.order_id)}>详情</Button> },
  ];

  return <div className="legacy-page apple-finance-page">
    <div className="content-heading">Apple ID 流水</div>
    <div className="apple-finance-content">
      <Typography.Paragraph type="secondary" className="apple-finance-intro">独立统计 Apple ID 的实际收款与确认退款，不计入套餐流水。</Typography.Paragraph>
      <Card className="apple-finance-filter-card">
        <div className="apple-finance-filters">
          <label className="apple-finance-field"><span>统计日期</span><DatePicker.RangePicker value={range} format="YYYY-MM-DD" popupClassName="apple-finance-date-popup" onChange={(value) => setRange(value?.[0] && value?.[1] ? [value[0], value[1]] : null)} allowClear placeholder={['开始日期', '结束日期']} /></label>
          <label className="apple-finance-field apple-finance-product"><span>商品</span><Select allowClear showSearch value={productID} loading={productsLoading} onChange={setProductID} optionFilterProp="label" placeholder="全部商品" options={products.map((product) => ({ label: `${product.name} (#${product.id})`, value: product.id }))} /></label>
          <Space wrap>
            <Button type="primary" icon={<SearchOutlined />} onClick={() => query()}>查询</Button>
            <Button onClick={() => { setRange(null); setProductID(undefined); setValidationError(''); loadAll({}, { ...page, current: 1 }); }}>重置</Button>
            <Button icon={<ReloadOutlined />} loading={statsLoading || tableLoading} onClick={() => loadAll()}>刷新</Button>
          </Space>
        </div>
        <div className="apple-finance-range-tools"><span className="text-muted">快捷日期</span><Space size={8} wrap>{[7, 30, 90].map((days) => <Button key={days} size="small" disabled={!serverToday} onClick={() => selectRecent(days)}>近 {days} 天</Button>)}</Space></div>
        {validationError && <Alert type="warning" showIcon message={validationError} />}
        {productsError && <Alert type="warning" showIcon message="商品筛选项加载失败，可继续查询全部商品" description={productsError} action={<Button size="small" loading={productsLoading} onClick={loadProducts}>重试</Button>} />}
      </Card>
      {statsError && <Alert type="error" showIcon message={statsError} action={<Button size="small" onClick={() => loadAll()}>重试</Button>} />}
      {statsLoading ? <Card className="apple-finance-placeholder"><Skeleton active paragraph={{ rows: 3 }} /></Card> : stats && <>
        <div className="apple-finance-period">{stats.start_date} 至 {stats.end_date}<span>统计时区：{stats.timezone}</span></div>
        <div className="apple-finance-summary">
          <Card><div className="apple-finance-stat-label">收款金额</div><div className="apple-finance-stat-value">{money(stats.summary.paid_total)}</div><div className="apple-finance-stat-note">含支付手续费</div></Card>
          <Card><div className="apple-finance-stat-label">确认退款</div><div className="apple-finance-stat-value">{money(stats.summary.refund_total)}</div><div className="apple-finance-stat-note">{stats.summary.refund_count} 笔退款</div></Card>
          <Card><div className="apple-finance-stat-label">净流水</div><div className="apple-finance-stat-value">{money(stats.summary.net_total)}</div><div className="apple-finance-stat-note">收款 − 确认退款</div></Card>
          <Card><div className="apple-finance-stat-label">支付订单数</div><div className="apple-finance-stat-value">{stats.summary.paid_count}<small>笔</small></div><div className="apple-finance-stat-note">按付款时间统计</div></Card>
        </div>
        <Card className="apple-finance-trend" title="每日流水趋势"><FinanceChart daily={stats.daily} /><Typography.Paragraph type="secondary" className="apple-finance-chart-note">退款按后台确认时间及订单全额统计。净流水为收款减退款，不代表利润或支付通道结算金额。</Typography.Paragraph></Card>
      </>}
      <Card className="block-card apple-finance-transactions" title="收款与退款明细">
        {tableError && <div className="apple-finance-table-error"><Alert type="error" showIcon message={tableError} action={!statsError && <Button size="small" onClick={() => loadTransactions(filters, page)}>重试</Button>} /></div>}
        <Table<Transaction> className="forest-table" rowKey="id" columns={columns} dataSource={rows} loading={tableLoading} scroll={{ x: 1670 }}
          locale={{ emptyText: tableError ? <span className="text-muted">数据未加载</span> : tableLoading ? '正在加载…' : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="所选日期暂无收款或退款" /> }}
          pagination={tableLoading || tableError || statsError ? false : { ...page, total, showSizeChanger: true, pageSizeOptions: [20, 50, 100], showTotal: (count) => `共 ${count} 笔流水`, size: 'small' }}
          onChange={(next) => loadTransactions(filters, { current: next.current || 1, pageSize: next.pageSize || 20 })} />
      </Card>
    </div>
    {selectedID !== null && <AppleIDOrderDetail key={selectedID} id={selectedID} onClose={() => setSelectedID(null)} onChanged={() => { if (alive.current) loadAll(); }} />}
  </div>;
}
