import React, { useEffect, useState } from 'react';
import { Alert, Button, Card, Col, Form, Input, InputNumber, Row, Space, Spin, Statistic, Switch, Table, Tag, Typography, message } from 'antd';
import { apiGet, apiPost, unwrapData } from '../lib/api';

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

export default function SubscribeGuardPage() {
  const [form] = Form.useForm();
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [stats, setStats] = useState<any>({});

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

  const sensitiveUserColumns: any[] = [
    { title: '账号', dataIndex: 'email', ellipsis: true, render: (value: any, row: any) => <Typography.Text copyable={{ text: String(value || row.user_id || '') }} ellipsis>{value || `用户 #${row.user_id}`}</Typography.Text> },
    { title: '命中数', dataIndex: 'count', width: 90 },
    { title: '域名数', dataIndex: 'domain_count', width: 90 },
    { title: '命中的域名', dataIndex: 'domains', ellipsis: true, render: (value: any) => {
      const domains = Array.isArray(value) ? value : [];
      return domains.length ? <Typography.Text copyable={{ text: domains.join('\n') }} ellipsis>{domains.join('、')}</Typography.Text> : '-';
    } },
  ];

  const subscribeUserColumns: any[] = [
    { title: '用户邮箱', dataIndex: 'email', ellipsis: true, render: (value: any, row: any) => <Typography.Text copyable={{ text: String(value || row.token || '') }} ellipsis>{value || '-'}</Typography.Text> },
    { title: '请求数', dataIndex: 'count', width: 90 },
    { title: 'UA数量', dataIndex: 'ua_count', width: 90 },
    { title: '用户UA明细', dataIndex: 'uas', ellipsis: true, render: (value: any) => {
      const uas = Array.isArray(value) ? value : [];
      return uas.length ? <Typography.Text copyable={{ text: uas.join('\n') }} ellipsis>{uas.join('、')}</Typography.Text> : '-';
    } },
  ];

  const sensitiveDomainColumns: any[] = [
    { title: '域名', dataIndex: 'domain', ellipsis: true, render: (value: any) => <Typography.Text copyable={{ text: String(value || '') }} ellipsis>{value || '-'}</Typography.Text> },
    { title: '次数', dataIndex: 'count', width: 90 },
  ];

  const sensitiveRecentColumns: any[] = [
    { title: '时间', dataIndex: 'last_at', width: 170, render: (_: any, row: any) => sensitiveTimeText(row) },
    { title: '账号', dataIndex: 'email', width: 210, ellipsis: true, render: (value: any, row: any) => <Typography.Text copyable={{ text: String(value || row.user_id || '') }} ellipsis>{value || `用户 #${row.user_id}`}</Typography.Text> },
    { title: '域名', dataIndex: 'domain', ellipsis: true, render: (value: any) => <Typography.Text copyable={{ text: String(value || '') }} ellipsis>{value || '-'}</Typography.Text> },
    { title: '规则', dataIndex: 'rule', width: 190, ellipsis: true },
    { title: '客户端 IP', dataIndex: 'client_ip', width: 150, render: (value: any) => value || '-' },
    { title: '次数', dataIndex: 'count', width: 80 },
  ];

  const sensitiveStats = stats.sensitive || {};

  return <div className="legacy-page config-page subscribe-guard-page">
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
          <Card className="mt-4" size="small" title="订阅防控用户排行">
            <Table size="small" rowKey={(row) => String(row.user_id || row.token)} pagination={false} columns={subscribeUserColumns} dataSource={stats.top_subscribe_users || []} scroll={{ x: 850 }} />
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
