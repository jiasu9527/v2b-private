import React, { useEffect, useMemo, useState } from 'react';
import { Button, Card, Col, Form, Input, InputNumber, Row, Select, Switch, Tabs, message } from 'antd';
import { apiGet, apiPost, unwrapData } from '../lib/api';

const groups = [
  ['site', '基础'], ['safe', '安全'], ['subscribe', '订阅'], ['frontend', '前端'], ['server', '节点'],
  ['email', '邮件'], ['telegram', 'Telegram'], ['invite', '邀请'], ['deposit', '充值'], ['ticket', '工单'], ['app', '客户端']
];

const resetOptions = [
  { label: '每月1号', value: 0 }, { label: '按月重置', value: 1 }, { label: '不重置', value: 2 },
  { label: '每年1月1日', value: 3 }, { label: '按年重置', value: 4 }
];

function flattenConfig(obj: any) {
  const out: any = {};
  Object.values(obj || {}).forEach((group: any) => {
    Object.entries(group || {}).forEach(([key, value]) => {
      out[key] = Array.isArray(value) ? value.join('\n') : value;
    });
  });
  return out;
}

function normalizeConfig(values: any) {
  const out: any = {};
  Object.entries(values).forEach(([key, value]) => {
    if (value === undefined) return;
    if (typeof value === 'boolean') out[key] = value ? 1 : 0;
    else if (typeof value === 'string' && value.includes('\n')) out[key] = value.split('\n').map((x) => x.trim()).filter(Boolean);
    else out[key] = value;
  });
  return out;
}

export default function ConfigPage() {
  const [form] = Form.useForm();
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [data, setData] = useState<any>({});

  const load = async () => {
    setLoading(true);
    try {
      const r = await apiGet('/config/fetch');
      const d = unwrapData(r) || {};
      setData(d);
      form.setFieldsValue(flattenConfig(d));
    } catch (e: any) {
      message.error(e.message || '配置加载失败');
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => { load(); }, []);

  const save = async () => {
    setSaving(true);
    try {
      await apiPost('/config/save', normalizeConfig(form.getFieldsValue()));
      message.success('已保存');
      load();
    } catch (e: any) {
      message.error(e.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };

  const tabItems = useMemo(() => groups.filter(([key]) => data[key]).map(([key, label]) => ({
    key,
    label,
    children: <Row gutter={16}>{Object.entries(data[key] || {}).map(([fieldKey, value]) => {
      let input: React.ReactNode = <Input />;
      if (typeof value === 'number') input = <InputNumber style={{ width: '100%' }} />;
      if (typeof value === 'boolean') input = <Switch />;
      if (Array.isArray(value)) input = <Input.TextArea rows={4} placeholder="一行一个" />;
      if (fieldKey === 'reset_traffic_method') input = <Select options={resetOptions} />;
      if (String(fieldKey).includes('password') || String(fieldKey).includes('token') || String(fieldKey).includes('secret')) input = <Input.Password autoComplete="new-password" />;
      return <Col xs={24} md={12} lg={8} key={fieldKey}>
        <Form.Item name={fieldKey} label={fieldKey} valuePropName={typeof value === 'boolean' ? 'checked' : 'value'}>{input}</Form.Item>
      </Col>;
    })}</Row>
  })), [data]);

  return <div className="page-stack">
    <Card title="系统配置" loading={loading} extra={<Button type="primary" loading={saving} onClick={save}>保存配置</Button>}>
      <Form form={form} layout="vertical"><Tabs items={tabItems} /></Form>
    </Card>
  </div>;
}
