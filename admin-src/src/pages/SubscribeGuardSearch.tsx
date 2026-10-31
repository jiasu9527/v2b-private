import React, { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Col, DatePicker, Form, Input, Row, Select, Space, Table, Tag, Typography } from 'antd';
import { ReloadOutlined, SearchOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { apiGet, bytes } from '../lib/api';
import { emptyGuardFilters, entrySearchOptions, guardFilterError, guardFilterFingerprint, guardSearchParams, normalizedGuardFilters, type EntrySearchOption, type SubscribeGuardFilters } from '../lib/subscribeGuardSearch';

function dateText(value: any) {
  return Number(value) > 0 ? new Date(Number(value) * 1000).toLocaleString() : '-';
}

function entryPolicies(row: any) {
  const policies = Array.isArray(row.entry_policies) ? row.entry_policies : [];
  const uncertain = row.entry_policy_uncertain === true;
  if (!policies.length) return uncertain ? <Tag color="orange" title="缺少可用请求 UA，无法确定当前规则命中结果">入口规则无法确定</Tag> : <Tag>未命中入口规则</Tag>;
  return <Space direction="vertical" size={4} style={{ width: '100%' }}>
    {uncertain && <Tag color="orange">缺少请求 UA，部分入口无法确定</Tag>}
    {policies.map((policy: any, index: number) => <div key={`${policy.id}-${policy.group_id || 0}-${index}`} title={policy.evaluated_ua ? `依据当前规则及请求 UA：${policy.evaluated_ua}` : '依据当前用户状态及入口规则计算'}>
      <Tag color={policy.mode === 'split' ? 'purple' : 'blue'}>{policy.mode === 'split' ? '固定分组' : '普通规则'}</Tag>
      <Typography.Text strong>{policy.name || `规则 #${policy.id}`}{policy.group_name ? ` / ${policy.group_name}` : ''}</Typography.Text>
      <div>{policy.action === 'hide' ? <Tag color="red">隐藏节点</Tag> : policy.action === 'original' ? <Tag color="cyan">原入口地址</Tag> : policy.entry_host ? <Typography.Text code copyable>{policy.entry_host}</Typography.Text> : null}</div>
    </div>)}
  </Space>;
}

type Props = {
  onUserDetail: (row: any) => void;
  onMatchDetail: (detail: any) => void;
  statusChange?: { id: number; banned: number; revision: number };
};

type SearchResult = { rows: any[]; current: number; pageSize: number; total: number; matched: number; unresolved: number; keepDays: number; from: number; to: number; warnings: string[] };

export default function SubscribeGuardSearch({ onUserDetail, onMatchDetail, statusChange }: Props) {
  const [draft, setDraft] = useState<SubscribeGuardFilters>(emptyGuardFilters);
  const [applied, setApplied] = useState<SubscribeGuardFilters | null>(null);
  const [result, setResult] = useState<SearchResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [options, setOptions] = useState<EntrySearchOption[]>([]);
  const [policyLoading, setPolicyLoading] = useState(false);
  const [policyError, setPolicyError] = useState('');
  const [policyRefresh, setPolicyRefresh] = useState(0);
  const sequence = useRef(0);
  const request = useRef<AbortController | null>(null);
  const statusOverrides = useRef(new Map<number, { banned: number; revision: number }>());
  const statusRevision = useRef(0);
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || '浏览器本地时区';
  const dirty = applied && guardFilterFingerprint(draft) !== guardFilterFingerprint(applied);
  const patchDraft = (patch: Partial<SubscribeGuardFilters>) => setDraft((previous) => ({ ...previous, ...patch }));

  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    let timedOut = false;
    const timeout = window.setTimeout(() => { timedOut = true; controller.abort(); }, 15000);
    setPolicyLoading(true);
    setPolicyError('');
    apiGet('/server/client-entry-user-policy/fetch', {}, { signal: controller.signal, cache: 'no-store' })
      .then((response) => { if (active) setOptions(entrySearchOptions(response?.data)); })
      .catch((e: any) => { if (active) setPolicyError(timedOut ? '入口规则加载超时，可重试；其他条件仍可查询。' : `入口规则加载失败：${e.message || '请重试'}`); })
      .finally(() => { clearTimeout(timeout); if (active) setPolicyLoading(false); });
    return () => { active = false; clearTimeout(timeout); controller.abort(); };
  }, [policyRefresh]);

  useEffect(() => () => { sequence.current += 1; request.current?.abort(); }, []);
  useEffect(() => {
    if (!statusChange) return;
    statusRevision.current = statusChange.revision;
    statusOverrides.current.set(statusChange.id, { banned: statusChange.banned, revision: statusChange.revision });
    setResult((previous) => previous ? { ...previous, rows: previous.rows.map((row) => Number(row.user_id || row.id) === statusChange.id ? { ...row, banned: statusChange.banned } : row) } : previous);
  }, [statusChange]);

  const search = async (filters: SubscribeGuardFilters, current = 1, pageSize = result?.pageSize || 20) => {
    const validation = guardFilterError(filters);
    if (validation) { setError(validation); return; }
    const submitted = normalizedGuardFilters(filters);
    const submittedStatusRevision = statusRevision.current;
    const ticket = ++sequence.current;
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    let timedOut = false;
    const timeout = window.setTimeout(() => { timedOut = true; controller.abort(); }, 60000);
    setError('');
    setLoading(true);
    try {
      const response = await apiGet('/subscribe-guard/search', guardSearchParams(submitted, current, pageSize), { signal: controller.signal, cache: 'no-store' });
      if (ticket !== sequence.current) return;
      setApplied(submitted);
      setResult({
        rows: (Array.isArray(response?.data) ? response.data : []).map((row: any) => {
          const changed = statusOverrides.current.get(Number(row.user_id || row.id));
          return changed && changed.revision > submittedStatusRevision ? { ...row, banned: changed.banned } : row;
        }),
        current: Number(response.current || current), pageSize: Number(response.page_size || pageSize), total: Number(response.total || 0),
        matched: Number(response.matched_events || 0), unresolved: Number(response.unresolved_events || 0), keepDays: Number(response.log_keep_days || 0),
        from: Number(response.available_from || 0), to: Number(response.available_to || 0),
        warnings: Array.isArray(response.warnings) ? response.warnings.map(String) : [],
      });
    } catch (e: any) {
      if (ticket === sequence.current) setError(timedOut ? '查询超过 60 秒，请缩小时间范围或增加条件后重试。' : e.message || '组合筛选失败，请重试');
    } finally {
      clearTimeout(timeout);
      if (ticket === sequence.current) { setLoading(false); request.current = null; }
    }
  };

  const reset = () => {
    sequence.current += 1;
    request.current?.abort();
    request.current = null;
    setLoading(false);
    setDraft(emptyGuardFilters());
    setApplied(null);
    setResult(null);
    setError('');
  };

  const showMatches = (row: any) => {
    const events = Array.isArray(row.recent) ? row.recent : [];
    onMatchDetail({
      title: '组合筛选匹配请求', owner: row.email || `用户 #${row.user_id || row.id}`,
      summary: `满足已提交条件的请求 ${row.count || 0} 次，展示最近 ${events.length} 条；当前入口规则重算，不代表历史分组。`,
      copyText: events.map((event: any) => `${dateText(event.time)}\t${event.blocked ? '已拦截' : '已放行'}\t${event.ip || '-'}\t${event.ua || '-'}`).join('\n'),
      sections: [{ title: '最近匹配请求', count: events.length, columns: [
        { title: '时间', dataIndex: 'time', width: 170, render: dateText },
        { title: '结果 / 原因', width: 160, render: (_: any, event: any) => <Space><Tag color={event.blocked ? 'red' : 'green'}>{event.blocked ? '已拦截' : '已放行'}</Tag>{event.reason || '-'}</Space> },
        { title: 'IP', dataIndex: 'ip', width: 150, render: (value: any) => <Typography.Text copyable>{value || '-'}</Typography.Text> },
        { title: 'UA', dataIndex: 'ua', ellipsis: true, render: (value: any) => <Typography.Text copyable ellipsis={{ tooltip: value }}>{value || '-'}</Typography.Text> },
      ], items: events.map((event: any, index: number) => ({ ...event, key: `${row.user_id || row.id}-${event.time || 0}-${index}` })) }],
    });
  };

  const columns: any[] = [
    { title: 'ID', width: 85, render: (_: any, row: any) => row.user_id || row.id },
    { title: '邮箱', dataIndex: 'email', width: 240, ellipsis: true, render: (value: any, row: any) => <a onClick={() => onUserDetail(row)}>{value || `用户 #${row.user_id || row.id}`}</a> },
    { title: '状态', width: 105, render: (_: any, row: any) => Number(row.banned) ? <Tag color="red">已封禁</Tag> : !(Number(row.plan_id) > 0) ? <Tag>无套餐</Tag> : Number(row.expired_at) > 0 && Number(row.expired_at) < Date.now() / 1000 ? <Tag color="orange">已过期</Tag> : <Tag color="green">正常</Tag> },
    { title: '当前命中入口分组', width: 300, render: (_: any, row: any) => entryPolicies(row) },
    { title: '匹配请求', dataIndex: 'count', width: 95, render: (value: any) => Number(value || 0) },
    { title: '已放行', dataIndex: 'allowed', width: 90, render: (value: any) => <Tag color="green">{Number(value || 0)}</Tag> },
    { title: '已拦截', dataIndex: 'blocked', width: 90, render: (value: any) => <Tag color={Number(value) ? 'red' : 'default'}>{Number(value || 0)}</Tag> },
    { title: 'IP / UA 数', width: 110, render: (_: any, row: any) => `${Number(row.ip_count || 0)} / ${Number(row.ua_count || 0)}` },
    { title: '最近匹配拉取', dataIndex: 'last_at', width: 180, render: dateText },
    { title: '套餐', dataIndex: 'plan_name', width: 140, ellipsis: true, render: (value: any) => value || '-' },
    { title: '已用 / 总流量', width: 180, render: (_: any, row: any) => `${bytes(Number(row.u || 0) + Number(row.d || 0))} / ${bytes(row.transfer_enable)}` },
    { title: '有效期', dataIndex: 'expired_at', width: 180, render: (value: any) => Number(value) > 0 ? dateText(value) : '长期有效' },
    { title: '操作', fixed: 'right', width: 185, render: (_: any, row: any) => <Space size="small">
      <Button size="small" onClick={() => onUserDetail(row)}>查看用户</Button>
      <Button size="small" disabled={!row.recent?.length} onClick={() => showMatches(row)}>匹配记录</Button>
    </Space> },
  ];

  const appliedSummary = applied ? [
    applied.account ? `${/^\d+$/.test(applied.account) ? 'ID 等于' : `邮箱${applied.account_match === 'exact' ? '等于' : '包含'}`} ${applied.account}` : '',
    applied.entry ? (options.find((option) => option.value === applied.entry)?.label || applied.entry) : '',
    applied.ua ? `UA ${applied.ua_match === 'equals' ? '等于' : applied.ua_match === 'not_contains' ? '不包含' : '包含'} ${applied.ua}` : '',
    applied.start_at !== undefined ? `${dateText(applied.start_at)} ≤ 拉取时间 < ${dateText(applied.end_at)}` : '全部保留期',
    applied.result === 'allowed' ? '已放行' : applied.result === 'blocked' ? '已拦截' : '全部请求结果',
    applied.sort === 'count' ? '匹配次数降序' : '最近拉取降序',
  ].filter(Boolean).join('；') : '';

  return <Card className="mt-4" size="small" title="用户组合筛选">
    <Space direction="vertical" size="middle" style={{ width: '100%' }}>
      <Alert type="info" showIcon style={{ background: '#f5f7fa', borderColor: '#d9dfe7' }} message="条件同时满足（AND），仅影响下方用户结果，不影响总统计和排行榜。" description={<>
        按当前入口规则和用户状态、结合匹配请求的 UA 重算，不是历史分组，也不等于节点最终下发结果。UA、时间、放行 / 拦截条件必须命中同一条请求。
        仅按账号 / 入口规则查询可包含无请求记录用户（0 次）；有请求条件时必须有匹配日志。防护关闭期间及日志保留期外无法追溯；已放行不代表订阅下载成功。
      </>} />
      <Form layout="vertical" onFinish={() => void search(draft)}>
        <Row gutter={[16, 0]}>
          <Col xs={24} lg={12}><Form.Item label="用户邮箱 / ID（纯数字按用户 ID 精确匹配）">
            <Space.Compact style={{ width: '100%' }}>
              <Select aria-label="邮箱匹配方式" value={draft.account_match} style={{ width: 125, flexShrink: 0 }} onChange={(account_match) => patchDraft({ account_match })} options={[{ label: '邮箱包含', value: 'contains' }, { label: '邮箱等于', value: 'exact' }]} />
              <Input allowClear value={draft.account} onChange={(event) => patchDraft({ account: event.target.value })} placeholder="用户邮箱或 ID" />
            </Space.Compact>
          </Form.Item></Col>
          <Col xs={24} lg={12}><Form.Item label="当前命中入口规则 / 固定子组">
            <Space.Compact style={{ width: '100%' }}>
              <Select aria-label="当前命中入口规则" allowClear showSearch optionFilterProp="label" style={{ width: '100%' }} loading={policyLoading} value={draft.entry || undefined} onChange={(entry) => patchDraft({ entry: entry || '' })} placeholder="不限；可选普通规则或具体固定子组" options={options} />
              <Button title="刷新入口规则选项" aria-label="刷新入口规则选项" loading={policyLoading} icon={<ReloadOutlined />} onClick={() => setPolicyRefresh((previous) => previous + 1)} />
            </Space.Compact>
          </Form.Item></Col>
          <Col xs={24} lg={12}><Form.Item label="请求 UA（忽略大小写）">
            <Space.Compact style={{ width: '100%' }}>
              <Select aria-label="UA 匹配方式" value={draft.ua_match} style={{ width: 125, flexShrink: 0 }} onChange={(ua_match) => patchDraft({ ua_match })} options={[{ label: '包含', value: 'contains' }, { label: '等于', value: 'equals' }, { label: '不包含', value: 'not_contains' }]} />
              <Input allowClear value={draft.ua} onChange={(event) => patchDraft({ ua: event.target.value })} placeholder="例如 Forest、Clash、curl；留空不限" />
            </Space.Compact>
          </Form.Item></Col>
          <Col xs={24} lg={12}><Form.Item label={`拉取时间（${timezone}；开始含、结束不含）`}>
            <DatePicker.RangePicker style={{ width: '100%' }} showTime format="YYYY-MM-DD HH:mm:ss" value={draft.start_at !== undefined && draft.end_at !== undefined ? [dayjs.unix(draft.start_at), dayjs.unix(draft.end_at)] : null}
              onChange={(values) => patchDraft({ start_at: values?.[0]?.unix(), end_at: values?.[1]?.unix() })}
              presets={[{ label: '最近 24 小时', value: () => [dayjs().subtract(24, 'hour'), dayjs()] }, { label: '最近 7 天', value: () => [dayjs().subtract(7, 'day'), dayjs()] }]} />
          </Form.Item></Col>
          <Col xs={12} lg={6}><Form.Item label="请求结果"><Select aria-label="请求结果" value={draft.result} onChange={(result) => patchDraft({ result })} options={[{ label: '全部', value: 'all' }, { label: '已放行', value: 'allowed' }, { label: '已拦截', value: 'blocked' }]} /></Form.Item></Col>
          <Col xs={12} lg={6}><Form.Item label="结果排序"><Select aria-label="结果排序" value={draft.sort} onChange={(sort) => patchDraft({ sort })} options={[{ label: '最近匹配拉取优先', value: 'last_at' }, { label: '匹配请求次数最多', value: 'count' }]} /></Form.Item></Col>
          <Col xs={24} lg={12}><Form.Item label=" "><Space wrap>
            <Button htmlType="submit" type="primary" loading={loading} icon={<SearchOutlined />}>查询</Button>
            <Button onClick={reset}>重置</Button>
            <Button href="#/server/client-entry-user-policy?evidence=1">查询实际下发证据</Button>
            <Button onClick={() => patchDraft({ start_at: dayjs().subtract(24, 'hour').unix(), end_at: dayjs().unix() })}>最近 24 小时</Button>
            <Button onClick={() => patchDraft({ start_at: dayjs().subtract(7, 'day').unix(), end_at: dayjs().unix() })}>最近 7 天</Button>
          </Space></Form.Item></Col>
        </Row>
      </Form>
      {policyError && <Alert type="warning" showIcon message={policyError} />}
      {error && <Alert type="error" showIcon message={error} description={result ? '下表仍为上次成功查询的结果，请按“当前结果条件”核对。' : undefined} />}
      {dirty && <Alert type="warning" showIcon message="筛选条件已修改，点击“查询”后生效；下表及翻页仍使用上次查询条件。" />}
      {applied && result && <Space direction="vertical" size={4}>
        <Typography.Text>当前结果条件：{appliedSummary}</Typography.Text>
        <Typography.Text type="secondary">共 {result.total} 位用户，匹配 {result.matched} 条请求{result.unresolved > 0 ? `；另有 ${result.unresolved} 条历史记录无法关联到当前用户` : ''}。日志保留：{result.keepDays > 0 ? `${result.keepDays} 天` : '不自动清理'}；可用记录时间：{result.from > 0 ? `${dateText(result.from)} 至 ${dateText(result.to)}` : '暂无日志'}。</Typography.Text>
      </Space>}
      {result?.warnings.map((warning, index) => <Alert key={index} type="warning" showIcon message={warning} />)}
      <Table size="small" rowKey={(row) => String(row.user_id || row.id)} loading={loading} columns={columns} dataSource={result?.rows || []} scroll={{ x: 2090 }}
        pagination={result ? { current: result.current, pageSize: result.pageSize, total: result.total, showSizeChanger: true, pageSizeOptions: [10, 20, 50, 100], disabled: loading, showTotal: (total) => `共 ${total} 位用户` } : false}
        onChange={(pagination) => { if (applied) void search(applied, pagination.pageSize !== result?.pageSize ? 1 : pagination.current || 1, pagination.pageSize); }}
        locale={{ emptyText: applied ? '未找到同时满足条件的用户；可调整条件或检查日志保留范围。' : '填写至少一项条件并点击“查询”，不会自动加载全部用户。' }} />
    </Space>
  </Card>;
}
