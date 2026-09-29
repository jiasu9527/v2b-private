import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Checkbox, Descriptions, Divider, Form, Input, Modal, Radio, Skeleton, Space, Table, Tag, Typography, message } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { CopyOutlined, EyeOutlined, ReloadOutlined } from '@ant-design/icons';
import { apiGet, apiPost, money, unixTime } from '../lib/api';

export type AppleIDOrder = {
  id: number; business_type: string; user_id: number; user_email: string; trade_no: string;
  product_id: number; product_name: string; region: string; price: number; handling_amount: number;
  total_amount: number; status: number; payment_id?: number; callback_no?: string; inventory_id?: number;
  account?: string; reserved_until?: number; paid_at?: number; created_at: number; updated_at: number;
};
type Credentials = { order_id: number; trade_no: string; inventory_id: number; product_id: number; account: string; password: string; credential?: string };
type Audit = { id: number; actor_admin_id?: number; action: string; detail: { reason?: string; inventory_id?: number; previous_inventory_id?: number; new_inventory_id?: number; status?: number; account?: string }; created_at: number };
type Inventory = { id: number; account: string; created_at: number };
type Page = { current: number; pageSize: number };

export const appleIDOrderStatuses = [
  { value: 0, label: '待支付' }, { value: 1, label: '已支付并发货' }, { value: 2, label: '已取消 / 已过期' },
  { value: 3, label: '已确认退款' }, { value: 4, label: '已支付待人工处理' },
];
const statusColors = ['processing', 'success', 'default', 'default', 'warning'];
export function AppleIDOrderStatus({ status }: { status: number }) {
  return <Tag color={statusColors[status]}>{appleIDOrderStatuses.find((item) => item.value === status)?.label || `状态 ${status}`}</Tag>;
}
const auditActions: Record<string, string> = {
  created: '创建订单', paid: '支付并发货', manual_paid: '人工确认支付', late_payment: '迟到付款待处理',
  delivery_view: '用户查看交付', credential_view: '管理员查看凭据', inventory_replace: '售后换号',
  refund_confirmed: '确认外部退款', cancel: '用户取消订单', reservation_expired: '预留超时释放',
};
function auditActor(row: Audit) {
  if (!row.actor_admin_id) return '系统';
  return `${['created', 'delivery_view', 'cancel'].includes(row.action) ? '用户' : '管理员'} #${row.actor_admin_id}`;
}
function auditDetail(detail: Audit['detail']) {
  const fragments = [
    detail?.reason,
    detail?.inventory_id ? `库存 #${detail.inventory_id}` : '',
    detail?.previous_inventory_id ? `原库存 #${detail.previous_inventory_id}` : '',
    detail?.new_inventory_id ? `新库存 #${detail.new_inventory_id}` : '',
    detail?.account ? `账号：${detail.account}` : '',
    detail?.status !== undefined ? `状态：${appleIDOrderStatuses.find((item) => item.value === detail.status)?.label || detail.status}` : '',
  ].filter(Boolean);
  return fragments.length ? fragments.join('；') : '-';
}

