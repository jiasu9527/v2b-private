import React, { useEffect, useRef, useState } from 'react';
import { Alert, AutoComplete, Button, Form, Input, InputNumber, Modal, Select, Space, Switch, Table, Typography, message } from 'antd';
import { apiGet, apiJsonPost } from '../lib/api';
import { splitMemberKey, type ClientEntryServerOption } from './clientEntryHelpers';
import { ENTRY_EVIDENCE_PATH } from './entryEvidence';

type Host = { host: string; users: number; last_at: number };
type User = { user_id: number; email_at_event: string; first_at: number; last_at: number; requests: number };
type Snapshot = { history_host: string; from: number; to: number; user_count: number; snapshot_hash: string };
const date = (value: number) => value ? new Date(value * 1000).toLocaleString() : '—';
const unwrap = (response: any) => response?.data;

export default function EntryHistoryIP({ serverOptions, onDone, disabled }: { serverOptions: ClientEntryServerOption[]; onDone: () => void; disabled?: boolean }) {
  const [open, setOpen] = useState(false);
  const [host, setHost] = useState('');
  const [days, setDays] = useState(7);
  const [hosts, setHosts] = useState<Host[]>([]);
  const [hostsTotal, setHostsTotal] = useState(0);
  const [hostsPage, setHostsPage] = useState(0);
  const [hostLoading, setHostLoading] = useState(false);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [snapshot, setSnapshot] = useState<Snapshot>();
  const [users, setUsers] = useState<User[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const sequence = useRef(0);
  const hostSequence = useRef(0);
  const [form] = Form.useForm();

  const loadHosts = async (current = 1) => {
    const seq = ++hostSequence.current;
    setHostLoading(true);
    try {
      const data = unwrap(await apiGet(`${ENTRY_EVIDENCE_PATH}/delivery-hosts`, { current, page_size: 100 }));
      if (seq !== hostSequence.current) return;
      if (!Array.isArray(data?.items) || !Number.isFinite(data?.total)) throw new Error('历史入口列表格式无效，请更新后端');
      setHosts((old) => current === 1 ? data.items : [...old, ...data.items]);
      setHostsTotal(data.total); setHostsPage(current);
    } catch (error: any) { if (seq === hostSequence.current) message.error(error?.message || '读取历史入口失败'); }
    finally { if (seq === hostSequence.current) setHostLoading(false); }
  };
  useEffect(() => () => { sequence.current++; hostSequence.current++; }, []);
  const invalidate = () => { sequence.current++; setSnapshot(undefined); setUsers([]); setTotal(0); setLoading(false); };
  const close = () => { if (saving) return; invalidate(); hostSequence.current++; setOpen(false); };
  const show = () => {
    invalidate(); setHost(''); setDays(7); form.resetFields(); form.setFieldsValue({ enabled: true, members: [] });
    setHosts([]); setOpen(true); void loadHosts();
  };
  const readUsers = async (s: Snapshot, current: number) => {
    const data = unwrap(await apiGet(`${ENTRY_EVIDENCE_PATH}/delivery-users`, { host: s.history_host, from: s.from, to: s.to, current, page_size: 20 }));
    if (!Array.isArray(data?.items) || !Number.isFinite(data?.total)) throw new Error('历史用户名单格式无效');
    return data;
  };
  const query = async () => {
    const selected = host.trim(); if (!selected) { message.warning('请选择或输入实际下发的入口 IP'); return; }
    const seq = ++sequence.current; setLoading(true); setSnapshot(undefined); setUsers([]); setTotal(0);
    try {
      const s: Snapshot = unwrap(await apiGet('/server/client-entry-user-policy/split-preview', { history_host: selected, minutes: days * 1440 }));
      if (!s?.snapshot_hash || s.history_host !== selected.toLowerCase() || !Number.isFinite(s.user_count)) throw new Error('历史预览不完整，请更新后端后重试');
      const data = await readUsers(s, 1);
      if (seq !== sequence.current) return;
      setSnapshot(s); setUsers(data.items); setTotal(data.total); setPage(1);
    } catch (error: any) { if (seq === sequence.current) message.error(error?.message || '查询历史拉取失败'); }
    finally { if (seq === sequence.current) setLoading(false); }
  };
  const changePage = async (current: number) => {
    if (!snapshot) return;
    const seq = ++sequence.current; setLoading(true);
    try { const data = await readUsers(snapshot, current); if (seq === sequence.current) { setUsers(data.items); setTotal(data.total); setPage(current); } }
    catch (error: any) { if (seq === sequence.current) message.error(error?.message || '读取用户失败'); }
    finally { if (seq === sequence.current) setLoading(false); }
  };
  const save = async () => {
    if (!snapshot || snapshot.user_count < 2) return;
    setSaving(true);
    try {
      const v = await form.validateFields();
      await apiJsonPost('/server/client-entry-user-policy/split-create', {
        name: String(v.name || '').trim(), history_host: snapshot.history_host, history_from: snapshot.from, history_to: snapshot.to, snapshot_hash: snapshot.snapshot_hash,
        members: (v.members || []).map(splitMemberKey).filter(Boolean), entry_host_a: String(v.entry_host_a || '').trim(), entry_host_b: String(v.entry_host_b || '').trim(),
        enabled: v.enabled ? 1 : 0, resolve_entry_host: 0, remarks: `历史实际下发 ${snapshot.history_host}；${date(snapshot.from)} ～ ${date(snapshot.to)}`,
      });
      message.success('历史用户名单已固定并二分，可在入口分组继续二分'); setOpen(false); invalidate(); onDone();
    } catch (error: any) { if (!error?.errorFields) message.error(error?.message || '创建失败，名单变化时请重新查询'); }
    finally { setSaving(false); }
  };
  return <>
    <Button disabled={disabled} onClick={show}>历史拉取 IP</Button>
    <Modal title="历史拉取 IP · 用户名单与二分" open={open} onCancel={close} width={1000} footer={null} destroyOnHidden>
      <Space direction="vertical" style={{ width: '100%' }} size="middle">
        <Alert type="info" showIcon message="只统计最终订阅响应确实包含该入口的用户，按用户 ID 去重" description="跨规则、跨节点、跨端口取并集，不以当前规则反推。服务端返回不代表客户端已导入或连接。日志默认保留 7 天，超过保留期或上线前的缺失记录无法补算；域名仅在服务端实际解析为 IP 下发时才计入对应 IP。" />
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, minWidth: 0 }}>
          <AutoComplete aria-label="历史入口 IP" value={host} options={hosts.map((v) => ({ value: v.host, label: `${v.host} · ${v.users} 人` }))} style={{ width: 330, maxWidth: '100%' }} placeholder="选择历史入口 IP，也可直接输入" filterOption={(input, option) => String(option?.value || '').includes(input.trim())} onChange={(value) => { setHost(value); invalidate(); }} disabled={saving} />
          <InputNumber aria-label="历史拉取天数" min={1} max={30} precision={0} value={days} addonBefore="最近" addonAfter="天" onChange={(value) => { setDays(value || 7); invalidate(); }} disabled={saving} />
          <Button type="primary" loading={loading} disabled={saving || !host.trim()} onClick={() => void query()}>查询用户</Button>
          <Button loading={hostLoading} disabled={hostLoading || saving} onClick={() => void loadHosts(hostsPage && hosts.length < hostsTotal ? hostsPage + 1 : 1)}>{hosts.length < hostsTotal ? `加载更多入口（${hosts.length}/${hostsTotal}）` : '刷新入口列表'}</Button>
        </div>
        {snapshot && <Typography.Text>历史去重 {total} 人，其中现存可分组 {snapshot.user_count} 人；已删除账号只展示历史，不加入新组。窗口：{date(snapshot.from)} ～ {date(snapshot.to)}。查询后的新拉取不会混入本次固定名单。</Typography.Text>}
        <Table<User> size="small" rowKey="user_id" loading={loading} dataSource={users} scroll={{ x: 800 }} pagination={{ current: page, total, pageSize: 20, showSizeChanger: false, onChange: (current) => void changePage(current) }} columns={[
          { title: '用户 ID', dataIndex: 'user_id', width: 100 }, { title: '拉取时邮箱', dataIndex: 'email_at_event', width: 230 },
          { title: '首次下发', dataIndex: 'first_at', render: date, width: 170 }, { title: '最近下发', dataIndex: 'last_at', render: date, width: 170 }, { title: '请求次数', dataIndex: 'requests', width: 100 },
        ]} locale={{ emptyText: '选择入口后查询保留期内的历史用户' }} />
        <details><summary>固定此名单并二分{snapshot ? `（A ${Math.ceil(snapshot.user_count / 2)} / B ${Math.floor(snapshot.user_count / 2)} 人）` : ''}</summary>
          <Alert style={{ margin: '12px 0' }} type="warning" showIcon message="新建独立固定名单，不修改原规则" description="两组使用你选择的节点，继承这些节点原端口；不会增加用户套餐或节点权限。创建后请检查规则顺序，更靠前的规则仍可能先命中。名单不随日志过期而丢失。" />
          <Form form={form} layout="vertical" disabled={saving}>
            <Form.Item name="name" label="规则名称" rules={[{ required: true, whitespace: true }]}><Input maxLength={100} /></Form.Item>
            <Form.Item name="members" label="共同生效节点" rules={[{ required: true, type: 'array', min: 1 }]}><Select mode="multiple" options={serverOptions} optionFilterProp="label" placeholder="选择需要分配入口的节点" /></Form.Item>
            <Form.Item name="entry_host_a" label="A 组新入口" rules={[{ required: true, whitespace: true }]}><Input placeholder="IP 或域名" /></Form.Item>
            <Form.Item name="entry_host_b" label="B 组新入口" rules={[{ required: true, whitespace: true }]}><Input placeholder="IP 或域名" /></Form.Item>
            <Form.Item name="enabled" label="启用规则" valuePropName="checked"><Switch /></Form.Item>
            <Button type="primary" loading={saving} disabled={loading || !snapshot || snapshot.user_count < 2} onClick={() => void save()}>固定名单并创建二分</Button>
          </Form>
        </details>
      </Space>
    </Modal>
  </>;
}
