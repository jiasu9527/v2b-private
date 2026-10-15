import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Alert, Button, Checkbox, Collapse, Empty, Input, Space, Table, Tag, Tooltip, Typography } from 'antd';
import { ReloadOutlined, SearchOutlined } from '@ant-design/icons';
import { supportGet } from '../lib/api';
import { type SupportHealthEntry, type SupportHealthProbe, type SupportHealthStatus, type SupportOverview, supportDate } from './supportTypes';

const statuses: Record<SupportHealthStatus, { label: string; color?: string }> = {
  healthy: { label: '正常', color: 'green' },
  partial: { label: '部分异常', color: 'orange' },
  unhealthy: { label: '异常', color: 'red' },
  stale: { label: '数据过期', color: 'gold' },
  unmonitored: { label: '未监测' },
  disabled: { label: '已停用' },
};
const priorities: Record<string, number> = { unhealthy: 0, partial: 1, stale: 2, unmonitored: 3, healthy: 4, disabled: 5 };
function statusTag(status: string, reason?: string) {
  const meta = statuses[status] || { label: '未知状态' };
  return <Tooltip title={reason}><Tag color={meta.color}>{meta.label}</Tag></Tooltip>;
}
function probeSummary(probes: SupportHealthProbe[]) {
  if (!probes?.length) return <Typography.Text type="secondary">暂无探针数据</Typography.Text>;
  return <>{probes.map((probe, i) => <Tooltip key={`${probe.probe_id}:${i}`} title={<><div>{probe.status_reason || probe.last_error || '无异常说明'}</div><div>最近上报：{supportDate(probe.last_reported_at)}</div>{probe.source_count > 1 && <div>关联 {probe.source_count} 个来源</div>}</>}><span className="support-probe-status">{statusTag(probe.status)}{probe.probe_name || `探针 #${probe.probe_id}`}{probe.status === 'healthy' && probe.last_latency_ms != null && <span className="support-muted">{probe.last_latency_ms} ms</span>}</span></Tooltip>)}</>;
}

function EntrySources({ entry }: { entry: SupportHealthEntry }) {
  const probeColumns: any[] = [
    { title: '探针', dataIndex: 'probe_name', render: (value: string, row: SupportHealthProbe) => value || `#${row.probe_id}` },
    { title: '状态', dataIndex: 'status', render: (value: string, row: SupportHealthProbe) => statusTag(value, row.status_reason) },
    { title: '在线', dataIndex: 'online', render: (value: boolean) => value ? '在线' : '离线' },
    { title: '延迟', dataIndex: 'last_latency_ms', render: (value: number | null, row: SupportHealthProbe) => row.last_success && value != null ? `${value} ms` : '—' },
    { title: '最近上报', dataIndex: 'last_reported_at', render: supportDate },
    { title: '结果说明', render: (_: any, row: SupportHealthProbe) => row.status_reason || row.last_error || '—' },
  ];
  return <Collapse size="small" items={(entry.sources || []).map((source, index) => ({
    key: `${source.source_key}:${index}`,
    label: <Space wrap>{statusTag(source.status, source.status_reason)}<span>{source.name || source.policy_name || `规则 #${source.policy_id}`}</span><Typography.Text type="secondary">{source.policy_name && source.name !== source.policy_name ? source.policy_name : `规则 #${source.policy_id}`}</Typography.Text></Space>,
    children: <><div className="support-source-detail"><span>{source.policy_enabled ? '规则已启用' : '规则已停用'}</span><span>{source.monitor_enabled ? '检测已启用' : '检测未启用'}</span>{source.monitored && <><span>间隔 {source.check_interval_sec} 秒</span><span>超时 {source.tcp_timeout_ms} ms</span><span>失败/恢复阈值 {source.failure_threshold}/{source.success_threshold}</span></>}</div>{source.status_reason && <Typography.Paragraph type="secondary">{source.status_reason}</Typography.Paragraph>}<Table size="small" rowKey={(probe) => String(probe.probe_id)} columns={probeColumns} dataSource={source.probes || []} pagination={false} scroll={{ x: 800 }} locale={{ emptyText: '该来源暂无检测结果，不能视为正常' }} /></>,
  }))} />;
}

