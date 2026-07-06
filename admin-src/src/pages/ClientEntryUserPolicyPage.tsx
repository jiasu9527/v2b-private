import React, { useEffect, useState } from 'react';
import { Button, Card, Form, Input, Modal, Select, Space, Spin, Switch, Table, Tag, message } from 'antd';
import { LoadingOutlined, PlusOutlined } from '@ant-design/icons';
import { apiGet, apiPost } from '../lib/api';
import { buildVisibleServerOptions, type ClientEntryServerOption } from './clientEntryHelpers';

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

function splitEntries(value: any) {
  return String(value || '')
    .split(/[\n,;]+/)
    .map((item) => item.trim())
    .filter((item, index, arr) => !!item && arr.indexOf(item) === index);
}

function PolicyEditor({ row, children, onDone, serverOptions }: { row?: any; children: React.ReactElement; onDone: () => void; serverOptions: ClientEntryServerOption[] }) {
  const [open, setOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [userOptions, setUserOptions] = useState<{ value: string; label: string }[]>([]);
  const [form] = Form.useForm();

  const show = () => {
    form.setFieldsValue({
      id: row?.id,
      emails: Array.isArray(row?.emails) ? row.emails : (row?.email ? [row.email] : []),
      entries_text: Array.isArray(row?.entries) ? row.entries.join('\n') : '',
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
        emails: Array.isArray(values.emails) ? values.emails : [],
        entries: splitEntries(values.entries_text),
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
        <Form.Item name="emails" label="用户邮箱" rules={[{ required: true, message: '请选择用户邮箱' }]} tooltip="一条规则可以绑定多个用户；输入邮箱关键字可检索已有用户，也可以直接粘贴邮箱后回车。">
          <Select
            mode="tags"
            showSearch
            allowClear
            placeholder="输入邮箱搜索用户，支持选择多个"
            options={userOptions}
            filterOption={false}
            onSearch={async (keyword) => {
              const value = String(keyword || '').trim();
              if (!value) return;
              try {
                const res = await apiGet('/user/fetch', {
                  current: 1,
                  pageSize: 20,
                  'filter[0][key]': 'email',
                  'filter[0][condition]': '模糊',
                  'filter[0][value]': value,
                });
                const list = Array.isArray(res.data) ? res.data : [];
                setUserOptions(list.map((item: any) => ({ value: String(item.email || '').trim(), label: String(item.email || '').trim() })).filter((item: any) => item.value));
              } catch {
                setUserOptions([]);
              }
            }}
          />
        </Form.Item>
        <Form.Item name="entries_text" label="入口地址" rules={[{ required: true, message: '请输入入口地址' }]} tooltip="一行一个入口地址，支持 IP 或域名；也可以用英文逗号或分号分隔。">
          <Input.TextArea rows={5} placeholder={'1.1.1.1\nentry.example.com'} />
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
  const [serverOptions, setServerOptions] = useState<ClientEntryServerOption[]>([]);
  const [loading, setLoading] = useState(false);

  const load = async () => {
    setLoading(true);
    try {
      const [policyRes, nodeRes] = await Promise.all([
        apiGet('/server/client-entry-user-policy/fetch'),
        apiGet('/server/manage/getNodes').catch(() => ({ data: [] })),
      ]);
      setRows(Array.isArray(policyRes.data) ? policyRes.data : []);
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
    { title: '用户邮箱', dataIndex: 'emails', width: 280, render: (value: any, row: any) => {
      const emails = Array.isArray(value) ? value : (row.email ? [row.email] : []);
      if (emails.length === 0) return '-';
      return <details className="client-entry-members"><summary>已选择 {emails.length} 个用户</summary><div className="client-entry-member-list">{emails.map((email: string) => <div key={email}>{email}</div>)}</div></details>;
    } },
    { title: '入口地址', dataIndex: 'entries', width: 240, render: (value: any) => {
      const entries = Array.isArray(value) ? value : [];
      if (entries.length === 0) return '-';
      return <details className="client-entry-members"><summary>共 {entries.length} 个入口</summary><div className="client-entry-member-list">{entries.map((entry: string) => <div key={entry}>{entry}</div>)}</div></details>;
    } },
    { title: '生效节点', dataIndex: 'server_name', width: 260, render: (value: any, row: any) => value ? `${value} / ${row.server_type} #${row.server_id}` : `${row.server_type} #${row.server_id}` },
    { title: '状态', dataIndex: 'enabled', width: 100, render: (value: any) => Number(value) === 0 ? <Tag>禁用</Tag> : <Tag color="green">启用</Tag> },
    { title: '备注', dataIndex: 'remarks', ellipsis: true },
    { title: '操作', key: 'action', align: 'right', width: 150, render: (_: any, row: any) => <Space>
      <PolicyEditor row={row} onDone={load} serverOptions={serverOptions}><a href="javascript:void(0);">编辑</a></PolicyEditor>
      <a href="javascript:void(0);" onClick={() => drop(row.id)}>删除</a>
    </Space> },
  ];

  return <div className="legacy-page client-entry-page">
    <div className="content-heading">用户入口分配</div>
    <Spin spinning={loading}>
      <Card className="block-card" styles={{ body: { padding: 0 } }}>
        <div className="forest-table-action">
          <PolicyEditor onDone={load} serverOptions={serverOptions}><Button icon={<PlusOutlined />}>新增分配</Button></PolicyEditor>
        </div>
        <Table className="forest-table" rowKey="id" tableLayout="auto" columns={columns} dataSource={rows} pagination={false} scroll={{ x: 1100 }} />
      </Card>
    </Spin>
  </div>;
}
