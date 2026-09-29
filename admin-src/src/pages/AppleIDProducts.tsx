import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Form, Input, InputNumber, Modal, Popconfirm, Space, Switch, Table, Tag, Tooltip, message } from 'antd';
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { apiGet, apiPost, money, unixTime } from '../lib/api';
import { appleIDPriceToCents } from './apple-id-import';

type Product = {
  id: number; name: string; region: string; owned_shadowrocket: boolean; price: number;
  after_sales: string; enabled: boolean; available_stock: number; reserved_stock: number;
  sold_stock: number; disabled_stock: number; created_at: number; updated_at: number;
};

export default function AppleIDProducts() {
  const [rows, setRows] = useState<Product[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [edit, setEdit] = useState<Partial<Product> | null>(null);
  const [saving, setSaving] = useState(false);
  const [busyID, setBusyID] = useState<number | null>(null);
  const [form] = Form.useForm();
  const request = useRef(0);
  const mutation = useRef(false);
  const mounted = useRef(true);

  const load = useCallback(async () => {
    const id = ++request.current;
    setLoading(true);
    setError('');
    try {
      const response = await apiGet('/apple-id/product/fetch');
      if (mounted.current && id === request.current) setRows(response.data || []);
    } catch (e: any) {
      if (mounted.current && id === request.current) setError(e?.message || '商品加载失败，请重试。');
    } finally {
      if (mounted.current && id === request.current) setLoading(false);
    }
  }, []);
  useEffect(() => {
    mounted.current = true;
    load();
    return () => { mounted.current = false; request.current += 1; };
  }, [load]);

  const openEdit = (row?: Product) => {
    setEdit(row || {});
    form.resetFields();
    form.setFieldsValue(row ? { ...row, price_yuan: (row.price / 100).toFixed(2) } : { enabled: false, owned_shadowrocket: false });
  };
  const save = async () => {
    if (mutation.current) return;
    mutation.current = true;
    setSaving(true);
    try {
      const values = await form.validateFields();
      await apiPost('/apple-id/product/save', {
        ...(edit?.id ? { id: edit.id } : {}),
        name: values.name.trim(), region: values.region.trim(),
        price: appleIDPriceToCents(values.price_yuan), owned_shadowrocket: !!values.owned_shadowrocket,
        enabled: !!values.enabled, after_sales: values.after_sales || '',
      });
      if (!mounted.current) return;
      message.success('商品已保存');
      setEdit(null);
      form.resetFields();
      await load();
    } catch (e: any) {
      if (mounted.current && !e?.errorFields) message.error(e?.message || '保存失败，请重试。');
    } finally {
      mutation.current = false;
      if (mounted.current) setSaving(false);
    }
  };
  const change = async (row: Product, action: 'show' | 'drop') => {
    if (mutation.current) return;
    mutation.current = true;
    setBusyID(row.id);
    try {
      await apiPost(`/apple-id/product/${action}`, action === 'show' ? { id: row.id, enabled: !row.enabled } : { id: row.id });
      if (!mounted.current) return;
      message.success(action === 'drop' ? '商品已删除' : row.enabled ? '商品已下架' : '商品已上架');
      await load();
    } catch (e: any) {
      if (mounted.current) message.error(e?.message || '操作失败，请重试。');
    } finally {
      mutation.current = false;
      if (mounted.current) setBusyID(null);
    }
  };

  const columns: any[] = [
    { title: '商品', dataIndex: 'name', width: 210, render: (value: string, row: Product) => <div><strong>{value}</strong><div className="apple-id-secondary">ID: {row.id}</div></div> },
    { title: '地区', dataIndex: 'region', width: 95 },
    { title: 'Shadowrocket', dataIndex: 'owned_shadowrocket', width: 130, render: (value: boolean) => <Tag color={value ? 'green' : undefined}>{value ? '已购买' : '未购买'}</Tag> },
    { title: '售价', dataIndex: 'price', width: 110, render: money },
    { title: '库存（可售 / 预留 / 已售 / 停用）', width: 285, render: (_: unknown, row: Product) => <Space size={[0, 6]} wrap><Tag color="green">可售 {row.available_stock}</Tag><Tag color="orange">预留 {row.reserved_stock}</Tag><Tag color="blue">已售 {row.sold_stock}</Tag><Tag>停用 {row.disabled_stock}</Tag></Space> },
    { title: '售后说明', dataIndex: 'after_sales', width: 210, ellipsis: true, render: (value: string) => <Tooltip title={value}>{value || '未填写'}</Tooltip> },
    { title: '更新时间', dataIndex: 'updated_at', width: 180, render: unixTime },
    { title: '上架', dataIndex: 'enabled', width: 85, render: (value: boolean, row: Product) => <Switch size="small" aria-label={`${row.name}上架状态`} checked={value} loading={busyID === row.id} disabled={busyID !== null || saving} onChange={() => change(row, 'show')} /> },
    { title: '操作', fixed: 'right', width: 130, render: (_: unknown, row: Product) => <Space><Button type="link" size="small" disabled={busyID !== null || saving} onClick={() => openEdit(row)}>编辑</Button><Popconfirm title="删除此商品？" description="已有库存或订单的商品无法删除，可改为下架。" okText="删除" cancelText="取消" onConfirm={() => change(row, 'drop')} disabled={busyID !== null || saving}><Button type="link" danger size="small" disabled={busyID !== null || saving}>删除</Button></Popconfirm></Space> },
  ];

  return <div className="apple-id-section">
    {error && <Alert type="error" showIcon message={error} action={<Button size="small" onClick={load}>重试</Button>} style={{ marginBottom: 16 }} />}
    <Card className="block-card" styles={{ body: { padding: 0 } }}>
      <div className="forest-table-action apple-id-toolbar"><Space wrap><Button type="primary" icon={<PlusOutlined />} disabled={busyID !== null || saving} onClick={() => openEdit()}>添加商品</Button><Button icon={<ReloadOutlined />} onClick={load} loading={loading}>刷新</Button><span className="apple-id-secondary">共 {rows.length} 个商品 · 新商品默认下架，可先导入库存</span></Space></div>
      <Table className="forest-table" rowKey="id" loading={loading} dataSource={rows} columns={columns} pagination={{ defaultPageSize: 20, showSizeChanger: true, showTotal: (total) => `共 ${total} 个商品` }} scroll={{ x: 1430 }} locale={{ emptyText: error ? '商品加载失败' : '暂无 Apple ID 商品，点击“添加商品”开始' }} />
    </Card>
    <Modal title={edit?.id ? '编辑 Apple ID 商品' : '添加 Apple ID 商品'} open={edit !== null} onOk={save} onCancel={() => { if (!mutation.current) { setEdit(null); form.resetFields(); } }} confirmLoading={saving} cancelButtonProps={{ disabled: saving }} maskClosable={!saving} keyboard={!saving} closable={!saving} okText="保存商品" cancelText="取消" width={640}>
      <Form form={form} layout="vertical" disabled={saving}>
        <Form.Item name="name" label="商品名称" rules={[{ required: true, whitespace: true, message: '请输入商品名称' }, { max: 255, message: '商品名称最多 255 个字符' }]}><Input maxLength={255} placeholder="例如：美区独享 Apple ID" /></Form.Item>
        <Form.Item name="region" label="账号地区" rules={[{ required: true, whitespace: true, message: '请输入账号地区' }, { max: 64, message: '地区最多 64 个字符' }]}><Input maxLength={64} placeholder="例如：US / 美国" /></Form.Item>
        <Form.Item name="price_yuan" label="售价（元）" rules={[{ required: true, message: '请输入售价' }, { validator: async (_, value) => { appleIDPriceToCents(value); } }]}><InputNumber stringMode min="0.01" precision={2} step="0.01" style={{ width: '100%' }} placeholder="例如：19.90" /></Form.Item>
        <Form.Item name="owned_shadowrocket" label="此账号已购买 Shadowrocket" valuePropName="checked"><Switch checkedChildren="已购买" unCheckedChildren="未购买" /></Form.Item>
        <Form.Item name="after_sales" label="售后说明"><Input.TextArea rows={4} placeholder="填写售后范围、有效期限及联系方法，展示给购买用户" /></Form.Item>
        <Form.Item name="enabled" label="上架销售" valuePropName="checked" extra="上架后网站可展示此商品；有可售库存时才能下单。"><Switch checkedChildren="已上架" unCheckedChildren="已下架" /></Form.Item>
      </Form>
    </Modal>
  </div>;
}
