import React, { useEffect, useRef, useState } from 'react';
import { Badge, Button, DatePicker, Drawer, Form, Input, InputNumber, Modal, Radio, Space, Spin, Table, Tooltip, message } from 'antd';
import { SolutionOutlined, UserOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { apiGet, apiPost, bytes, getAdminPath } from '../lib/api';
import { userFormValuesToPayload, userToAdminFormValues } from './UserPage';

const levels = ['低', '中', '高'];

function time(value: any) {
  return value ? dayjs(value * 1000).format('YYYY/MM/DD HH:mm') : '-';
}

function isMobileWindow() {
  const ua = window.navigator.userAgent.toLowerCase();
  return ua.includes('mobile') || ua.includes('ipad');
}

function ticketChatUrl(id: any) {
  const adminPath = String(getAdminPath() || 'localadmin').replace(/^\/+|\/+$/g, '');
  return `${window.location.origin}/${adminPath}#/ticket/${id}`;
}

function UserManageDrawer({ userId, open, onClose }: { userId?: any; open: boolean; onClose: () => void }) {
  const [form] = Form.useForm();
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);

  const load = async () => {
    if (!open || !userId) return;
    setLoading(true);
    try {
      const res = await apiGet('/user/getUserInfoById', { id: userId });
      form.setFieldsValue(userToAdminFormValues(res.data || {}));
    } catch (e: any) {
      message.error(e.message || '用户信息加载失败');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { load(); }, [open, userId]);

  const saveUser = async () => {
    if (!userId) return;
    const values = await form.validateFields();
    setSaving(true);
    try {
      await apiPost('/user/update', { id: userId, ...userFormValuesToPayload(values) }, { form: true, keepEmpty: true });
      message.success('保存成功');
      onClose();
    } catch (e: any) {
      message.error(e.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };

  return <Drawer className="legacy-drawer ticket-user-drawer" title="用户管理" width="80%" open={open} onClose={onClose} destroyOnHidden>
    <Spin spinning={loading}>
      <Form form={form} layout="vertical" className="modal-grid-form ticket-user-form">
        <Form.Item hidden name="banned"><Input type="hidden" /></Form.Item>
        <Form.Item hidden name="is_admin"><Input type="hidden" /></Form.Item>
        <Form.Item hidden name="is_staff"><Input type="hidden" /></Form.Item>
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
        <Form.Item name="expired_at" label="到期时间"><DatePicker showTime style={{ width: '100%' }} placeholder="长期有效" /></Form.Item>
        <Form.Item name="speed_limit" label="限速"><InputNumber addonAfter="Mbps" style={{ width: '100%' }} placeholder="留空则不限制" /></Form.Item>
        <Form.Item name="remarks" label="备注"><Input.TextArea rows={4} placeholder="请在这里记录.." /></Form.Item>
      </Form>
    </Spin>
    <div className="forest-drawer-action"><Space><Button onClick={onClose}>取消</Button><Button loading={saving} type="primary" onClick={saveUser}>提交</Button></Space></div>
  </Drawer>;
}

function UserTrafficModal({ userId, open, onClose }: { userId?: any; open: boolean; onClose: () => void }) {
  const [records, setRecords] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [pagination, setPagination] = useState({ current: 1, pageSize: 10, total: 0 });

  const load = async (override: any = {}) => {
    if (!open || !userId) return;
    const next = { ...pagination, ...override };
    setLoading(true);
    try {
      const res = await apiGet('/stat/getStatUser', { user_id: userId, page: next.current, current: next.current, pageSize: next.pageSize });
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
  }, [open, userId]);

  const columns: any[] = [
    { title: '日期', dataIndex: 'record_at', key: 'record_at', render: (value: any) => value ? dayjs(Number(value) * 1000).format('YYYY-MM-DD') : '-' },
    { title: '上行', dataIndex: 'u', key: 'u', align: 'right', render: bytes },
    { title: '下行', dataIndex: 'd', key: 'd', align: 'right', render: bytes },
    { title: '倍率', dataIndex: 'server_rate', key: 'server_rate', align: 'right' },
  ];

  return <Modal title="流量记录" open={open} onCancel={onClose} footer={null} width={1000} styles={{ body: { padding: 0 } }} destroyOnHidden>
    <Spin spinning={loading}>
      <Table className="forest-table" rowKey={(row, index) => `${row.record_at || 'row'}-${index}`} columns={columns} dataSource={records} pagination={{ ...pagination, size: 'small' }} onChange={(pageInfo: any) => load({ current: pageInfo.current, pageSize: pageInfo.pageSize })} />
    </Spin>
  </Modal>;
}

function TicketChat({ ticket, text, onText, onSend, sending, standalone = false }: { ticket: any; text: string; onText: (value: string) => void; onSend: () => void; sending?: boolean; standalone?: boolean }) {
  const messages = ticket?.message || [];
  const chatRef = useRef<HTMLDivElement | null>(null);
  const [userDrawerOpen, setUserDrawerOpen] = useState(false);
  const [trafficOpen, setTrafficOpen] = useState(false);

  useEffect(() => {
    if (chatRef.current) chatRef.current.scrollTo(0, chatRef.current.scrollHeight);
  }, [messages.length]);

  return <div className={standalone ? 'ticket-chat-standalone' : ''}>
    <div className="block-content-full bg-gray-lighter p-3 ticket-chat-head">
      <span className="ticket-chat-tag">{ticket?.subject || `#${ticket?.id || ''}`}</span>
      <div className="ticket-chat-ctrl">
        <Tooltip title="用户管理" placement="left"><Button type="text" size="small" icon={<UserOutlined />} onClick={() => ticket?.user_id && setUserDrawerOpen(true)} /></Tooltip>
        <span className="ant-divider ant-divider-vertical" />
        <Tooltip title="TA的流量记录" placement="left"><Button type="text" size="small" icon={<SolutionOutlined />} onClick={() => ticket?.user_id && setTrafficOpen(true)} /></Tooltip>
      </div>
    </div>
    <div ref={chatRef} className="bg-white js-chat-messages block-content block-content-full text-wrap-break-word overflow-y-auto chat-messages ticket-chat-content">
      {messages.map((item: any) => item.is_me ? <div key={item.id}>
        <div className="font-size-sm text-muted my-2 text-right">{time(item.created_at)}</div>
        <div className="text-right ml-4"><div className="d-inline-block bg-gray-lighter px-3 py-2 mb-2 mw-100 rounded text-left">{item.message}</div></div>
      </div> : <div key={item.id}>
        <div className="font-size-sm text-muted my-2">{time(item.created_at)}</div>
        <div className="mr-4"><div className="d-inline-block bg-success-lighter px-3 py-2 mb-2 mw-100 rounded text-left">{item.message}</div></div>
      </div>)}
    </div>
    <div className="js-chat-form block-content p-2 bg-body-dark chat-input-row ticket-chat-input">
      <Input value={text} onChange={(e) => onText(e.target.value)} onPressEnter={onSend} placeholder="输入内容回复工单..." className="bg-body-dark border-0 form-control form-control-alt" disabled={sending} />
      {!standalone && <Button type="primary" loading={sending} onClick={onSend}>回复</Button>}
    </div>
    <UserManageDrawer userId={ticket?.user_id} open={userDrawerOpen} onClose={() => setUserDrawerOpen(false)} />
    <UserTrafficModal userId={ticket?.user_id} open={trafficOpen} onClose={() => setTrafficOpen(false)} />
  </div>;
}

export function TicketDetailPage({ ticketId, standalone = true }: { ticketId: string | number; standalone?: boolean }) {
  const [ticket, setTicket] = useState<any>(null);
  const [text, setText] = useState('');
  const [loading, setLoading] = useState(false);
  const [sending, setSending] = useState(false);

  const load = async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const res = await apiGet('/ticket/fetch', { id: ticketId });
      setTicket(res.data || null);
    } catch (e: any) {
      if (!silent) message.error(e.message || '加载失败');
    } finally {
      if (!silent) setLoading(false);
    }
  };

  useEffect(() => {
    load();
    const timer = window.setInterval(() => load(true), 5000);
    return () => window.clearInterval(timer);
  }, [ticketId]);

  const sendReply = async () => {
    if (!ticketId || sending) return;
    const messageText = text.trim();
    if (!messageText) return message.warning('消息不能为空');
    setSending(true);
    try {
      await apiPost('/ticket/reply', { id: ticketId, message: messageText }, { form: true });
      setText('');
      await load(true);
    } catch (e: any) {
      message.error(e.message || '回复失败');
    } finally {
      setSending(false);
    }
  };

  return <Spin spinning={loading}>
    <TicketChat ticket={ticket || { id: ticketId, message: [] }} text={text} onText={setText} onSend={sendReply} sending={sending} standalone={standalone} />
  </Spin>;
}

export default function TicketPage() {
  const [rows, setRows] = useState<any[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [status, setStatus] = useState<any>(0);
  const [replyStatus, setReplyStatus] = useState<any[] | undefined>(undefined);
  const [email, setEmail] = useState('');
  const [page, setPage] = useState({ current: 1, pageSize: 50 });

  const load = async (override: any = {}) => {
    setLoading(true);
    try {
      const next = { ...page, ...override };
      const params: any = { current: next.current, pageSize: next.pageSize, status };
      if (replyStatus?.length) params.reply_status = replyStatus;
      if (email.trim()) params.email = email.trim();
      const res = await apiGet('/ticket/fetch', params);
      setRows(res.data || []);
      setTotal(res.total || 0);
      setPage(next);
    } catch (e: any) {
      message.error(e.message || '加载失败');
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => { load({ current: 1 }); }, [status, replyStatus]);

  const openDetail = (row: any) => {
    const url = ticketChatUrl(row.id);
    if (isMobileWindow()) {
      window.location.href = url;
      return;
    }
    window.open(url, '_blank', 'height=600,width=800,top=0,left=0,toolbar=no,menubar=no,scrollbars=no,resizable=no,location=no,status=no');
  };

  const closeTicket = async (row: any) => {
    if (Number(row.status)) return;
    await apiPost('/ticket/close', { id: row.id }, { form: true });
    message.success('已关闭');
    load();
  };

  const columns: any[] = [
    { title: '#', dataIndex: 'id', width: 80 },
    { title: '主题', dataIndex: 'subject', width: 260 },
    { title: '工单级别', dataIndex: 'level', width: 110, render: (value: any) => levels[Number(value)] || value },
    { title: '工单状态', dataIndex: 'reply_status', width: 130, filteredValue: replyStatus || null, filters: Number(status) !== 1 ? [{ text: '已回复', value: 1 }, { text: '待回复', value: 0 }] : undefined, render: (value: any, row: any) => Number(row.status) === 1 ? <span><Badge status="success" /> 已关闭</span> : <span><Badge status={Number(value) ? 'processing' : 'error'} /> {Number(value) ? '已回复' : '待回复'}</span> },
    { title: '创建时间', dataIndex: 'created_at', width: 170, render: time },
    { title: '最后回复', dataIndex: 'updated_at', width: 170, render: time },
    { title: '操作', dataIndex: 'action', align: 'right', fixed: 'right', width: 120, render: (_: any, row: any) => <Space split={<span className="ant-divider ant-divider-vertical" />}><a onClick={() => openDetail(row)}>查看</a><a className={Number(row.status) ? 'disabled-link' : ''} onClick={() => closeTicket(row)}>关闭</a></Space> },
  ];

  return <div className="legacy-page ticket-page">
    <div className="content-heading">工单管理</div>
    <Spin spinning={loading}>
      <div className="block border-bottom">
        <div className="bg-white">
          <div className="p-3 ticket-toolbar">
            <Radio.Group value={status} onChange={(e) => { setReplyStatus(undefined); setStatus(e.target.value); }}>
              <Radio.Button value={0}>已开启</Radio.Button>
              <Radio.Button value={1}>已关闭</Radio.Button>
            </Radio.Group>
            <div className="ticket-search"><Input allowClear placeholder="输入邮箱搜索" value={email} onChange={(e) => setEmail(e.target.value)} onPressEnter={() => load({ current: 1 })} /></div>
          </div>
          <Table className="forest-table" tableLayout="auto" dataSource={rows} pagination={{ total, current: page.current, pageSize: page.pageSize, size: 'small' }} columns={columns} rowKey="id" scroll={{ x: 900 }} onChange={(pagination: any, filters: any) => { setReplyStatus(filters.reply_status as any); load({ current: pagination.current, pageSize: pagination.pageSize }); }} />
        </div>
      </div>
    </Spin>
  </div>;
}
