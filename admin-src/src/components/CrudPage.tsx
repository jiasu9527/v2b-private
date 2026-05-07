import React, { useEffect, useMemo, useState } from 'react';
import { Button, Card, Col, Form, Input, InputNumber, Modal, Popconfirm, Row, Select, Space, Switch, Table, Tag, Typography, message } from 'antd';
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';
import type { ResourceConfig, FieldConfig, ColumnConfig } from '../lib/crud';
import { dropResource, fetchResource, saveResource } from '../lib/crud';
import { bytes, money, safeJsonParse, truthy, unixTime } from '../lib/api';

function renderCell(value: any, col: ColumnConfig) {
  if (col.type === 'money') return money(value);
  if (col.type === 'bytes') return bytes(value);
  if (col.type === 'time') return unixTime(value);
  if (col.type === 'bool') return <Tag color={truthy(value) ? 'green' : 'default'}>{truthy(value) ? '是' : '否'}</Tag>;
  if (col.type === 'array') {
    const arr = Array.isArray(value) ? value : safeJsonParse(value, []);
    return Array.isArray(arr) && arr.length ? <Typography.Text ellipsis>{arr.join(', ')}</Typography.Text> : '-';
  }
  if (col.type === 'json') return <code>{JSON.stringify(value)}</code>;
  if (typeof value === 'object' && value !== null) return <code>{JSON.stringify(value)}</code>;
  return value ?? '-';
}

function Field({ field }: { field: FieldConfig }) {
  const common = { placeholder: field.placeholder || field.label, style: { width: '100%' } };
  if (field.type === 'textarea' || field.type === 'json') return <Input.TextArea rows={field.type === 'json' ? 8 : 5} {...common} />;
  if (field.type === 'number') return <InputNumber {...common} />;
  if (field.type === 'switch') return <Switch />;
  if (field.type === 'select') return <Select allowClear options={field.options || []} {...common} />;
  if (field.type === 'tags') return <Select mode="tags" open={false} {...common} />;
  return <Input {...common} />;
}

function prepareInitialValues(row: any, config: ResourceConfig) {
  const next = { ...(row || {}) };
  for (const field of config.fields) {
    const value = next[field.name];
    if (field.type === 'switch') next[field.name] = truthy(value);
    if (field.type === 'json' && value !== undefined && value !== null && typeof value !== 'string') next[field.name] = JSON.stringify(value, null, 2);
    if (field.type === 'textarea' && Array.isArray(value)) next[field.name] = value.join('\n');
  }
  return next;
}

export default function CrudPage({ config }: { config: ResourceConfig }) {
  const [rows, setRows] = useState<any[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<any>(null);
  const [query, setQuery] = useState('');
  const [form] = Form.useForm();

  const load = async () => {
    setLoading(true);
    try {
      const params: any = { current: 1, pageSize: config.pageSize || 50 };
      if (query.trim() && config.searchKey) params[config.searchKey] = query.trim();
      const res = await fetchResource(config, params);
      setRows(res.rows);
      setTotal(res.total);
    } catch (e: any) {
      message.error(e.message || '加载失败');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { load(); }, [config.key]);

  const columns = useMemo<ColumnsType<any>>(() => {
    const base: ColumnsType<any> = (config.columns || []).map((col) => ({
      title: col.title,
      dataIndex: col.key,
      key: col.key,
      width: col.width,
      ellipsis: true,
      render: (v: any) => renderCell(v, col),
    }));
    base.push({
      title: '操作', key: 'actions', width: 190, fixed: 'right',
      render: (_: any, row: any) => <Space wrap>
        {config.save && <Button size="small" onClick={() => { setEditing(row); form.setFieldsValue(prepareInitialValues(row, config)); setOpen(true); }}>编辑</Button>}
        {config.show && <Button size="small" onClick={async () => { await saveResource({ ...config, save: config.show }, { id: row.id }); message.success('操作成功'); load(); }}>显示</Button>}
        {config.drop && <Popconfirm title="确认删除？" onConfirm={async () => { await dropResource(config, row.id); message.success('已删除'); load(); }}><Button size="small" danger>删除</Button></Popconfirm>}
      </Space>
    });
    return base;
  }, [config, form, query]);

  const submit = async () => {
    setSaving(true);
    try {
      const values = await form.validateFields();
      let payload = { ...(editing || {}), ...values };
      Object.entries(payload).forEach(([k, v]) => {
        if (typeof v === 'boolean') payload[k] = v ? 1 : 0;
        if (typeof v === 'string') payload[k] = safeJsonParse(v, v);
      });
      if (config.beforeSave) payload = config.beforeSave(payload, editing);
      await saveResource(config, payload);
      message.success('保存成功');
      setOpen(false); setEditing(null); form.resetFields(); load();
    } catch (e: any) {
      if (e?.errorFields) return;
      message.error(e.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };

  return <div className="legacy-page crud-page">
    <div className="content-heading">{config.title}</div>
    <Card className="block-card" styles={{ body: { padding: 0 } }}>
      <div className="forest-table-action"><Space wrap>
        <Input.Search allowClear placeholder={config.searchKey ? '快速搜索' : '当前页筛选'} value={query} onChange={e => setQuery(e.target.value)} onSearch={load} style={{ width: 220 }} />
        <Button icon={<ReloadOutlined />} onClick={load}>刷新</Button>
        {config.save && <Button type="primary" icon={<PlusOutlined />} onClick={() => { setEditing(null); form.resetFields(); setOpen(true); }}>新增</Button>}
      </Space></div>
      <Table className="forest-table" rowKey={(r) => r.id || r.code || r.trade_no || JSON.stringify(r)} loading={loading} columns={columns} dataSource={rows} scroll={{ x: 'max-content' }} pagination={{ total, pageSize: config.pageSize || 50, showSizeChanger: false }} />
    </Card>
    <Modal width={900} title={editing ? `编辑${config.title}` : `新增${config.title}`} open={open} onCancel={() => setOpen(false)} onOk={submit} confirmLoading={saving} destroyOnHidden>
      <Form form={form} layout="vertical">
        <Row gutter={16}>
          {config.fields.map((field) => <Col span={field.span || 12} key={field.name}>
            <Form.Item name={field.name} label={field.label} valuePropName={field.type === 'switch' ? 'checked' : 'value'} rules={field.required ? [{ required: true, message: `请输入${field.label}` }] : undefined}>
              <Field field={field} />
            </Form.Item>
          </Col>)}
        </Row>
      </Form>
    </Modal>
  </div>;
}
