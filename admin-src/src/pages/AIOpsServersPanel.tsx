import React, { useState } from 'react';
import { Button, Empty, Input, Pagination, Space, Table, Tag, Typography } from 'antd';
import { PlusOutlined, ReloadOutlined, SearchOutlined } from '@ant-design/icons';
import type { AIServer } from './aiOpsModel';
import { aiServerConnection } from './aiOpsServerIdentity';

type Props = {
  servers: AIServer[]; compact: boolean; busy: boolean; unavailable: boolean;
  onEdit: (server: AIServer | null) => void; onCheck: (server: AIServer) => void;
  onDeploy: (server: AIServer) => void; onDelete: (server: AIServer) => void;
  onRefresh: () => void;
};

export function filterAIServers(servers: AIServer[], query: string): AIServer[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return servers;
  return servers.filter((server) => [server.name, server.host, server.username, String(server.id)].some((value) => value.toLowerCase().includes(needle)));
}

export default function AIOpsServersPanel({ servers, compact, busy, unavailable, onEdit, onCheck, onDeploy, onDelete, onRefresh }: Props) {
  const [query, setQuery] = useState('');
  const [page, setPage] = useState(1);
  const filtered = filterAIServers(servers, query);
  // Refresh/deletion on the last page must not leave an apparently empty list.
  const current = Math.min(page, Math.max(1, Math.ceil(filtered.length / 10)));
  const status = (server: AIServer) => <Space size={4} wrap>
    <Tag color={server.enabled ? 'success' : 'default'}>{server.enabled ? '启用' : '停用'}</Tag>
    {server.has_password && <Tag>密码已保存</Tag>}{server.has_private_key && <Tag>私钥已保存</Tag>}
  </Space>;
  const actions = (server: AIServer) => <div className="ai-ops-server-actions">
    <Button type={compact ? 'default' : 'link'} disabled={busy} onClick={() => onEdit(server)}>编辑</Button>
    <Button type={compact ? 'default' : 'link'} disabled={busy || !server.enabled || unavailable} onClick={() => onCheck(server)}>在聊天中检查</Button>
    <Button type={compact ? 'default' : 'link'} disabled={busy || !server.enabled || unavailable} onClick={() => onDeploy(server)}>部署／配置</Button>
    <Button type={compact ? 'default' : 'link'} danger disabled={busy} onClick={() => onDelete(server)}>删除</Button>
  </div>;
  return <section className="ai-ops-servers" aria-label="已登记服务器">
    <div className="ai-ops-server-toolbar">
      <Input aria-label="搜索服务器" placeholder="搜索名称、地址、用户名或 ID" prefix={<SearchOutlined />} allowClear value={query} onChange={(event) => { setQuery(event.target.value); setPage(1); }} />
      <Space wrap><Button type="primary" icon={<PlusOutlined />} disabled={busy} onClick={() => onEdit(null)}>登记服务器</Button><Button aria-label="刷新服务器" icon={<ReloadOutlined />} disabled={busy} onClick={onRefresh}>刷新</Button></Space>
    </div>
    <Typography.Text type="secondary">{query.trim() ? `找到 ${filtered.length} / ${servers.length} 台服务器` : `共 ${servers.length} 台服务器`}</Typography.Text>
    {compact ? <>
      <div className="ai-ops-server-cards">
        {filtered.slice((current - 1) * 10, current * 10).map((server) => <article key={server.id} className="ai-ops-server-card" aria-label={`服务器 #${server.id}`}>
          <header><Typography.Text type="secondary">#{server.id}</Typography.Text><h3 className="ai-ops-server-name">{server.name}</h3></header>
          <Typography.Text className="ai-ops-server-address" copyable={{ text: aiServerConnection(server) }}>{aiServerConnection(server)}</Typography.Text>
          {status(server)}
          <details className="ai-ops-server-fingerprint"><summary>SSH 主机指纹</summary><Typography.Text copyable={server.host_key_sha256 ? { text: server.host_key_sha256 } : false}>{server.host_key_sha256 || '尚未登记'}</Typography.Text></details>
          {actions(server)}
        </article>)}
        {!filtered.length && <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={query.trim() ? '没有匹配的服务器，请调整搜索条件' : '暂无登记的服务器'} />}
      </div>
      {!!filtered.length && <Pagination aria-label="服务器分页" current={current} pageSize={10} total={filtered.length} onChange={setPage} simple showSizeChanger={false} hideOnSinglePage />}
    </> : <Table<AIServer> className="ai-ops-server-table" rowKey="id" size="small" dataSource={filtered} scroll={{ x: 1160 }} columns={[
      { title: '名称', dataIndex: 'name', width: 160, render: (name, server) => <><strong className="ai-ops-server-name">{name}</strong><div>#{server.id}</div></> },
      { title: '连接', width: 260, render: (_, server) => <Typography.Text className="ai-ops-server-address" copyable={{ text: aiServerConnection(server) }}>{aiServerConnection(server)}</Typography.Text> },
      { title: '主机指纹', dataIndex: 'host_key_sha256', width: 300, render: (value) => <span className="ai-ops-wrap">{value || '—'}</span> },
      { title: '凭据 / 状态', width: 170, render: (_, server) => status(server) },
      { title: '操作', fixed: 'right', width: 270, render: (_, server) => actions(server) },
    ]} pagination={{ current, pageSize: 10, total: filtered.length, onChange: setPage, showSizeChanger: false }} locale={{ emptyText: query.trim() ? '没有匹配的服务器' : '暂无登记的服务器' }} />}
  </section>;
}
