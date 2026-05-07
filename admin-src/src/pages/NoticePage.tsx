import React, { useEffect, useState } from 'react';
import { Button, Form, Input, Modal, Popconfirm, Select, Space, Spin, Switch, Table, message } from 'antd';
import { PlusOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { apiGet, apiPost } from '../lib/api';

export default function NoticePage() {
  const [rows, setRows] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [edit, setEdit] = useState<any>(null);
  const [form] = Form.useForm();

  const load = async () => {
    setLoading(true);
    try {
      const res = await apiGet('/notice/fetch');
      setRows(res.data || []);
    } catch (e: any) {
      message.error(e.message || '加载失败');
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => { load(); }, []);

  const openEdit = (row: any = {}) => {
    setEdit(row);
    form.resetFields();
    form.setFieldsValue({ ...row, tags: row.tags || [] });
  };

  const save = async () => {
    setSaving(true);
    try {
      const values = await form.validateFields();
      await apiPost('/notice/save', { ...edit, ...values });
      message.success('保存成功');
      setEdit(null);
      load();
    } catch (e: any) {
      if (!e?.errorFields) message.error(e.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };

  const toggle = async (row: any) => {
    await apiPost('/notice/show', { id: row.id }, { form: true });
    message.success('操作成功');
    load();
  };

  const drop = async (row: any) => {
    await apiPost('/notice/drop', { id: row.id }, { form: true });
    message.success('已删除');
    load();
  };

  const columns: any[] = [
    { title: '#', dataIndex: 'id', width: 80 },
    { title: '显示', dataIndex: 'show', width: 90, render: (value: any, row: any) => <Switch size="small" checked={!!Number(value)} onChange={() => toggle(row)} /> },
    { title: '标题', dataIndex: 'title' },
    { title: '创建时间', dataIndex: 'created_at', width: 180, render: (value: any) => value ? dayjs(value * 1000).format('YYYY/MM/DD HH:mm') : '-' },
    { title: '操作', dataIndex: 'action', align: 'right', width: 120, render: (_: any, row: any) => <Space split={<span className="ant-divider ant-divider-vertical" />}><a onClick={() => openEdit(row)}>编辑</a><Popconfirm title="确定要删除该条项目吗？" onConfirm={() => drop(row)}><a>删除</a></Popconfirm></Space> },
  ];

  return <div className="legacy-page notice-page">
    <div className="content-heading">公告管理</div>
    <Spin spinning={loading}>
      <div className="block border-bottom">
        <div className="bg-white">
          <div className="forest-table-action"><Button icon={<PlusOutlined />} onClick={() => openEdit({})}>添加公告</Button></div>
          <Table className="forest-table" tableLayout="auto" dataSource={rows} columns={columns} rowKey="id" pagination={false} />
        </div>
      </div>
    </Spin>

    <Modal title={edit?.id ? '编辑公告' : '新建公告'} open={!!edit} onCancel={() => setEdit(null)} onOk={save} confirmLoading={saving} okText="提交" width={760} destroyOnHidden>
      <Form form={form} layout="vertical">
        <Form.Item name="title" label="标题" rules={[{ required: true, message: '请输入公告标题' }]}><Input placeholder="请输入公告标题" /></Form.Item>
        <Form.Item name="content" label="公告内容" rules={[{ required: true, message: '请输入公告内容' }]}><Input.TextArea rows={8} placeholder="请输入公告内容" /></Form.Item>
        <Form.Item name="tags" label="标签"><Select mode="tags" open={false} placeholder="输入标签后回车添加" /></Form.Item>
        <Form.Item name="img_url" label="图片URL"><Input placeholder="请输入图片URL(选填)" /></Form.Item>
      </Form>
    </Modal>
  </div>;
}
