import React, { useEffect, useMemo, useState } from 'react';
import { Badge, Button, Card, DatePicker, Dropdown, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Switch, Table, Tag, Tooltip, message } from 'antd';
import type { MenuProps } from 'antd';
import { AccountBookOutlined, CopyOutlined, DeleteOutlined, DownOutlined, EditOutlined, ExportOutlined, MailOutlined, ReloadOutlined, SolutionOutlined, StopOutlined, UserAddOutlined, UsergroupAddOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { apiGet, apiPost, bytes, bytesToGB, bytesToGBText, gbToBytes, getAdminPath, unixTime } from '../lib/api';
import LegacyFilterDrawer, { FilterButton } from '../components/LegacyFilterDrawer';

const periodText: Record<string, string> = {
  month_price: '月付',
  quarter_price: '季付',
  half_year_price: '半年付',
  year_price: '年付',
  two_year_price: '两年付',
  three_year_price: '三年付',
  onetime_price: '一次性',
  reset_price: '流量重置包',
};

const filterDefinitions = [
  { key: 'email', title: '邮箱', condition: ['模糊'] },
  { key: 'id', title: '用户ID', condition: ['=', '>=', '>', '<', '<='] },
  { key: 'plan_id', title: '订阅', condition: ['='] },
  { key: 'transfer_enable', title: '流量', condition: ['>=', '>', '<', '<='] },
  { key: 'd', title: '下行', condition: ['>=', '>', '<', '<='] },
  { key: 'expired_at', title: '到期时间', condition: ['>=', '>', '<', '<='], type: 'date' },
  { key: 'uuid', title: 'UUID', condition: ['='] },
  { key: 'token', title: 'TOKEN', condition: ['='] },
  { key: 'banned', title: '账号状态', condition: ['='], type: 'select', options: [{ label: '正常', value: 0 }, { label: '封禁', value: 1 }] },
  { key: 'invite_by_email', title: '邀请人邮箱', condition: ['模糊'] },
  { key: 'invite_user_id', title: '邀请人ID', condition: ['='] },
  { key: 'remarks', title: '备注', condition: ['模糊'] },
  { key: 'is_admin', title: '管理员', condition: ['='], type: 'select', options: [{ label: '是', value: 1 }, { label: '否', value: 0 }] },
  { key: 't', title: '最后在线', condition: ['>=', '>', '<', '<='], type: 'date' },
];

function dateText(ts: any) {
  const n = Number(ts || 0);
  if (!n) return '-';
  return dayjs(n * 1000).format('YYYY/MM/DD HH:mm');
}

function moneyText(value: any) {
  const n = Number(value || 0);
  if (!Number.isFinite(n)) return '-';
  return (n / 100).toFixed(2);
}

function initialFilters(search = location.search) {
  const query = new URLSearchParams(search);
  const key = query.get('filter_key');
  if (!key) return [];
  const def = filterDefinitions.find((item) => item.key === key) || filterDefinitions[0];
  return [{ key, condition: query.get('condition') || def.condition[0], value: query.get('value') || '' }];
}

function pathUrl(path: string) {
  return `/${getAdminPath()}${path}`;
}

function navigateAdmin(path: string) {
  history.pushState(null, '', pathUrl(path));
  window.dispatchEvent(new PopStateEvent('popstate'));
}

function UserTrafficModal({ user, open, onClose }: { user?: any; open: boolean; onClose: () => void }) {
  const [records, setRecords] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [pagination, setPagination] = useState({ current: 1, pageSize: 10, total: 0 });

  const load = async (override: any = {}) => {
    if (!open || !user?.id) return;
    const next = { ...pagination, ...override };
    setLoading(true);
    try {
      const res = await apiGet('/stat/getStatUser', {
        user_id: user.id,
        current: next.current,
        page: next.current,
        pageSize: next.pageSize,
      });
      setRecords(res.data || []);
      setPagination({ ...next, total: Number(res.total || 0) });
    } catch (e: any) {
      message.error(e.message || '流量记录加载失败');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    if (open) load({ current: 1 });
  }, [open, user?.id]);

  const columns: any[] = [
    { title: '日期', dataIndex: 'record_at', key: 'record_at', render: (value: any) => value ? dayjs(Number(value) * 1000).format('YYYY-MM-DD') : '-' },
    { title: '上行', dataIndex: 'u', key: 'u', align: 'right', render: bytes },
    { title: '下行', dataIndex: 'd', key: 'd', align: 'right', render: bytes },
    { title: '倍率', dataIndex: 'server_rate', key: 'server_rate', align: 'right' },
  ];

  return <Modal
    title="流量记录"
    open={open}
    onCancel={onClose}
    footer={null}
    width="100%"
    style={{ maxWidth: 1000, padding: '0 10px', top: 20 }}
    styles={{ body: { padding: 0 } }}
    destroyOnHidden
  >
    <Table
      className="forest-table"
      rowKey={(row, index) => `${row.record_at || row.id || 'row'}-${index}`}
      loading={loading}
      columns={columns}
      dataSource={records}
      pagination={{ ...pagination, size: 'small' }}
      onChange={(pageInfo: any) => load({ current: pageInfo.current, pageSize: pageInfo.pageSize })}
    />
  </Modal>;
}

function UserAssignOrderModal({ user, open, plans, onClose, onDone }: { user?: any; open: boolean; plans: any[]; onClose: () => void; onDone: () => void }) {
  const [form] = Form.useForm();
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!open) return;
    form.resetFields();
    form.setFieldsValue({
      email: user?.email || '',
      plan_id: undefined,
      period: undefined,
      total_amount: undefined,
    });
  }, [open, user?.email]);

  const submit = async () => {
    const values = await form.validateFields();
    setLoading(true);
    try {
      await apiPost('/order/assign', {
        ...values,
        total_amount: centsFromMoney(values.total_amount),
      }, { form: true });
      message.success('分配成功');
      onClose();
      onDone();
    } catch (e: any) {
      message.error(e.message || '分配失败');
    } finally {
      setLoading(false);
    }
  };

  return <Modal
    title="订单分配"
    open={open}
    onCancel={onClose}
    onOk={submit}
    okText="确定"
    cancelText="取消"
    confirmLoading={loading}
    destroyOnHidden
  >
    <Form form={form} layout="vertical">
      <Form.Item name="email" label="用户邮箱" rules={[{ required: true, message: '请输入用户邮箱' }]}>
        <Input placeholder="请输入用户邮箱" />
      </Form.Item>
      <Form.Item name="plan_id" label="请选择订阅" rules={[{ required: true, message: '请选择订阅' }]}>
        <Select placeholder="请选择订阅" options={plans.map((plan) => ({ label: plan.name, value: plan.id }))} />
      </Form.Item>
      <Form.Item name="period" label="请选择周期" rules={[{ required: true, message: '请选择周期' }]}>
        <Select placeholder="请选择周期" options={Object.entries(periodText).map(([value, label]) => ({ value, label }))} />
      </Form.Item>
      <Form.Item name="total_amount" label="支付金额" rules={[{ required: true, message: '请输入需要支付的金额' }]}>
        <InputNumber addonAfter="¥" min={0} precision={2} style={{ width: '100%' }} placeholder="请输入需要支付的金额" />
      </Form.Item>
    </Form>
  </Modal>;
}


