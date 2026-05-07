import React, { useEffect, useState } from 'react';
import { Button, Form, Input, Modal, Popconfirm, Select, Space, Spin, Table, message } from 'antd';
import { PlusOutlined } from '@ant-design/icons';
import { apiGet, apiPost, safeJsonParse } from '../lib/api';

const actionText: Record<string, string> = {
  block: '禁止访问(域名目标)',
  block_ip: '禁止访问(IP目标)',
  block_port: '禁止访问(端口目标)',
  protocol: '禁止访问(协议)',
  dns: '指定DNS服务器进行解析',
  route: '指定出站服务器(域名目标)',
  route_ip: '指定出站服务器(IP目标)',
  default_out: '自定义默认出站',
};

const actions = Object.entries(actionText).map(([value, label]) => ({ value, label }));

function toLines(value: any) {
  const parsed = safeJsonParse(value, value);
  if (Array.isArray(parsed)) return parsed.join('\n');
  if (typeof parsed === 'string') return parsed.split(',').filter(Boolean).join('\n');
  return '';
}

function routePlaceholder(action: string) {
  if (action === 'protocol') return 'bittorrent';
  if (action === 'block_port') return '25(单一端口)\n5000-6000(范围端口)';
  if (['route_ip', 'block_ip'].includes(action)) return '127.0.0.1(单一匹配)\n10.0.0.0/8(范围匹配)\ngeoip:cn(预定义列表匹配)';
  return 'example.com(关键字匹配)\ndomain:example.com(子域名匹配)\ngeosite:netflix(预定义域名列表)';
}

function outboundPlaceholder() {
  return JSON.stringify({
    tag: 'ss_out',
    sendThrough: '0.0.0.0',
    protocol: 'shadowsocks',
    settings: {
      email: 'love@xray.com',
      address: '8.8.8.8',
      port: 5555,
      method: 'chacha20-ietf-poly1305',
      password: 'abcdefghijklmnopqrstuvwxyz',
      level: 0,
    },
  }, null, 4);
}

export default function ServerRoutePage() {
  const [rows, setRows] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [edit, setEdit] = useState<any>(null);
  const [form] = Form.useForm();
  const action = Form.useWatch('action', form) || 'block';

  const load = async () => {
    setLoading(true);
    try {
      const res = await apiGet('/server/route/fetch');
      setRows(res.data || []);
    } catch (e: any) {
      message.error(e.message || '加载失败');
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => { load(); }, []);

  const openEdit = (row: any = { action: 'block' }) => {
    setEdit(row);
    form.resetFields();
    form.setFieldsValue({ ...row, match: toLines(row.match) });
  };

  const save = async () => {
    setSaving(true);
    try {
      const values = await form.validateFields();
      await apiPost('/server/route/save', {
        ...edit,
        ...values,
        match: String(values.match || '').split('\n').map((item) => item.trim()).filter(Boolean),
      });
      message.success('保存成功');
      setEdit(null);
      load();
    } catch (e: any) {
      if (!e?.errorFields) message.error(e.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };

  const drop = async (row: any) => {
    await apiPost('/server/route/drop', { id: row.id }, { form: true });
    message.success('已删除');
    load();
  };

  const columns: any[] = [
    { title: 'ID', dataIndex: 'id', width: 80 },
    { title: '备注', dataIndex: 'remarks' },
    { title: '匹配数量', dataIndex: 'match', width: 150, render: (value: any) => {
      const parsed = Array.isArray(value) ? value : toLines(value).split('\n').filter(Boolean);
      return parsed.length === 0 ? '无规则时默认' : `匹配 ${parsed.length} 条规则`;
    } },
    { title: '动作', dataIndex: 'action', width: 210, render: (value: any) => actionText[value] || value },
    { title: '操作', dataIndex: 'action2', align: 'right', width: 120, render: (_: any, row: any) => <Space split={<span className="ant-divider ant-divider-vertical" />}><a onClick={() => openEdit(row)}>编辑</a><Popconfirm title="确定要删除该条项目吗？" onConfirm={() => drop(row)}><a>删除</a></Popconfirm></Space> },
  ];

  return <div className="legacy-page server-route-page">
    <div className="content-heading">路由管理</div>
    <Spin spinning={loading}>
      <div className="block block-rounded">
        <div className="bg-white">
          <div className="forest-table-action"><Button icon={<PlusOutlined />} onClick={() => openEdit({ action: 'block' })}>添加路由</Button></div>
          <Table className="forest-table" tableLayout="auto" dataSource={rows} columns={columns} rowKey="id" pagination={false} />
        </div>
      </div>
    </Spin>

    <Modal title={edit?.id ? '编辑路由规则' : '添加路由规则'} open={!!edit} onCancel={() => setEdit(null)} onOk={save} confirmLoading={saving} okText="提交" width={820} destroyOnHidden>
      <Form form={form} layout="vertical">
        <Form.Item name="remarks" label="备注" rules={[{ required: true, message: '请输入备注' }]}><Input placeholder="备注" /></Form.Item>
        {action !== 'default_out' && <Form.Item name="match" label="匹配值" rules={[{ required: true, message: '请输入匹配值' }]}><Input.TextArea rows={6} placeholder={routePlaceholder(action)} /></Form.Item>}
        <Form.Item name="action" label="动作" rules={[{ required: true, message: '请选择动作' }]}><Select placeholder="请选择动作" options={actions} /></Form.Item>
        {action === 'dns' && <Form.Item name="action_value" label="DNS服务器" rules={[{ required: true, message: '请输入DNS服务器' }]}><Input placeholder="请输入用于解析的DNS服务器地址" /></Form.Item>}
        {['route', 'route_ip', 'default_out'].includes(action) && <Form.Item name="action_value" label={<span>Xray出站配置 <a href="https://xtls.github.io/config/outbound.html" target="_blank" rel="noreferrer">填写参考</a></span>} rules={[{ required: true, message: '请输入Xray出站配置' }]}><Input.TextArea rows={8} placeholder={outboundPlaceholder()} /></Form.Item>}
      </Form>
    </Modal>
  </div>;
}
