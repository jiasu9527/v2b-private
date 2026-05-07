import React, { useEffect, useState } from 'react';
import { Button, Card, Checkbox, Form, Input, Modal, Select, Space, Spin, Table, Tooltip, message } from 'antd';
import { LoadingOutlined, PlusOutlined } from '@ant-design/icons';
import { apiGet, apiPost } from '../lib/api';
import {
  buildVisibleServerOptions,
  memberKey,
  normalizeEntryForVisibleMembers,
  splitMemberKey,
  visibleMemberNames,
  type ClientEntryServerOption,
} from './clientEntryHelpers';

function listText(value: any) {
  if (Array.isArray(value)) return value.filter(Boolean).join('\n');
  if (typeof value === 'string') return value.split(',').map((item) => item.trim()).filter(Boolean).join('\n');
  return '';
}

function splitList(value: any) {
  if (Array.isArray(value)) return value.map((item) => String(item).trim()).filter(Boolean);
  return String(value || '').split(/[\n,]+/).map((item) => item.trim()).filter((item, index, arr) => !!item && arr.indexOf(item) === index);
}

function countList(value: any) {
  if (Array.isArray(value)) return value.filter(Boolean).length;
  if (typeof value === 'string') return value.split(',').filter(Boolean).length;
  return 0;
}

function normalizeEntry(row: any = {}, visibleMemberKeys?: Set<string>) {
  const displayName = row.display_name || row.name || row.remarks || '';
  return {
    ...row,
    display_name: displayName,
    name: row.name || displayName,
    remarks: row.remarks || displayName,
    match_text: listText(row.match),
    members: normalizeEntryForVisibleMembers(row, visibleMemberKeys).members,
    remote_exclude_names_text: listText(row.remote_exclude_names),
    remote_enabled: !!row.remote_enabled,
  };
}

function savePayload(values: any) {
  const displayName = String(values.display_name || values.name || values.remarks || '').trim();
  const remoteEnabled = !!values.remote_enabled;
  return {
    ...values,
    display_name: displayName,
    name: displayName,
    remarks: displayName,
    match: splitList(values.match_text),
    members: (Array.isArray(values.members) ? values.members : []).map((value: any, index: number) => {
      const member = splitMemberKey(value);
      return member ? { ...member, sort: index + 1 } : null;
    }).filter(Boolean),
    remote_enabled: remoteEnabled,
    remote_host: remoteEnabled ? String(values.remote_host || '').trim() : '',
    remote_ssh_port: remoteEnabled ? Number(values.remote_ssh_port || 0) : 0,
    remote_ssh_user: remoteEnabled ? String(values.remote_ssh_user || '').trim() : '',
    remote_ssh_password: remoteEnabled ? String(values.remote_ssh_password || '').trim() : '',
    remote_group_ref: remoteEnabled ? String(values.remote_group_ref || '').trim() : '',
    remote_exclude_names: remoteEnabled ? splitList(values.remote_exclude_names_text) : [],
    remote_refresh_sec: remoteEnabled ? Number(values.remote_refresh_sec || 0) : 0,
    action: 'ordered-fallback',
    strategy: 'ordered-fallback',
  };
}