function UserGenerateModal({ open, plans, onClose, onDone }: { open: boolean; plans: any[]; onClose: () => void; onDone: () => void }) {
  const [form] = Form.useForm();
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!open) return;
    form.resetFields();
    form.setFieldsValue({ email_prefix: '', email_suffix: '', generate_count: undefined, expired_at: null, plan_id: undefined, password: '' });
  }, [open]);

  const submit = async () => {
    const values = await form.validateFields();
    const payload: any = { ...values };
    payload.expired_at = values.expired_at ? values.expired_at.unix() : '';
    if (payload.plan_id === undefined || payload.plan_id === null) payload.plan_id = '';
    setLoading(true);
    try {
      const res = await apiPost('/user/generate', payload, { form: true, keepEmpty: true, raw: !!payload.generate_count });
      if (payload.generate_count && typeof res === 'string') {
        downloadTextFile(`USER ${dayjs().format('YYYY-MM-DD HH:mm:ss')}.csv`, res);
      }
      message.success('生成成功');
      onClose();
      onDone();
    } catch (e: any) {
      message.error(e.message || '生成失败');
    } finally {
      setLoading(false);
    }
  };

  return <Modal
    title="创建用户"
    open={open}
    onCancel={onClose}
    onOk={submit}
    okText="生成"
    cancelText="取消"
    confirmLoading={loading}
    destroyOnHidden
  >
    <Form form={form} layout="vertical">
      <Form.Item label="邮箱" required>
        <Space.Compact style={{ width: '100%' }}>
          <Form.Item name="email_prefix" noStyle>
            <Input placeholder="账号（批量生成请留空）" style={{ width: '45%' }} />
          </Form.Item>
          <Input disabled placeholder="@" style={{ width: '10%', textAlign: 'center' }} />
          <Form.Item name="email_suffix" noStyle rules={[{ required: true, message: '请输入邮箱域' }]}>
            <Input placeholder="域" style={{ width: '45%' }} />
          </Form.Item>
        </Space.Compact>
      </Form.Item>
      <Form.Item name="password" label="密码">
        <Input.Password placeholder="留空则密码与邮箱相同" autoComplete="new-password" />
      </Form.Item>
      <Form.Item name="expired_at" label="到期时间">
        <DatePicker showTime style={{ width: '100%' }} placeholder="请选择用户到期日期，为空则不限制到期时间" />
      </Form.Item>
      <Form.Item name="plan_id" label="订阅计划">
        <Select allowClear placeholder="请选择用户订阅计划" options={[{ label: '无', value: null }, ...plans.map((plan) => ({ label: plan.name, value: plan.id }))]} />
      </Form.Item>
      <Form.Item shouldUpdate noStyle>
        {({ getFieldValue }) => !getFieldValue('email_prefix') && <Form.Item name="generate_count" label="生成数量">
          <InputNumber min={1} max={500} precision={0} style={{ width: '100%' }} placeholder="如果为批量生成请输入生成数量" />
        </Form.Item>}
      </Form.Item>
    </Form>
  </Modal>;
}


