import React, { useEffect, useMemo, useState } from 'react';
import { Button, Card, Form, Input, Modal, Select, Space, Spin, Switch, Table, Tag, message } from 'antd';
import { LoadingOutlined, PlusOutlined } from '@ant-design/icons';
import { apiGet, apiPost } from '../lib/api';
import { buildVisibleServerOptions, memberKey, splitMemberKey, type ClientEntryServerOption } from './clientEntryHelpers';
function PolicyEditor({ row, children, onDone, serverOptions }: { row?: any; children: React.ReactElement; onDone: () => void; serverOptions: ClientEntryServerOption[] }) {
  const [open, setOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [userOptions, setUserOptions] = useState<{ value: string; label: string }[]>([]);
  const [form] = Form.useForm();

  const show = () => {
    form.setFieldsValue({
      id: row?.id,
      emails: Array.isArray(row?.emails) ? row.emails : (row?.email ? [row.email] : []),
      entry_host: row?.entry_host || '',
      members: Array.isArray(row?.members) ? row.members.map(memberKey).filter(Boolean) : [],
      enabled: row?.enabled === undefined ? true : Number(row.enabled) !== 0,
      remarks: row?.remarks || '',
    });
    setOpen(true);
  };

  const save = async () => {
    setSaving(true);
    try {
      const values = await form.validateFields();
      await apiPost('/server/client-entry-user-policy/save', {
        id: values.id,
        emails: Array.isArray(values.emails) ? values.emails : [],
        entry_host: values.entry_host || '',
        members: (Array.isArray(values.members) ? values.members : []).map(splitMemberKey).filter(Boolean),
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
        <Form.Item name="entry_host" label="节点地址" rules={[{ required: true, message: '请输入节点地址' }]} tooltip="命中这些用户时，所选生效节点会下发这个地址；其他用户仍使用节点原地址。">
          <Input placeholder="例如 vip.example.com 或 1.2.3.4" />
        </Form.Item>
        <Form.Item name="members" label="生效节点" rules={[{ required: true, message: '请选择生效节点' }]} tooltip="可选择多个节点；命中这些用户时，所选节点会下发上面的节点地址。">
          <Select mode="multiple" showSearch allowClear placeholder="选择多个生效节点" options={serverOptions} optionFilterProp="label" />
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

  const serverOptionMap = useMemo(() => Object.fromEntries(serverOptions.map((item) => [item.value, item.label])), [serverOptions]);

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
    { title: '节点地址', dataIndex: 'entry_host', width: 220, render: (value: any) => value || '-' },
    { title: '生效节点', dataIndex: 'members', width: 220, render: (value: any) => {
      const members = Array.isArray(value) ? value : [];
      if (members.length === 0) return '-';
      return <details className="client-entry-members"><summary>已选择 {members.length} 个节点</summary><div className="client-entry-member-list">{members.map((member: any) => { const key = memberKey(member); return <div key={key}>{serverOptionMap[key] || key}</div>; })}</div></details>;
    } },
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
        <Table className="forest-table" rowKey="id" tableLayout="auto" columns={columns} dataSource={rows} pagination={false} scroll={{ x: 1250 }} />
      </Card>
    </Spin>
  </div>;
}
