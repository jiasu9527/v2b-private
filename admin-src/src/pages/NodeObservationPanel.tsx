import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Col, DatePicker, Form, Input, InputNumber, Modal, Row, Select, Space, Switch, Table, Tag, Typography, message } from 'antd';
import { SearchOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { apiGet, apiJsonPost, unwrapData } from '../lib/api';

type ObservationEvent = {
  id: number;
  event_id: string;
  node_type: string;
  node_id: number;
  user_id: number;
  email?: string;
  occurred_at: number;
  client_ip: string;
  target_host: string;
  target_ip: string;
  target_port: number;
  network: string;
  result: string;
  uplink_bytes: number;
  downlink_bytes: number;
};

type ObservationPage = { items: ObservationEvent[]; total: number; page: number; page_size: number; keep_days?: number; warnings?: string[] };
type ObservationFilter = { node_type: string; node_id?: number; user_id?: number; from: number; to: number };
type ObservationSummary = { events: number; users: number; known_domain_events: number; unknown_domain_events: number; top_domains: { domain: string; users: number; events: number }[]; top_ips: { ip: string; users: number; events: number }[] };
type ObservationMode = 'all' | 'allowlist';
type ObservationNode = { node_type: string; node_id: number; name: string; protocol: string; show: number; supported: boolean; configured: boolean; enabled: boolean; mode?: ObservationMode; allowed_user_ids: number[]; last_event_at: number };
type ObservationEditor = { node_id?: number; enabled: boolean; mode: ObservationMode; allowed_user_ids: string; existing?: boolean };

const ENDPOINT = '/observation';
const initialFilter = (): ObservationFilter => ({ node_type: '', from: dayjs().subtract(1, 'day').unix(), to: dayjs().unix() });
const timeText = (value: number) => value > 0 ? dayjs.unix(value).format('YYYY-MM-DD HH:mm:ss') : '—';
const bytesText = (value: number) => {
  if (!Number.isFinite(value) || value < 0) return '—';
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`;
  return `${(value / (1024 * 1024)).toFixed(1)} MB`;
};
const parseUserIDs = (value: string): number[] => {
  const parts = value.trim().split(/[\s,，;；]+/).filter(Boolean);
  if (parts.length > 2000 || parts.some((part) => !/^[1-9]\d*$/.test(part) || !Number.isSafeInteger(Number(part)))) throw new Error('用户 ID 只能是正整数，最多 2000 个');
  return [...new Set(parts.map(Number))];
};

export default function NodeObservationPanel() {
  const [draft, setDraft] = useState<ObservationFilter>(initialFilter);
  const [applied, setApplied] = useState<ObservationFilter | null>(null);
  const [page, setPage] = useState<ObservationPage | null>(null);
  const [summary, setSummary] = useState<ObservationSummary | null>(null);
  const [nodes, setNodes] = useState<ObservationNode[] | null>(null);
  const [nodesError, setNodesError] = useState(false);
  const [editor, setEditor] = useState<ObservationEditor | null>(null);
  const [saving, setSaving] = useState(false);
  const [configError, setConfigError] = useState('');
  const [issuedToken, setIssuedToken] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const pending = useRef<AbortController | null>(null);
  const supportedNodes = (nodes || []).filter((node) => node.supported);
  const allModeNodes = supportedNodes.filter((node) => node.configured && node.enabled && node.mode === 'all');
  const visibleSupportedNodes = supportedNodes.filter((node) => node.show === 1);
  const visibleAllModeNodes = visibleSupportedNodes.filter((node) => node.configured && node.enabled && node.mode === 'all');

  const refreshNodes = async (signal?: AbortSignal) => {
    try {
      const response = await apiGet(`${ENDPOINT}/nodes`, {}, { cache: 'no-store', signal });
      const result = unwrapData(response);
      if (!Array.isArray(result)) throw new Error('节点配置格式无效');
      if (!signal?.aborted) { setNodes(result as ObservationNode[]); setNodesError(false); }
    } catch {
      if (!signal?.aborted) setNodesError(true);
    }
  };

  useEffect(() => {
    const controller = new AbortController();
    void refreshNodes(controller.signal);
    return () => { controller.abort(); pending.current?.abort(); };
  }, []);

  const saveNode = async () => {
    if (!editor || saving) return;
    try {
      const nodeID = editor.node_id;
      if (!nodeID || !Number.isSafeInteger(nodeID) || nodeID < 1) throw new Error('请填写已登记的 v2node 节点 ID');
      if (!editor.existing && nodes?.some((node) => node.node_type === 'v2node' && node.node_id === nodeID)) throw new Error('该节点已配置，请从下方列表点击“编辑”');
      const allowedUserIDs = editor.mode === 'all' ? [] : parseUserIDs(editor.allowed_user_ids);
      if (editor.enabled && editor.mode === 'allowlist' && allowedUserIDs.length === 0) throw new Error('指定用户模式启用时至少选择一个用户');
      if (editor.mode === 'all' || !editor.enabled || allowedUserIDs.length === 0) {
        const confirmed = await new Promise<boolean>((resolve) => Modal.confirm({
          title: editor.mode === 'all' && editor.enabled ? '确认观察此节点的全部用户？' : allowedUserIDs.length === 0 && editor.mode === 'allowlist' ? '确认清空观察节点名单？' : '确认暂停观察采集？',
          content: editor.mode === 'all' ? editor.enabled ? '启用后会记录此节点所有已鉴权用户的代理目标元数据；其他节点需要分别配置，不能把本节点覆盖当作全站覆盖。' : '暂停采集不改变普通节点的用户鉴权，也不会自动关闭其他节点的采集。' : allowedUserIDs.length === 0 ? '指定用户模式的空名单会撤销此节点的全部用户鉴权；已有连接也将随节点刷新断开。' : '暂停后仍只允许当前名单中的用户连接，不会恢复全员访问。',
          okText: '确认保存', okButtonProps: { danger: editor.mode === 'allowlist' && allowedUserIDs.length === 0 }, onOk: () => resolve(true), onCancel: () => resolve(false),
        }));
        if (!confirmed) return;
      }
      setSaving(true); setConfigError('');
      const response = await apiJsonPost(`${ENDPOINT}/nodes`, { node_type: 'v2node', node_id: nodeID, enabled: editor.enabled, mode: editor.mode, allowed_user_ids: allowedUserIDs });
      const result = unwrapData(response) as { token?: string };
      if (!result?.token || !/^[a-f0-9]{64}$/.test(result.token)) throw new Error('保存结果缺少独立令牌，请暂停采集并联系管理员');
      setEditor(null);
      setIssuedToken(result.token);
      void refreshNodes();
      message.success('观察节点配置已保存');
    } catch (cause) { setConfigError(cause instanceof Error ? cause.message : '保存观察节点失败'); }
    finally { setSaving(false); }
  };

  const search = async (filter: ObservationFilter, current = 1, pageSize = 20) => {
    if (filter.to <= filter.from) { setError('结束时间必须晚于开始时间'); return; }
    if (filter.to - filter.from > 7 * 86400) { setError('单次查询最多 7 天，请缩小时间范围'); return; }
    pending.current?.abort();
    const controller = new AbortController(); pending.current = controller;
    setLoading(true); setError('');
    try {
      const params = { ...filter, page: current, page_size: pageSize };
      const [events, counts] = await Promise.all([
        apiGet(`${ENDPOINT}/events`, params, { cache: 'no-store', signal: controller.signal }),
        apiGet(`${ENDPOINT}/summary`, filter, { cache: 'no-store', signal: controller.signal }),
      ]);
      const result = unwrapData(events) as ObservationPage;
      const nextSummary = unwrapData(counts) as ObservationSummary;
      if (!result || !Array.isArray(result.items) || !Number.isFinite(result.total)) throw new Error('节点观察数据格式无效，请更新后端');
      if (!nextSummary || !Number.isFinite(nextSummary.events) || !Array.isArray(nextSummary.top_domains) || !Array.isArray(nextSummary.top_ips)) throw new Error('节点观察汇总格式无效，请更新后端');
      if (controller.signal.aborted) return;
      setPage(result); setSummary(nextSummary); setApplied({ ...filter });
    } catch (cause) {
      if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : '查询节点观察记录失败');
    } finally { if (!controller.signal.aborted) setLoading(false); }
  };

  return <Space direction="vertical" size="middle" style={{ width: '100%' }}>
    <Alert showIcon type="info" message="节点目标观察" description="可选择观察单个节点的全部已鉴权用户，或仅观察指定用户；全站覆盖需要逐个配置并升级所有受支持节点。当前记录的是代理路由尝试，不代表目标网站已连接成功或网页已打开。没有记录不等于用户没有访问。不记录网页正文或完整网址。" />
    {nodesError && <Alert showIcon type="warning" message="观察节点配置暂时无法读取" description="无法据此判断节点是否已经启用；请检查后台接口后重试。" />}
    {nodes && !nodes.some((node) => node.enabled) && <Alert showIcon type="warning" message="尚未启用节点观察" description="仅提供查询页面不会自动采集；请先配置已升级的 Xray-backed v2node，并通过测试账号验证上报。" />}
    <Card size="small" title="节点采集配置" extra={<Button onClick={() => void refreshNodes()}>刷新节点</Button>}>
      <Typography.Paragraph type="secondary">“全部用户”只覆盖该节点上成功鉴权并发起路由的用户。所有节点须分别配置并部署支持采集的 v2node；外部协议节点不在本功能覆盖范围。保存时会轮换独立上报令牌，必须同步更新节点本地 ObservationToken。</Typography.Paragraph>
      {nodes && <Alert type={visibleAllModeNodes.length === visibleSupportedNodes.length && visibleSupportedNodes.length > 0 ? 'info' : 'warning'} showIcon style={{ marginBottom: 12 }} message={`用户可见的受支持节点：已配置全员采集 ${visibleAllModeNodes.length} / ${visibleSupportedNodes.length}；全部受支持节点 ${allModeNodes.length} / ${supportedNodes.length}`} description="这是后台配置数，不代表节点已升级、正在上报或覆盖了所有用户；请逐个核对最近路由记录。隐藏节点也可能仍被旧客户端使用。" />}
      <Table<ObservationNode> size="small" rowKey={(node) => `${node.node_type}:${node.node_id}`} pagination={{ pageSize: 20, showSizeChanger: true }} dataSource={nodes || []} loading={nodes === null && !nodesError} scroll={{ x: 1050 }} columns={[
        { title: '节点', width: 270, render: (_, node) => <><Typography.Text>{node.name || `#${node.node_id}`}</Typography.Text><br /><Typography.Text type="secondary">v2node #{node.node_id} · {node.protocol}</Typography.Text></> },
        { title: '订阅显示', width: 110, render: (_, node) => node.show === 1 ? '显示' : '隐藏' },
        { title: '采集状态', width: 140, render: (_, node) => !node.supported ? <Tag>协议不支持</Tag> : !node.configured ? <Tag>未配置</Tag> : <Tag color={node.enabled ? 'green' : 'default'}>{node.enabled ? '已启用' : '已暂停'}</Tag> },
        { title: '采集范围', width: 150, render: (_, node) => !node.configured ? '—' : node.mode === 'all' ? <Tag color="blue">该节点全部用户</Tag> : `指定 ${node.allowed_user_ids?.length || 0} 人` },
        { title: '最近路由记录', width: 180, render: (_, node) => timeText(node.last_event_at) },
        { title: '操作', width: 95, render: (_, node) => <Button size="small" disabled={!node.supported} onClick={() => { setConfigError(''); setEditor({ node_id: node.node_id, enabled: node.enabled, mode: node.mode === 'all' ? 'all' : node.configured ? 'allowlist' : 'all', allowed_user_ids: (node.allowed_user_ids || []).join(', '), existing: true }); }}>{node.configured ? '编辑' : '配置'}</Button> },
      ]} locale={{ emptyText: '暂无可配置的 v2node 节点' }} />
    </Card>
    <Card size="small" title="筛选观察记录">
      <Form layout="vertical" onFinish={() => void search(draft)}>
        <Row gutter={[12, 0]}>
          <Col xs={24} sm={12} lg={5}><Form.Item label="节点协议"><Select value={draft.node_type || undefined} allowClear placeholder="全部协议" options={[{ value: 'v2node', label: 'v2node' }]} onChange={(node_type) => setDraft((old) => ({ ...old, node_type: node_type || '' }))} /></Form.Item></Col>
          <Col xs={12} sm={6} lg={3}><Form.Item label="观察节点"><Select allowClear value={draft.node_id} placeholder="全部已配置节点" options={(nodes || []).map((node) => ({ value: node.node_id, label: `${node.node_type} #${node.node_id}${node.enabled ? ' · 已启用' : ' · 未启用'}` }))} onChange={(node_id) => setDraft((old) => ({ ...old, node_type: node_id ? 'v2node' : old.node_type, node_id: node_id || undefined }))} /></Form.Item></Col>
          <Col xs={12} sm={6} lg={3}><Form.Item label="用户 ID"><InputNumber style={{ width: '100%' }} min={1} precision={0} value={draft.user_id} placeholder="不限" onChange={(user_id) => setDraft((old) => ({ ...old, user_id: user_id || undefined }))} /></Form.Item></Col>
          <Col xs={24} lg={13}><Form.Item label="路由时间范围（本地时区）"><DatePicker.RangePicker style={{ width: '100%' }} showTime allowClear={false} format="YYYY-MM-DD HH:mm" value={[dayjs.unix(draft.from), dayjs.unix(draft.to)]} onChange={(range) => { if (range?.[0] && range[1]) setDraft((old) => ({ ...old, from: range[0]!.unix(), to: range[1]!.unix() })); }} /></Form.Item></Col>
        </Row>
        <Button htmlType="submit" type="primary" loading={loading} icon={<SearchOutlined />}>查询</Button>
      </Form>
    </Card>
    {error && <Alert showIcon type="error" message={error} description={page ? '下表仍是上次成功查询的结果。' : undefined} />}
    {page?.warnings?.map((warning, index) => <Alert key={index} showIcon type="warning" message={warning} />)}
    {page && <Typography.Text type="secondary">当前筛选共 {page.total} 条路由事件 · 日志默认保留 7 天。目标域名未知时只展示目标 IP，不能据此认为没有访问网站。</Typography.Text>}
    {summary && <Card size="small" title="观察概况"><Space wrap><Tag>路由尝试 {summary.events}</Tag><Tag>去重用户 {summary.users}</Tag><Tag color="green">识别域名 {summary.known_domain_events}</Tag><Tag color="orange">域名未知 {summary.unknown_domain_events}</Tag></Space><Typography.Paragraph type="secondary" style={{ margin: '12px 0 0' }}>已上报路由尝试中可识别域名的比例 {summary.events ? ((summary.known_domain_events / summary.events) * 100).toFixed(1) : '—'}%。这不是采集完整率，也不是网站访问成功率；暂未配置正常对照组，不能从共同域名或共同 IP 直接判断账号关联。共享 CDN、测速和系统请求都可能造成相同目标。</Typography.Paragraph><Row gutter={[12, 12]}><Col xs={24} lg={12}><Table size="small" rowKey="domain" pagination={false} dataSource={summary.top_domains} columns={[{ title: '尝试目标域名（最多 30 个）', dataIndex: 'domain', render: (domain: string) => <Typography.Text copyable>{domain}</Typography.Text> }, { title: '去重用户', dataIndex: 'users', width: 110 }, { title: '尝试次数', dataIndex: 'events', width: 90 }]} locale={{ emptyText: '当前范围无可识别的目标域名' }} /></Col><Col xs={24} lg={12}><Table size="small" rowKey="ip" pagination={false} dataSource={summary.top_ips} columns={[{ title: '尝试目标 IP（最多 30 个）', dataIndex: 'ip', render: (ip: string) => <Typography.Text copyable>{ip}</Typography.Text> }, { title: '去重用户', dataIndex: 'users', width: 110 }, { title: '尝试次数', dataIndex: 'events', width: 90 }]} locale={{ emptyText: '当前范围无可识别的目标 IP' }} /></Col></Row></Card>}
    <Table<ObservationEvent> rowKey={(row) => `${row.node_type}:${row.node_id}:${row.event_id || row.id}`} size="small" loading={loading} dataSource={page?.items || []} scroll={{ x: 1300 }} columns={[
      { title: '路由时间', dataIndex: 'occurred_at', width: 180, render: timeText },
      { title: '用户', width: 240, render: (_, row) => <><Typography.Text copyable>{row.email || `#${row.user_id}`}</Typography.Text><br /><Typography.Text type="secondary">#{row.user_id}</Typography.Text></> },
      { title: '节点', width: 145, render: (_, row) => `${row.node_type} #${row.node_id}` },
      { title: '来源 IP', dataIndex: 'client_ip', width: 160, render: (value: string) => value || '未记录' },
      { title: '目标', width: 290, render: (_, row) => <>{row.target_host ? <Typography.Text copyable>{row.target_host}</Typography.Text> : <Tag color="default">域名未知</Tag>}<br /><Typography.Text type="secondary">{row.target_ip || 'IP 未知'}:{row.target_port || '—'}</Typography.Text></> },
      { title: '网络 / 结果', width: 150, render: (_, row) => <>{row.network || '—'}<br /><Tag color={row.result === 'failed' ? 'red' : 'default'}>{row.result === 'route_attempt' ? '路由尝试' : row.result === 'failed' ? '失败' : '未知'}</Tag></> },
      { title: '上行 / 下行', width: 165, render: (_, row) => row.result === 'route_attempt' ? '未采集' : `${bytesText(row.uplink_bytes)} / ${bytesText(row.downlink_bytes)}` },
    ]} pagination={page && applied ? { current: page.page, pageSize: page.page_size, total: page.total, showSizeChanger: true, onChange: (current, size) => void search(applied, current, size) } : false} locale={{ emptyText: applied ? '当前范围暂无上报记录；先核对观察节点是否启用及采集覆盖率。' : '指定时间范围并查询专用观察节点。' }} />
    <Modal title="配置节点观察" open={!!editor} onCancel={() => { if (!saving) { setEditor(null); setConfigError(''); } }} onOk={() => void saveNode()} confirmLoading={saving} okText="保存并生成新令牌" destroyOnHidden>
      <Alert showIcon type="warning" message="每次保存都会轮换独立上报令牌" description="复制保存后显示的新令牌，立即更新对应节点本地 ObservationToken；否则该节点无法上报。未完成节点端更新前，不要向真实用户下发入口。" style={{ marginBottom: 16 }} />
      {configError && <Alert showIcon type="error" message={configError} style={{ marginBottom: 16 }} />}
      <Form layout="vertical"><Form.Item label="v2node 节点 ID"><InputNumber style={{ width: '100%' }} min={1} precision={0} value={editor?.node_id} onChange={(node_id) => setEditor((old) => old && { ...old, node_id: node_id || undefined })} disabled={!!editor?.existing} /></Form.Item>
        <Form.Item label="采集范围"><Select value={editor?.mode} options={[{ value: 'all', label: '该节点全部已鉴权用户' }, { value: 'allowlist', label: '仅指定用户' }]} onChange={(mode: ObservationMode) => setEditor((old) => old && { ...old, mode })} /></Form.Item>
        {editor?.mode === 'allowlist' && <Form.Item label="允许用户 ID（逗号、空格或换行分隔）"><Input.TextArea rows={4} value={editor.allowed_user_ids} onChange={(event) => setEditor((old) => old && { ...old, allowed_user_ids: event.target.value })} placeholder="例如：1001, 1002；留空并暂停可撤销全部用户" /></Form.Item>}
        <Form.Item label="采集路由尝试"><Switch checked={!!editor?.enabled} onChange={(enabled) => setEditor((old) => old && { ...old, enabled })} checkedChildren="启用" unCheckedChildren="暂停" /></Form.Item>
      </Form>
    </Modal>
    <Modal title="独立上报令牌（仅显示一次）" open={!!issuedToken} footer={<Button type="primary" onClick={() => setIssuedToken('')}>已复制并关闭</Button>} onCancel={() => setIssuedToken('')} destroyOnHidden>
      <Alert showIcon type="warning" message="关闭后无法再次查看" description="请复制到对应节点本地配置 ObservationToken。不要放在节点订阅、截图或聊天记录中。" style={{ marginBottom: 16 }} />
      <Typography.Paragraph copyable={{ text: issuedToken }}>{issuedToken}</Typography.Paragraph>
    </Modal>
  </Space>;
}