function UserMailModal({ open, filtered, onClose, onSubmit }: { open: boolean; filtered: boolean; onClose: () => void; onSubmit: (values: any) => Promise<void> }) {
  const [form] = Form.useForm();
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!open) return;
    form.resetFields();
    form.setFieldsValue({ receiver: filtered ? '过滤用户' : '全部用户', subject: '', content: '' });
  }, [open, filtered]);

  const submit = async () => {
    const values = await form.validateFields();
    setLoading(true);
    try {
      await onSubmit({ subject: values.subject, content: values.content });
      message.success('已提交发送');
      onClose();
    } catch (e: any) {
      message.error(e.message || '发送失败');
    } finally {
      setLoading(false);
    }
  };

  return <Modal title="发送邮件" open={open} onCancel={onClose} onOk={submit} confirmLoading={loading} destroyOnHidden>
    <Form form={form} layout="vertical">
      <Form.Item name="receiver" label="收件人"><Input disabled /></Form.Item>
      <Form.Item name="subject" label="主题" rules={[{ required: true, message: '请输入邮件主题' }]}><Input placeholder="请输入邮件主题" /></Form.Item>
      <Form.Item name="content" label="发送内容" rules={[{ required: true, message: '请输入邮件内容' }]}><Input.TextArea rows={12} placeholder="请输入邮件内容" /></Form.Item>
    </Form>
  </Modal>;
}


const moneyFromCents = (value: any) => {
  if (value === undefined || value === null || value === '') return value;
  const n = Number(value);
  if (!Number.isFinite(n)) return value;
  return Number((n / 100).toFixed(2));
};

const centsFromMoney = (value: any) => {
  if (value === undefined || value === null || value === '') return value;
  const n = Number(value);
  if (!Number.isFinite(n)) return value;
  return Math.round(n * 100);
};


function downloadTextFile(filename: string, content: string) {
  const blob = new Blob([content], { type: 'text/plain;charset=UTF-8' });
  const url = window.URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.style.display = 'none';
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  document.body.removeChild(link);
  window.URL.revokeObjectURL(url);
}