function ClientEntryEditor({ row, children, onDone, serverOptions }: { row?: any; children: React.ReactElement; onDone: () => void; serverOptions: ClientEntryServerOption[] }) {
  const [open, setOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [form] = Form.useForm();
  const remoteEnabled = Form.useWatch('remote_enabled', form);

  const show = () => {
    const next = normalizeEntry(row || {}, new Set(serverOptions.map((item) => item.value)));
    form.setFieldsValue(next);
    setOpen(true);
  };

  const save = async () => {
    setSaving(true);
    try {
      const values = await form.validateFields();
      await apiPost('/server/client-entry/save', savePayload(values));
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
    <Modal
      title={row?.id ? '编辑客户端入口组' : '创建客户端入口组'}
      open={open}
      onCancel={() => setOpen(false)}
      onOk={save}
      okText={saving ? <LoadingOutlined /> : (row?.id ? '保存更改' : '创建入口组')}
      cancelText="取消"
      confirmLoading={saving}
      width={720}
      destroyOnHidden
    >
      <Form form={form} layout="vertical" className="legacy-client-entry-form">
        <Form.Item name="id" hidden><Input /></Form.Item>
        <Form.Item name="code" hidden><Input /></Form.Item>
        <Form.Item name="display_name" label="入口组名称" rules={[{ required: true, message: '请输入入口组名称' }]}>
          <Input placeholder="请输入入口组名称" />
        </Form.Item>
        <Form.Item label="数据源" className="client-entry-source-item">
          <div className="text-muted client-entry-help">最终下发 = 手填 IP + 远程站点抓取 IP，系统会登录站点并读取设备组在线设备的 ip4 / ip6，再自动合并去重。</div>
          <Form.Item name="remote_enabled" valuePropName="checked" noStyle>
            <Checkbox>启用远程站点抓取</Checkbox>
          </Form.Item>
        </Form.Item>
        <Form.Item name="match_text" label="入口地址列表（支持 IP / 域名）">
          <Input.TextArea rows={5} placeholder={'1.1.1.1\n8.8.8.8\nentry-a.example.com'} />
        </Form.Item>
        <Form.Item name="members" label="生效节点" tooltip="只给选中的节点下发这个入口组；不选择则该入口组不会对任何节点生效。">
          <Select
            mode="multiple"
            allowClear
            showSearch
            placeholder="选择该入口组生效的节点"
            options={serverOptions}
            optionFilterProp="label"
            maxTagCount="responsive"
            notFoundContent="暂无节点，请先到节点管理添加"
          />
        </Form.Item>
        {remoteEnabled && <>
          <Form.Item name="remote_host" label="远程站点地址"><Input placeholder="https://iso.sllbaidu.com" /></Form.Item>
          <Form.Item name="remote_ssh_user" label="登录账号"><Input placeholder="admin" /></Form.Item>
          <Form.Item name="remote_ssh_password" label="登录密码"><Input.Password placeholder="请输入站点登录密码" /></Form.Item>
          <Form.Item name="remote_group_ref" label="设备组名称 / 引用"><Input placeholder="专线直出 (#15)" /></Form.Item>
          <Form.Item name="remote_exclude_names_text" label="排除设备名"><Input.TextArea rows={4} placeholder={'alice\nbob'} /></Form.Item>
          <Form.Item name="remote_refresh_sec" label="远程刷新间隔（秒）"><Input type="number" placeholder="300" /></Form.Item>
        </>}
        <Form.Item label="入口切换逻辑">
          <div className="text-muted">固定逻辑：先测速排序，异常时自动切换同节点入口</div>
        </Form.Item>
      </Form>
    </Modal>
  </>;
}

export default function ClientEntryPage() {
  const [rows, setRows] = useState<any[]>([]);
  const [serverOptions, setServerOptions] = useState<ClientEntryServerOption[]>([]);
  const [loading, setLoading] = useState(false);
  const [resolvingId, setResolvingId] = useState<number | string | null>(null);

  const load = async () => {
    setLoading(true);
    try {
      const [res, nodeRes] = await Promise.all([
        apiGet('/server/client-entry/fetch'),
        apiGet('/server/manage/getNodes').catch(() => ({ data: [] })),
      ]);
      setRows(Array.isArray(res.data) ? res.data : []);
      setServerOptions(buildVisibleServerOptions(Array.isArray(nodeRes.data) ? nodeRes.data : []));
    } catch (e: any) {
      message.error(e.message || '加载失败');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { load(); }, []);

  const drop = async (id: any) => {
    await apiPost('/server/client-entry/drop', { id }, { form: true });
    message.success('已删除');
    load();
  };

  const resolve = async (id: any) => {
    setResolvingId(id);
    try {
      await apiPost('/server/client-entry/resolve', { id }, { form: true });
      message.success('已拉取');
      load();
    } catch (e: any) {
      message.error(e.message || '拉取失败');
    } finally {
      setResolvingId(null);
    }
  };

  const serverOptionMap = serverOptions.reduce((acc, item) => {
    acc[item.value] = item.label;
    return acc;
  }, {} as Record<string, string>);

  const columns: any[] = [
    { title: 'ID', dataIndex: 'id', key: 'id', width: 70 },
    { title: '入口组名称', dataIndex: 'remarks', key: 'remarks', width: 180, render: (_: any, row: any) => row.display_name || row.name || row.remarks || '-' },
    { title: '数据源', dataIndex: 'remote_enabled', key: 'remote_enabled', width: 260, render: (enabled: any, row: any) => {
      const staticCount = countList(row.match);
      const parts = [];
      if (row.remote_host) parts.push(row.remote_host);
      if (row.remote_group_ref) parts.push(row.remote_group_ref);
      return enabled ? `${staticCount > 0 ? '手填 + 远程' : '仅远程'}${parts.length ? ` / ${parts.join(' / ')}` : ''}` : '仅手填';
    } },
    { title: '生效节点', dataIndex: 'members', key: 'members', width: 260, render: (_: any, row: any) => {
      const names = visibleMemberNames(row, serverOptionMap);
      if (names.length === 0) return <Tooltip title="未选择节点时，这个入口组不会下发给任何节点"><span className="text-muted">未选择节点</span></Tooltip>;
      return <details className="client-entry-members">
        <summary>已选择 {names.length} 个节点</summary>
        <div className="client-entry-member-list">{names.map((name) => <div key={name}>{name}</div>)}</div>
      </details>;
    } },
    { title: '入口数量', dataIndex: 'effective_entry_count', key: 'effective_entry_count', width: 160, render: (value: any, row: any) => {
      const staticCount = typeof row.static_entry_count === 'number' ? row.static_entry_count : countList(row.match);
      const remoteCount = typeof row.remote_resolved_count === 'number' ? row.remote_resolved_count : 0;
      const total = typeof value === 'number' ? value : staticCount + remoteCount;
      if (total === 0) return row.remote_enabled ? '暂无可用入口' : '未配置入口';
      return <div><div>共 {total} 个入口</div><div className="text-muted">手填 {staticCount} / 远程 {remoteCount}</div></div>;
    } },
    { title: '远程拉取结果', dataIndex: 'remote_resolved_ips', key: 'remote_resolved_ips', width: 320, render: (value: any, row: any) => {
      if (!row.remote_enabled) return '-';
      const ips = Array.isArray(value) ? value.filter(Boolean) : [];
      const error = typeof row.remote_resolve_error === 'string' ? row.remote_resolve_error : '';
      const summary = error ? '拉取异常，点击展开' : ips.length > 0 ? `已拉取 ${ips.length} 条，点击展开` : '暂无远程结果';
      return <details className="client-entry-details">
        <summary className={error ? 'client-entry-summary-error' : 'client-entry-summary-ok'}>{summary}</summary>
        <div className="client-entry-detail-body">
          {row.remote_group_ref && <div className="text-muted mb-1">{row.remote_group_ref}</div>}
          {error && <div className="client-entry-error">{error}</div>}
          {ips.length > 0 ? <div>{ips.join('\n')}</div> : <div className="text-muted">暂无远程结果</div>}
        </div>
      </details>;
    } },
    { title: '入口切换逻辑', dataIndex: 'action', key: 'action', width: 280, render: () => '固定：先测速排序，异常时自动切换同节点入口' },
    { title: '操作', dataIndex: 'action2', key: 'action2', align: 'right', width: 190, render: (_: any, row: any) => <div>
      <ClientEntryEditor row={row} onDone={load} serverOptions={serverOptions}><a href="javascript:void(0);">编辑</a></ClientEntryEditor>
      {row.remote_enabled && <><span className="ant-divider ant-divider-vertical" /> <a href="javascript:void(0);" className={resolvingId === row.id ? 'client-entry-action-disabled' : ''} onClick={() => resolvingId !== row.id && resolve(row.id)}>{resolvingId === row.id ? <><LoadingOutlined /> 立即拉取</> : '立即拉取'}</a></>}
      <span className="ant-divider ant-divider-vertical" /> <a href="javascript:void(0);" onClick={() => drop(row.id)}>删除</a>
    </div> },
  ];

  return <div className="legacy-page client-entry-page">
    <div className="content-heading">客户端入口组</div>
    <Spin spinning={loading}>
      <Card className="block-card" styles={{ body: { padding: 0 } }}>
        <div className="forest-table-action">
          <ClientEntryEditor onDone={load} serverOptions={serverOptions}><Button icon={<PlusOutlined />}>新建客户端入口组</Button></ClientEntryEditor>
        </div>
        <Table className="forest-table" rowKey="id" tableLayout="auto" columns={columns} dataSource={rows} pagination={false} scroll={{ x: 1280 }} />
      </Card>
    </Spin>
  </div>;
}
