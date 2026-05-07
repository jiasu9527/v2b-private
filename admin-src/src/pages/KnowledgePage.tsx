import React, { useEffect, useState } from 'react';
import { Button, Form, Input, Modal, Popconfirm, Select, Space, Spin, Switch, Table, message } from 'antd';
import { MenuOutlined, PlusOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { apiGet, apiPost } from '../lib/api';

export default function KnowledgePage() {
  const [rows, setRows] = useState<any[]>([]);
  const [categories, setCategories] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [edit, setEdit] = useState<any>(null);
  const [form] = Form.useForm();

  const load = async () => {
    setLoading(true);
    try {
      const [knowledgeRes, categoryRes] = await Promise.all([
        apiGet('/knowledge/fetch'),
        apiGet('/knowledge/getCategory').catch(() => ({ data: [] })),
      ]);
      setRows(knowledgeRes.data || []);
      setCategories(categoryRes.data || []);
    } catch (e: any) {
      message.error(e.message || '加载失败');
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => { load(); }, []);

  const openEdit = async (row: any = {}) => {
    setEdit(row);
    form.resetFields();
    let data = row;
    if (row.id) {
      const res = await apiGet('/knowledge/fetch', { id: row.id }).catch(() => ({ data: row }));
      data = res.data || row;
    }
    form.setFieldsValue({ language: 'zh-CN', ...data });
  };

  const save = async () => {
    setSaving(true);
    try {
      const values = await form.validateFields();
      const category = Array.isArray(values.category) ? values.category[0] : values.category;
      await apiPost('/knowledge/save', { ...edit, ...values, category }, { form: true });
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
    await apiPost('/knowledge/show', { id: row.id }, { form: true });
    message.success('操作成功');
    load();
  };

  const drop = async (row: any) => {
    await apiPost('/knowledge/drop', { id: row.id }, { form: true });
    message.success('已删除');
    load();
  };

  const columns: any[] = [
    { title: '排序', dataIndex: 'sort', width: 80, render: () => <MenuOutlined className="drag-handle" /> },
    { title: '文章ID', dataIndex: 'id', width: 100 },
    { title: '显示', dataIndex: 'show', width: 90, render: (value: any, row: any) => <Switch size="small" checked={!!Number(value)} onChange={() => toggle(row)} /> },
    { title: '标题', dataIndex: 'title' },
    { title: '分类', dataIndex: 'category', width: 160 },
    { title: '更新时间', dataIndex: 'updated_at', width: 180, render: (value: any) => value ? dayjs(value * 1000).format('YYYY/MM/DD HH:mm') : '-' },
    { title: '操作', dataIndex: 'action', align: 'right', width: 120, render: (_: any, row: any) => <Space split={<span className="ant-divider ant-divider-vertical" />}><a onClick={() => openEdit(row)}>编辑</a><Popconfirm title="确定要删除该条项目吗？" onConfirm={() => drop(row)}><a>删除</a></Popconfirm></Space> },
  ];

  return <div className="legacy-page knowledge-page">
    <div className="content-heading">知识库管理</div>
    <Spin spinning={loading}>
      <div className="block border-bottom">
        <div className="bg-white">
          <div className="forest-table-action"><Button icon={<PlusOutlined />} onClick={() => openEdit({ language: 'zh-CN' })}>添加知识</Button></div>
          <Table className="forest-table" tableLayout="auto" dataSource={rows} columns={columns} rowKey="id" pagination={false} />
        </div>
      </div>
    </Spin>

    <Modal title={edit?.id ? '编辑知识' : '新建知识'} open={!!edit} onCancel={() => setEdit(null)} onOk={save} confirmLoading={saving} okText="提交" width={860} destroyOnHidden>
      <Form form={form} layout="vertical">
        <Form.Item name="title" label="标题" rules={[{ required: true, message: '请输入标题' }]}><Input placeholder="请输入标题" /></Form.Item>
        <Form.Item name="category" label="分类" rules={[{ required: true, message: '请输入分类' }]}><Select mode="tags" maxCount={1} open={false} placeholder="请输入或选择分类" options={categories.map((item) => ({ label: item, value: item }))} /></Form.Item>
        <Form.Item name="language" label="语言" rules={[{ required: true, message: '请选择语言' }]}><Select options={[{ label: '简体中文', value: 'zh-CN' }, { label: 'English', value: 'en-US' }]} /></Form.Item>
        <Form.Item name="body" label="内容" rules={[{ required: true, message: '请输入内容' }]}><Input.TextArea rows={10} placeholder="请输入知识库内容" /></Form.Item>
      </Form>
    </Modal>
  </div>;
}
