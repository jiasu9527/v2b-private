import React, { useEffect, useMemo, useState } from 'react';
import { Button, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Spin, Switch, Table, Tooltip, message } from 'antd';
import { MenuOutlined, PlusOutlined, QuestionCircleOutlined } from '@ant-design/icons';
import { apiGet, apiPost } from '../lib/api';

type PaymentFormField = {
  label?: string;
  description?: string;
  type?: string;
  value?: string;
};

export default function PaymentPage() {
  const [rows, setRows] = useState<any[]>([]);
  const [methods, setMethods] = useState<string[]>([]);
  const [gatewayForm, setGatewayForm] = useState<Record<string, PaymentFormField>>({});
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [edit, setEdit] = useState<any>(null);
  const [form] = Form.useForm();
  const selectedGateway = Form.useWatch('payment', form);

  const load = async () => {
    setLoading(true);
    try {
      const [paymentRes, methodRes] = await Promise.all([
        apiGet('/payment/fetch'),
        apiGet('/payment/getPaymentMethods').catch(() => ({ data: [] })),
      ]);
      setRows(paymentRes.data || []);
      setMethods(methodRes.data || []);
    } catch (e: any) {
      message.error(e.message || '加载失败');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { load(); }, []);

  const loadGatewayForm = async (payment: string, row?: any) => {
    if (!payment) {
      setGatewayForm({});
      return;
    }
    try {
      const res = await apiGet('/payment/getPaymentForm', { payment, id: row?.id });
      const next = res.data || {};
      setGatewayForm(next);
      const values: any = { config: {} };
      Object.entries(next).forEach(([key, field]: any) => {
        values.config[key] = row?.config?.[key] ?? field?.value ?? '';
      });
      form.setFieldsValue(values);
    } catch (e: any) {
      setGatewayForm({});
      message.error(e.message || '加载支付表单失败');
    }
  };

  const openEdit = async (row: any = {}) => {
    const payment = row.payment || methods[0] || '';
    setEdit(row);
    setGatewayForm({});
    form.resetFields();
    form.setFieldsValue({
      ...row,
      payment,
      config: row.config || {},
      handling_fee_fixed: row.handling_fee_fixed ?? undefined,
      handling_fee_percent: row.handling_fee_percent ?? undefined,
    });
    await loadGatewayForm(payment, row);
  };

  const save = async () => {
    setSaving(true);
    try {
      const values = await form.validateFields();
      await apiPost('/payment/save', {
        ...edit,
        ...values,
        config: values.config || {},
      });
      message.success('保存成功');
      setEdit(null);
      form.resetFields();
      load();
    } catch (e: any) {
      if (!e?.errorFields) message.error(e.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };

  const toggle = async (row: any) => {
    await apiPost('/payment/show', { id: row.id }, { form: true });
    message.success('操作成功');
    load();
  };

  const drop = async (row: any) => {
    await apiPost('/payment/drop', { id: row.id }, { form: true });
    message.success('已删除');
    load();
  };

  const gatewayKeys = useMemo(() => Object.keys(gatewayForm), [gatewayForm]);

  const columns: any[] = [
    { title: 'ID', dataIndex: 'id', width: 90, render: (id: any) => <><MenuOutlined className="drag-handle" /> {id}</> },
    { title: '启用', dataIndex: 'enable', width: 90, render: (value: any, row: any) => <Switch size="small" checked={!!Number(value)} onChange={() => toggle(row)} /> },
    { title: '显示名称', dataIndex: 'name', width: 180 },
    { title: '支付接口', dataIndex: 'payment', width: 160 },
    { title: <span>通知地址 <Tooltip title="支付网关将会把数据通知到本地址，请通过防火墙放行本地址。"><QuestionCircleOutlined /></Tooltip></span>, dataIndex: 'notify_url', width: 420, ellipsis: true },
    { title: '操作', dataIndex: 'action', fixed: 'right', align: 'right', width: 120, render: (_: any, row: any) => <Space split={<span className="ant-divider ant-divider-vertical" />}>
      <a onClick={() => openEdit(row)}>编辑</a>
      <Popconfirm title="确定要删除该条项目吗？" onConfirm={() => drop(row)}><a>删除</a></Popconfirm>
    </Space> },
  ];

  return <div className="legacy-page payment-page">
    <div className="content-heading">支付配置</div>
    <Spin spinning={loading}>
      <div className="block block-rounded">
        <div className="bg-white">
          <div className="forest-table-action"><Button icon={<PlusOutlined />} onClick={() => openEdit({})}>添加支付方式</Button></div>
          <Table className="forest-table" tableLayout="auto" rowKey="id" dataSource={rows} columns={columns} pagination={false} scroll={{ x: 1300 }} />
        </div>
      </div>
    </Spin>

    <Modal title={edit?.id ? '编辑支付方式' : '添加支付方式'} open={!!edit} onCancel={() => setEdit(null)} onOk={save} okText={edit?.id ? '保存' : '添加'} confirmLoading={saving} width={760} destroyOnHidden>
      <Form form={form} layout="vertical">
        <Form.Item name="name" label="显示名称" rules={[{ required: true, message: '请输入显示名称' }]}><Input placeholder="用于前端显示使用" /></Form.Item>
        <Form.Item name="icon" label="图标URL(选填)"><Input placeholder="用于前端显示使用(https://x.com/icon.svg)" /></Form.Item>
        <Form.Item name="notify_domain" label="自定义通知域名(选填)"><Input placeholder="网关的通知将会发送到该域名(https://x.com)" /></Form.Item>
        <div className="modal-grid-form">
          <Form.Item name="handling_fee_percent" label="百分比手续费(选填)"><InputNumber addonAfter="%" style={{ width: '100%' }} placeholder="在订单金额基础上附加手续费" /></Form.Item>
          <Form.Item name="handling_fee_fixed" label="固定手续费(选填)"><InputNumber addonAfter="分" style={{ width: '100%' }} placeholder="固定附加手续费" /></Form.Item>
        </div>
        <Form.Item name="payment" label="网关" rules={[{ required: true, message: '请选择网关' }]}>
          <Select placeholder="请选择网关" options={methods.map((item) => ({ label: item, value: item }))} onChange={(value) => loadGatewayForm(value, edit)} />
        </Form.Item>
        {gatewayKeys.map((key) => {
          const item = gatewayForm[key] || {};
          return <Form.Item key={key} name={['config', key]} label={item.label || key} rules={[{ required: true, message: `请输入${item.label || key}` }]}>
            <Input placeholder={item.description || item.label || key} />
          </Form.Item>;
        })}
        {selectedGateway === 'MGate' && <div className="legacy-warning">MGate TG@nulledsan</div>}
      </Form>
    </Modal>
  </div>;
}