export default function AppleIDOrderDetail({ id, onClose, onChanged }: { id: number; onClose: () => void; onChanged: () => void }) {
  const [order, setOrder] = useState<AppleIDOrder | null>(null);
  const [detailLoading, setDetailLoading] = useState(true);
  const [detailError, setDetailError] = useState('');
  const [credentials, setCredentials] = useState<Credentials | null>(null);
  const [credentialsLoading, setCredentialsLoading] = useState(false);
  const [passwordVisible, setPasswordVisible] = useState(false);
  const [audits, setAudits] = useState<Audit[]>([]);
  const [auditTotal, setAuditTotal] = useState(0);
  const [auditLoading, setAuditLoading] = useState(false);
  const [auditError, setAuditError] = useState('');
  const [auditPage, setAuditPage] = useState<Page>({ current: 1, pageSize: 20 });
  const [replaceOpen, setReplaceOpen] = useState(false);
  const [refundOpen, setRefundOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [replaceForm] = Form.useForm();
  const [refundForm] = Form.useForm();
  const [allocation, setAllocation] = useState('auto');
  const [inventoryID, setInventoryID] = useState<number | undefined>();
  const [inventory, setInventory] = useState<Inventory[]>([]);
  const [inventoryTotal, setInventoryTotal] = useState(0);
  const [inventoryPage, setInventoryPage] = useState<Page>({ current: 1, pageSize: 20 });
  const [inventoryLoading, setInventoryLoading] = useState(false);
  const [inventoryError, setInventoryError] = useState('');
  const alive = useRef(true);
  const detailRequest = useRef(0);
  const credentialRequest = useRef(0);
  const auditRequest = useRef(0);
  const inventoryRequest = useRef(0);
  const mutationPending = useRef(false);

  const clearCredentials = () => {
    credentialRequest.current += 1;
    setCredentials(null);
    setPasswordVisible(false);
    setCredentialsLoading(false);
  };
  const loadDetail = async () => {
    const token = ++detailRequest.current;
    setDetailLoading(true);
    setDetailError('');
    try {
      const res = await apiGet('/apple-id/order/detail', { id });
      if (!alive.current || token !== detailRequest.current) return;
      setOrder(res.data.order);
    } catch (e: any) {
      if (!alive.current || token !== detailRequest.current) return;
      setOrder(null);
      setDetailError(e.message || '订单详情加载失败');
    } finally {
      if (alive.current && token === detailRequest.current) setDetailLoading(false);
    }
  };
  const loadAudits = async (nextPage = auditPage) => {
    const token = ++auditRequest.current;
    setAuditLoading(true);
    setAuditError('');
    setAuditPage(nextPage);
    try {
      const res = await apiGet('/apple-id/order/audits', { id, current: nextPage.current, page_size: nextPage.pageSize });
      if (!alive.current || token !== auditRequest.current) return;
      setAudits(res.data || []);
      setAuditTotal(Number(res.total) || 0);
    } catch (e: any) {
      if (!alive.current || token !== auditRequest.current) return;
      setAudits([]);
      setAuditTotal(0);
      setAuditError(e.message || '审计记录加载失败');
    } finally {
      if (alive.current && token === auditRequest.current) setAuditLoading(false);
    }
  };
  const loadInventory = async (nextPage: Page) => {
    if (!order) return;
    const token = ++inventoryRequest.current;
    setInventoryLoading(true);
    setInventoryError('');
    setInventoryPage(nextPage);
    try {
      const res = await apiGet('/apple-id/inventory/fetch', { product_id: order.product_id, status: 0, current: nextPage.current, page_size: nextPage.pageSize });
      if (!alive.current || token !== inventoryRequest.current) return;
      setInventory(res.data || []);
      setInventoryTotal(Number(res.total) || 0);
    } catch (e: any) {
      if (!alive.current || token !== inventoryRequest.current) return;
      setInventory([]);
      setInventoryTotal(0);
      setInventoryError(e.message || '可售库存加载失败');
    } finally {
      if (alive.current && token === inventoryRequest.current) setInventoryLoading(false);
    }
  };

  useEffect(() => {
    alive.current = true;
    loadDetail();
    loadAudits();
    return () => {
      alive.current = false;
      detailRequest.current += 1;
      credentialRequest.current += 1;
      auditRequest.current += 1;
      inventoryRequest.current += 1;
    };
  }, [id]);

  const refresh = () => { clearCredentials(); loadDetail(); loadAudits({ ...auditPage, current: 1 }); };
  const viewCredentials = async () => {
    if (credentialsLoading || busy) return;
    const token = ++credentialRequest.current;
    setCredentialsLoading(true);
    try {
      const res = await apiPost('/apple-id/order/credentials', { id });
      if (!alive.current || token !== credentialRequest.current) return;
      if (res.data?.order_id !== id) throw new Error('凭据与当前订单不匹配');
      setCredentials(res.data);
      setPasswordVisible(false);
      loadAudits({ ...auditPage, current: 1 });
    } catch (e: any) {
      if (alive.current && token === credentialRequest.current) message.error(e.message || '查看凭据失败');
    } finally {
      if (alive.current && token === credentialRequest.current) setCredentialsLoading(false);
    }
  };
  const copy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      if (alive.current) message.success('已复制');
    } catch {
      if (alive.current) message.warning('浏览器未允许自动复制，请手动选择并复制');
    }
  };
  const openReplacement = () => {
    clearCredentials();
    replaceForm.resetFields();
    setAllocation('auto');
    setInventoryID(undefined);
    setInventory([]);
    setReplaceOpen(true);
    loadInventory({ current: 1, pageSize: 20 });
  };
  const closeReplacement = () => {
    if (mutationPending.current) return;
    inventoryRequest.current += 1;
    setReplaceOpen(false);
    setInventory([]);
    setInventoryID(undefined);
    replaceForm.resetFields();
  };
  const submitReplacement = async () => {
    if (mutationPending.current) return;
    let values: any;
    try { values = await replaceForm.validateFields(); } catch { return; }
    if (mutationPending.current || !alive.current) return;
    if (allocation === 'manual' && !inventoryID) { message.error('请选择同商品的可售库存'); return; }
    mutationPending.current = true;
    setBusy(true);
    clearCredentials();
    try {
      await apiPost('/apple-id/order/replace', { id, reason: values.reason.trim(), ...(allocation === 'manual' ? { inventory_id: inventoryID } : {}) });
      onChanged();
      if (!alive.current) return;
      message.success('换号成功，旧账号已停用');
      setReplaceOpen(false);
      setInventory([]);
      await Promise.all([loadDetail(), loadAudits({ ...auditPage, current: 1 })]);
    } catch (e: any) {
      if (alive.current) message.error(e.message || '换号失败，请刷新可售库存后重试');
    } finally {
      mutationPending.current = false;
      if (alive.current) setBusy(false);
    }
  };
  const openRefund = () => { clearCredentials(); refundForm.resetFields(); setRefundOpen(true); };
  const submitRefund = async () => {
    if (mutationPending.current) return;
    let values: any;
    try { values = await refundForm.validateFields(); } catch { return; }
    if (mutationPending.current || !alive.current) return;
    mutationPending.current = true;
    setBusy(true);
    clearCredentials();
    try {
      await apiPost('/apple-id/order/refund', { id, reason: values.reason.trim() });
      onChanged();
      if (!alive.current) return;
      message.success('已记录外部退款结果');
      setRefundOpen(false);
      await Promise.all([loadDetail(), loadAudits({ ...auditPage, current: 1 })]);
    } catch (e: any) {
      if (alive.current) message.error(e.message || '退款确认失败');
    } finally {
      mutationPending.current = false;
      if (alive.current) setBusy(false);
    }
  };

  const auditColumns: ColumnsType<Audit> = [
    { title: '时间', dataIndex: 'created_at', width: 180, render: (value) => unixTime(value) },
    { title: '操作', dataIndex: 'action', width: 160, render: (value) => auditActions[value] || value },
    { title: '操作人', key: 'actor', width: 120, render: (_, row) => auditActor(row) },
    { title: '记录', dataIndex: 'detail', render: (value) => <span style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{auditDetail(value)}</span> },
  ];
  const locked = busy || detailLoading || credentialsLoading;
  const canView = !!order?.inventory_id && (order.status === 1 || order.status === 3);

  return <>
    <Modal className="apple-id-detail" open title="Apple ID 订单详情" onCancel={() => { clearCredentials(); onClose(); }} width={1040} footer={<Button onClick={() => { clearCredentials(); onClose(); }}>关闭</Button>} destroyOnHidden>
      <Space direction="vertical" style={{ width: '100%' }} size={16}>
        <div><Button icon={<ReloadOutlined />} onClick={refresh} disabled={busy || credentialsLoading} loading={detailLoading}>刷新详情</Button></div>
        {detailError && <Alert showIcon type="error" message={detailError} action={<Button size="small" onClick={refresh}>重试</Button>} />}
        {detailLoading && !order && <Skeleton active paragraph={{ rows: 7 }} />}
        {order && <>
          {order.status === 4 && <Alert type="warning" showIcon message="已收到付款，尚未发货" description="此订单需要人工跟进。请核实支付流水与交付情况；如已在支付渠道退款，可在此确认退款结果。" />}
          <Descriptions bordered size="small" column={{ xs: 1, sm: 2 }}>
            <Descriptions.Item label="订单号" span={2}><Typography.Text copyable>{order.trade_no}</Typography.Text></Descriptions.Item>
            <Descriptions.Item label="业务类型">独享 Apple ID</Descriptions.Item><Descriptions.Item label="订单状态"><AppleIDOrderStatus status={order.status} /></Descriptions.Item>
            <Descriptions.Item label="购买用户">{order.user_email || '用户已删除'}（#{order.user_id}）</Descriptions.Item><Descriptions.Item label="订单 ID">{order.id}</Descriptions.Item>
            <Descriptions.Item label="商品快照">{order.product_name}（#{order.product_id}）</Descriptions.Item><Descriptions.Item label="地区快照">{order.region || '-'}</Descriptions.Item>
            <Descriptions.Item label="商品金额">{money(order.price)}</Descriptions.Item><Descriptions.Item label="支付手续费">{money(order.handling_amount)}</Descriptions.Item>
            <Descriptions.Item label="应付金额">{money(order.total_amount)}</Descriptions.Item><Descriptions.Item label="支付方式 ID">{order.payment_id || '-'}</Descriptions.Item>
            <Descriptions.Item label="支付流水" span={2}><span style={{ overflowWrap: 'anywhere' }}>{order.callback_no || '-'}</span></Descriptions.Item>
            <Descriptions.Item label="库存 ID">{order.inventory_id || '-'}</Descriptions.Item><Descriptions.Item label="账号（脱敏）">{order.account || '-'}</Descriptions.Item>
            <Descriptions.Item label="创建时间">{unixTime(order.created_at)}</Descriptions.Item><Descriptions.Item label="预留到期时间">{unixTime(order.reserved_until)}</Descriptions.Item>
            <Descriptions.Item label="付款时间">{unixTime(order.paid_at)}</Descriptions.Item><Descriptions.Item label="更新时间">{unixTime(order.updated_at)}</Descriptions.Item>
          </Descriptions>
          <Space wrap>
            <Button icon={<EyeOutlined />} onClick={viewCredentials} loading={credentialsLoading} disabled={!canView || busy || detailLoading || !!credentials}>查看账号凭据</Button>
            {credentials && <Button onClick={clearCredentials}>清除明文</Button>}
            <Button disabled={locked || order.status !== 1} onClick={openReplacement}>售后换号</Button>
            <Button danger disabled={locked || ![1, 4].includes(order.status)} onClick={openRefund}>确认外部退款</Button>
          </Space>
          <Typography.Text type="secondary">查看账号凭据会留下审计记录。关闭详情、刷新或进行售后操作时会清除当前页面的明文。</Typography.Text>
          {credentials && <div className="apple-id-credentials">
            <Form layout="vertical" autoComplete="off">
              {credentials.credential ? <Form.Item label="交付资料" style={{ marginBottom: 0 }}><Space.Compact style={{ width: '100%', alignItems: 'stretch' }}><Input.TextArea readOnly value={credentials.credential} autoComplete="off" aria-label="交付资料" autoSize={{ minRows: 3, maxRows: 12 }} /><Button icon={<CopyOutlined />} onClick={() => copy(credentials.credential || '')}>复制资料</Button></Space.Compact></Form.Item> : <>
                <Form.Item label="交付账号"><Space.Compact style={{ width: '100%' }}><Input readOnly value={credentials.account} autoComplete="off" aria-label="交付账号" /><Button icon={<CopyOutlined />} onClick={() => copy(credentials.account)}>复制账号</Button></Space.Compact></Form.Item>
                <Form.Item label="账号密码" style={{ marginBottom: 0 }}><Space.Compact style={{ width: '100%' }}><Input.Password readOnly value={credentials.password} autoComplete="new-password" aria-label="账号密码" visibilityToggle={{ visible: passwordVisible, onVisibleChange: setPasswordVisible }} /><Button icon={<CopyOutlined />} onClick={() => copy(credentials.password)}>复制密码</Button></Space.Compact></Form.Item>
              </>}
            </Form>
          </div>}
        </>}
        <Divider orientation="left" style={{ margin: '8px 0' }}>操作审计</Divider>
        {auditError && <Alert showIcon type="error" message={auditError} action={<Button size="small" onClick={() => loadAudits()}>重试</Button>} />}
        <Table<Audit> className="forest-table" size="small" rowKey="id" dataSource={audits} columns={auditColumns} loading={auditLoading} scroll={{ x: 760 }}
          pagination={{ ...auditPage, total: auditTotal, showSizeChanger: true, pageSizeOptions: [20, 50, 100, 200], size: 'small', showTotal: (count) => `共 ${count} 条记录` }}
          onChange={(next) => loadAudits({ current: next.current || 1, pageSize: next.pageSize || 20 })} />
      </Space>
    </Modal>
    <Modal open={replaceOpen} title="售后换号" width={760} onCancel={closeReplacement} onOk={submitReplacement} confirmLoading={busy} cancelButtonProps={{ disabled: busy }} okText="确认换号并停用旧号" cancelText="取消" closable={!busy} maskClosable={!busy} keyboard={!busy} destroyOnHidden>
      <Alert type="warning" showIcon message="换号成功后，旧账号会永久退出可售库存，新账号立即交付给此订单。" description={`订单：${order?.trade_no || ''}；仅可选择同一商品的可售库存。`} style={{ marginBottom: 16 }} />
      <Form form={replaceForm} layout="vertical">
        <Form.Item label="分配方式"><Radio.Group value={allocation} onChange={(event) => setAllocation(event.target.value)} disabled={busy}><Radio value="auto">自动分配可售账号</Radio><Radio value="manual">指定可售账号</Radio></Radio.Group></Form.Item>
        {allocation === 'manual' && <Form.Item label={`选择库存${inventoryID ? `（已选择 #${inventoryID}）` : ''}`}>
          {inventoryError && <Alert type="error" showIcon message={inventoryError} />}
          <Button size="small" icon={<ReloadOutlined />} onClick={() => { setInventoryID(undefined); loadInventory(inventoryPage); }} disabled={busy} style={{ marginBottom: 8 }}>刷新可售库存</Button>
          <Table<Inventory> className="forest-table" size="small" rowKey="id" dataSource={inventory} loading={inventoryLoading}
            columns={[{ title: '库存 ID', dataIndex: 'id', width: 100 }, { title: '账号（脱敏）', dataIndex: 'account' }, { title: '入库时间', dataIndex: 'created_at', render: (value) => unixTime(value) }]}
            rowSelection={{ type: 'radio', selectedRowKeys: inventoryID ? [inventoryID] : [], preserveSelectedRowKeys: true, onChange: (keys) => setInventoryID(Number(keys[0])), getCheckboxProps: () => ({ disabled: busy }) }}
            pagination={{ ...inventoryPage, total: inventoryTotal, showSizeChanger: false, size: 'small', showTotal: (count) => `共 ${count} 个可售账号` }}
            onChange={(next) => loadInventory({ current: next.current || 1, pageSize: next.pageSize || 20 })} />
        </Form.Item>}
        <Form.Item name="reason" label="换号原因" rules={[{ required: true, whitespace: true, message: '请填写换号原因' }]}><Input.TextArea rows={3} maxLength={1000} showCount disabled={busy} placeholder="例如：用户反馈无法登录，已核实并更换账号" /></Form.Item>
      </Form>
    </Modal>
    <Modal open={refundOpen} title="确认外部退款" onCancel={() => { if (!busy) setRefundOpen(false); }} onOk={submitRefund} confirmLoading={busy} okButtonProps={{ danger: true }} cancelButtonProps={{ disabled: busy }} okText="记录已完成退款" cancelText="取消" closable={!busy} maskClosable={!busy} keyboard={!busy} destroyOnHidden>
      <Alert type="warning" showIcon message="请先在支付渠道完成退款" description="此操作仅记录退款结果，不会发起资金退回。确认后订单将标记为已退款，已交付账号不会重新上架。" style={{ marginBottom: 16 }} />
      <p>订单：{order?.trade_no}<br />订单金额：{money(order?.total_amount)}</p>
      <Form form={refundForm} layout="vertical">
        <Form.Item name="reason" label="退款原因与处理记录" rules={[{ required: true, whitespace: true, message: '请填写退款原因与处理记录' }]}><Input.TextArea rows={3} maxLength={1000} showCount disabled={busy} placeholder="填写退款原因及支付渠道处理结果，不要录入账号密码" /></Form.Item>
        <Form.Item name="confirmed" valuePropName="checked" rules={[{ validator: (_, value) => value ? Promise.resolve() : Promise.reject(new Error('请确认已在支付渠道完成退款')) }]}><Checkbox disabled={busy}>我已在支付渠道完成该订单退款</Checkbox></Form.Item>
      </Form>
    </Modal>
  </>;
}
