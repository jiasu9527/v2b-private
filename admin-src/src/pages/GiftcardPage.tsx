import React, { useEffect, useState } from 'react';
import { Button, DatePicker, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Spin, Table, Typography, message } from 'antd';
import { PlusOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { apiGet, apiPost } from '../lib/api';

const giftcardTypes: Record<number, string> = { 1: '金额', 2: '时长', 3: '流量', 4: '重置', 5: '套餐' };
const unitByType: Record<number, string> = { 1: '¥', 2: '天', 3: 'GB', 4: '', 5: '天' };

function timeRange(row: any) {
  if (!row.started_at || !row.ended_at) return '-';
  return `${dayjs(row.started_at * 1000).format('YYYY/MM/DD HH:mm')} ~ ${dayjs(row.ended_at * 1000).format('YYYY/MM/DD HH:mm')}`;
}

function valueText(value: any, row: any) {
  const type = Number(row.type);
  if (type === 4) return '-';
  return `${value ?? 0} ${unitByType[type] || ''}`;
}

export default function GiftcardPage() {
  const [rows, setRows] = useState<any[]>([]);
  const [plans, setPlans] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [edit, setEdit] = useState<any>(null);
  const [form] = Form.useForm();
  const type = Number(Form.useWatch('type', form) || 1);
  const generateCount = Form.useWatch('generate_count', form);

  const load = async () => {
    setLoading(true);
    try {
      const [giftRes, planRes] = await Promise.all([
        apiGet('/giftcard/fetch', { current: 1, pageSize: 50, sort: 'id', sort_type: 'DESC' }),
        apiGet('/plan/fetch').catch(() => ({ data: [] })),
      ]);
      setRows(giftRes.data || []);
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
        value: Number(values.type) === 4 ? 0 : values.value,
        started_at: range[0] ? range[0].unix() : undefined,
        ended_at: range[1] ? range[1].unix() : undefined,
      };
      delete payload.valid_range;
      await apiPost('/giftcard/generate', payload, { form: true });
      message.success('提交成功');
      setEdit(null);
      load();
    } catch (e: any) {
      if (!e?.errorFields) message.error(e.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };

  const drop = async (row: any) => {
    await apiPost('/giftcard/drop', { id: row.id }, { form: true });
    message.success('已删除');
    load();
  };

  const columns: any[] = [
    { title: '#', dataIndex: 'id', width: 80 },
    { title: '名称', dataIndex: 'name', width: 180 },
    { title: '类型', dataIndex: 'type', width: 90, render: (value: any) => giftcardTypes[Number(value)] || '' },
    { title: '数值', dataIndex: 'value', width: 100, render: valueText },
    { title: '套餐', dataIndex: 'plan_id', width: 160, render: (id: any) => plans.find((plan) => Number(plan.id) === Number(id))?.name || id || '-' },
    { title: '卡密', dataIndex: 'code', width: 210, render: (value: any) => <Typography.Text copyable>{value}</Typography.Text> },
    { title: '剩余次数', dataIndex: 'limit_use', width: 110, render: (value: any) => value ?? '无限' },
    { title: '有效期', dataIndex: 'started_at', width: 290, render: (_: any, row: any) => timeRange(row) },
    { title: '操作', dataIndex: 'action', fixed: 'right', align: 'right', width: 120, render: (_: any, row: any) => <Space split={<span className="ant-divider ant-divider-vertical" />}><a onClick={() => openEdit(row)}>编辑</a><Popconfirm title="确定要删除该条项目吗？" onConfirm={() => drop(row)}><a>删除</a></Popconfirm></Space> },
  ];

  return <div className="legacy-page giftcard-page">
    <div className="content-heading">礼品卡管理</div>
    <Spin spinning={loading}>
      <div className="block border-bottom">
        <div className="bg-white">
          <div className="forest-table-action"><Button icon={<PlusOutlined />} onClick={() => openEdit({ type: 1 })}>添加礼品卡</Button></div>
          <Table className="forest-table" tableLayout="auto" dataSource={rows} columns={columns} rowKey="id" scroll={{ x: 1050 }} pagination={{ size: 'small', pageSize: 50, showSizeChanger: true, pageSizeOptions: [10, 50, 100, 150] }} />
        </div>
      </div>
    </Spin>

    <Modal title={edit?.id ? '编辑礼品卡' : '新建礼品卡'} open={!!edit} onCancel={() => setEdit(null)} onOk={save} okText="提交" confirmLoading={saving} width={720} destroyOnHidden>
      <Form form={form} layout="vertical">
        <Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入礼品卡名称' }]}><Input placeholder="请输入礼品卡名称" /></Form.Item>
        {!generateCount && <Form.Item name="code" label="自定义礼品卡卡密"><Input placeholder="自定义礼品卡卡密(留空随机生成)" /></Form.Item>}
        <Form.Item label="礼品卡类型" required>
          <Space.Compact block>
            <Form.Item name="type" noStyle><Select style={{ width: 170 }} options={[
              { label: '增加账户余额', value: 1 },
              { label: '增加订阅时长', value: 2 },
              { label: '增加套餐流量', value: 3 },
              { label: '重置套餐流量', value: 4 },
              { label: '兑换订阅套餐', value: 5 },
            ]} /></Form.Item>
            <Form.Item name="value" noStyle rules={type === 4 ? [] : [{ required: true, message: '请输入值' }]}><InputNumber disabled={type === 4} style={{ width: '100%' }} placeholder={type === 5 ? '一次性套餐输入0' : '请输入值'} addonAfter={unitByType[type] || ''} /></Form.Item>
          </Space.Compact>
        </Form.Item>
        {type === 5 && <Form.Item name="plan_id" label="兑换订阅套餐" rules={[{ required: true, message: '请选择订阅套餐' }]}><Select placeholder="请选择订阅套餐" options={plans.map((plan) => ({ label: plan.name, value: plan.id }))} /></Form.Item>}
        <Form.Item name="valid_range" label="礼品卡有效期" rules={[{ required: true, message: '请选择有效期' }]}><DatePicker.RangePicker style={{ width: '100%' }} showTime format="YYYY-MM-DD HH:mm" placeholder={['Start Time', 'End Time']} /></Form.Item>
        <Form.Item name="limit_use" label="最大使用次数"><InputNumber style={{ width: '100%' }} placeholder="限制最大使用次数，用完则无法使用(为空则不限制)" /></Form.Item>
        {!edit?.id && <Form.Item name="generate_count" label="批量生成数量"><InputNumber style={{ width: '100%' }} placeholder="生成数量最大为500个(留空则不批量生成)" /></Form.Item>}
      </Form>
    </Modal>
  </div>;
}
