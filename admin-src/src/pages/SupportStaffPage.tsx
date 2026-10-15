import React, { useEffect, useState } from 'react';
import { Alert, Button, Form, Input, Modal, Select, Space, Switch, Table, Tabs, Tag, Typography, message } from 'antd';
import { AuditOutlined, EditOutlined, PlusOutlined, ReloadOutlined, TeamOutlined } from '@ant-design/icons';
import { apiGet, apiJsonPost } from '../lib/api';
import { type SupportEntryTarget, type SupportStaff, supportDate } from './supportTypes';
import SupportOperations from './SupportOperations';
import './SupportPage.css';

function StaffConfiguration() {
  const [items, setItems] = useState<SupportStaff[]>([]);
  const [targets, setTargets] = useState<SupportEntryTarget[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [editing, setEditing] = useState<SupportStaff | null>(null);
  const [open, setOpen] = useState(false);
  const [form] = Form.useForm();
  const load = async () => {
    setLoading(true); setError('');
    try { const res = await apiGet('/support/staff'); setItems(res.data?.items || []); setTargets(res.data?.targets || []); }
    catch (e: any) { setError(e.message || '加载员工配置失败'); setItems([]); setTargets([]); }
    finally { setLoading(false); }
  };
  useEffect(() => { load(); }, []);
  const edit = (staff?: SupportStaff) => {
    setEditing(staff || null);
    form.setFieldsValue({ email: staff?.email || '', enabled: staff ? !!staff.enabled : true, target_keys: staff?.target_keys || [] });
    setOpen(true);
  };
  const save = async () => {
    if (saving) return;
    const values = await form.validateFields();
    setSaving(true);
    try {
      await apiJsonPost('/support/staff/save', { email: values.email.trim(), enabled: !!values.enabled, target_keys: values.target_keys || [], version: editing?.version || 0 });
      message.success('员工权限已保存；该员工的现有会话已撤销，请重新登录'); setOpen(false); await load();
    } catch (e: any) { message.error(e.message || '保存失败，请刷新后重试'); }
    finally { setSaving(false); }
  };
  const toggle = (staff: SupportStaff) => Modal.confirm({
    title: staff.enabled ? '停用该员工的运维权限？' : '启用该员工的运维权限？',
    content: <><Typography.Paragraph>{staff.email}</Typography.Paragraph><Typography.Paragraph>现有登录会话会被撤销。{staff.enabled ? '停用后不能再进入员工工作台。' : '重新登录后可使用下方已授权的目标入口。'}</Typography.Paragraph></>,
    okText: staff.enabled ? '确认停用' : '确认启用', cancelText: '取消',
    onOk: async () => {
      if (saving) throw new Error('正在保存，请稍后');
      setSaving(true);
      try { await apiJsonPost('/support/staff/save', { email: staff.email, enabled: !staff.enabled, target_keys: staff.target_keys || [], version: staff.version }); message.success('员工权限已更新，原会话已撤销'); await load(); }
      catch (e: any) { message.error(e.message || '更新失败，请刷新后重试'); throw e; }
      finally { setSaving(false); }
    },
  });
  const byKey = new Map(targets.map((target) => [target.key, target]));
  const columns: any[] = [
    { title: '员工账号', dataIndex: 'email', width: 240, render: (email: string, row: SupportStaff) => <>{email}<div className="support-muted">用户 ID {row.user_id}</div></> },
    { title: '权限状态', dataIndex: 'enabled', width: 110, render: (enabled: boolean) => <Tag color={enabled ? 'green' : undefined}>{enabled ? '已启用' : '已停用'}</Tag> },
    { title: '允许指定的目标入口', dataIndex: 'target_keys', width: 440, render: (keys: string[]) => keys?.length ? <Space size={[4, 6]} wrap>{keys.map((key) => { const target = byKey.get(key); return <Tag key={key}>{target?.name || key}{!target && '（已失效）'}</Tag>; })}</Space> : <Typography.Text type="secondary">只读检测与精准查询，暂无可指定入口</Typography.Text> },
    { title: '更新于', dataIndex: 'updated_at', width: 180, render: supportDate },
    { title: '操作', width: 170, fixed: 'right', render: (_: any, row: SupportStaff) => <Space><Button size="small" icon={<EditOutlined aria-hidden />} disabled={saving} onClick={() => edit(row)}>编辑</Button><Button size="small" danger={row.enabled} disabled={saving} onClick={() => toggle(row)}>{row.enabled ? '停用' : '启用'}</Button></Space> },
  ];
  const knownKeys = new Set(targets.map((target) => target.key));
  const invalidOptions = (editing?.target_keys || []).filter((key) => !knownKeys.has(key)).map((key) => ({ value: key, label: `${key}（目标已失效，请移除）` }));
  return <div className="support-panel">
    <Alert type="info" showIcon className="support-alert" message="员工工作台是独立受限权限，不等同于旧版员工 is_staff，也不开放完整后台。" description="绑定已注册的普通用户账号；可查看入口健康状态、精准查询用户，并在授权目标内指定单用户入口。不能修改规则、套餐、财务或批量分流。启停和修改授权会撤销该员工的现有登录会话。" />
    <div className="support-toolbar"><Typography.Text type="secondary">目标入口授权后，后端仍会逐次校验与用户当前线路范围是否兼容。</Typography.Text><Space><Button icon={<ReloadOutlined aria-hidden />} loading={loading} disabled={saving} onClick={load}>刷新</Button><Button type="primary" icon={<PlusOutlined aria-hidden />} disabled={loading || saving} onClick={() => edit()}>添加员工</Button></Space></div>
    {error && <Alert type="error" showIcon message={error} className="support-alert" />}
    <Table rowKey="user_id" columns={columns} dataSource={items} loading={loading} scroll={{ x: 1100 }} pagination={{ pageSize: 20, showSizeChanger: false }} />
    <Modal title={editing ? '编辑员工权限' : '添加员工权限'} open={open} onCancel={() => { if (!saving) setOpen(false); }} onOk={save} okText="保存权限" cancelText="取消" confirmLoading={saving} closable={!saving} maskClosable={!saving} destroyOnHidden width={650}>
      <Form form={form} layout="vertical" preserve={false} style={{ marginTop: 20 }}>
        <Form.Item name="email" label="已注册账号的完整邮箱" rules={[{ required: true, message: '请输入完整邮箱' }, { type: 'email', message: '请输入有效邮箱' }]}><Input disabled={!!editing || saving} placeholder="绑定已有注册的普通账号，不自动创建用户" autoComplete="off" /></Form.Item>
        <Form.Item name="enabled" label="启用员工工作台" valuePropName="checked"><Switch disabled={saving} /></Form.Item>
        <Form.Item name="target_keys" label="允许指定的目标入口组" extra="只授权选择已有入口，不允许编辑整个组。未选择目标时可查用户和检测状态，但不能人工指定新的入口。"><Select mode="multiple" showSearch optionFilterProp="label" disabled={saving} placeholder="选择允许员工切换到的入口组" options={[...targets.map((target) => ({ value: target.key, label: `${target.name} · ${target.entry_host}` })), ...invalidOptions]} /></Form.Item>
        <Alert type="warning" showIcon message="保存后会撤销该员工的登录会话，员工需重新登录。" />
      </Form>
    </Modal>
  </div>;
}

export default function SupportStaffPage() {
  const [tab, setTab] = useState('staff');
  return <div className="legacy-page"><div className="content-heading">员工管理</div><div className="support-content"><Tabs activeKey={tab} onChange={setTab} destroyOnHidden items={[
    { key: 'staff', label: <span><TeamOutlined aria-hidden /> 员工权限</span>, children: <StaffConfiguration /> },
    { key: 'operations', label: <span><AuditOutlined aria-hidden /> 全部操作记录</span>, children: <SupportOperations administrator /> },
  ]} /></div></div>;
}
