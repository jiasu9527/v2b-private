import React, { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Col, Form, Input, InputNumber, Modal, Popconfirm, Row, Space, Spin, Statistic, Switch, Table, Tag, Typography, message } from 'antd';
import { SearchOutlined } from '@ant-design/icons';
import { apiGet, apiPost, bytes, money, unwrapData } from '../lib/api';

const textListFields = [
  'subscribe_guard_ip_whitelist',
  'subscribe_guard_ip_blacklist',
  'subscribe_guard_ua_whitelist',
  'subscribe_guard_ua_blacklist',
  'subscribe_guard_token_blacklist',
  'subscribe_guard_sensitive_rules',
];

const switchFields = [
  'subscribe_guard_enable',
  'subscribe_guard_block_empty_ua',
  'subscribe_guard_block_crawler_ua',
  'subscribe_guard_sensitive_enable',
  'subscribe_guard_sensitive_log_ip',
];

const fields = [
  {
    key: 'subscribe_guard_enable',
    title: '启用订阅防护',
    description: '开启后订阅请求会先经过 IP、UA、Token 和频率规则检查。',
    type: 'switch',
  },
  {
    key: 'subscribe_guard_ip_whitelist',
    title: 'IP 白名单',
    description: '每行一个 IP 或 CIDR。命中后跳过订阅防护检查。',
    type: 'textarea',
    placeholder: '1.1.1.1\n203.0.113.0/24',
  },
  {
    key: 'subscribe_guard_ip_blacklist',
    title: 'IP 黑名单',
    description: '每行一个 IP 或 CIDR。命中后直接禁止拉取订阅。',
    type: 'textarea',
    placeholder: '8.8.8.8\n198.51.100.0/24',
  },
  {
    key: 'subscribe_guard_ua_whitelist',
    title: 'UA 白名单',
    description: '每行一个关键词，大小写不敏感。命中后跳过 UA 拦截。',
    type: 'textarea',
    placeholder: 'clash\nsurge\nsing-box\nshadowrocket',
  },
  {
    key: 'subscribe_guard_ua_blacklist',
    title: 'UA 黑名单',
    description: '每行一个关键词，大小写不敏感。命中后禁止拉取订阅。',
    type: 'textarea',
    placeholder: 'curl\nwget\npython\ngo-http-client',
  },
  {
    key: 'subscribe_guard_token_blacklist',
    title: 'Token 黑名单',
    description: '每行一个订阅 Token。命中后禁止拉取订阅。',
    type: 'textarea',
    placeholder: 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx',
  },
  {
    key: 'subscribe_guard_block_empty_ua',
    title: '拦截空 UA',
    description: '开启后没有 User-Agent 的订阅请求会被拒绝。',
    type: 'switch',
  },
  {
    key: 'subscribe_guard_block_crawler_ua',
    title: '拦截常见爬虫 UA',
    description: '开启后自动拦截 curl、wget、python、Go-http-client、Java、Postman 等常见脚本请求。',
    type: 'switch',
  },
  {
    key: 'subscribe_guard_rate_limit_per_minute',
    title: '单 IP 每分钟限制',
    description: '0 表示不限制。超过限制返回 429。',
    type: 'number',
  },
  {
    key: 'subscribe_guard_log_keep_days',
    title: '日志保留天数',
    description: '订阅防护日志会持久保存到本地文件，并自动清理超过该天数的记录。0 表示不自动清理。',
    type: 'days',
  },
  {
    key: 'subscribe_guard_sensitive_enable',
    title: '启用敏感访问监控',
    description: '开启后 v2node 会识别用户通过节点访问的敏感域名，并批量上报到面板。',
    type: 'switch',
  },
  {
    key: 'subscribe_guard_sensitive_rules',
    title: '敏感访问规则',
    description: '每行一条规则，支持 domain:example.com、suffix:example.com、keyword:xxx。命中后只记录和统计，不主动拦截。',
    type: 'textarea',
    placeholder: 'suffix:example.com\nkeyword:test\ndomain:example.org',
  },
  {
    key: 'subscribe_guard_sensitive_interval',
    title: '敏感访问上报间隔',
    description: 'v2node 聚合命中记录后按该间隔批量上报，建议 30-120 秒。',
    type: 'seconds',
  },
  {
    key: 'subscribe_guard_sensitive_log_ip',
    title: '记录客户端 IP',
    description: '开启后敏感访问日志会记录用户连接节点时的客户端 IP。',
    type: 'switch',
  },
  {
    key: 'subscribe_guard_sensitive_log_keep_days',
    title: '敏感日志保留天数',
    description: '超过该天数的敏感访问日志会在节点上报时自动清理。0 表示不自动清理。',
    type: 'days',
  },
];

function arrayToText(value: any) {
  if (Array.isArray(value)) return value.join('\n');
  return value ?? '';
}

