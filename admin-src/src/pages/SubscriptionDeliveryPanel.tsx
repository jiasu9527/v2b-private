import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Col, Collapse, DatePicker, Form, Input, InputNumber, Row, Select, Space, Statistic, Table, Tag, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { SearchOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { apiGet, unwrapData } from '../lib/api';
import {
  DELIVERY_PROTOCOLS, SUBSCRIPTION_DELIVERY_PATH, deliveryFilterError, deliveryFilterKey, deliveryPage, deliveryParams,
  deliveryStatistics, deliveryTime, deliveryVersionFilters, deliveryVersionKey, initialDeliveryFilters, normalizedDeliveryFilters,
  type DeliveryFilters, type DeliveryPage, type DeliveryStatistics, type DeliveryUser, type DeliveryVersion,
} from './subscriptionDelivery';

type DeliveryResult = { filters: DeliveryFilters; statistics: DeliveryStatistics; users: DeliveryPage<DeliveryUser>; versions: DeliveryPage<DeliveryVersion> };
type VersionResult = { version: DeliveryVersion; filters: DeliveryFilters; statistics: DeliveryStatistics; users: DeliveryPage<DeliveryUser> };

export default function SubscriptionDeliveryPanel() {
  const [draft, setDraft] = useState<DeliveryFilters>(initialDeliveryFilters);
  const [result, setResult] = useState<DeliveryResult | null>(null);
  const [selected, setSelected] = useState<VersionResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const sequence = useRef(0);
  const pending = useRef<AbortController | null>(null);
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || '浏览器本地时区';
  const dirty = result && deliveryFilterKey(draft) !== deliveryFilterKey(result.filters);
  const active = selected || result;
  const patch = (value: Partial<DeliveryFilters>) => setDraft((previous) => ({ ...previous, ...value }));
  useEffect(() => () => { sequence.current += 1; pending.current?.abort(); }, []);

  async function perform<T,>(fetchResult: (signal: AbortSignal) => Promise<T>, apply: (value: T) => void) {
    const ticket = ++sequence.current;
    pending.current?.abort();
    const controller = new AbortController();
    pending.current = controller;
    let timedOut = false;
    const timer = window.setTimeout(() => { timedOut = true; controller.abort(); }, 60000);
    setLoading(true);
    setError('');
    try {
      const value = await fetchResult(controller.signal);
      if (ticket !== sequence.current) return;
      apply(value);
    } catch (e: unknown) {
      controller.abort();
      if (ticket === sequence.current) setError(timedOut ? '查询超过 60 秒，请缩小时间范围或增加入口条件后重试。' : e instanceof Error ? e.message : '下发查询失败，请重试');
    } finally {
      clearTimeout(timer);
      if (ticket === sequence.current) { setLoading(false); pending.current = null; }
    }
  }
  const get = async (name: string, filters: DeliveryFilters, signal: AbortSignal, current = 1, pageSize = 20) => unwrapData(await apiGet(SUBSCRIPTION_DELIVERY_PATH + name, deliveryParams(filters, current, pageSize), { signal, cache: 'no-store' }));
  const loadUsersAndStats = async (filters: DeliveryFilters, signal: AbortSignal) => {
    const [statistics, users] = await Promise.all([get('delivery-stats', filters, signal), get('delivery-users', filters, signal)]);
    return { statistics: deliveryStatistics(statistics), users: deliveryPage<DeliveryUser>(users) };
  };
  const search = () => {
    const validation = deliveryFilterError(draft);
    if (validation) { setError(validation); return; }
    const filters = normalizedDeliveryFilters(draft);
    void perform(async (signal) => {
      const [read, versions] = await Promise.all([loadUsersAndStats(filters, signal), get('delivery-versions', filters, signal)]);
      return { filters, ...read, versions: deliveryPage<DeliveryVersion>(versions) };
    }, (value) => { setResult(value); setSelected(null); });
  };
  const chooseVersion = (version: DeliveryVersion) => {
    if (!result) return;
    let filters: DeliveryFilters;
    try { filters = deliveryVersionFilters(result.filters, version); } catch (e) { setError((e as Error).message); return; }
    void perform(async (signal) => ({ version, filters, ...await loadUsersAndStats(filters, signal) }), setSelected);
  };
  const pageUsers = (current: number, pageSize: number) => {
    if (!active) return;
    const target = active;
    void perform(async (signal) => deliveryPage<DeliveryUser>(await get('delivery-users', target.filters, signal, current, pageSize)), (users) => {
      if (selected) setSelected((previous) => previous ? { ...previous, users } : previous);
      else setResult((previous) => previous ? { ...previous, users } : previous);
    });
  };
  const pageVersions = (current: number, pageSize: number) => {
    if (!result) return;
    void perform(async (signal) => deliveryPage<DeliveryVersion>(await get('delivery-versions', result.filters, signal, current, pageSize)), (versions) => setResult((previous) => previous ? { ...previous, versions } : previous));
  };
  const reset = () => {
    sequence.current += 1;
    pending.current?.abort();
    pending.current = null;
    setLoading(false); setError(''); setResult(null); setSelected(null); setDraft(initialDeliveryFilters());
  };
  const userColumns: ColumnsType<DeliveryUser> = [
    { title: '用户 ID', dataIndex: 'user_id', width: 100 },
    { title: '下发时邮箱', dataIndex: 'email_at_event', width: 250, render: (value: string) => value ? <Typography.Text copyable style={{ overflowWrap: 'anywhere' }}>{value}</Typography.Text> : '未记录' },
    { title: '下发请求数', dataIndex: 'requests', width: 110 },
    { title: '首次记录', dataIndex: 'first_at', width: 180, render: deliveryTime },
    { title: '最近记录', dataIndex: 'last_at', width: 180, render: deliveryTime },
  ];
  const versionColumns: ColumnsType<DeliveryVersion> = [
    { title: '下发入口', width: 245, render: (_, row) => <div><Typography.Text style={{ overflowWrap: 'anywhere' }}>{row.delivered_host}:{row.port}</Typography.Text>{row.source_host && row.source_host !== row.delivered_host && <div><Typography.Text type="secondary">原始：{row.source_host}</Typography.Text></div>}</div> },
    { title: '历史归属', width: 170, render: (_, row) => <Space direction="vertical" size={0}><span>规则 {row.policy_id || '未记录'}／叶子 {row.group_id || '未记录'}</span><Typography.Text type="secondary">合集 {row.collection_id || '未记录'}{row.provider_id > 0 ? `／Provider ${row.provider_id}` : ''}</Typography.Text></Space> },
    { title: '下发快照版本', width: 175, render: (_, row) => row.rule_revision ? <Typography.Text copyable={{ text: row.rule_revision }} ellipsis={{ tooltip: row.rule_revision }} style={{ maxWidth: 150 }}>{row.rule_revision}</Typography.Text> : <Tag>版本未知</Tag> },
    { title: '去重用户', dataIndex: 'users', width: 105 },
    { title: '请求数', dataIndex: 'requests', width: 100 },
    { title: '最早／最近记录', width: 200, render: (_, row) => <Space direction="vertical" size={0}><span>{deliveryTime(row.first_at)}</span><span>{deliveryTime(row.last_at)}</span></Space> },
    { title: '明细', width: 130, render: (_, row) => <Button size="small" disabled={loading || !/^[a-f0-9]{64}$/.test(row.rule_revision || '') || !/^[a-f0-9]{32}$/.test(row.version || '')} onClick={() => chooseVersion(row)}>按此版本查看</Button> },
  ];
  const warnings = Array.from(new Set([...(active?.statistics.warnings || []), ...(active?.users.warnings || []), ...(result?.versions.warnings || [])].filter((value) => typeof value === 'string')));
  const filterLabels: Record<string, string> = { host: '入口', port: '端口', account: '账号', ua: 'UA 包含', from: '开始', to: '结束', policy_id: '规则 ID', group_id: '叶子 ID', collection_id: '合集 ID', server_type: '节点协议', server_id: '节点 ID', rule_revision: '快照版本', version: '精确快照' };
  const filtersSummary = result ? Object.entries(deliveryParams(result.filters)).filter(([key]) => !['current', 'page_size'].includes(key)).map(([key, value]) => `${filterLabels[key] || key}：${key === 'from' || key === 'to' ? deliveryTime(Number(value)) : value}`).join('；') : '';

  return <Collapse className="mt-4" items={[{ key: 'delivery', label: '实际下发统计 · 历史快照与用户明细', children: <Space direction="vertical" size="middle" style={{ width: '100%' }}>
    <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>这里使用订阅最终生成并完成服务端写入的历史记录，不按当前规则反推。人数按用户去重，一次请求中多个节点共用入口只计一次；仅统计完整覆盖的成功响应。不能证明客户端已导入或使用。</Typography.Paragraph>
    <Form layout="vertical" onFinish={search}>
      <Row gutter={[12, 0]}>
        <Col xs={24} lg={9}><Form.Item label="实际下发的域名 / IP"><Input allowClear value={draft.host} onChange={(event) => patch({ host: event.target.value })} placeholder="仅域名或 IP；留空不限" /></Form.Item></Col>
        <Col xs={24} sm={8} lg={3}><Form.Item label="下发端口"><Input allowClear value={draft.port} onChange={(event) => patch({ port: event.target.value })} placeholder="留空不限" /></Form.Item></Col>
        <Col xs={24} sm={16} lg={12}><Form.Item label={`下发完成时间（${timezone}；开始含、结束不含）`}><DatePicker.RangePicker showTime style={{ width: '100%' }} value={draft.from !== undefined && draft.to !== undefined ? [dayjs.unix(draft.from), dayjs.unix(draft.to)] : null} onChange={(values) => patch({ from: values?.[0]?.unix(), to: values?.[1]?.unix() })} presets={[{ label: '最近 24 小时', value: () => [dayjs().subtract(24, 'hour'), dayjs()] }, { label: '最近 7 天', value: () => [dayjs().subtract(7, 'day'), dayjs()] }]} /></Form.Item></Col>
        <Col xs={24} lg={12}><Form.Item label="用户 ID / 下发时邮箱（精确匹配）"><Input allowClear value={draft.account} onChange={(event) => patch({ account: event.target.value })} placeholder="数字为用户 ID；完整邮箱；留空不限" /></Form.Item></Col>
        <Col xs={24} lg={12}><Form.Item label="请求 UA 包含（忽略大小写）"><Input allowClear value={draft.ua} onChange={(event) => patch({ ua: event.target.value })} placeholder="例如 Forest、Clash" /></Form.Item></Col>
      </Row>
      <details style={{ marginBottom: 16 }}><summary style={{ cursor: 'pointer', marginBottom: 12 }}>更多历史条件：规则、叶子、合集、节点</summary><Row gutter={[12, 0]}>
        {([{ key: 'policy_id', title: '规则 ID' }, { key: 'group_id', title: '固定叶子 ID' }, { key: 'collection_id', title: '合集 ID' }] as const).map(({ key, title }) => <Col xs={24} sm={8} key={key}><Form.Item label={title}><InputNumber min={1} precision={0} style={{ width: '100%' }} value={draft[key]} onChange={(value) => patch({ [key]: value ?? undefined })} placeholder="留空不限" /></Form.Item></Col>)}
        <Col xs={24} sm={12}><Form.Item label="节点协议"><Select allowClear showSearch value={draft.server_type || undefined} onChange={(value) => patch({ server_type: value || '' })} options={DELIVERY_PROTOCOLS.map((value) => ({ value, label: value }))} placeholder="留空不限" /></Form.Item></Col>
        <Col xs={24} sm={12}><Form.Item label="节点 ID（额外节点可为负数）"><InputNumber precision={0} style={{ width: '100%' }} value={draft.server_id} onChange={(value) => patch({ server_id: value ?? undefined })} placeholder="留空不限；与节点协议组合更精确" /></Form.Item></Col>
      </Row></details>
      <Space wrap><Button htmlType="submit" type="primary" loading={loading} icon={<SearchOutlined />}>查询实际下发</Button><Button onClick={reset}>重置</Button><Button disabled={loading} onClick={() => patch({ from: undefined, to: undefined })}>全部保留期</Button></Space>
    </Form>
    {error && <Alert type="error" showIcon message={error} description={result ? '下方仍是上次成功查询的结果；本次查询未成功，不能当作零人或新条件的结果。' : undefined} />}
    {dirty && <Alert type="warning" showIcon message="条件已修改，点击查询后生效；下方统计、明细及翻页仍使用上次提交的条件。" />}
    {result && <Typography.Text type="secondary" style={{ overflowWrap: 'anywhere' }}>已提交条件：{filtersSummary || '全部保留期，不限入口'}</Typography.Text>}
    {active && <>
      {selected && <Alert type="info" showIcon message={<Space wrap><span>已选择快照 {selected.version.rule_revision.slice(0, 12)}… · {selected.version.delivered_host}:{selected.version.port}</span><Button size="small" disabled={loading} onClick={() => setSelected(null)}>返回全部匹配版本</Button></Space>} description="统计和用户列表已按选中快照筛选，仍保留原查询的时间、UA 和节点条件。下方版本表保持原查询范围。" />}
      <Row gutter={[12, 12]}><Col xs={12} lg={6}><Statistic title="实际下发去重用户" value={active.statistics.users} /></Col><Col xs={12} lg={6}><Statistic title="下发请求数" value={active.statistics.requests} /></Col><Col xs={24} lg={6}><Statistic title="首次下发记录" value={deliveryTime(active.statistics.first_at)} valueStyle={{ fontSize: 16 }} /></Col><Col xs={24} lg={6}><Statistic title="最近下发记录" value={deliveryTime(active.statistics.last_at)} valueStyle={{ fontSize: 16 }} /></Col></Row>
      <Typography.Text type="secondary">原始记录保留 {active.statistics.keep_days || '—'} 天；当前可用记录：{deliveryTime(active.statistics.available_from)} 至 {deliveryTime(active.statistics.available_to)}。这里只统计仍在保留期内的已记录下发，首次记录不等于入口启用时间，缺少记录不等于没拿到。</Typography.Text>
      {warnings.length > 0 && <details style={{ color: '#737b86', fontSize: 13 }}><summary style={{ cursor: 'pointer' }}>统计口径与记录缺口</summary><ul style={{ margin: '8px 0 0', paddingLeft: 20 }}>{warnings.map((warning, index) => <li key={index}>{warning}</li>)}</ul></details>}
      <Typography.Title level={5} style={{ margin: 0 }}>实际下发用户</Typography.Title>
      <Table size="small" rowKey="user_id" loading={loading} columns={userColumns} dataSource={active.users.items} scroll={{ x: 820 }} pagination={{ current: active.users.current, pageSize: active.users.page_size, total: active.users.total, disabled: loading, showSizeChanger: true, pageSizeOptions: [10, 20, 50, 100], showTotal: (total) => `共 ${total} 位用户` }} onChange={(page) => pageUsers(page.pageSize !== active.users.page_size ? 1 : page.current || 1, page.pageSize || 20)} locale={{ emptyText: '当前条件下没有完整成功下发记录，不代表用户从未获得过入口。' }} />
    </>}
    {result && <>
      <Typography.Title level={5} style={{ margin: 0 }}>下发快照版本</Typography.Title>
      <Typography.Text type="secondary">同一快照内用户与请求均去重；不同版本、叶子及合集可能包含同一用户，不能把各行人数直接相加。版本来自当时订阅快照，不是入口变更的完整轮次档案；版本未知的旧记录不能准确区分换 IP 前后。</Typography.Text>
      <Table size="small" rowKey={deliveryVersionKey} loading={loading} columns={versionColumns} dataSource={result.versions.items} scroll={{ x: 1125 }} pagination={{ current: result.versions.current, pageSize: result.versions.page_size, total: result.versions.total, disabled: loading, showSizeChanger: true, pageSizeOptions: [10, 20, 50, 100], showTotal: (total) => `共 ${total} 个入口快照` }} onChange={(page) => pageVersions(page.pageSize !== result.versions.page_size ? 1 : page.current || 1, page.pageSize || 20)} locale={{ emptyText: '当前条件下暂无已记录的完整下发快照。' }} />
    </>}
    {!result && <Typography.Text type="secondary">填写条件并点击查询。默认最近 24 小时、端口 54101；端口可清空。展开面板不会自动查询全部用户。</Typography.Text>}
  </Space> }]} />;
}
