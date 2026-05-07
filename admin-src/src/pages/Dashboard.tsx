import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Alert, Button, Col, Row, Space, Spin, message } from 'antd';
import {
  SettingOutlined,
  ShoppingCartOutlined,
  AppstoreOutlined,
  TeamOutlined,
  LineChartOutlined,
  UserOutlined,
} from '@ant-design/icons';
import * as echarts from 'echarts';
import { apiGet, getSettings } from '../lib/api';

type ChartPayload = Record<string, any>[];

type ChartBlockProps = {
  title?: string;
  height?: number;
  option: echarts.EChartsOption;
  themeName?: string;
};

const defaultCurrency = 'CNY';

function numberValue(value: any) {
  const n = Number(value || 0);
  return Number.isFinite(n) ? n : 0;
}

function cents(value: any) {
  return (numberValue(value) / 100).toFixed(2);
}

function rankValue(value: any) {
  const n = Number(value || 0);
  return Number.isFinite(n) ? Number(n.toFixed(2)) : 0;
}

function pathUrl(path: string) {
  const settings = getSettings();
  const adminPath = String(settings.secure_path || 'localadmin').replace(/^\/+|\/+$/g, '');
  return `/${adminPath}${path}`;
}

echarts.registerTheme('vintage', {
  color: ['#d87c7c', '#919e8b', '#d7ab82', '#6e7074', '#61a0a8', '#efa18d', '#787464', '#cc7e63', '#724e58', '#4b565b'],
  graph: { color: ['#d87c7c', '#919e8b', '#d7ab82', '#6e7074', '#61a0a8', '#efa18d', '#787464', '#cc7e63', '#724e58', '#4b565b'] },
});

function ChartBlock({ title, height = 400, option, themeName }: ChartBlockProps) {
  const el = useRef<HTMLDivElement | null>(null);
  const chart = useRef<echarts.ECharts | null>(null);
  const chartTheme = useRef<string | undefined>();

  useEffect(() => {
    if (!el.current) return;
    if (!chart.current || chartTheme.current !== themeName) {
      chart.current?.dispose();
      chart.current = echarts.init(el.current, themeName, { renderer: 'svg' });
      chartTheme.current = themeName;
    }
    chart.current.setOption(option, true);
    const resize = () => chart.current?.resize();
    window.addEventListener('resize', resize);
    return () => window.removeEventListener('resize', resize);
  }, [option, title, themeName]);

  useEffect(() => () => chart.current?.dispose(), []);

  return <div className="legacy-chart-wrap">
    {title && <div className="block-header block-header-default"><h3 className="block-title">{title}</h3></div>}
    <div className="block-content"><div ref={el} className="legacy-chart" style={{ height }} /></div>
  </div>;
}

function buildOrderOption(rows: ChartPayload): echarts.EChartsOption {
  const dates: string[] = [];
  const seriesMap = new Map<string, any[]>();
  rows.forEach((item) => {
    const date = String(item.date || '');
    const type = String(item.type || '');
    if (!date || !type) return;
    if (!dates.includes(date)) dates.push(date);
    if (!seriesMap.has(type)) seriesMap.set(type, []);
  });
  const indexByDate = new Map(dates.map((date, index) => [date, index]));
  seriesMap.forEach((_, key) => seriesMap.set(key, new Array(dates.length).fill(0)));
  rows.forEach((item) => {
    const date = String(item.date || '');
    const type = String(item.type || '');
    const idx = indexByDate.get(date);
    if (idx === undefined || !seriesMap.has(type)) return;
    seriesMap.get(type)![idx] = numberValue(item.value);
  });

  return {
    backgroundColor: '#ffffff',
    tooltip: { trigger: 'axis' },
    legend: { data: Array.from(seriesMap.keys()), left: 0, z: 4, type: 'scroll' },
    grid: { left: '1%', right: '1%', bottom: '3%', top: 48, containLabel: true },
    xAxis: { type: 'category', boundaryGap: false, data: dates },
    yAxis: { type: 'value' },
    series: Array.from(seriesMap.entries()).map(([name, data]) => ({ name, type: 'line', smooth: true, data })),
  };
}

function buildRankOption(rows: ChartPayload, labelKey: string): echarts.EChartsOption {
  const data = [...rows].reverse();
  return {
    tooltip: { trigger: 'axis', formatter: (params: any) => `${params?.[0]?.value || 0} GB` },
    grid: { top: '1%', left: '1%', right: '1%', bottom: '3%', containLabel: true },
    xAxis: { type: 'value' },
    yAxis: { type: 'category', data: data.map((item) => String(item[labelKey] || item.name || item.email || '-')) },
    series: [{ data: data.map((item) => rankValue(item.total ?? ((numberValue(item.u) + numberValue(item.d)) / 1024 / 1024 / 1024))), type: 'bar' }],
  };
}

