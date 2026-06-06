import React, { useEffect, useState } from 'react';
import { Button, Card, Dropdown, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Switch, Table, Tag, Tooltip, message } from 'antd';
import { DeleteOutlined, DownOutlined, EditOutlined, MenuOutlined, PlusOutlined, QuestionCircleOutlined, UserOutlined } from '@ant-design/icons';
import { apiGet, apiPost } from '../lib/api';

function price(v: any) { return v !== null && v !== undefined ? centsToYuan(v).toFixed(2) : '-'; }
function gb(v: any) { return v !== null && v !== undefined ? planTrafficToGB(v).toString() : '-'; }

const trafficGB = 1024 * 1024 * 1024;
const resetOptions = [
  { label: '每月1号', value: 0 },
  { label: '按月重置', value: 1 },
  { label: '不重置', value: 2 },
  { label: '每年1月1日', value: 3 },
  { label: '按年重置', value: 4 },
];

function planTrafficToGB(value: any) {
  if (value === undefined || value === null || value === '') return value;
  const n = Number(value);
  if (!Number.isFinite(n)) return value;
  return n >= trafficGB ? Number((n / trafficGB).toFixed(2)) : n;
}


const priceFields = ['month_price', 'quarter_price', 'half_year_price', 'year_price', 'two_year_price', 'three_year_price', 'onetime_price', 'reset_price'];

function centsToYuan(value: any) {
  if (value === undefined || value === null || value === '') return value;
  const n = Number(value);
  if (!Number.isFinite(n)) return value;
  return Number((n / 100).toFixed(2));
}

function yuanToCents(value: any) {
  if (value === undefined || value === null || value === '') return value;
  const n = Number(value);
  if (!Number.isFinite(n)) return value;
  return Math.round(n * 100);
}

function planToFormValues(row: any = {}) {
  const next = { ...row };
  priceFields.forEach((key) => { next[key] = centsToYuan(next[key]); });
  next.transfer_enable = planTrafficToGB(next.transfer_enable);
  return next;
}

function planFormValuesToPayload(values: any = {}) {
  const next = { ...values };
  priceFields.forEach((key) => { next[key] = yuanToCents(next[key]); });
  return next;
}

