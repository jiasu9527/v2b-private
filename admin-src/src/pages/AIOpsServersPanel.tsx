import React, { useState } from 'react';
import { Button, Empty, Input, Pagination, Space, Table, Tag, Typography, Select, Modal, Form, message } from 'antd';
import { PlusOutlined, ReloadOutlined, SearchOutlined, FolderOutlined } from '@ant-design/icons';
import type { AIServer } from './aiOpsModel';
import { aiServerConnection } from './aiOpsServerIdentity';

type Props = {
  servers: AIServer[]; compact: boolean; busy: boolean; unavailable: boolean;
  onEdit: (server: AIServer | null) => void; onCheck: (server: AIServer) => void;
  onDeploy: (server: AIServer) => void; onDelete: (server: AIServer) => void;
  onRefresh: () => void;
  onSetGroup: (server: AIServer, group: string) => Promise<void>;
};

export function filterAIServers(servers: AIServer[], query: string): AIServer[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return servers;
  return servers.filter((server) => [server.name, server.host, server.username, String(server.id), server.group || ''].some((value) => String(value).toLowerCase().includes(needle)));
}

export function normalizeAIServerGroup(value: unknown): string {
  const choices = Array.isArray(value) ? value : [value];
  if (choices.length > 1) throw new Error('每台服务器只能选择一个分组');
  const group = String(choices[0] || '').trim();
  if ([...group].length > 100 || new TextEncoder().encode(group).length > 400) throw new Error('分组名称最多 100 个字符');
  if (/[\u0000-\u001f\u007f-\u009f]/u.test(group)) throw new Error('分组名称不能包含控制字符');
  return group;
}