function buildInviteRankOption(rows: ChartPayload): echarts.EChartsOption {
  const data = [...rows].reverse();
  return {
    tooltip: { trigger: 'axis', formatter: (params: any) => `${params?.[0]?.value || 0} 人` },
    grid: { top: '1%', left: '1%', right: '1%', bottom: '3%', containLabel: true },
    xAxis: { type: 'value' },
    yAxis: { type: 'category', data: data.map((item) => String(item.email || item.name || item.invite_user_id || '-')) },
    series: [{ data: data.map((item) => numberValue(item.count)), type: 'bar' }],
  };
}

function QuickNav({ onGo }: { onGo: (path: string) => void }) {
  const items = [
    { title: '系统设置', path: '/config/system', icon: <SettingOutlined /> },
    { title: '订单管理', path: '/order', icon: <ShoppingCartOutlined /> },
    { title: '订阅管理', path: '/plan', icon: <AppstoreOutlined /> },
    { title: '用户管理', path: '/user', icon: <TeamOutlined /> },
  ];
  return <div className="block border-bottom js-classic-nav d-none d-sm-block">
    <div className="block-content block-content-full">
      <div className="legacy-quick-grid">
        {items.map((item) => <button key={item.path} className="block block-bordered block-link-pop text-center mb-0 legacy-quick-item" onClick={() => onGo(item.path)}>
          <span className="legacy-quick-icon">{item.icon}</span>
          <span className="font-w600 text-uppercase">{item.title}</span>
        </button>)}
      </div>
    </div>
  </div>;
}

function PrimaryStats({ stat, currency }: { stat: any; currency: string }) {
  const items = [
    { label: '在线人数', value: stat.online_user || '0', icon: <TeamOutlined />, labelClass: 'text-muted mb-1', labelStyle: { width: 120 } as React.CSSProperties },
    { label: '今日收入', value: cents(stat.day_income), unit: currency, icon: <LineChartOutlined />, labelClass: 'text-muted w-75 mb-1' },
    { label: '实时注册', value: stat.day_register_total || '0', icon: <UserOutlined />, labelClass: 'text-muted mb-1', labelStyle: { width: 120 } as React.CSSProperties },
  ];
  return <div className="block border-bottom mb-0 forest-stats-bar">
    <div className="block-content">
      <div className="d-flex align-items-center legacy-stat-row-primary">
        {items.map((item) => <div className="pr-4 pr-sm-5 pl-0 pl-sm-3 legacy-stat-main" key={item.label}>
          <span className="legacy-stat-icon" aria-hidden="true">{item.icon}</span>
          <div className={item.labelClass} style={item.labelStyle}>{item.label}</div>
          <div className="display-4 text-black font-w300 mb-2">{item.value}{item.unit && <span className="font-size-h5 font-w600 text-muted">{item.unit}</span>}</div>
        </div>)}
      </div>
    </div>
  </div>;
}

function SecondaryStats({ stat, currency }: { stat: any; currency: string }) {
  const items = [
    { label: '本月收入', value: `${cents(stat.month_income)} ${currency}` },
    { label: '本月新增用户', value: stat.month_register_total || 0 },
    { label: '本月新增付费用户', value: stat.month_paid_user_total || 0 },
    { label: '上月收入', value: `${cents(stat.last_month_income)} ${currency}` },
    { label: '上月新增用户', value: stat.last_month_register_total || 0 },
    { label: '上月新增付费用户', value: stat.last_month_paid_user_total || 0 },
    { label: '上月佣金支出', value: `${cents(stat.commission_last_month_payout)} ${currency}` },
  ];
  return <div className="block border-bottom mb-0 forest-stats-bar">
    <div className="block-content block-content-full">
      <div className="legacy-stat-row legacy-stat-row-secondary">
        {items.map((item) => <div className="legacy-stat-small" key={item.label}>
          <p className="fs-3 text-dark mb-0">{item.value}</p>
          <p className="text-muted mb-0">{item.label}</p>
        </div>)}
      </div>
    </div>
  </div>;
}