const bytesFromGBForm = (value: any) => {
  if (value === undefined || value === null || value === '') return value;
  return gbToBytes(value);
};

const boolToNumber = (value: any) => (value === true || value === 1 || value === '1' ? 1 : 0);
const isNeverExpire = (value: any) => !Number(value || 0);

export function userToAdminFormValues(data: any = {}) {
  const expiredAt = Number(data.expired_at || 0);
  return {
    ...data,
    password: '',
    transfer_enable: bytesToGB(data.transfer_enable),
    u: bytesToGB(data.u),
    d: bytesToGB(data.d),
    balance: moneyFromCents(data.balance),
    commission_balance: moneyFromCents(data.commission_balance),
    invite_user_email: data.invite_user_email ?? data.invite_user?.email,
    never_expire: isNeverExpire(data.expired_at),
    expired_at: expiredAt > 0 ? dayjs(expiredAt * 1000) : null,
  };
}

export function userFormValuesToPayload(values: any = {}) {
  const payload = { ...values };
  payload.transfer_enable = bytesFromGBForm(values.transfer_enable);
  payload.u = bytesFromGBForm(values.u);
  payload.d = bytesFromGBForm(values.d);
  payload.balance = centsFromMoney(values.balance);
  payload.commission_balance = centsFromMoney(values.commission_balance);
  payload.expired_at = values.never_expire || !values.expired_at ? 0 : values.expired_at.unix();
  delete payload.never_expire;
  ['plan_id', 'device_limit', 'commission_rate', 'discount', 'speed_limit'].forEach((key) => {
    if (key in values && (payload[key] === undefined || payload[key] === null)) payload[key] = '';
  });
  if ('is_admin' in payload) payload.is_admin = boolToNumber(payload.is_admin);
  if ('is_staff' in payload) payload.is_staff = boolToNumber(payload.is_staff);
  if (payload.invite_user) delete payload.invite_user;
  if (payload.invite_user_email === undefined || payload.invite_user_email === null) payload.invite_user_email = '';
  return payload;
}