export default function PlanPage() {
  const [rows, setRows] = useState<any[]>([]);
  const [groups, setGroups] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [edit, setEdit] = useState<any>(null);
  const [saving, setSaving] = useState(false);
  const [form] = Form.useForm();

  const load = async () => {
    setLoading(true);
    try {
      const [plans, groups] = await Promise.all([apiGet('/plan/fetch'), apiGet('/server/group/fetch').catch(() => ({ data: [] }))]);
      setRows(plans.data || []);
      setGroups(groups.data || []);
    } catch (e: any) { message.error(e.message || '加载失败'); }
    finally { setLoading(false); }
  };
  useEffect(() => { load(); }, []);

  const openEdit = (row: any = {}) => {
    setEdit(row);
    form.resetFields();
    form.setFieldsValue(planToFormValues(row));
  };
  const save = async () => {
    setSaving(true);
    try {
      const values = await form.validateFields();
      await apiPost('/plan/save', { ...edit, ...planFormValuesToPayload(values) });
      message.success('保存成功');
      setEdit(null);
      load();
    } catch (e: any) {
      if (e?.errorFields) {
        message.error('请检查套餐必填项');
      } else {
        message.error(e?.message || '保存失败');
      }
    } finally {
      setSaving(false);
    }
  };
  const update = async (row: any, key: string, value: any) => { await apiPost('/plan/update', { id: row.id, [key]: value }, { form: true }); message.success('已更新'); load(); };
  const drop = async (row: any) => { await apiPost('/plan/drop', { id: row.id }, { form: true }); message.success('已删除'); load(); };

  const columns: any[] = [
    { title: '排序', dataIndex: 'sort', width: 80, render: () => <MenuOutlined className="drag-handle" /> },
    { title: '销售状态', dataIndex: 'show', width: 100, render: (v: any, row: any) => <Switch size="small" checked={!!Number(v)} onClick={() => update(row, 'show', Number(v) ? 0 : 1)} /> },
    { title: <span>续费 <Tooltip title="在订阅停止销售时，已购用户是否可以续费"><QuestionCircleOutlined /></Tooltip></span>, dataIndex: 'renew', width: 90, render: (v: any, row: any) => <Switch size="small" checked={!!Number(v)} onClick={() => update(row, 'renew', Number(v) ? 0 : 1)} /> },
    { title: '名称', dataIndex: 'name', width: 180 },
    { title: '统计', dataIndex: 'count', width: 90, render: (v: any) => <><UserOutlined className="drag-handle" /> {v || 0}</> },
    { title: '流量', dataIndex: 'transfer_enable', width: 100, render: (v: any) => <>{gb(v)} GB</> },
    { title: '设备数限制', dataIndex: 'device_limit', width: 120, render: (v: any) => v ?? '-' },
    { title: '月付', dataIndex: 'month_price', width: 90, render: price },
    { title: '季付', dataIndex: 'quarter_price', width: 90, render: price },
    { title: '半年付', dataIndex: 'half_year_price', width: 90, render: price },
    { title: '年付', dataIndex: 'year_price', width: 90, render: price },
    { title: '两年付', dataIndex: 'two_year_price', width: 90, render: price },
    { title: '三年付', dataIndex: 'three_year_price', width: 90, render: price },
    { title: '一次性', dataIndex: 'onetime_price', width: 90, render: price },
    { title: '重置包', dataIndex: 'reset_price', width: 90, render: price },
    { title: '权限组', dataIndex: 'group_id', width: 130, render: (id: any) => <Tag>{groups.find((group) => Number(group.id) === Number(id))?.name || id}</Tag> },
    { title: '操作', fixed: 'right', align: 'right', width: 120, render: (_: any, row: any) => <Dropdown trigger={['click']} menu={{ items: [
      { key: 'edit', label: <span><EditOutlined /> 编辑</span>, onClick: () => openEdit(row) },
      { key: 'delete', danger: true, label: <Popconfirm title="确认删除？" onConfirm={() => drop(row)}><span><DeleteOutlined /> 删除</span></Popconfirm> },
    ] }}><a>操作 <DownOutlined /></a></Dropdown> },
  ];

  return <div className="legacy-page plan-page">
    <div className="content-heading">订阅管理</div>
    <Card className="block-card" styles={{ body: { padding: 0 } }}>
      <div className="forest-table-action"><Button icon={<PlusOutlined />} onClick={() => openEdit({ show: 1, renew: 1 })}> 添加订阅</Button></div>
      <Table className="forest-table" rowKey="id" loading={loading} tableLayout="auto" dataSource={rows} columns={columns} pagination={false} scroll={{ x: 1500 }} />
    </Card>
    <Modal title={edit?.id ? '编辑订阅' : '添加订阅'} open={!!edit} onCancel={() => setEdit(null)} onOk={save} confirmLoading={saving} width={860}>
      <Form form={form} layout="vertical" className="modal-grid-form">
        <Form.Item name="name" label="名称" rules={[{ required: true }]}><Input /></Form.Item>
        <Form.Item name="group_id" label="权限组" rules={[{ required: true }]}><Select options={groups.map((group) => ({ label: group.name, value: group.id }))} /></Form.Item>
        <Form.Item name="transfer_enable" label="流量(GB)" rules={[{ required: true }]}><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="device_limit" label="设备数限制"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="speed_limit" label="限速 Mbps"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="capacity_limit" label="最大容纳用户"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="month_price" label="月付(元)"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="quarter_price" label="季付(元)"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="half_year_price" label="半年付(元)"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="year_price" label="年付(元)"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="two_year_price" label="两年付(元)"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="three_year_price" label="三年付(元)"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="onetime_price" label="一次性(元)"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="reset_price" label="重置包(元)"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="reset_traffic_method" label="流量重置方式"><Select allowClear placeholder="跟随系统配置" options={resetOptions} /></Form.Item>
        <Form.Item name="show" label="销售状态"><Select options={[{ label: '显示', value: 1 }, { label: '隐藏', value: 0 }]} /></Form.Item>
        <Form.Item name="renew" label="续费"><Select options={[{ label: '允许', value: 1 }, { label: '禁止', value: 0 }]} /></Form.Item>
        <Form.Item name="force_update" label={<Tooltip title="开启后会把当前套餐的权限组、总流量、设备数限制、限速同步到已购买该套餐的用户">强制更新用户 <QuestionCircleOutlined /></Tooltip>} valuePropName="checked"><Switch /></Form.Item>
        <Form.Item name="content" label="说明"><Input.TextArea rows={4} /></Form.Item>
      </Form>
    </Modal>
  </div>;
}