export default function SupportOverviewPanel() {
  const [overview, setOverview] = useState<SupportOverview | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [keyword, setKeyword] = useState('');
  const [onlyIssues, setOnlyIssues] = useState(false);
  const pending = useRef(false);
  const mounted = useRef(true);
  const load = async () => {
    if (pending.current) return;
    pending.current = true; setLoading(true);
    try {
      const res = await supportGet('/overview');
      if (!mounted.current) return;
      setOverview(res.data || null); setError('');
    } catch (e: any) { if (mounted.current) setError(e.message || '加载入口检测结果失败'); }
    finally { pending.current = false; if (mounted.current) setLoading(false); }
  };
  useEffect(() => {
    mounted.current = true; load();
    const timer = window.setInterval(() => { if (document.visibilityState === 'visible') load(); }, 30000);
    return () => { mounted.current = false; window.clearInterval(timer); };
  }, []);
  const entries = useMemo(() => {
    const search = keyword.trim().toLowerCase();
    return (overview?.items || []).filter((entry) => {
      if (onlyIssues && !['unhealthy', 'partial', 'stale', 'unmonitored'].includes(entry.status)) return false;
      return !search || `${entry.host}:${entry.port} ${(entry.sources || []).map((source) => `${source.name} ${source.policy_name}`).join(' ')}`.toLowerCase().includes(search);
    }).slice().sort((a, b) => (priorities[a.status] ?? 3) - (priorities[b.status] ?? 3) || a.host.localeCompare(b.host) || a.port - b.port);
  }, [overview, keyword, onlyIssues]);
  const columns: any[] = [
    { title: '入口', key: 'entry', width: 270, render: (_: any, row: SupportHealthEntry) => <><Typography.Text code>{row.host.includes(':') ? `[${row.host}]` : row.host}:{row.port}</Typography.Text><div className="support-muted">{(row.protocol || 'tcp').toUpperCase()}</div></> },
    { title: '状态', dataIndex: 'status', width: 130, render: (value: string, row: SupportHealthEntry) => statusTag(value, row.status_reason) },
    { title: '探针结果', dataIndex: 'probes', width: 370, render: probeSummary },
    { title: '关联分组', dataIndex: 'sources', width: 130, render: (sources: any[]) => `${sources?.length || 0} 个 · 展开查看` },
    { title: '最近检测', dataIndex: 'last_reported_at', width: 180, render: supportDate },
  ];
  return <div className="support-panel">
    <Alert type="info" showIcon className="support-alert" message={overview?.coverage_note || '显示入口分配中的 TCP 检测结果；同地址、同端口合并显示。未监测或过期的数据不视为正常。'} description="仅展示已有检测结果，每 30 秒刷新；不触发即时探测，不开放规则、探针参数或整组分流修改。" />
    {error && <Alert type="error" showIcon className="support-alert" message={error} description={overview ? `当前仍显示 ${supportDate(overview.generated_at)} 的旧快照，请勿当作实时状态。` : undefined} />}
    <div className="support-summary"><div className="support-summary-card"><span>去重入口</span><strong>{overview?.summary?.total ?? '—'}</strong></div>{(['healthy', 'partial', 'unhealthy', 'stale', 'unmonitored'] as SupportHealthStatus[]).map((key) => <div className="support-summary-card" key={key}><span>{statuses[key].label}</span><strong>{overview?.summary?.[key] ?? '—'}</strong></div>)}</div>
    <div className="support-toolbar"><Space wrap><Input aria-label="筛选入口或关联组" prefix={<SearchOutlined aria-hidden />} placeholder="筛选入口或关联组" value={keyword} allowClear onChange={(event) => setKeyword(event.target.value)} style={{ width: 260 }} /><Checkbox checked={onlyIssues} onChange={(event) => setOnlyIssues(event.target.checked)}>只看异常 / 未知</Checkbox></Space><Button icon={<ReloadOutlined aria-hidden />} loading={loading} onClick={load}>刷新</Button></div>
    <Typography.Paragraph type="secondary">最近快照：{supportDate(overview?.generated_at)} · 展开入口查看关联分组，再展开分组查看原始探针结果。</Typography.Paragraph>
    <Table rowKey="key" columns={columns} dataSource={entries} loading={loading} scroll={{ x: 1050 }} expandable={{ expandedRowRender: (entry) => <EntrySources entry={entry} />, rowExpandable: (entry) => !!entry.sources?.length }} pagination={{ pageSize: 20, showSizeChanger: false, showTotal: (total) => `共 ${total} 个去重入口` }} locale={{ emptyText: <Empty description={overview ? '暂无符合条件的入口' : '暂无检测数据'} /> }} />
  </div>;
}