export default function UserPage() {
  const [rows, setRows] = useState<any[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [filters, setFilters] = useState<any[]>(initialFilters);
  const [page, setPage] = useState({ current: 1, pageSize: 50, sort: 'id', sort_type: 'DESC' });
  const [plans, setPlans] = useState<any[]>([]);
  const [groups, setGroups] = useState<any[]>([]);
  const [edit, setEdit] = useState<any>(null);
  const [trafficUser, setTrafficUser] = useState<any>(null);
  const [assignUser, setAssignUser] = useState<any>(null);
  const [generateOpen, setGenerateOpen] = useState(false);
  const [mailOpen, setMailOpen] = useState(false);
  const [form] = Form.useForm();

  const effectiveFilters = useMemo(() => filters.filter((item) => String(item.value ?? '').trim() !== ''), [filters]);


  const load = async (override: any = {}, filterOverride?: any[]) => {
    setLoading(true);
    try {
      const nextPage = { ...page, ...override };
      const activeFilters = filterOverride ?? effectiveFilters;
      const params: any = { current: nextPage.current, pageSize: nextPage.pageSize, sort: nextPage.sort, sort_type: nextPage.sort_type };
      if (activeFilters.length) params.filter = activeFilters;
      const [userRes, planRes, groupRes] = await Promise.all([
        apiGet('/user/fetch', params),
        plans.length ? Promise.resolve({ data: plans }) : apiGet('/plan/fetch').catch(() => ({ data: [] })),
        groups.length ? Promise.resolve({ data: groups }) : apiGet('/server/group/fetch').catch(() => ({ data: [] })),
      ]);
      setRows(userRes.data || []);
      setTotal(userRes.total || userRes.data?.total || 0);
      setPlans(planRes.data || []);
      setGroups(groupRes.data || []);
      setPage(nextPage);
    } catch (e: any) {
      message.error(e.message || '加载失败');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { load(); }, []);


  const save = async () => {
    const values = await form.validateFields();
    await apiPost('/user/update', { id: edit.id, ...userFormValuesToPayload(values) }, { form: true, keepEmpty: true });
    message.success('保存成功');
    setEdit(null);
    load();
  };

  const resetSecret = async (row: any) => {
    await apiPost('/user/resetSecret', { id: row.id }, { form: true });
    message.success('已重置UUID及订阅URL');
    load();
  };

  const copySubscribe = async (row: any) => {
    await navigator.clipboard?.writeText(row.subscribe_url || '');
    message.success('已复制订阅URL');
  };

  const batchAction = async (path: string, label: string) => {
    if (!effectiveFilters.length) return message.warning('请先设置过滤器');
    await apiPost(path, { filter: effectiveFilters }, { form: true });
    message.success(label);
    load();
  };

  const dumpCSV = async () => {
    if (!effectiveFilters.length) return message.warning('请先设置过滤器');
    try {
      const content = await apiPost('/user/dumpCSV', { filter: effectiveFilters }, { form: true, raw: true });
      downloadTextFile(`${dayjs().format('YYYY-MM-DD HH:mm:ss')}.csv`, content);
      message.success('已导出CSV');
    } catch (e: any) {
      message.error(e.message || '导出失败');
    }
  };

  const sendMail = async (values: any) => {
    await apiPost('/user/sendMail', { filter: effectiveFilters, ...values }, { form: true });
    load();
  };

  const rowMenu = (row: any): MenuProps['items'] => [
    { key: 'edit', label: <span><EditOutlined /> 编辑</span>, onClick: async () => { const d = await apiGet('/user/getUserInfoById', { id: row.id }).catch(() => ({ data: row })); const data = d.data || row; form.resetFields(); setEdit(data); form.setFieldsValue(userToAdminFormValues(data)); } },
    { key: 'assign', label: <span><UserAddOutlined /> 分配订单</span>, onClick: () => setAssignUser(row) },
    { key: 'copy', label: <span><CopyOutlined /> 复制订阅URL</span>, onClick: () => copySubscribe(row) },
    { key: 'reset', danger: true, label: <span><ReloadOutlined /> 重置UUID及订阅URL</span>, onClick: () => resetSecret(row) },
    { key: 'orders', label: <span><AccountBookOutlined /> TA的订单</span>, onClick: () => navigateAdmin(`/order?filter_key=user_id&condition=${encodeURIComponent('=')}&value=${encodeURIComponent(String(row.id))}`) },
    { key: 'invites', label: <span><UsergroupAddOutlined /> TA的邀请</span>, onClick: () => { const next = [{ key: 'invite_user_id', condition: '=', value: row.id }]; setFilters(next); load({ current: 1 }, next); } },
    { key: 'traffic', label: <span><SolutionOutlined /> TA的流量记录</span>, onClick: () => setTrafficUser(row) },
    { type: 'divider' as const },
    { key: 'delete', danger: true, label: <Popconfirm title={`确定要删除 ${row.email} 的用户信息吗？`} onConfirm={async () => { await apiPost('/user/delUser', { id: row.id }, { form: true }); message.success('已删除'); load(); }}><span><DeleteOutlined /> 删除用户</span></Popconfirm> },
  ];

  const batchMenu: MenuProps['items'] = [
    { key: 'csv', label: <span><ExportOutlined /> 导出CSV</span>, onClick: dumpCSV },
    { key: 'mail', label: <span><MailOutlined /> 发送邮件</span>, onClick: () => setMailOpen(true) },
    { key: 'ban', label: <span><StopOutlined /> 批量封禁</span>, disabled: !effectiveFilters.length, onClick: () => batchAction('/user/ban', '已批量封禁') },
    { key: 'delete', danger: true, label: <span><DeleteOutlined /> 批量删除</span>, disabled: !effectiveFilters.length, onClick: () => batchAction('/user/allDel', '已批量删除') },
  ];

  const columns: any[] = [
    { title: 'ID', dataIndex: 'id', sorter: true, width: 80 },
    { title: '邮箱', dataIndex: 'email', width: 240, render: (email: any, row: any) => <Tooltip title={row.t ? `最后在线${dayjs(row.t * 1000).format('YYYY-MM-DD HH:mm:ss')}` : '从未在线'}><Space><Badge status={Date.now() / 1000 - 600 > Number(row.t || 0) ? 'default' : 'success'} />{email}</Space></Tooltip> },
    { title: '状态', dataIndex: 'banned', sorter: true, width: 90, render: (banned: any) => <Tag color={banned ? 'red' : 'green'}>{banned ? '封禁' : '正常'}</Tag> },
    { title: '订阅', dataIndex: 'plan_name', sorter: true, width: 160, render: (v: any) => v || '-' },
    { title: '权限组', dataIndex: 'group_id', sorter: true, width: 130, render: (id: any) => groups.find((g) => Number(g.id) === Number(id))?.name || id || '-' },
    { title: '已用(G)', dataIndex: 'total_used', sorter: true, width: 110, render: (_: any, row: any) => <Tag color={Number(row.total_used || row.u + row.d || 0) > Number(row.transfer_enable || 0) ? 'red' : 'green'}>{bytesToGBText(row.total_used ?? (Number(row.u || 0) + Number(row.d || 0)))}</Tag> },
    { title: '流量(G)', dataIndex: 'transfer_enable', sorter: true, width: 110, render: bytesToGBText },
    { title: '设备数', dataIndex: 'device_limit', sorter: true, width: 110, render: (_: any, row: any) => <Tooltip title={row.ips || ''}>{row.alive_ip ?? 0} / {row.device_limit ?? '∞'}</Tooltip> },
    { title: '到期时间', dataIndex: 'expired_at', sorter: true, width: 160, render: (ts: any) => <Tag color={!isNeverExpire(ts) && Number(ts) < Date.now() / 1000 ? 'red' : 'green'}>{isNeverExpire(ts) ? '长期有效' : dateText(ts)}</Tag> },
    { title: '余额', dataIndex: 'balance', sorter: true, width: 90, render: moneyText },
    { title: '佣金', dataIndex: 'commission_balance', sorter: true, width: 90, render: moneyText },
    { title: '加入时间', dataIndex: 'created_at', sorter: true, width: 160, render: dateText },
    { title: '最后在线', dataIndex: 't', width: 160, render: (_: any, row: any) => row.t ? unixTime(row.t) : (row.last_login_at ? unixTime(row.last_login_at) : '-') },
    { title: '操作', dataIndex: 'action', align: 'right', fixed: 'right', width: 120, render: (_: any, row: any) => <Dropdown trigger={['click']} menu={{ items: rowMenu(row) }}><a>操作 <DownOutlined /></a></Dropdown> },
  ];

  return <div className="legacy-page user-page">
    <div className="content-heading">用户管理</div>
    <Card className="block-card" styles={{ body: { padding: 0 } }}>
      <div className="forest-table-action">
        <Tooltip title="Tips：可以使用过滤器过滤后再使用操作对过滤的用户进行操作。" placement="right">
          <Space>
            <LegacyFilterDrawer value={filters} keys={filterDefinitions as any} onOk={(next) => { setFilters(next); load({ current: 1 }, next.filter((item) => String(item.value ?? '').trim() !== '')); }}>
              <FilterButton active={effectiveFilters.length > 0} />
            </LegacyFilterDrawer>
            <Dropdown menu={{ items: batchMenu }}><Button>操作</Button></Dropdown>
            <Button className="ml-2" icon={<UserAddOutlined />} onClick={() => setGenerateOpen(true)} />
          </Space>
        </Tooltip>
      </div>
      <Table className="forest-table" rowKey="id" loading={loading} columns={columns} dataSource={rows} pagination={{ total, current: page.current, pageSize: page.pageSize, size: 'small', showSizeChanger: true, pageSizeOptions: [10, 50, 100, 150] }} scroll={{ x: 1700 }} onChange={(pagination: any, _filters: any, sorter: any) => load({ current: pagination.current, pageSize: pagination.pageSize, sort: sorter.field || page.sort, sort_type: sorter.order === 'ascend' ? 'ASC' : 'DESC' })} />
    </Card>
    <Modal title="编辑用户" open={!!edit} onCancel={() => { setEdit(null); form.resetFields(); }} onOk={save} width={720} destroyOnHidden>
      <Form form={form} layout="vertical" className="modal-grid-form user-edit-form">
        <Form.Item name="email" label="邮箱"><Input placeholder="请输入邮箱" /></Form.Item>
        <Form.Item name="invite_user_email" label="邀请人邮箱"><Input placeholder="请输入邀请人邮箱" /></Form.Item>
        <Form.Item name="invite_code" label="邀请码"><Input placeholder="可直接修改，例如 888" /></Form.Item>
        <Form.Item name="password" label="密码"><Input.Password placeholder="如需修改密码请输入" autoComplete="new-password" /></Form.Item>
        <Form.Item name="balance" label="余额"><InputNumber addonAfter="¥" style={{ width: '100%' }} placeholder="余额" /></Form.Item>
        <Form.Item name="commission_balance" label="推广佣金"><InputNumber addonAfter="¥" style={{ width: '100%' }} placeholder="推广佣金" /></Form.Item>
        <Form.Item name="u" label="已用上行"><InputNumber addonAfter="GB" style={{ width: '100%' }} placeholder="已用上行" /></Form.Item>
        <Form.Item name="d" label="已用下行"><InputNumber addonAfter="GB" style={{ width: '100%' }} placeholder="已用下行" /></Form.Item>
        <Form.Item name="transfer_enable" label="流量"><InputNumber addonAfter="GB" style={{ width: '100%' }} placeholder="请输入流量" /></Form.Item>
        <Form.Item name="device_limit" label="设备数限制"><InputNumber style={{ width: '100%' }} placeholder="留空则不限制" /></Form.Item>
        <Form.Item name="never_expire" label="长期有效" valuePropName="checked"><Switch checkedChildren="不限时" unCheckedChildren="指定时间" onChange={(checked) => { if (checked) form.setFieldValue('expired_at', null); }} /></Form.Item>
        <Form.Item noStyle shouldUpdate={(prev, next) => prev.never_expire !== next.never_expire}>
          {({ getFieldValue }) => <Form.Item name="expired_at" label="到期时间"><DatePicker showTime disabled={!!getFieldValue('never_expire')} style={{ width: '100%' }} placeholder="长期有效" /></Form.Item>}
        </Form.Item>
        <Form.Item name="plan_id" label="订阅计划"><Select allowClear placeholder="请选择用户订阅计划" options={[{ label: '无', value: null }, ...plans.map((plan) => ({ label: plan.name, value: plan.id }))]} /></Form.Item>
        <Form.Item name="banned" label="账户状态"><Select options={[{ label: '封禁', value: 1 }, { label: '正常', value: 0 }]} /></Form.Item>
        <Form.Item name="commission_type" label="推荐返利类型"><Select options={[{ label: '跟随系统设置', value: 0 }, { label: '循环返利', value: 1 }, { label: '首次返利', value: 2 }]} /></Form.Item>
        <Form.Item name="commission_rate" label="推荐返利比例"><InputNumber addonAfter="%" style={{ width: '100%' }} placeholder="请输入推荐返利比例(为空则跟随站点设置返利比例)" /></Form.Item>
        <Form.Item name="discount" label="专享折扣比例"><InputNumber addonAfter="%" style={{ width: '100%' }} placeholder="请输入专享折扣比例" /></Form.Item>
        <Form.Item name="speed_limit" label="限速"><InputNumber addonAfter="Mbps" style={{ width: '100%' }} placeholder="留空则不限制" /></Form.Item>
        <Form.Item name="is_admin" label="是否管理员" valuePropName="checked"><Switch /></Form.Item>
        <Form.Item name="is_staff" label="是否员工" valuePropName="checked"><Switch /></Form.Item>
        <Form.Item name="remarks" label="备注" className="form-col-full"><Input.TextArea rows={4} placeholder="请在这里记录.." /></Form.Item>
      </Form>
    </Modal>
    <UserTrafficModal user={trafficUser} open={!!trafficUser} onClose={() => setTrafficUser(null)} />
    <UserAssignOrderModal user={assignUser} open={!!assignUser} plans={plans} onClose={() => setAssignUser(null)} onDone={() => load()} />
    <UserGenerateModal open={generateOpen} plans={plans} onClose={() => setGenerateOpen(false)} onDone={() => load()} />
    <UserMailModal open={mailOpen} filtered={effectiveFilters.length > 0} onClose={() => setMailOpen(false)} onSubmit={sendMail} />
  </div>;
}
