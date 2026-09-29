import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Form, Input, Select, Space, Table, Typography, message } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { ReloadOutlined, SearchOutlined } from '@ant-design/icons';
import { apiGet, money, unixTime } from '../lib/api';
import AppleIDOrderDetail, { AppleIDOrder, AppleIDOrderStatus, appleIDOrderStatuses } from './AppleIDOrderDetail';
import AppleIDCancelOrderButton from './AppleIDCancelOrderButton';

type Filters = { email?: string; trade_no?: string; product_id?: number; status?: number };
type Page = { current: number; pageSize: number };

export default function AppleIDOrders() {
  const [form] = Form.useForm<Filters>();
  const [rows, setRows] = useState<AppleIDOrder[]>([]);
  const [total, setTotal] = useState(0);
  const [products, setProducts] = useState<{ id: number; name: string }[]>([]);
  const [filters, setFilters] = useState<Filters>({});
  const [page, setPage] = useState<Page>({ current: 1, pageSize: 20 });
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [selectedID, setSelectedID] = useState<number | null>(null);
  const alive = useRef(true);
  const request = useRef(0);

  const load = async (nextPage = page, nextFilters = filters) => {
    const token = ++request.current;
    setLoading(true);
    setError('');
    setPage(nextPage);
    setFilters(nextFilters);
    try {
      const res = await apiGet('/apple-id/order/fetch', {
        ...nextFilters, current: nextPage.current, page_size: nextPage.pageSize,
      });
      if (!alive.current || token !== request.current) return;
      setRows(res.data || []);
      setTotal(Number(res.total) || 0);
    } catch (e: any) {
      if (!alive.current || token !== request.current) return;
      setRows([]);
      setTotal(0);
      setError(e.message || '订单加载失败，请重试');
    } finally {
      if (alive.current && token === request.current) setLoading(false);
    }
  };

  useEffect(() => {
    alive.current = true;
    load();
    apiGet('/apple-id/product/fetch').then((res) => {
      if (alive.current) setProducts(res.data || []);
    }).catch(() => {
      if (alive.current) message.warning('商品筛选项加载失败，可继续通过订单号或邮箱查询');
    });
    return () => { alive.current = false; request.current += 1; };
  }, []);

  const columns: ColumnsType<AppleIDOrder> = [
    { title: '订单号', dataIndex: 'trade_no', width: 280, render: (value, row) => <Button type="link" style={{ padding: 0 }} onClick={() => setSelectedID(row.id)}>{value}</Button> },
    { title: '购买用户', dataIndex: 'user_email', width: 210, render: (value, row) => <div>{value || '用户已删除'}<div className="text-muted">用户 ID：{row.user_id}</div></div> },
    { title: '商品快照', dataIndex: 'product_name', width: 210, render: (value, row) => <div>{value}<div className="text-muted">{row.region || '未设置地区'} · 商品 #{row.product_id}</div></div> },
    { title: '应付金额', dataIndex: 'total_amount', width: 130, render: (value) => money(value) },
    { title: '订单状态', dataIndex: 'status', width: 165, render: (value) => <AppleIDOrderStatus status={value} /> },
    { title: '账号资料', dataIndex: 'account', width: 360, render: (value) => value ? <Typography.Paragraph className="apple-id-account-text" copyable>{value}</Typography.Paragraph> : '-' },
    { title: '创建时间', dataIndex: 'created_at', width: 180, render: (value) => unixTime(value) },
    { title: '预留到期时间', dataIndex: 'reserved_until', width: 180, render: (value, row) => row.status === 0 ? unixTime(value) : '-' },
    { title: '操作', key: 'actions', width: 200, fixed: 'right', render: (_, row) => <Space size={0}>
      <Button type="link" onClick={() => setSelectedID(row.id)}>详情</Button>
      {row.status === 0 && <AppleIDCancelOrderButton compact id={row.id} tradeNo={row.trade_no} disabled={loading} onChanged={() => load()} />}
    </Space> },
  ];

  return <div className="apple-id-section apple-id-orders-page">
    <Alert className="apple-id-order-intro" style={{ marginBottom: 16 }} type="info" showIcon message="待支付订单预留账号 15 分钟，到期自动取消并释放库存；也可手动取消。" description="后台每分钟检查过期订单。Apple ID 订单的支付与售后不会修改用户套餐、流量或有效期。" />
    <Card className="block-card">
      <div className="forest-table-action">
        <Form className="apple-id-toolbar" form={form} layout="inline" onFinish={(values) => load({ ...page, current: 1 }, { ...values, email: values.email?.trim(), trade_no: values.trade_no?.trim() })} style={{ rowGap: 12 }}>
          <Form.Item name="trade_no" label="订单号"><Input allowClear placeholder="输入订单号" style={{ width: 210 }} /></Form.Item>
          <Form.Item name="email" label="用户邮箱"><Input allowClear placeholder="输入用户邮箱" style={{ width: 200 }} /></Form.Item>
          <Form.Item name="product_id" label="商品"><Select allowClear showSearch optionFilterProp="label" placeholder="全部商品" style={{ width: 210 }} options={products.map((product) => ({ label: `${product.name} (#${product.id})`, value: product.id }))} /></Form.Item>
          <Form.Item name="status" label="状态"><Select allowClear placeholder="全部状态" style={{ width: 170 }} options={appleIDOrderStatuses} /></Form.Item>
          <Form.Item><Space wrap>
            <Button type="primary" htmlType="submit" icon={<SearchOutlined />}>查询</Button>
            <Button onClick={() => { form.resetFields(); load({ ...page, current: 1 }, {}); }}>重置</Button>
            <Button icon={<ReloadOutlined />} onClick={() => load()} loading={loading}>刷新</Button>
          </Space></Form.Item>
        </Form>
      </div>
      {error && <Alert type="error" showIcon message={error} action={<Button size="small" onClick={() => load()}>重试</Button>} />}
      <Table<AppleIDOrder> className="forest-table" rowKey="id" columns={columns} dataSource={rows} loading={loading} scroll={{ x: 1900 }}
        pagination={{ ...page, total, showSizeChanger: true, pageSizeOptions: [20, 50, 100, 200], showTotal: (count) => `共 ${count} 笔订单`, size: 'small' }}
        onChange={(next) => load({ current: next.current || 1, pageSize: next.pageSize || 20 })} />
    </Card>
    {selectedID !== null && <AppleIDOrderDetail key={selectedID} id={selectedID} onClose={() => setSelectedID(null)} onChanged={() => { if (alive.current) load(); }} />}
  </div>;
}
