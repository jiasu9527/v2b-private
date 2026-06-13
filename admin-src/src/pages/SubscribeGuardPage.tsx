import React, { useEffect, useState } from 'react';
import { Alert, Button, Card, Form, Input, InputNumber, Space, Spin, Switch, message } from 'antd';
import { apiGet, apiPost, unwrapData } from '../lib/api';

const textListFields = [
  'subscribe_guard_ip_whitelist',
  'subscribe_guard_ip_blacklist',
  'subscribe_guard_ua_whitelist',
  'subscribe_guard_ua_blacklist',
  'subscribe_guard_token_blacklist',
];

const switchFields = [
  'subscribe_guard_enable',
  'subscribe_guard_block_empty_ua',
  'subscribe_guard_block_crawler_ua',
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

export default function SubscribeGuardPage() {
  const [form] = Form.useForm();
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);

  const load = async () => {
    setLoading(true);
    try {
      const res = await apiGet('/config/fetch', { key: 'subscribe_guard' });
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
    return <Input.TextArea rows={5} placeholder={field.placeholder || '请输入'} />;
  };

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