function textToArray(value: any) {
  return String(value || '')
    .split(/[\n,]+/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function normalizeValues(values: any) {
  const next: any = {};
  Object.entries(values).forEach(([key, value]) => {
    if (textListFields.includes(key)) next[key] = textToArray(value);
    else if (switchFields.includes(key)) next[key] = value ? 1 : 0;
    else next[key] = value ?? 0;
  });
  return next;
}

function dateText(ts: any) {
  const n = Number(ts || 0);
  if (!n) return '-';
  return new Date(n * 1000).toLocaleString();
}

function reasonText(reason: any) {
  const map: Record<string, string> = {
    pass: '放行',
    whitelist: '白名单',
    ip: 'IP拦截',
    token: 'Token拦截',
    ua: 'UA拦截',
    rate_limit: '频率限制',
  };
  return map[String(reason)] || String(reason || '-');
}

function sensitiveTimeText(row: any) {
  const last = Number(row?.last_at || row?.time || 0);
  return dateText(last);
}

function safeList(value: any) {
  return Array.isArray(value) ? value.map((item) => String(item || '').trim()).filter(Boolean) : [];
}

function DetailRow({ label, children }: { label: string; children: React.ReactNode }) {
  return <div className="legacy-detail-row">
    <div className="legacy-detail-label">{label}</div>
    <div className="legacy-detail-value">{children === null || children === undefined || children === '' ? '-' : children}</div>
  </div>;
}

function DetailSection({ title, children }: { title: string; children: React.ReactNode }) {
  return <section className="legacy-detail-section">
    <div className="legacy-detail-section-title">{title}</div>
    <div className="legacy-detail-grid">{children}</div>
  </section>;
}

function userStatus(row: any) {
  if (Number(row?.banned)) return <Tag color="red">已封禁</Tag>;
  if (!(Number(row?.plan_id) > 0)) return <Tag>未购买套餐</Tag>;
  const expiredAt = Number(row?.expired_at || 0);
  if (expiredAt > 0 && expiredAt < Date.now() / 1000) return <Tag color="orange">已过期</Tag>;
  return <Tag color="green">正常</Tag>;
}

function userRole(row: any) {
  if (Number(row?.is_admin)) return '管理员';
  if (Number(row?.is_staff)) return '员工';
  return '普通用户';
}

function userExpiryText(value: any) {
  const timestamp = Number(value || 0);
  return timestamp > 0 ? dateText(timestamp) : '长期有效';
}

export default function SubscribeGuardPage() {
  const [form] = Form.useForm();
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [stats, setStats] = useState<any>({});
  const [detailModal, setDetailModal] = useState<any>(null);
  const [userSearch, setUserSearch] = useState('');
  const [userSearching, setUserSearching] = useState(false);
  const [userSearchRows, setUserSearchRows] = useState<any[]>([]);
  const [uaSearch, setUASearch] = useState('');
  const [uaAppliedSearch, setUAAppliedSearch] = useState('');
  const [uaSearching, setUASearching] = useState(false);
  const [uaSearchRows, setUASearchRows] = useState<any[]>([]);
  const [uaSearchPage, setUASearchPage] = useState({ current: 1, pageSize: 20, total: 0 });
  const [uaSearchMatchedEvents, setUASearchMatchedEvents] = useState(0);
  const [uaSearchUnresolvedEvents, setUASearchUnresolvedEvents] = useState(0);
  const [userDetail, setUserDetail] = useState<any>(null);
  const [userDetailLoading, setUserDetailLoading] = useState(false);
  const userSearchSequence = useRef(0);
  const uaSearchSequence = useRef(0);
  const userDetailSequence = useRef(0);

  const loadStats = async () => {
    try {
      const res = await apiGet('/subscribe-guard/stats');
      setStats(unwrapData(res) || {});
    } catch {
      setStats({});
    }
  };

  const load = async () => {
    setLoading(true);
    try {
      const [res] = await Promise.all([
        apiGet('/config/fetch', { key: 'subscribe_guard' }),
        loadStats(),
      ]);
      const data = unwrapData(res)?.subscribe_guard || {};
      const values: any = {};
      Object.entries(data).forEach(([key, value]) => {
        values[key] = textListFields.includes(key) ? arrayToText(value) : value;
      });
      form.setFieldsValue(values);
    } catch (e: any) {
      message.error(e.message || '加载失败');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { load(); }, []);

  const save = async () => {
    setSaving(true);
    try {
      await apiPost('/config/save', normalizeValues(form.getFieldsValue()));
      message.success('已保存');
      load();
    } catch (e: any) {
      message.error(e.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };

  const setUserBanned = async (row: any, banned: number) => {
    const userID = Number(row?.user_id || row?.id || row?.user?.id || 0);
    if (!userID) return message.warning('用户ID不存在');
    setSaving(true);
    try {
      await apiPost('/subscribe-guard/set-user-banned', { id: userID, banned }, { form: true });
      message.success(banned ? '已封禁用户' : '已恢复正常');
      await loadStats();
      setUserSearchRows((current) => current.map((item) => Number(item.id) === userID ? { ...item, banned } : item));
      setUASearchRows((current) => current.map((item) => Number(item.user_id) === userID ? { ...item, banned } : item));
      setUserDetail((current: any) => current && Number(current.user?.id) === userID
        ? { ...current, user: { ...current.user, banned } }
        : current);
    } catch (e: any) {
      message.error(e.message || '操作失败');
    } finally {
      setSaving(false);
    }
  };

  const searchUsers = async (keyword = userSearch) => {
    const value = String(keyword || '').trim();
    const sequence = ++userSearchSequence.current;
    setUserSearch(value);
    if (!value) {
      setUserSearchRows([]);
      setUserSearching(false);
      return;
    }
    setUserSearching(true);
    try {
      const response = await apiGet('/subscribe-guard/user-search', { keyword: value });
      if (sequence === userSearchSequence.current) {
        const rows = Array.isArray(response.data) ? response.data.slice() : [];
        const normalized = value.toLowerCase();
        rows.sort((left: any, right: any) => {
          const leftEmail = String(left?.email || '').trim().toLowerCase();
          const rightEmail = String(right?.email || '').trim().toLowerCase();
          const rank = (email: string) => email === normalized ? 0 : email.startsWith(normalized) ? 1 : 2;
          return rank(leftEmail) - rank(rightEmail) || Number(left?.id || 0) - Number(right?.id || 0);
        });
        setUserSearchRows(rows);
      }
    } catch (e: any) {
      if (sequence === userSearchSequence.current) message.error(e.message || '用户搜索失败');
    } finally {
      if (sequence === userSearchSequence.current) setUserSearching(false);
    }
  };

  const searchUsersByUA = async (keyword = uaSearch, current = 1, pageSize = uaSearchPage.pageSize) => {
    const value = String(keyword || '').trim();
    const sequence = ++uaSearchSequence.current;
    setUASearch(value);
    if (!value) {
      setUASearchRows([]);
      setUAAppliedSearch('');
      setUASearchPage({ current: 1, pageSize, total: 0 });
      setUASearchMatchedEvents(0);
      setUASearchUnresolvedEvents(0);
      setUASearching(false);
      return;
    }
    setUASearching(true);
    try {
      const response = await apiGet('/subscribe-guard/ua-search', {
        keyword: value,
        current,
        page_size: pageSize,
      });
      if (sequence !== uaSearchSequence.current) return;
      const rows = Array.isArray(response?.data) ? response.data : [];
      setUASearchRows(rows);
      setUAAppliedSearch(value);
      setUASearchPage({
        current: Number(response?.current || current),
        pageSize: Number(response?.page_size || pageSize),
        total: Number(response?.total || 0),
      });
      setUASearchMatchedEvents(Number(response?.matched_events || 0));
      setUASearchUnresolvedEvents(Number(response?.unresolved_events || 0));
    } catch (e: any) {
      if (sequence === uaSearchSequence.current) {
        setUASearchRows([]);
        setUAAppliedSearch('');
        setUASearchPage((previous) => ({ ...previous, current, pageSize, total: 0 }));
        setUASearchMatchedEvents(0);
        setUASearchUnresolvedEvents(0);
        message.error(e.message || 'UA 用户搜索失败');
      }
    } finally {
      if (sequence === uaSearchSequence.current) setUASearching(false);
    }
  };

  const showUserDetail = async (row: any) => {
    const userID = Number(row?.user_id || row?.id || 0);
    if (!userID) return message.warning('用户ID不存在');
    const sequence = ++userDetailSequence.current;
    setUserDetail({ user: { ...row, id: userID }, stats: {} });
    setUserDetailLoading(true);
    try {
      const response = await apiGet('/subscribe-guard/user-detail', { id: userID });
      const data = unwrapData(response) || {};
      if (sequence === userDetailSequence.current) {
        setUserDetail({
          ...data,
          user: { ...row, ...(data.user || {}), id: userID },
        });
      }
    } catch (e: any) {
      if (sequence === userDetailSequence.current) {
        setUserDetail(null);
        message.error(e.message || '用户详情加载失败');
      }
    } finally {
      if (sequence === userDetailSequence.current) setUserDetailLoading(false);
    }
  };

  const closeUserDetail = () => {
    userDetailSequence.current += 1;
    setUserDetail(null);
    setUserDetailLoading(false);
  };

  const renderControl = (field: any) => {
    if (field.type === 'switch') return <Switch />;
    if (field.type === 'number') return <InputNumber min={0} addonAfter="次/分钟" style={{ width: '100%', maxWidth: 360 }} />;
    if (field.type === 'days') return <InputNumber min={0} addonAfter="天" style={{ width: '100%', maxWidth: 360 }} />;
    if (field.type === 'seconds') return <InputNumber min={0} addonAfter="秒" style={{ width: '100%', maxWidth: 360 }} />;
    return <Input.TextArea rows={5} placeholder={field.placeholder || '请输入'} />;
  };

  const appendToList = async (fieldKey: string, value: any) => {
    const raw = String(value || '').trim();
    if (!raw) return;
    const current = textToArray(form.getFieldValue(fieldKey));
    if (current.includes(raw)) {
      message.info('已经在列表中');
      return;
    }
    const nextText = [...current, raw].join('\n');
    form.setFieldValue(fieldKey, nextText);
    try {
      setSaving(true);
      await apiPost('/config/save', normalizeValues({ ...form.getFieldsValue(), [fieldKey]: nextText }));
      message.success('已追加并保存生效');
      load();
    } catch (e: any) {
      message.error(e.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };

  const compactColumns = (field: string, title: string) => [
    { title, dataIndex: field, ellipsis: true, render: (value: any) => <Typography.Text copyable={{ text: String(value || '') }} ellipsis>{value || '-'}</Typography.Text> },
    { title: '次数', dataIndex: 'count', width: 80 },
  ];

  const recentColumns: any[] = [
    { title: '时间', dataIndex: 'time', width: 170, render: dateText },
    { title: '状态', dataIndex: 'blocked', width: 90, render: (blocked: any, row: any) => <Tag color={blocked ? 'red' : 'green'}>{reasonText(row.reason)}</Tag> },
    { title: 'IP', dataIndex: 'ip', width: 150, render: (value: any) => <Typography.Text copyable={{ text: value }}>{value || '-'}</Typography.Text> },
    { title: 'Token', dataIndex: 'token', ellipsis: true, render: (value: any) => <Typography.Text copyable={{ text: value }} ellipsis>{value || '-'}</Typography.Text> },
    { title: 'UA', dataIndex: 'ua', ellipsis: true, render: (value: any) => <Typography.Text copyable={{ text: value }} ellipsis>{value || '-'}</Typography.Text> },
    { title: '操作', width: 210, render: (_: any, row: any) => <Space size="small">
      <a onClick={() => appendToList('subscribe_guard_ip_blacklist', row.ip)}>封IP</a>
      <a onClick={() => appendToList('subscribe_guard_token_blacklist', row.token)}>封Token</a>
      <a onClick={() => appendToList('subscribe_guard_ua_blacklist', row.ua)}>封UA</a>
    </Space> },
  ];

  const showListDetail = (payload: any) => setDetailModal(payload);

  const buildDetailSection = (title: string, values: string[], valueTitle: string) => ({
    title,
    count: values.length,
    copyText: values.join('\n'),
    columns: [
      { title: valueTitle, dataIndex: 'value', ellipsis: true, render: (item: any) => <Typography.Text copyable={{ text: String(item || '') }} ellipsis>{item || '-'}</Typography.Text> },
    ],
    items: values.map((item: string, index: number) => ({ key: `${title}-${index}-${item}`, value: item })),
  });

  const sensitiveUserColumns: any[] = [
    { title: '账号', dataIndex: 'email', ellipsis: true, render: (value: any, row: any) => <a onClick={() => showUserDetail(row)}>{value || `用户 #${row.user_id}`}</a> },
    { title: '命中数', dataIndex: 'count', width: 90 },
    { title: '域名数', dataIndex: 'domain_count', width: 90 },
    { title: 'IP数', dataIndex: 'ip_count', width: 80, render: (value: any) => value || 0 },
    { title: '明细', dataIndex: 'domains', width: 110, render: (value: any, row: any) => {
      const domains = safeList(value);
      const ips = safeList(row.ips);
      return (domains.length || ips.length) ? <Button size="small" onClick={() => showListDetail({
        title: '敏感访问明细',
        owner: row.email || `用户 #${row.user_id}`,
        summary: `命中 ${row.count || 0} 次，${domains.length} 个域名，${ips.length} 个客户端 IP`,
        copyText: [`命中域名：`, domains.join('\n'), '', '客户端 IP：', ips.join('\n')].join('\n'),
        sections: [
          buildDetailSection('命中域名', domains, '域名'),
          buildDetailSection('客户端 IP', ips, 'IP'),
        ],
      })}>查看明细</Button> : '-';
    } },
  ];

  const subscribeUserColumns: any[] = [
    { title: '用户邮箱', dataIndex: 'email', ellipsis: true, render: (value: any, row: any) => <a onClick={() => showUserDetail(row)}>{value || `用户 #${row.user_id}`}</a> },
    { title: '状态', dataIndex: 'banned', width: 90, render: (value: any) => Number(value) ? <Tag color="red">封禁</Tag> : <Tag color="green">正常</Tag> },
    { title: '请求数', dataIndex: 'count', width: 90 },
    { title: 'IP数', dataIndex: 'ip_count', width: 80, render: (value: any) => value || 0 },
    { title: 'UA数量', dataIndex: 'ua_count', width: 90 },
    { title: '明细', dataIndex: 'uas', width: 110, render: (value: any, row: any) => {
      const uas = safeList(value);
      const ips = safeList(row.ips);
      return (uas.length || ips.length) ? <Button size="small" onClick={() => showListDetail({
        title: '订阅防控用户明细',
        owner: row.email || row.token || '-',
        summary: `请求 ${row.count || 0} 次，${ips.length} 个请求 IP，${uas.length} 个 UA`,
        copyText: [`请求 IP：`, ips.join('\n'), '', 'User-Agent：', uas.join('\n')].join('\n'),
        sections: [
          buildDetailSection('请求 IP', ips, 'IP'),
          buildDetailSection('User-Agent', uas, 'User-Agent'),
        ],
      })}>查看明细</Button> : '-';
    } },
    { title: '操作', width: 180, render: (_: any, row: any) => <Space size="small">
      <a onClick={() => showUserDetail(row)}>查看详细</a>
      {Number(row.banned) ? <Popconfirm title={`恢复 ${row.email || row.user_id} 为正常？`} onConfirm={() => setUserBanned(row, 0)}><a>恢复正常</a></Popconfirm> : <Popconfirm title={`封禁 ${row.email || row.user_id}？`} onConfirm={() => setUserBanned(row, 1)}><a className="text-danger">封禁</a></Popconfirm>}
    </Space> },
  ];

  const userSearchColumns: any[] = [
    { title: 'ID', dataIndex: 'id', width: 85 },
    { title: '邮箱', dataIndex: 'email', width: 250, ellipsis: true, render: (value: any, row: any) => <a onClick={() => showUserDetail(row)}>{value || `用户 #${row.id}`}</a> },
    { title: '状态', dataIndex: 'banned', width: 95, render: (_: any, row: any) => userStatus(row) },
    { title: '套餐', dataIndex: 'plan_name', width: 150, ellipsis: true, render: (value: any) => value || '-' },
    { title: '已用 / 总流量', width: 190, render: (_: any, row: any) => `${bytes(Number(row.u || 0) + Number(row.d || 0))} / ${bytes(row.transfer_enable)}` },
    { title: '到期时间', dataIndex: 'expired_at', width: 175, render: userExpiryText },
    { title: '操作', width: 100, fixed: 'right', render: (_: any, row: any) => <Button size="small" onClick={() => showUserDetail(row)}>查看详细</Button> },
  ];

  const uaSearchColumns: any[] = [
    { title: 'ID', dataIndex: 'user_id', width: 85 },
    { title: '邮箱', dataIndex: 'email', width: 250, ellipsis: true, render: (value: any, row: any) => <a onClick={() => showUserDetail(row)}>{value || `用户 #${row.user_id}`}</a> },
    { title: '状态', dataIndex: 'banned', width: 100, render: (_: any, row: any) => userStatus(row) },
    { title: '匹配请求', dataIndex: 'count', width: 105 },
    { title: '已放行', dataIndex: 'allowed', width: 90, render: (value: any) => <Tag color="green">{Number(value || 0)}</Tag> },
    { title: '已拦截', dataIndex: 'blocked', width: 90, render: (value: any) => <Tag color={Number(value || 0) ? 'red' : 'default'}>{Number(value || 0)}</Tag> },
    { title: 'IP 数', dataIndex: 'ip_count', width: 80, render: (value: any) => Number(value || 0) },
    { title: 'UA 数', dataIndex: 'ua_count', width: 80, render: (value: any) => Number(value || 0) },
    { title: '最近请求', dataIndex: 'last_at', width: 175, render: dateText },
    { title: '操作', width: 185, fixed: 'right', render: (_: any, row: any) => <Space size="small">
      <Button size="small" onClick={() => showUserDetail(row)}>查看用户</Button>
      <Button size="small" disabled={!Array.isArray(row.recent) || row.recent.length === 0} onClick={() => {
        const events = Array.isArray(row.recent) ? row.recent : [];
        showListDetail({
          title: 'UA 匹配请求',
          owner: row.email || `用户 #${row.user_id}`,
          summary: `匹配 ${row.count || 0} 次，当前结果展示最近 ${events.length} 条请求`,
          copyText: events.map((event: any) => `${dateText(event.time)}\t${event.blocked ? '已拦截' : '已放行'}\t${event.ip || '-'}\t${event.ua || '-'}`).join('\n'),
          sections: [{
            title: '最近匹配请求',
            count: events.length,
            columns: [
              { title: '时间', dataIndex: 'time', width: 170, render: dateText },
              { title: '状态', dataIndex: 'blocked', width: 95, render: (blocked: any, event: any) => <Tag color={blocked ? 'red' : 'green'}>{reasonText(event.reason)}</Tag> },
              { title: 'IP', dataIndex: 'ip', width: 150, render: (value: any) => <Typography.Text copyable={{ text: String(value || '') }}>{value || '-'}</Typography.Text> },
              { title: 'UA', dataIndex: 'ua', ellipsis: true, render: (value: any) => <Typography.Text copyable={{ text: String(value || '') }} ellipsis>{value || '-'}</Typography.Text> },
            ],
            items: events.map((event: any, index: number) => ({ ...event, key: `${row.user_id}-${event.time || 0}-${index}` })),
          }],
        });
      }}>匹配记录</Button>
    </Space> },
  ];

  const sensitiveDomainColumns: any[] = [
    { title: '域名', dataIndex: 'domain', ellipsis: true, render: (value: any) => <Typography.Text copyable={{ text: String(value || '') }} ellipsis>{value || '-'}</Typography.Text> },
    { title: '次数', dataIndex: 'count', width: 90 },
  ];

  const sensitiveRecentColumns: any[] = [
    { title: '时间', dataIndex: 'last_at', width: 170, render: (_: any, row: any) => sensitiveTimeText(row) },
    { title: '账号', dataIndex: 'email', width: 210, ellipsis: true, render: (value: any, row: any) => <a onClick={() => showUserDetail(row)}>{value || `用户 #${row.user_id}`}</a> },
    { title: '域名', dataIndex: 'domain', ellipsis: true, render: (value: any) => <Typography.Text copyable={{ text: String(value || '') }} ellipsis>{value || '-'}</Typography.Text> },
    { title: '规则', dataIndex: 'rule', width: 190, ellipsis: true },
    { title: '客户端 IP', dataIndex: 'client_ip', width: 150, render: (value: any) => value || '-' },
    { title: '次数', dataIndex: 'count', width: 80 },
  ];

  const sensitiveStats = stats.sensitive || {};
  const detailUser = userDetail?.user || {};
  const detailStats = userDetail?.stats || {};
  const detailRecent = Array.isArray(detailStats.recent) ? detailStats.recent : [];
  const detailIPs = Array.isArray(detailStats.ips) ? detailStats.ips : [];
  const detailUAs = Array.isArray(detailStats.uas) ? detailStats.uas : [];
  const detailIPCount = Number(detailStats.ip_count ?? detailIPs.length);
  const detailUACount = Number(detailStats.ua_count ?? detailUAs.length);
  const detailUsed = Number(detailUser.u || 0) + Number(detailUser.d || 0);
  const detailRecentColumns: any[] = [
    { title: '时间', dataIndex: 'time', width: 170, render: dateText },
    { title: '状态', dataIndex: 'blocked', width: 95, render: (blocked: any, row: any) => <Tag color={blocked ? 'red' : 'green'}>{reasonText(row.reason)}</Tag> },
    { title: 'IP', dataIndex: 'ip', width: 150, render: (value: any) => <Typography.Text copyable={{ text: String(value || '') }}>{value || '-'}</Typography.Text> },
    { title: 'UA', dataIndex: 'ua', ellipsis: true, render: (value: any) => <Typography.Text ellipsis={{ tooltip: value }}>{value || '-'}</Typography.Text> },
  ];

  return <div className="legacy-page config-page subscribe-guard-page">
    <Modal
      open={!!userDetail}
      title="订阅防护用户详情"
      width={980}
      footer={<Space>
        {Number(detailUser.banned) ? <Popconfirm title="确认恢复该用户为正常状态？" onConfirm={() => setUserBanned(detailUser, 0)}><Button>恢复正常</Button></Popconfirm> : <Popconfirm title="确认封禁该用户？" onConfirm={() => setUserBanned(detailUser, 1)}><Button danger>封禁用户</Button></Popconfirm>}
        <Button onClick={closeUserDetail}>关闭</Button>
      </Space>}
      onCancel={closeUserDetail}
      destroyOnHidden
    >
      <Spin spinning={userDetailLoading}>
        <div className="legacy-detail-modal">
          <DetailSection title="账号信息">
            <DetailRow label="用户 ID">#{detailUser.id || '-'}</DetailRow>
            <DetailRow label="邮箱"><Typography.Text copyable={{ text: String(detailUser.email || '') }}>{detailUser.email || '-'}</Typography.Text></DetailRow>
            <DetailRow label="账号状态">{userStatus(detailUser)}</DetailRow>
            <DetailRow label="账号身份">{userRole(detailUser)}</DetailRow>
            <DetailRow label="套餐">{detailUser.plan_name || (detailUser.plan_id ? `套餐 #${detailUser.plan_id}` : '无套餐')}</DetailRow>
            <DetailRow label="权限组">{detailUser.group_name || (detailUser.group_id ? `权限组 #${detailUser.group_id}` : '-')}</DetailRow>
            <DetailRow label="到期时间">{userExpiryText(detailUser.expired_at)}</DetailRow>
            <DetailRow label="设备限制">{detailUser.device_limit ?? '不限制'}</DetailRow>
            <DetailRow label="已用流量">{bytes(detailUsed)}</DetailRow>
            <DetailRow label="总流量">{bytes(detailUser.transfer_enable)}</DetailRow>
            <DetailRow label="余额">{money(detailUser.balance)}</DetailRow>
            <DetailRow label="推广佣金">{money(detailUser.commission_balance)}</DetailRow>
            <DetailRow label="限速">{detailUser.speed_limit ? `${detailUser.speed_limit} Mbps` : '不限制'}</DetailRow>
            <DetailRow label="邀请人">{detailUser.invite_user?.email || (detailUser.invite_user_id ? `用户 #${detailUser.invite_user_id}` : '-')}</DetailRow>
            <DetailRow label="邀请码">{detailUser.invite_code || '-'}</DetailRow>
            <DetailRow label="注册时间">{dateText(detailUser.created_at)}</DetailRow>
            <DetailRow label="最后在线">{dateText(detailUser.t || detailUser.last_login_at)}</DetailRow>
            <DetailRow label="备注">{detailUser.remarks || '-'}</DetailRow>
          </DetailSection>
          <DetailSection title="订阅防护统计">
            <DetailRow label="总请求">{Number(detailStats.total || 0)}</DetailRow>
            <DetailRow label="已放行">{Number(detailStats.allowed || 0)}</DetailRow>
            <DetailRow label="已拦截">{Number(detailStats.blocked || 0)}</DetailRow>
            <DetailRow label="请求 IP">{detailIPCount} 个</DetailRow>
            <DetailRow label="User-Agent">{detailUACount} 个</DetailRow>
            <DetailRow label="结果统计">{Object.entries(detailStats.reason_counts || {}).map(([reason, count]) => `${reasonText(reason)} ${count}`).join(' / ') || '-'}</DetailRow>
          </DetailSection>
          <Row gutter={[16, 16]}>
            <Col xs={24} lg={12}><Card size="small" title={`请求 IP (${detailIPCount})`}><Table size="small" rowKey="ip" pagination={{ pageSize: 8, size: 'small' }} columns={compactColumns('ip', 'IP')} dataSource={detailIPs} /></Card></Col>
            <Col xs={24} lg={12}><Card size="small" title={`User-Agent (${detailUACount})`}><Table size="small" rowKey="ua" pagination={{ pageSize: 8, size: 'small' }} columns={compactColumns('ua', 'User-Agent')} dataSource={detailUAs} /></Card></Col>
          </Row>
          <Card size="small" title={`最近请求 (${detailRecent.length})`}>
            <Table size="small" rowKey={(_, index) => String(index)} pagination={{ pageSize: 10, size: 'small' }} columns={detailRecentColumns} dataSource={detailRecent} scroll={{ x: 760 }} locale={{ emptyText: '保留期内暂无该用户的订阅防护记录' }} />
          </Card>
        </div>
      </Spin>
    </Modal>
    <Modal
      open={!!detailModal}
      title={detailModal?.title || '明细'}
      width={760}
      footer={<Space><Button onClick={() => setDetailModal(null)}>关闭</Button></Space>}
      onCancel={() => setDetailModal(null)}
      destroyOnClose
    >
      <Space direction="vertical" size="middle" style={{ width: '100%' }}>
        <div>
          <Typography.Text type="secondary">用户</Typography.Text>
          <div><Typography.Text copyable={{ text: String(detailModal?.owner || '') }} strong>{detailModal?.owner || '-'}</Typography.Text></div>
        </div>
        <Alert type="info" showIcon message={detailModal?.summary || '暂无统计'} />
        <div style={{ textAlign: 'right' }}>
          <Button size="small" onClick={() => { navigator.clipboard?.writeText(detailModal?.copyText || ''); message.success('已复制明细'); }}>复制全部</Button>
        </div>
        {(detailModal?.sections || []).map((section: any) => <Card key={section.title} size="small" title={`${section.title} (${section.count || 0})`}>
          <Table
            size="small"
            rowKey="key"
            pagination={{ pageSize: 8, size: 'small' }}
            columns={section.columns || []}
            dataSource={section.items || []}
            locale={{ emptyText: '暂无明细' }}
            scroll={{ x: 620 }}
          />
        </Card>)}
      </Space>
    </Modal>
    <div className="content-heading">订阅防护</div>
    <Card className="block-card">
      <Spin spinning={loading}>
        <div className="block-content">
          <Alert
            type="info"
            showIcon
            className="mb-3"
            message="用于防止订阅链接被扫描、脚本批量拉取或泄露滥用。"
            description="规则只作用于订阅接口，不影响用户登录、后台管理和节点上报。白名单优先级最高。"
          />
          <Row gutter={[16, 16]}>
            <Col xs={12} md={6}><Card size="small"><Statistic title="总请求" value={stats.total || 0} /></Card></Col>
            <Col xs={12} md={6}><Card size="small"><Statistic title="已放行" value={stats.allowed || 0} valueStyle={{ color: '#389e0d' }} /></Card></Col>
            <Col xs={12} md={6}><Card size="small"><Statistic title="已拦截" value={stats.blocked || 0} valueStyle={{ color: '#cf1322' }} /></Card></Col>
            <Col xs={12} md={6}><Card size="small"><Statistic title="频率限制" value={stats.reason_counts?.rate_limit || 0} valueStyle={{ color: '#d48806' }} /></Card></Col>
          </Row>
          <Row gutter={[16, 16]} className="mt-4">
            <Col xs={24} lg={8}><Card size="small" title="Top IP"><Table size="small" rowKey="ip" pagination={false} columns={compactColumns('ip', 'IP')} dataSource={stats.top_ips || []} /></Card></Col>
            <Col xs={24} lg={8}><Card size="small" title="Top Token"><Table size="small" rowKey="token" pagination={false} columns={compactColumns('token', 'Token')} dataSource={stats.top_tokens || []} /></Card></Col>
            <Col xs={24} lg={8}><Card size="small" title="Top UA"><Table size="small" rowKey="ua" pagination={false} columns={compactColumns('ua', 'UA')} dataSource={stats.top_uas || []} /></Card></Col>
          </Row>
          <Card className="mt-4" size="small" title="搜索用户">
            <Space direction="vertical" size="middle" style={{ width: '100%' }}>
              <Input.Search
                allowClear
                enterButton={<><SearchOutlined /> 搜索</>}
                value={userSearch}
                loading={userSearching}
                onChange={(event) => {
                  const value = event.target.value;
                  setUserSearch(value);
                  if (!value) {
                    userSearchSequence.current += 1;
                    setUserSearchRows([]);
                    setUserSearching(false);
                  }
                }}
                onSearch={searchUsers}
                placeholder="输入用户 ID 或邮箱，支持邮箱模糊搜索"
                style={{ maxWidth: 620 }}
              />
              <Table
                size="small"
                rowKey="id"
                loading={userSearching}
                pagination={false}
                columns={userSearchColumns}
                dataSource={userSearchRows}
                scroll={{ x: 1100 }}
                locale={{ emptyText: userSearch ? '未找到匹配用户' : '输入用户 ID 或邮箱后搜索' }}
              />
            </Space>
          </Card>
          <Card className="mt-4" size="small" title="按 UA 搜索用户">
            <Space direction="vertical" size="middle" style={{ width: '100%' }}>
              <Input.Search
                allowClear
                enterButton={<><SearchOutlined /> 搜索</>}
                value={uaSearch}
                loading={uaSearching}
                onChange={(event) => {
                  const value = event.target.value;
                  setUASearch(value);
                  uaSearchSequence.current += 1;
                  setUAAppliedSearch('');
                  setUASearchRows([]);
                  setUASearchPage((previous) => ({ ...previous, current: 1, total: 0 }));
                  setUASearchMatchedEvents(0);
                  setUASearchUnresolvedEvents(0);
                  setUASearching(false);
                }}
                onSearch={(keyword) => searchUsersByUA(keyword, 1, uaSearchPage.pageSize)}
                placeholder="输入 UA 关键词，例如 curl；大小写不敏感"
                style={{ maxWidth: 620 }}
              />
              {uaAppliedSearch && uaAppliedSearch === uaSearch ? <Typography.Text type="secondary">
                保留期内命中 {uaSearchMatchedEvents} 条请求，已关联 {uaSearchPage.total} 位用户
                {uaSearchUnresolvedEvents > 0 ? `；${uaSearchUnresolvedEvents} 条历史记录无法关联到当前用户` : ''}。
              </Typography.Text> : null}
              <Table
                size="small"
                rowKey="user_id"
                loading={uaSearching}
                columns={uaSearchColumns}
                dataSource={uaSearchRows}
                pagination={{
                  current: uaSearchPage.current,
                  pageSize: uaSearchPage.pageSize,
                  total: uaSearchPage.total,
                  size: 'small',
                  showSizeChanger: true,
                  pageSizeOptions: [10, 20, 50, 100],
                }}
                onChange={(pagination: any) => searchUsersByUA(uaSearch, pagination.current, pagination.pageSize)}
                scroll={{ x: 1300 }}
                locale={{ emptyText: uaAppliedSearch && uaAppliedSearch === uaSearch ? '保留期内未找到使用该 UA 的用户' : uaSearch ? '点击“搜索”查看结果' : '输入 UA 关键词后搜索，例如 curl' }}
              />
            </Space>
          </Card>
          <Card className="mt-4" size="small" title="订阅防控用户排行">
            <Table size="small" rowKey={(row) => String(row.user_id || row.token)} pagination={{ pageSize: 10, size: 'small', showSizeChanger: true }} columns={subscribeUserColumns} dataSource={stats.top_subscribe_users || []} scroll={{ x: 900 }} />
          </Card>
          <Card className="mt-4" size="small" title="最近订阅防护记录" extra={<Button size="small" onClick={loadStats}>刷新统计</Button>}>
            <Table size="small" rowKey={(_, index) => String(index)} pagination={{ pageSize: 10, size: 'small' }} columns={recentColumns} dataSource={stats.recent || []} scroll={{ x: 1100 }} />
          </Card>
          <Row gutter={[16, 16]} className="mt-4">
            <Col xs={24} lg={12}><Card size="small" title="敏感访问账号排行"><Table size="small" rowKey={(row) => String(row.user_id || row.email)} pagination={false} columns={sensitiveUserColumns} dataSource={sensitiveStats.top_users || []} /></Card></Col>
            <Col xs={24} lg={12}><Card size="small" title="敏感访问域名排行"><Table size="small" rowKey="domain" pagination={false} columns={sensitiveDomainColumns} dataSource={sensitiveStats.top_domains || []} /></Card></Col>
          </Row>
          <Card className="mt-4" size="small" title="最近敏感访问记录" extra={<Button size="small" onClick={loadStats}>刷新统计</Button>}>
            <Table size="small" rowKey={(row, index) => String(row.id || index)} pagination={{ pageSize: 10, size: 'small' }} columns={sensitiveRecentColumns} dataSource={sensitiveStats.recent || []} scroll={{ x: 1150 }} />
          </Card>
        </div>
        <Form form={form} layout="vertical">
          <div className="config-tab-content">
            {fields.map((field) => <div className="config-row" key={field.key}>
              <div className="config-row-copy">
                <div className="config-row-title">{field.title}</div>
                <div className="config-row-desc">{field.description}</div>
              </div>
              <div className="config-row-control">
                <Form.Item name={field.key} valuePropName={field.type === 'switch' ? 'checked' : 'value'} noStyle>
                  {renderControl(field)}
                </Form.Item>
              </div>
            </div>)}
          </div>
        </Form>
        <div className="config-save-bar"><Space><Button onClick={load}>刷新</Button><Button type="primary" loading={saving} onClick={save}>保存配置</Button></Space></div>
      </Spin>
    </Card>
  </div>;
}
