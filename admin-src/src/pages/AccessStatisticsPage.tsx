import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Col, DatePicker, Form, Input, Row, Select, Space, Statistic, Table, Tag, Typography } from 'antd';
import { ReloadOutlined, SearchOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { apiGet, unwrapData } from '../lib/api';
import './AccessStatisticsPage.css';

type RouteEvent = {
  id?: number; event_id: string; node_type: string; node_id: number; user_id: number; email?: string;
  occurred_at: number; client_ip: string; target_host: string; target_ip: string; target_port: number;
  network: string; result: string;
};
type RoutePage = {
  items: RouteEvent[]; total?: number; total_exact?: boolean; page?: number; page_size: number;
  has_more?: boolean; next_cursor?: string; warnings?: string[];
};
type RouteNavigation = { index: number; cursors: string[]; pageSize: number };
type RouteSummary = {
  events: number; users: number; known_domain_events: number; unknown_domain_events: number;
  top_domains: { domain: string; users: number; events: number }[];
  top_ips: { ip: string; users: number; events: number }[];
};
type RouteNode = {
  node_type: string; node_id: number; name: string; protocol: string; show: number; supported: boolean;
  configured?: boolean; enabled?: boolean; capability?: string; auto_capable?: boolean;
  last_seen_at?: number; last_event_at: number;
};
type Filter = { user: string; target: string; node_id?: number; from: number; to: number };

const ENDPOINT = '/observation';
const initialFilter = (): Filter => ({ user: '', target: '', from: dayjs().subtract(15, 'minute').unix(), to: dayjs().unix() });
const firstPage = (pageSize = 20): RouteNavigation => ({ index: 0, cursors: [''], pageSize });
const timeText = (value?: number) => value && value > 0 ? dayjs.unix(value).format('YYYY-MM-DD HH:mm:ss') : '—';
const countText = (value?: number) => new Intl.NumberFormat('zh-CN').format(value || 0);
const errorText = (cause: unknown) => cause instanceof Error ? cause.message : '请求失败，请稍后重试';

function query(filter: Filter) {
  const user = filter.user.trim();
  const target = filter.target.trim();
  if (!Number.isSafeInteger(filter.from) || !Number.isSafeInteger(filter.to) || filter.to <= filter.from) throw new Error('请选择有效的起止时间');
  if (filter.to - filter.from > 7 * 86400) throw new Error('单次最多查询 7 天，请缩小时间范围');
  if (/^\d+$/.test(user) && (!Number.isSafeInteger(Number(user)) || Number(user) < 1)) throw new Error('用户 ID 无效');
  if (user && !/^\d+$/.test(user) && !user.includes('@')) throw new Error('请输入完整邮箱或用户 ID');
  if (target && /[\/?#@\s]/.test(target)) throw new Error('目标只能填写域名或 IP，不要填写完整网址');
  return {
    from: filter.from, to: filter.to, node_type: filter.node_id ? 'v2node' : undefined,
    node_id: filter.node_id, user_id: /^\d+$/.test(user) ? Number(user) : undefined,
    account: user && !/^\d+$/.test(user) ? user : undefined, target: target || undefined,
  };
}

export default function AccessStatisticsPage() {
  const [draft, setDraft] = useState<Filter>(initialFilter);
  const [applied, setApplied] = useState<Filter | null>(null);
  const [page, setPage] = useState<RoutePage | null>(null);
  const [summary, setSummary] = useState<RouteSummary | null>(null);
  const [navigation, setNavigation] = useState<RouteNavigation>(() => firstPage());
  const [nodes, setNodes] = useState<RouteNode[]>([]);
  const [nodesError, setNodesError] = useState('');
  const [nodesLoading, setNodesLoading] = useState(false);
  const [error, setError] = useState('');
  const [summaryError, setSummaryError] = useState('');
  const [loading, setLoading] = useState(false);
  const [summaryLoading, setSummaryLoading] = useState(false);
  const pending = useRef<AbortController | null>(null);
  const summaryPending = useRef<AbortController | null>(null);
  const nodesPending = useRef<AbortController | null>(null);

  // Rows are independent from optional, expensive whole-range aggregation.
  // A summary error must never discard a successfully loaded page.
  const load = async (filter: Filter, requestedNavigation?: RouteNavigation) => {
    let params: ReturnType<typeof query>;
    try { params = query(filter); } catch (cause) { setError(errorText(cause)); return; }
    const next = requestedNavigation || firstPage(navigation.pageSize);
    pending.current?.abort();
    if (!requestedNavigation) {
      summaryPending.current?.abort();
      summaryPending.current = null;
      setSummary(null); setSummaryError(''); setSummaryLoading(false);
    }
    const controller = new AbortController();
    pending.current = controller;
    let timedOut = false;
    const timeout = window.setTimeout(() => { timedOut = true; controller.abort(); }, 20000);
    setLoading(true); setError('');
    try {
      const response = await apiGet(`${ENDPOINT}/events`, {
        ...params, cursor: next.cursors[next.index] || undefined,
        page: next.index + 1, page_size: next.pageSize,
      }, { cache: 'no-store', signal: controller.signal });
      const events = unwrapData(response) as RoutePage;
      if (!events || !Array.isArray(events.items) || !Number.isFinite(events.page_size) ||
          (typeof events.has_more !== 'boolean' && !Number.isFinite(events.total))) {
        throw new Error('访问明细返回格式无效，请更新后端');
      }
      if (controller.signal.aborted || pending.current !== controller) return;
      setPage(events); setApplied({ ...filter }); setNavigation(next);
    } catch (cause) {
      if (pending.current === controller && (timedOut || !controller.signal.aborted)) {
        setError(timedOut ? '明细读取超时，请缩小时间范围，或指定用户、目标后重试。' : errorText(cause));
      }
    } finally {
      window.clearTimeout(timeout);
      if (pending.current === controller) setLoading(false);
    }
  };

  const loadSummary = async (filter: Filter) => {
    let params: ReturnType<typeof query>;
    try { params = query(filter); } catch (cause) { setSummaryError(errorText(cause)); return; }
    summaryPending.current?.abort();
    const controller = new AbortController();
    summaryPending.current = controller;
    let timedOut = false;
    const timeout = window.setTimeout(() => { timedOut = true; controller.abort(); }, 30000);
    setSummaryLoading(true); setSummaryError('');
    try {
      const response = await apiGet(`${ENDPOINT}/summary`, params, { cache: 'no-store', signal: controller.signal });
      const counts = unwrapData(response) as RouteSummary;
      if (!counts || ![counts.events, counts.users, counts.known_domain_events, counts.unknown_domain_events].every(Number.isFinite) ||
          !Array.isArray(counts.top_domains) || !Array.isArray(counts.top_ips)) {
        throw new Error('访问汇总返回格式无效，请更新后端');
      }
      if (controller.signal.aborted || summaryPending.current !== controller) return;
      setSummary(counts);
    } catch (cause) {
      if (summaryPending.current === controller && (timedOut || !controller.signal.aborted)) {
        setSummaryError(timedOut ? '统计计算超时，请缩小时间范围，或指定用户、目标；下方明细仍可正常查看。' : errorText(cause));
      }
    } finally {
      window.clearTimeout(timeout);
      if (summaryPending.current === controller) setSummaryLoading(false);
    }
  };

  const loadNodes = async () => {
    nodesPending.current?.abort();
    const controller = new AbortController();
    nodesPending.current = controller;
    let timedOut = false;
    const timeout = window.setTimeout(() => { timedOut = true; controller.abort(); }, 15000);
    setNodesLoading(true);
    try {
      const response = await apiGet(`${ENDPOINT}/nodes`, {}, { cache: 'no-store', signal: controller.signal });
      const result = unwrapData(response);
      if (!Array.isArray(result)) throw new Error('节点列表格式无效');
      if (!controller.signal.aborted && nodesPending.current === controller) { setNodes(result as RouteNode[]); setNodesError(''); }
    } catch (cause) {
      if (nodesPending.current === controller && (timedOut || !controller.signal.aborted)) setNodesError(timedOut ? '节点覆盖状态读取超时，明细查询不受影响。' : errorText(cause));
    } finally {
      window.clearTimeout(timeout);
      if (nodesPending.current === controller) setNodesLoading(false);
    }
  };

  useEffect(() => {
    void loadNodes();
    void load(draft);
    return () => {
      pending.current?.abort(); pending.current = null;
      summaryPending.current?.abort(); summaryPending.current = null;
      nodesPending.current?.abort(); nodesPending.current = null;
    };
  }, []);

  const shown = nodes.filter((node) => node.supported && node.show === 1);
  const auto = shown.filter((node) => node.auto_capable === true);
  const searched = applied ? JSON.stringify(applied) === JSON.stringify(draft) : false;
  const cursorPaging = page && (typeof page.has_more === 'boolean' || page.total_exact === false);
  const hasNext = page ? cursorPaging ? page.has_more === true && !!page.next_cursor :
    (navigation.index + 1) * page.page_size < (page.total || 0) : false;
  const changePage = (index: number) => {
    if (!applied || !page || loading || index < 0) return;
    const cursors = index > navigation.index ? [...navigation.cursors.slice(0, index), page.next_cursor || ''] : navigation.cursors;
    void load(applied, { ...navigation, index, cursors });
  };
  const focusTarget = (target: string) => {
    const next = { ...draft, target };
    setDraft(next); void load(next);
  };

  return <div className="legacy-page access-statistics-page">
    <div className="content-heading">访问统计</div>
    <Typography.Paragraph type="secondary" className="access-statistics-intro">查询用户经节点发起的代理路由目标。新版 v2node 使用原节点 Key 自动上报所有已鉴权用户；可查最近 7 天；近期日志实时查询，超过 12 小时的日志自动无损压缩，仍保留每条记录与原始时间。</Typography.Paragraph>
    <Alert showIcon type="info" message="记录的是路由尝试，不是网站访问成功" description="只保存可见的目标域名或 IP、时间、用户及节点，不保存完整网址、网页内容。没有记录也不能证明用户没有访问；旧版或未上报的节点不在统计范围内。" />

    <Card title="筛选记录" size="small">
      <Form layout="vertical" onFinish={() => void load(draft)}>
        <Row gutter={[12, 0]}>
          <Col xs={24} md={12} xl={6}><Form.Item label="用户邮箱或 ID"><Input aria-label="用户邮箱或 ID" value={draft.user} maxLength={254} allowClear placeholder="例如 user@example.com 或 12345" onChange={(event) => setDraft((previous) => ({ ...previous, user: event.target.value }))} /></Form.Item></Col>
          <Col xs={24} md={12} xl={6}><Form.Item label="目标域名或 IP"><Input aria-label="目标域名或 IP" value={draft.target} maxLength={255} allowClear placeholder="例如 example.com 或 1.2.3.4" onChange={(event) => setDraft((previous) => ({ ...previous, target: event.target.value }))} /></Form.Item></Col>
          <Col xs={24} md={8} xl={4}><Form.Item label="节点"><Select aria-label="节点" allowClear placeholder="全部节点" value={draft.node_id} options={nodes.filter((node) => node.supported).map((node) => ({ value: node.node_id, label: `${node.name || `#${node.node_id}`} · #${node.node_id}` }))} onChange={(node_id) => setDraft((previous) => ({ ...previous, node_id }))} /></Form.Item></Col>
          <Col xs={24} md={16} xl={8}><Form.Item label="时间范围（精确到分）"><DatePicker.RangePicker aria-label="时间范围" style={{ width: '100%' }} showTime={{ format: 'HH:mm' }} allowClear={false} format="YYYY-MM-DD HH:mm" value={[dayjs.unix(draft.from), dayjs.unix(draft.to)]} onChange={(range) => { if (range?.[0] && range[1]) setDraft((previous) => ({ ...previous, from: range[0]!.unix(), to: range[1]!.unix() })); }} /></Form.Item></Col>
        </Row>
        <Space wrap><Button type="primary" htmlType="submit" loading={loading} icon={<SearchOutlined />}>查询</Button><Button onClick={() => { const next = initialFilter(); setDraft(next); void load(next); }}>最近 15 分钟</Button><Typography.Text type="secondary">默认只读取近期明细；汇总按需计算，最多查询 7 天。</Typography.Text></Space>
      </Form>
    </Card>

    {nodesError && <Alert showIcon type="warning" message="节点覆盖状态暂时无法读取" description={nodesError} />}
    <Card size="small" title="节点覆盖" extra={<Button size="small" loading={nodesLoading} icon={<ReloadOutlined />} onClick={() => void loadNodes()}>刷新</Button>}>
      <Typography.Paragraph type="secondary">用户可见且受支持的节点中，{nodesLoading ? '正在读取上报状态…' : `最近确认启用自动上报 ${auto.length} / ${shown.length}。`}新版本节点无需逐台配置白名单或独立上报令牌；配置心跳只表示节点能力与启用状态，实际记录以最近上报时间为准。</Typography.Paragraph>
      <details><summary>查看各节点上报状态</summary><Table<RouteNode> size="small" rowKey={(node) => `${node.node_type}:${node.node_id}`} dataSource={nodes} scroll={{ x: 760 }} pagination={{ pageSize: 10, showSizeChanger: true }} columns={[
        { title: '节点', width: 280, render: (_, node) => <><Typography.Text>{node.name || `#${node.node_id}`}</Typography.Text><br /><Typography.Text type="secondary">{node.node_type} #{node.node_id} · {node.protocol}</Typography.Text></> },
        { title: '订阅', width: 90, render: (_, node) => node.show === 1 ? '显示' : '隐藏' },
        { title: '上报能力', width: 160, render: (_, node) => !node.supported ? <Tag>协议不支持</Tag> : node.configured && !node.enabled ? <Tag color="orange">已显式关闭</Tag> : node.auto_capable ? <Tag color="green">新版自动上报</Tag> : node.capability === 'all-v1' ? <Tag color="orange">旧版，待升级</Tag> : <Tag>版本未确认</Tag> },
        { title: '最近节点请求', dataIndex: 'last_seen_at', width: 180, render: timeText },
        { title: '最近路由记录', dataIndex: 'last_event_at', width: 180, render: timeText },
      ]} locale={{ emptyText: '暂无可展示的节点' }} /></details>
    </Card>

    {error && <Alert showIcon type="error" message={error} description={page ? '下方仍为上一次成功查询的结果。' : undefined} />}
    {!searched && page && <Alert showIcon type="warning" message="筛选条件已修改，请重新查询；下方仍是上次结果。" />}
    {page?.warnings?.map((warning, index) => <Alert key={index} showIcon type="warning" message={warning} />)}
    <Card size="small" title="当前范围汇总" extra={<Button loading={summaryLoading} disabled={!page || !applied || loading || !searched} onClick={() => applied && void loadSummary(applied)}>计算当前范围统计</Button>}>
      <Typography.Text type="secondary">汇总与明细独立加载，翻页不会重复计算。{summaryLoading ? '正在计算，可继续查看和翻页明细。' : !summary ? '需要总次数、去重用户或常见目标时再计算。' : ''}</Typography.Text>
      {summaryError && <Alert className="access-statistics-summary-error" showIcon type="warning" message="汇总暂时无法计算" description={`${summaryError}${summary ? ' 当前保留上一次成功汇总。' : ' 尚未取得汇总数据，不表示为 0。'}`} />}
    </Card>
    {summary && <Row gutter={[12, 12]}>
      <Col xs={12} md={6}><Card size="small"><Statistic title="路由尝试" value={summary.events} /></Card></Col>
      <Col xs={12} md={6}><Card size="small"><Statistic title="去重用户" value={summary.users} /></Card></Col>
      <Col xs={12} md={6}><Card size="small"><Statistic title="可识别域名" value={summary.known_domain_events} /></Card></Col>
      <Col xs={12} md={6}><Card size="small"><Statistic title="域名未知" value={summary.unknown_domain_events} /></Card></Col>
    </Row>}
    {summary && <Row gutter={[12, 12]}>
      <Col xs={24} lg={12}><Card size="small" title="常见目标域名"><Table size="small" rowKey="domain" dataSource={summary.top_domains} pagination={false} scroll={{ y: 360 }} columns={[{ title: '域名', dataIndex: 'domain', render: (value: string) => <Button type="link" className="access-statistics-target" onClick={() => focusTarget(value)}>{value}</Button> }, { title: '用户', dataIndex: 'users', width: 74 }, { title: '次数', dataIndex: 'events', width: 74 }]} locale={{ emptyText: '当前条件下没有可识别域名' }} /></Card></Col>
      <Col xs={24} lg={12}><Card size="small" title="常见目标 IP"><Table size="small" rowKey="ip" dataSource={summary.top_ips} pagination={false} scroll={{ y: 360 }} columns={[{ title: 'IP', dataIndex: 'ip', render: (value: string) => <Button type="link" className="access-statistics-target" onClick={() => focusTarget(value)}>{value}</Button> }, { title: '用户', dataIndex: 'users', width: 74 }, { title: '次数', dataIndex: 'events', width: 74 }]} locale={{ emptyText: '当前条件下没有目标 IP' }} /></Card></Col>
    </Row>}

    <Card size="small" title={`路由明细${page && !cursorPaging && Number.isFinite(page.total) ? ` · ${countText(page.total)} 条` : ''}`}>
      <Table<RouteEvent> size="small" rowKey={(row) => `${row.node_type}:${row.node_id}:${row.event_id || row.id}`} loading={loading} dataSource={page?.items || []} scroll={{ x: 1050 }} columns={[
        { title: '时间', dataIndex: 'occurred_at', width: 175, render: timeText },
        { title: '用户', width: 230, render: (_, row) => <><Typography.Text copyable>{row.email || `#${row.user_id}`}</Typography.Text><br /><Typography.Text type="secondary">#{row.user_id}</Typography.Text></> },
        { title: '节点', width: 130, render: (_, row) => `${row.node_type} #${row.node_id}` },
        { title: '客户端 IP', dataIndex: 'client_ip', width: 155, render: (value: string) => value || '未记录' },
        { title: '目标', width: 255, render: (_, row) => <><Typography.Text copyable={!!row.target_host}>{row.target_host || '域名未知'}</Typography.Text><br /><Typography.Text type="secondary">{row.target_ip || 'IP 未知'}:{row.target_port || '—'}</Typography.Text></> },
        { title: '网络', dataIndex: 'network', width: 80, render: (value: string) => value || '—' },
        { title: '结果', dataIndex: 'result', width: 100, render: (value: string) => value === 'route_attempt' ? <Tag>路由尝试</Tag> : value === 'failed' ? <Tag color="red">失败</Tag> : <Tag>{value || '未知'}</Tag> },
      ]} pagination={false} locale={{ emptyText: loading ? '正在读取记录…' : applied ? '当前范围暂无记录；请核对节点版本和最近上报状态。' : '请查询访问记录。' }} />
      {page && applied && <div className="access-statistics-pagination">
        <Typography.Text type="secondary">第 {navigation.index + 1} 页 · 本页 {countText(page.items.length)} 条{cursorPaging ? ' · 游标翻页，不扫描全量计数' : ''}</Typography.Text>
        <Space wrap>
          <Select aria-label="每页条数" value={navigation.pageSize} disabled={loading} options={[20, 50, 100].map((value) => ({ value, label: `${value} 条/页` }))} onChange={(pageSize) => void load(applied, firstPage(pageSize))} />
          <Button disabled={loading || navigation.index === 0} onClick={() => changePage(navigation.index - 1)}>上一页</Button>
          <Button disabled={loading || !hasNext} onClick={() => changePage(navigation.index + 1)}>下一页</Button>
        </Space>
      </div>}
    </Card>
  </div>;
}
