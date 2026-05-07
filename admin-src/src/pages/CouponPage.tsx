import React, { useEffect, useState } from 'react';
import { Button, DatePicker, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Spin, Switch, Table, Typography, message } from 'antd';
import { PlusOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { apiGet, apiPost } from '../lib/api';

const periodOptions = [
  { label: '月付', value: 'month_price' },
  { label: '季付', value: 'quarter_price' },
  { label: '半年付', value: 'half_year_price' },
  { label: '年付', value: 'year_price' },
  { label: '两年付', value: 'two_year_price' },
  { label: '三年付', value: 'three_year_price' },
  { label: '一次性', value: 'onetime_price' },
  { label: '流量重置包', value: 'reset_price' },
];

function timeRange(row: any) {
  if (!row.started_at || !row.ended_at) return '-';
  return `${dayjs(row.started_at * 1000).format('YYYY/MM/DD HH:mm')} ~ ${dayjs(row.ended_at * 1000).format('YYYY/MM/DD HH:mm')}`;
}

export default function CouponPage() {
  const [rows, setRows] = useState<any[]>([]);
  const [plans, setPlans] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [edit, setEdit] = useState<any>(null);
  const [form] = Form.useForm();
  const type = Form.useWatch('type', form) || 1;
  const generateCount = Form.useWatch('generate_count', form);

  const load = async () => {
    setLoading(true);
    try {
      const [couponRes, planRes] = await Promise.all([
        apiGet('/coupon/fetch', { current: 1, pageSize: 50, sort: 'id', sort_type: 'DESC' }),
        apiGet('/plan/fetch').catch(() => ({ data: [] })),
      ]);
      setRows(couponRes.data || []);
      setPlans(planRes.data || []);
    } catch (e: any) {
      message.error(e.message || '加载失败');
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => { load(); }, []);

  const openEdit = (row: any = { type: 1 }) => {
    setEdit(row);
    form.resetFields();
    form.setFieldsValue({
      ...row,
      valid_range: row.started_at && row.ended_at ? [dayjs(row.started_at * 1000), dayjs(row.ended_at * 1000)] : undefined,
      limit_plan_ids: (row.limit_plan_ids || []).map(Number),
      limit_period: row.limit_period || [],
    });
  };

  const save = async () => {
    setSaving(true);
    try {
      const values = await form.validateFields();
      const range = values.valid_range || [];
      const payload = {
        ...edit,
        ...values,
        started_at: range[0] ? range[0].unix() : undefined,
        ended_at: range[1] ? range[1].unix() : undefined,
      };
      delete payload.valid_range;
      await apiPost('/coupon/generate', payload, { form: true });
      message.success('提交成功');
      setEdit(null);
      load();
    } catch (e: any) {
      if (!e?.errorFields) message.error(e.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };

  const toggle = async (row: any) => {
    await apiPost('/coupon/show', { id: row.id }, { form: true });
    message.success('操作成功');
    load();
  };
  const drop = async (row: any) => {
    await apiPost('/coupon/drop', { id: row.id }, { form: true });
    message.success('已删除');
    load();
  };

  const columns: any[] = [
    { title: '#', dataIndex: 'id', width: 80 },
    { title: '启用', dataIndex: 'show', width: 90, render: (value: any, row: any) => <Switch size="small" checked={!!Number(value)} onChange={() => toggle(row)} /> },
    { title: '券名称', dataIndex: 'name', width: 180 },
    { title: '类型', dataIndex: 'type', width: 90, render: (value: any) => Number(value) === 1 ? '金额' : '比例' },
    { title: '券码', dataIndex: 'code', width: 170, render: (value: any) => <Typography.Text copyable>{value}</Typography.Text> },
    { title: '剩余次数', dataIndex: 'limit_use', width: 110, render: (value: any) => value ?? '无限' },
    { title: '有效期', dataIndex: 'started_at', width: 290, render: (_: any, row: any) => timeRange(row) },
    { title: '操作', dataIndex: 'action', fixed: 'right', align: 'right', width: 120, render: (_: any, row: any) => <Space split={<span className="ant-divider ant-divider-vertical" />}><a onClick={() => openEdit(row)}>编辑</a><Popconfirm title="确定要删除该条项目吗？" onConfirm={() => drop(row)}><a>删除</a></Popconfirm></Space> },
  ];

  return <div className="legacy-page coupon-page">
    <div className="content-heading">优惠券管理</div>
    <Spin spinning={loading}>
      <div className="block border-bottom">
        <div className="bg-white">
          <div className="forest-table-action"><Button icon={<PlusOutlined />} onClick={() => openEdit({ type: 1 })}>添加优惠券</Button></div>
          <Table className="forest-table" tableLayout="auto" dataSource={rows} columns={columns} rowKey="id" scroll={{ x: 1050 }} pagination={{ size: 'small', pageSize: 50, showSizeChanger: true, pageSizeOptions: [10, 50, 100, 150] }} />
        </div>
      </div>
    </Spin>

    <Modal title={edit?.id ? '编辑优惠券' : '新建优惠券'} open={!!edit} onCancel={() => setEdit(null)} onOk={save} okText="提交" confirmLoading={saving} width={720} destroyOnHidden>
      <Form form={form} layout="vertical">
        <Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入优惠券名称' }]}><Input placeholder="请输入优惠券名称" /></Form.Item>
        {!generateCount && <Form.Item name="code" label="自定义优惠券码"><Input placeholder="自定义优惠券码(留空随机生成)" /></Form.Item>}
        <Form.Item label="优惠信息" required>
          <Space.Compact block>
            <Form.Item name="type" noStyle><Select style={{ width: 150 }} options={[{ label: '按金额优惠', value: 1 }, { label: '按比例优惠', value: 2 }]} /></Form.Item>
            <Form.Item name="value" noStyle rules={[{ required: true, message: '请输入值' }]}><InputNumber style={{ width: '100%' }} placeholder="请输入值" addonAfter={Number(type) === 1 ? '¥' : '%'} /></Form.Item>
          </Space.Compact>
        </Form.Item>
        <Form.Item name="valid_range" label="优惠券有效期" rules={[{ required: true, message: '请选择有效期' }]}><DatePicker.RangePicker style={{ width: '100%' }} showTime format="YYYY-MM-DD HH:mm" placeholder={['Start Time', 'End Time']} /></Form.Item>
        <Form.Item name="limit_use" label="最大使用次数"><InputNumber style={{ width: '100%' }} placeholder="限制最大使用次数，用完则无法使用(为空则不限制)" /></Form.Item>
        <Form.Item name="limit_use_with_user" label="每个用户可使用次数"><InputNumber style={{ width: '100%' }} placeholder="限制每个用户可使用次数(为空则不限制)" /></Form.Item>
        <Form.Item name="limit_plan_ids" label="指定订阅"><Select mode="multiple" allowClear placeholder="限制指定订阅可以使用优惠(为空则不限制)" options={plans.map((plan) => ({ label: plan.name, value: plan.id }))} /></Form.Item>
        <Form.Item name="limit_period" label="指定周期"><Select mode="multiple" allowClear placeholder="限制指定周期可以使用优惠(为空则不限制)" options={periodOptions} /></Form.Item>
        {!edit?.id && <Form.Item name="generate_count" label="批量生成数量"><InputNumber style={{ width: '100%' }} placeholder="生成数量最大为500个(留空则不批量生成)" /></Form.Item>}
      </Form>
    </Modal>
  </div>;
}