export default function AIOpsServersPanel({ servers, compact, busy, unavailable, onEdit, onCheck, onDeploy, onDelete, onRefresh, onSetGroup }: Props) {
  const [query, setQuery] = useState('');
  const [page, setPage] = useState(1);
  const [selectedGroup, setSelectedGroup] = useState<string>('__all__');
  const [groupModalVisible, setGroupModalVisible] = useState(false);
  const [editingServer, setEditingServer] = useState<AIServer | null>(null);
  const [savingGroup, setSavingGroup] = useState(false);
  const [form] = Form.useForm();
  
  const PAGE_SIZE = 50; // 每页50台
  
  // 提取所有分组
  const groups = Array.from(new Set(servers.map(s => s.group?.trim() || '').filter(Boolean))).sort((a, b) => a.localeCompare(b, 'zh-CN'));
  
  // 过滤服务器
  let filtered = filterAIServers(servers, query);
  if (selectedGroup !== '__all__') {
    filtered = filtered.filter(s => (s.group?.trim() || '') === (selectedGroup === '__none__' ? '' : selectedGroup.slice('group:'.length)));
  }
  
  const current = Math.min(page, Math.max(1, Math.ceil(filtered.length / PAGE_SIZE)));
  
  const status = (server: AIServer) => <Space size={4} wrap>
    <Tag color={server.enabled ? 'success' : 'default'}>{server.enabled ? '启用' : '停用'}</Tag>
    {server.has_password && <Tag>密码</Tag>}
    {server.has_private_key && <Tag>密钥</Tag>}
  </Space>;
  
  const actions = (server: AIServer) => <div className="ai-ops-server-actions">
    <Button type="link" size="small" disabled={busy} onClick={() => onEdit(server)}>编辑</Button>
    <Button type="link" size="small" icon={<FolderOutlined />} disabled={busy || savingGroup} onClick={() => { setEditingServer(server); form.setFieldsValue({ group: server.group?.trim() ? [server.group.trim()] : [] }); setGroupModalVisible(true); }}>分组</Button>
    <Button type="link" size="small" disabled={busy || !server.enabled || unavailable} onClick={() => onCheck(server)}>检查</Button>
    <Button type="link" size="small" disabled={busy || !server.enabled || unavailable} onClick={() => onDeploy(server)}>部署</Button>
    <Button type="link" size="small" danger disabled={busy} onClick={() => onDelete(server)}>删除</Button>
  </div>;
  
  const closeGroup = () => {
    if (savingGroup) return;
    setGroupModalVisible(false);
    setEditingServer(null);
    form.resetFields();
  };
  const handleSetGroup = async () => {
    if (!editingServer || savingGroup || busy) return;
    try {
      const values = await form.validateFields();
      const group = normalizeAIServerGroup(values.group);
      setSavingGroup(true);
      await onSetGroup(editingServer, group);
      setSavingGroup(false);
      setGroupModalVisible(false);
      setEditingServer(null);
      form.resetFields();
      message.success(group ? '服务器分组已保存' : '已移出分组');
    } catch (error) {
      setSavingGroup(false);
      if (!(error && typeof error === 'object' && 'errorFields' in error)) message.error(error instanceof Error ? error.message : '分组保存失败，请刷新后重试');
    }
  };
  
  return <section className="ai-ops-servers" aria-label="已登记服务器">
    <div className="ai-ops-server-toolbar">
      <Input aria-label="搜索服务器" placeholder="搜索名称、地址、分组" prefix={<SearchOutlined />} allowClear value={query} onChange={(event) => { setQuery(event.target.value); setPage(1); }} />
      <Select
        style={{ width: 150 }}
        placeholder="选择分组"
        value={selectedGroup}
        onChange={(value) => { setSelectedGroup(value); setPage(1); }}
      >
        <Select.Option value="__all__">全部分组</Select.Option>
        <Select.Option value="__none__">未分组</Select.Option>
        {groups.map(g => <Select.Option key={g} value={`group:${g}`}>{g}</Select.Option>)}
      </Select>
      <Space wrap>
        <Button type="primary" icon={<PlusOutlined />} disabled={busy} onClick={() => onEdit(null)}>登记服务器</Button>
        <Button aria-label="刷新服务器" icon={<ReloadOutlined />} disabled={busy} onClick={onRefresh}>刷新</Button>
      </Space>
    </div>
    <Typography.Text type="secondary">
      {selectedGroup !== '__all__' && `${selectedGroup === '__none__' ? '未分组' : selectedGroup.slice('group:'.length)} · `}
      {query.trim() ? `找到 ${filtered.length} / ${servers.length} 台` : `共 ${filtered.length} 台`} · 每页 {PAGE_SIZE} 台
    </Typography.Text>
    {compact ? <>
      <div className="ai-ops-server-cards">
        {filtered.slice((current - 1) * PAGE_SIZE, current * PAGE_SIZE).map((server) => <article key={server.id} className="ai-ops-server-card" aria-label={`服务器 #${server.id}`}>
          <header>
            <div className="ai-ops-server-card-title">
              <Typography.Text type="secondary">#{server.id}</Typography.Text>
              <h3 className="ai-ops-server-name">{server.name}</h3>
            </div>
            {!!server.group?.trim() && <Tag icon={<FolderOutlined />} color="blue">{server.group}</Tag>}
          </header>
          <Typography.Text className="ai-ops-server-address" copyable={{ text: aiServerConnection(server) }}>{aiServerConnection(server)}</Typography.Text>
          <div className="ai-ops-server-meta">
            {status(server)}
          </div>
          {actions(server)}
        </article>)}
        {!filtered.length && <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={query.trim() ? '没有匹配的服务器' : '暂无登记的服务器'} />}
      </div>
      {!!filtered.length && <Pagination aria-label="服务器分页" current={current} pageSize={PAGE_SIZE} total={filtered.length} onChange={setPage} showSizeChanger={false} hideOnSinglePage />}
    </> : <Table<AIServer> className="ai-ops-server-table" rowKey="id" size="small" dataSource={filtered} scroll={{ x: 1160 }} columns={[
      { title: '名称', dataIndex: 'name', width: 160, render: (name, server) => <><strong className="ai-ops-server-name">{name}</strong><div>#{server.id}</div></> },
      { title: '分组', dataIndex: 'group', width: 120, render: (group) => group ? <Tag icon={<FolderOutlined />} color="blue">{group}</Tag> : <span>—</span> },
      { title: '连接', width: 260, render: (_, server) => <Typography.Text className="ai-ops-server-address" copyable={{ text: aiServerConnection(server) }}>{aiServerConnection(server)}</Typography.Text> },
      { title: '状态', width: 150, render: (_, server) => status(server) },
      { title: '操作', fixed: 'right', width: 310, render: (_, server) => actions(server) },
    ]} pagination={{ current, pageSize: PAGE_SIZE, total: filtered.length, onChange: setPage, showSizeChanger: false }} locale={{ emptyText: query.trim() ? '没有匹配的服务器' : '暂无登记的服务器' }} />}
    
    <Modal
      title="设置服务器分组"
      open={groupModalVisible}
      onOk={handleSetGroup}
      onCancel={closeGroup}
      confirmLoading={savingGroup}
      okButtonProps={{ disabled: busy || !editingServer }}
      cancelButtonProps={{ disabled: savingGroup }}
      closable={!savingGroup}
      maskClosable={!savingGroup}
      okText="确定"
      cancelText="取消"
    >
      <Form form={form} layout="vertical">
        <Form.Item label="服务器" style={{ marginBottom: 16 }}>
          <Typography.Text strong>{editingServer?.name}</Typography.Text>
          <br />
          <Typography.Text type="secondary">{editingServer?.host}:{editingServer?.port}</Typography.Text>
        </Form.Item>
        <Form.Item name="group" label="分组名称" extra="留空并保存可移出当前分组；不会删除服务器。">
          <Select
            mode="tags"
            maxCount={1}
            allowClear
            disabled={savingGroup}
            placeholder="输入或选择分组"
            style={{ width: '100%' }}
          >
            {groups.map(g => <Select.Option key={g} value={g}>{g}</Select.Option>)}
          </Select>
        </Form.Item>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          提示：可以直接输入新分组名称，或从已有分组中选择
        </Typography.Text>
      </Form>
    </Modal>
  </section>;
}
