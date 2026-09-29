import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Form, Input, Modal, Popconfirm, Select, Space, Table, Tag, message } from 'antd';
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { apiGet, apiPost, unixTime } from '../lib/api';
import { parseAppleIDImport } from './apple-id-import';

type ProductOption = { id: number; name: string; region: string; enabled: boolean };
type Inventory = {
  id: number; product_id: number; product_name: string; account: string; status: number;
  reserved_order_id?: number; reserved_until?: number; sold_order_id?: number; created_at: number;
};
type InventoryQuery = { current: number; page_size: number; product_id?: number; status?: number };
const states = [
  { value: 0, label: '可售', color: 'green' },
  { value: 1, label: '预留', color: 'orange' },
  { value: 2, label: '已售', color: 'blue' },
  { value: 3, label: '停用', color: 'default' },
];

export default function AppleIDInventory() {
  const [rows, setRows] = useState<Inventory[]>([]);
  const [total, setTotal] = useState(0);
  const [products, setProducts] = useState<ProductOption[]>([]);
  const [loading, setLoading] = useState(false);
  const [productsLoading, setProductsLoading] = useState(false);
  const [error, setError] = useState('');
  const [productError, setProductError] = useState('');
  const [query, setQuery] = useState<InventoryQuery>({ current: 1, page_size: 20 });
  const [importOpen, setImportOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [busyID, setBusyID] = useState<number | null>(null);
  const [form] = Form.useForm();
  const request = useRef(0);
  const productRequest = useRef(0);
  const mutation = useRef(false);
  const mounted = useRef(true);
  const queryRef = useRef(query);
  queryRef.current = query;

  const load = useCallback(async () => {
    const id = ++request.current;
    setLoading(true);
    setError('');
    try {
      const response = await apiGet('/apple-id/inventory/fetch', queryRef.current);
      if (mounted.current && id === request.current) {
        setRows(response.data || []);
        setTotal(Number(response.total || 0));
      }
    } catch (e: any) {
      if (mounted.current && id === request.current) setError(e?.message || '库存加载失败，请重试。');
    } finally {
      if (mounted.current && id === request.current) setLoading(false);
    }
  }, []);
  const loadProducts = useCallback(async () => {
    const id = ++productRequest.current;
    setProductsLoading(true);
    setProductError('');
    try {
      const response = await apiGet('/apple-id/product/fetch');
      if (mounted.current && id === productRequest.current) setProducts(response.data || []);
    } catch (e: any) {
      if (mounted.current && id === productRequest.current) setProductError(e?.message || '商品选项加载失败，请重试。');
    } finally {
      if (mounted.current && id === productRequest.current) setProductsLoading(false);
    }
  }, []);
  useEffect(() => {
    mounted.current = true;
    loadProducts();
    return () => { mounted.current = false; request.current += 1; productRequest.current += 1; };
  }, [loadProducts]);
  useEffect(() => { load(); }, [query, load]);

  const openImport = () => {
    form.resetFields();
    form.setFieldsValue({ product_id: query.product_id, credentials: '' });
    setImportOpen(true);
    loadProducts();
  };
  const closeImport = () => {
    if (mutation.current) return;
    setImportOpen(false);
    form.resetFields();
  };
  const save = async () => {
    if (mutation.current) return;
    mutation.current = true;
    setSaving(true);
    try {
      const values = await form.validateFields();
      const items = parseAppleIDImport(values.credentials);
      const response = await apiPost('/apple-id/inventory/import', { product_id: values.product_id, items });
      if (!mounted.current) return;
      message.success(`已导入 ${Number(response.data?.count ?? items.length)} 条账号资料`);
      form.resetFields();
      setImportOpen(false);
      setQuery((previous) => ({ ...previous, current: 1, product_id: values.product_id, status: 0 }));
    } catch (e: any) {
      if (mounted.current && !e?.errorFields) message.error(e?.message || '导入失败，输入已保留，请检查后重试。');
    } finally {
      mutation.current = false;
      if (mounted.current) setSaving(false);
    }
  };
  const disable = async (row: Inventory) => {
    if (mutation.current) return;
    mutation.current = true;
    setBusyID(row.id);
    try {
      await apiPost('/apple-id/inventory/disable', { id: row.id });
      if (!mounted.current) return;
      message.success('库存已停用');
      if (rows.length === 1 && queryRef.current.current > 1 && queryRef.current.status === 0) {
        setQuery((previous) => ({ ...previous, current: previous.current - 1 }));
      } else {
        await load();
      }
    } catch (e: any) {
      if (mounted.current) message.error(e?.message || '停用失败，请刷新库存状态后重试。');
    } finally {
      mutation.current = false;
      if (mounted.current) setBusyID(null);
    }
  };
  const productOptions = products.map((product) => ({ value: product.id, label: `${product.name} · ${product.region}${product.enabled ? '' : '（已下架）'}` }));
  const columns: any[] = [
    { title: '库存 ID', dataIndex: 'id', width: 100 },
    { title: '商品', dataIndex: 'product_name', width: 200 },
    { title: '账号（脱敏）', dataIndex: 'account', width: 220 },
    { title: '状态', dataIndex: 'status', width: 100, render: (value: number) => <Tag color={states[value]?.color}>{states[value]?.label || '未知'}</Tag> },
    { title: '关联订单 ID', width: 135, render: (_: unknown, row: Inventory) => row.sold_order_id || row.reserved_order_id || '-' },
    { title: '预留到期', dataIndex: 'reserved_until', width: 185, render: unixTime },
    { title: '导入时间', dataIndex: 'created_at', width: 185, render: unixTime },
    { title: '操作', fixed: 'right', width: 95, render: (_: unknown, row: Inventory) => row.status === 0 ? <Popconfirm title="停用此账号？" description="停用后不再用于销售，此页面不提供重新启用。" okText="确认停用" cancelText="取消" onConfirm={() => disable(row)} disabled={busyID !== null || saving}><Button type="link" size="small" danger loading={busyID === row.id} disabled={busyID !== null || saving}>停用</Button></Popconfirm> : <span className="apple-id-secondary">-</span> },
  ];

  return <div className="apple-id-section">
    {productError && <Alert type="warning" showIcon message={`商品列表：${productError}`} action={<Button size="small" onClick={loadProducts}>重试</Button>} style={{ marginBottom: 16 }} />}
    {error && <Alert type="error" showIcon message={error} action={<Button size="small" onClick={load}>重试</Button>} style={{ marginBottom: 16 }} />}
    <Card className="block-card" styles={{ body: { padding: 0 } }}>
      <div className="forest-table-action apple-id-toolbar"><Space wrap>
        <Select aria-label="按商品筛选库存" placeholder="全部商品" allowClear showSearch optionFilterProp="label" style={{ width: 260 }} loading={productsLoading} options={productOptions} value={query.product_id} onChange={(value) => setQuery((previous) => ({ ...previous, product_id: value, current: 1 }))} />
        <Select aria-label="按库存状态筛选" placeholder="全部库存状态" allowClear style={{ width: 155 }} options={states} value={query.status} onChange={(value) => setQuery((previous) => ({ ...previous, status: value, current: 1 }))} />
        <Button type="primary" icon={<PlusOutlined />} onClick={openImport} disabled={busyID !== null || saving}>批量导入</Button>
        <Button icon={<ReloadOutlined />} loading={loading} onClick={() => { load(); loadProducts(); }}>刷新</Button>
      </Space></div>
      <Table className="forest-table" rowKey="id" loading={loading} dataSource={rows} columns={columns} scroll={{ x: 1220 }} pagination={{ current: query.current, pageSize: query.page_size, total, showSizeChanger: true, pageSizeOptions: [20, 50, 100, 200], showTotal: (value) => `共 ${value} 个账号`, onChange: (current, pageSize) => setQuery((previous) => ({ ...previous, current: pageSize === previous.page_size ? current : 1, page_size: pageSize })) }} locale={{ emptyText: error ? '库存加载失败' : '暂无符合条件的库存' }} />
    </Card>
    <Modal title="批量导入 Apple ID 库存" open={importOpen} onOk={save} onCancel={closeImport} confirmLoading={saving} cancelButtonProps={{ disabled: saving }} maskClosable={!saving} keyboard={!saving} closable={!saving} okText="确认导入" cancelText="取消" width={720}>
      <Form form={form} layout="vertical" disabled={saving} autoComplete="off">
        <Form.Item name="product_id" label="所属商品" rules={[{ required: true, message: '请选择所属商品' }]}><Select showSearch optionFilterProp="label" options={productOptions} loading={productsLoading} placeholder="请选择商品" notFoundContent={productsLoading ? '正在加载商品…' : productError ? '商品加载失败，请关闭后重试' : '暂无商品，请先在商品管理添加'} /></Form.Item>
        <Alert type="info" showIcon style={{ marginBottom: 16 }} message="单次最多 500 条；每行一整条账号资料" description="每行粘贴一条完整资料，账号、密码、密保等内容按原样保留，不要求固定分隔格式。空行忽略。" />
        <Form.Item name="credentials" label="账号资料（每行一条）" rules={[{ required: true, message: '请输入待导入的账号资料' }, { validator: async (_, value) => { if (value) parseAppleIDImport(value); } }]} validateTrigger="onBlur" extra="导入成功或关闭窗口会清空输入；失败时保留供修正。列表仅显示脱敏内容。"><Input.TextArea rows={10} autoComplete="off" spellCheck={false} autoCorrect="off" autoCapitalize="none" placeholder={'example@icloud.com----密码----密保问题答案\nuser@example.com | password | security answer'} /></Form.Item>
      </Form>
    </Modal>
  </div>;
}
