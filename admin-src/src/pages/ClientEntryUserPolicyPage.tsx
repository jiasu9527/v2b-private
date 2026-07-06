import React, { useEffect, useState } from 'react';
import { Button, Card, Form, Input, Modal, Select, Space, Spin, Switch, Table, Tag, message } from 'antd';
import { LoadingOutlined, PlusOutlined } from '@ant-design/icons';
import { apiGet, apiPost } from '../lib/api';
import { buildVisibleServerOptions, type ClientEntryServerOption } from './clientEntryHelpers';

type EntryGroupOption = { value: number; label: string };

function splitNodeValue(value: any) {
  const raw = String(value || '').trim();
  const index = raw.indexOf(':');
  if (index <= 0) return { server_type: '', server_id: 0 };
  return { server_type: raw.slice(0, index), server_id: Number(raw.slice(index + 1)) };
}

function nodeValue(row: any) {
  if (row?.server_type && row?.server_id) return `${row.server_type}:${row.server_id}`;
  return undefined;
}

function PolicyEditor({ row, children, onDone, entryGroups, serverOptions }: { row?: any; children: React.ReactElement; onDone: () => void; entryGroups: EntryGroupOption[]; serverOptions: ClientEntryServerOption[] }) {
  const [open, setOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [form] = Form.useForm();

  const show = () => {
    form.setFieldsValue({
      id: row?.id,
      email: row?.email || '',
      entry_group_id: row?.entry_group_id,
      server: nodeValue(row),
      enabled: row?.enabled === undefined ? true : Number(row.enabled) !== 0,
      remarks: row?.remarks || '',
    });
    setOpen(true);
  };

  const save = async () => {
    setSaving(true);
    try {
      const values = await form.validateFields();
      const node = splitNodeValue(values.server);
      await apiPost('/server/client-entry-user-policy/save', {
        id: values.id,
        email: values.email,
        entry_group_id: Number(values.entry_group_id),
        server_type: node.server_type,
        server_id: node.server_id,
        enabled: values.enabled ? 1 : 0,
        remarks: values.remarks || '',
      });
      message.success('保存成功');
      setOpen(false);
      onDone();
    } catch (e: any) {
      if (!e?.errorFields) message.error(e.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };

  return <>
    {React.cloneElement(children, { onClick: show })}
    <Modal title={row?.id ? '编辑用户入口分配' : '新增用户入口分配'} open={open} onCancel={() => setOpen(false)} onOk={save} okText={saving ? <LoadingOutlined /> : '保存'} cancelText="取消" confirmLoading={saving} width={680} destroyOnHidden>
      <Form form={form} layout="vertical">
        <Form.Item name="id" hidden><Input /></Form.Item>
        <Form.Item name="email" label="用户邮箱" rules={[{ required: true, message: '请输入用户邮箱' }, { type: 'email', message: '邮箱格式不正确' }]}>
          <Input placeholder="user@example.com" />
        </Form.Item>
        <Form.Item name="entry_group_id" label="入口组" rules={[{ required: true, message: '请选择入口组' }]}>
          <Select showSearch placeholder="选择已有客户端入口组" options={entryGroups} optionFilterProp="label" />
        </Form.Item>
        <Form.Item name="server" label="生效节点" rules={[{ required: true, message: '请选择生效节点' }]} tooltip="只覆盖这个邮箱在所选节点上的连接地址，其他节点照旧。">
          <Select showSearch placeholder="选择需要覆盖入口的节点" options={serverOptions} optionFilterProp="label" />
        </Form.Item>
        <Form.Item name="enabled" label="状态" valuePropName="checked">
          <Switch checkedChildren="启用" unCheckedChildren="禁用" />
        </Form.Item>
        <Form.Item name="remarks" label="备注">
          <Input.TextArea rows={3} placeholder="可选" />
        </Form.Item>
      </Form>
    </Modal>
  </>;
}

export default function ClientEntryUserPolicyPage() {
  const [rows, setRows] = useState<any[]>([]);
  const [entryGroups, setEntryGroups] = useState<EntryGroupOption[]>([]);
  const [serverOptions, setServerOptions] = useState<ClientEntryServerOption[]>([]);
  const [loading, setLoading] = useState(false);

  const load = async () => {
    setLoading(true);
    try {
      const [policyRes, groupRes, nodeRes] = await Promise.all([
        apiGet('/server/client-entry-user-policy/fetch'),
        apiGet('/server/client-entry/fetch'),
        apiGet('/server/manage/getNodes').catch(() => ({ data: [] })),
      ]);
      setRows(Array.isArray(policyRes.data) ? policyRes.data : []);
      setEntryGroups((Array.isArray(groupRes.data) ? groupRes.data : []).filter((item: any) => Number(item.show ?? 1) !== 0).map((item: any) => ({ value: Number(item.id), label: item.display_name || item.name || item.remarks || `入口组 #${item.id}` })));
      setServerOptions(buildVisibleServerOptions(Array.isArray(nodeRes.data) ? nodeRes.data : []));
    } catch (e: any) {
      message.error(e.message || '加载失败');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { load(); }, []);

  const drop = async (id: any) => {
    await apiPost('/server/client-entry-user-policy/drop', { id }, { form: true });
    message.success('已删除');
    load();
  };

  const columns: any[] = [
    { title: 'ID', dataIndex: 'id', width: 70 },
    { title: '用户邮箱', dataIndex: 'email', width: 240 },
    { title: '入口组', dataIndex: 'entry_group_name', width: 200, render: (value: any, row: any) => value || `#${row.entry_group_id}` },
    { title: '生效节点', dataIndex: 'server_name', width: 260, render: (value: any, row: any) => value ? `${value} / ${row.server_type} #${row.server_id}` : `${row.server_type} #${row.server_id}` },
    { title: '状态', dataIndex: 'enabled', width: 100, render: (value: any) => Number(value) === 0 ? <Tag>禁用</Tag> : <Tag color="green">启用</Tag> },
    { title: '备注', dataIndex: 'remarks', ellipsis: true },
    { title: '操作', key: 'action', align: 'right', width: 150, render: (_: any, row: any) => <Space>
      <PolicyEditor row={row} onDone={load} entryGroups={entryGroups} serverOptions={serverOptions}><a href="javascript:void(0);">编辑</a></PolicyEditor>
      <a href="javascript:void(0);" onClick={() => drop(row.id)}>删除</a>
    </Space> },
  ];

  return <div className="legacy-page client-entry-page">
    <div className="content-heading">用户入口分配</div>
    <Spin spinning={loading}>
      <Card className="block-card" styles={{ body: { padding: 0 } }}>
        <div className="forest-table-action">
          <PolicyEditor onDone={load} entryGroups={entryGroups} serverOptions={serverOptions}><Button icon={<PlusOutlined />}>新增分配</Button></PolicyEditor>
        </div>
        <Table className="forest-table" rowKey="id" tableLayout="auto" columns={columns} dataSource={rows} pagination={false} scroll={{ x: 1100 }} />
      </Card>
    </Spin>
  </div>;
}
