import React, { useEffect, useState } from 'react';
import { Button, Card, Dropdown, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Switch, Table, Tag, Tooltip, message } from 'antd';
import { DeleteOutlined, DownOutlined, EditOutlined, MenuOutlined, PlusOutlined, QuestionCircleOutlined, UserOutlined } from '@ant-design/icons';
import { apiGet, apiPost } from '../lib/api';

function price(v: any) { return v !== null && v !== undefined ? (Number(v || 0) / 100).toFixed(2) : '-'; }
function gb(v: any) { return v !== null && v !== undefined ? Number(v || 0).toString() : '-'; }

export default function PlanPage() {
  const [rows, setRows] = useState<any[]>([]);
  const [groups, setGroups] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [edit, setEdit] = useState<any>(null);
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
    form.setFieldsValue({ ...row });
  };
  const save = async () => {
    const values = await form.validateFields();
    await apiPost('/plan/save', { ...edit, ...values });
    message.success('保存成功'); setEdit(null); load();
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
    <Modal title={edit?.id ? '编辑订阅' : '添加订阅'} open={!!edit} onCancel={() => setEdit(null)} onOk={save} width={860}>
      <Form form={form} layout="vertical" className="modal-grid-form">
        <Form.Item name="name" label="名称" rules={[{ required: true }]}><Input /></Form.Item>
        <Form.Item name="group_id" label="权限组" rules={[{ required: true }]}><Select options={groups.map((group) => ({ label: group.name, value: group.id }))} /></Form.Item>
        <Form.Item name="transfer_enable" label="流量(GB)" rules={[{ required: true }]}><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="device_limit" label="设备数限制"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="speed_limit" label="限速 Mbps"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="capacity_limit" label="最大容纳用户"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="month_price" label="月付(分)"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="quarter_price" label="季付(分)"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="half_year_price" label="半年付(分)"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="year_price" label="年付(分)"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="two_year_price" label="两年付(分)"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="three_year_price" label="三年付(分)"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="onetime_price" label="一次性(分)"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="reset_price" label="重置包(分)"><InputNumber style={{ width: '100%' }} /></Form.Item>
        <Form.Item name="show" label="销售状态"><Select options={[{ label: '显示', value: 1 }, { label: '隐藏', value: 0 }]} /></Form.Item>
        <Form.Item name="renew" label="续费"><Select options={[{ label: '允许', value: 1 }, { label: '禁止', value: 0 }]} /></Form.Item>
        <Form.Item name="content" label="说明"><Input.TextArea rows={4} /></Form.Item>
      </Form>
    </Modal>
  </div>;
}