export default function Dashboard() {
  const [loading, setLoading] = useState(true);
  const [stat, setStat] = useState<any>({});
  const [orderRows, setOrderRows] = useState<ChartPayload>([]);
  const [serverToday, setServerToday] = useState<ChartPayload>([]);
  const [serverLast, setServerLast] = useState<ChartPayload>([]);
  const [userToday, setUserToday] = useState<ChartPayload>([]);
  const [userLast, setUserLast] = useState<ChartPayload>([]);
  const [inviteToday, setInviteToday] = useState<ChartPayload>([]);
  const [inviteLast, setInviteLast] = useState<ChartPayload>([]);
  const [site, setSite] = useState<any>({ currency: defaultCurrency });
  const [queueStatus, setQueueStatus] = useState<string>('');

  useEffect(() => {
    let mounted = true;
    (async () => {
      setLoading(true);
      try {
        const [override, order, st, sl, ut, ul, it, il, siteCfg] = await Promise.all([
          apiGet('/stat/getOverride'),
          apiGet('/stat/getOrder'),
          apiGet('/stat/getServerTodayRank'),
          apiGet('/stat/getServerLastRank'),
          apiGet('/stat/getUserTodayRank'),
          apiGet('/stat/getUserLastRank'),
          apiGet('/stat/getInviteTodayRank'),
          apiGet('/stat/getInviteLastRank'),
          apiGet('/config/fetch', { key: 'site' }).catch(() => ({ data: {} })),
        ]);
        if (!mounted) return;
        setStat(override.data || {});
        setOrderRows(order.data || []);
        setServerToday(st.data || []);
        setServerLast(sl.data || []);
        setUserToday(ut.data || []);
        setUserLast(ul.data || []);
        setInviteToday(it.data || []);
        setInviteLast(il.data || []);
        setSite(siteCfg.data?.site || siteCfg.data || { currency: defaultCurrency });
      } catch (e: any) {
        message.error(e.message || '加载仪表盘失败');
      } finally {
        if (mounted) setLoading(false);
      }
      try {
        const res = await fetch('/monitor/api/stats', { credentials: 'same-origin' });
        const payload = await res.json();
        if (mounted) setQueueStatus(payload?.status || '');
      } catch {
        if (mounted) setQueueStatus('');
      }
    })();
    return () => { mounted = false; };
  }, []);

  const currency = site.currency || defaultCurrency;
  const orderOption = useMemo(() => buildOrderOption(orderRows), [orderRows]);
  const serverTodayOption = useMemo(() => buildRankOption(serverToday, 'server_name'), [serverToday]);
  const serverLastOption = useMemo(() => buildRankOption(serverLast, 'server_name'), [serverLast]);
  const userTodayOption = useMemo(() => buildRankOption(userToday, 'email'), [userToday]);
  const userLastOption = useMemo(() => buildRankOption(userLast, 'email'), [userLast]);
  const inviteTodayOption = useMemo(() => buildInviteRankOption(inviteToday), [inviteToday]);
  const inviteLastOption = useMemo(() => buildInviteRankOption(inviteLast), [inviteLast]);
  const go = (path: string) => { history.pushState(null, '', pathUrl(path)); window.dispatchEvent(new PopStateEvent('popstate')); };

  return <div className="legacy-page dashboard-page">
    <div className="content-heading">仪表盘</div>
    <Spin spinning={loading}>
      {queueStatus && queueStatus !== 'running' && <Alert className="legacy-alert" type="error" showIcon message="当前队列服务运行异常，可能会导致业务无法使用。" />}
      {!!stat.ticket_pending_total && <Alert className="legacy-alert" type="error" showIcon message={<span>有 {stat.ticket_pending_total} 条工单等待处理 <Button type="link" className="alert-link" onClick={() => go('/ticket')}>立即处理</Button></span>} />}
      {!!stat.commission_pending_total && <Alert className="legacy-alert" type="error" showIcon message={<span>有 {stat.commission_pending_total} 笔佣金等待确认 <Button type="link" className="alert-link" onClick={() => go('/order?commission_pending=1')}>立即处理</Button></span>} />}

      <QuickNav onGo={go} />
      <PrimaryStats stat={stat} currency={currency} />
      <SecondaryStats stat={stat} currency={currency} />

      <div className="block border-bottom mb-0">
        <ChartBlock option={orderOption} height={400} themeName="vintage" />
      </div>

      <Row gutter={[16, 16]} className="legacy-chart-grid">
        <Col xs={24} lg={12}><div className="block block-card"><ChartBlock title="今日节点流量排行" option={serverTodayOption} /></div></Col>
        <Col xs={24} lg={12}><div className="block block-card"><ChartBlock title="昨日节点流量排行" option={serverLastOption} /></div></Col>
        <Col xs={24} lg={12}><div className="block block-card"><ChartBlock title="今日用户流量排行" option={userTodayOption} /></div></Col>
        <Col xs={24} lg={12}><div className="block block-card"><ChartBlock title="昨日用户流量排行" option={userLastOption} /></div></Col>
      </Row>

      <Row gutter={[16, 16]} className="legacy-chart-grid">
        <Col xs={24} lg={12}><div className="block block-card"><ChartBlock title="今日邀请排行" option={inviteTodayOption} /></div></Col>
        <Col xs={24} lg={12}><div className="block block-card"><ChartBlock title="昨日邀请排行" option={inviteLastOption} /></div></Col>
      </Row>
    </Spin>
  </div>;
}
