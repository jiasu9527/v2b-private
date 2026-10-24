import React, { useEffect, useState } from 'react';
import { Alert, Button, Empty, Input, Modal, Space, Table, Tag } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { apiGet } from '../lib/api';
import { onlineUserLocation, onlineUserLocationNote, onlineUsersParams, type OnlineUser, type OnlineUsersResult, type OnlineUsersSelection } from '../lib/onlineAnalytics';

function time(value: number) {
  return Number.isFinite(value) && value > 0 ? new Date(value * 1000).toLocaleString('zh-CN', { hour12: false }) : '未收到';
}

export default function OnlineUsersModal({ selection, unknownReasons, onClose }: {
  selection: OnlineUsersSelection;
  unknownReasons: Record<string, string>;
  onClose: () => void;
}) {
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [search, setSearch] = useState('');
  const [query, setQuery] = useState('');
  const [refresh, setRefresh] = useState(0);
  const [result, setResult] = useState<OnlineUsersResult | null>(null);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    let active = true;
    const controller = new AbortController();
    const timeout = window.setTimeout(() => controller.abort(), 30_000);
    setLoading(true);
    setError('');
    setResult(null);
    (async () => {
      try {
        const response = await apiGet('/stat/online/users', onlineUsersParams(selection, page, pageSize, query), { signal: controller.signal, cache: 'no-store', keepEmpty: true });
        const value = response?.data as OnlineUsersResult;
        if (!value || !Array.isArray(value.users) || !Number.isFinite(value.total) || value.total < 0) throw new Error('在线用户列表返回格式不正确');
        if (!active) return;
        setTotal(value.total);
        const lastPage = Math.max(1, Math.ceil(value.total / pageSize));
        if (page > lastPage) {
          setPage(lastPage);
          return;
        }
        setResult(value);
      } catch (error: any) {
        if (active) setError(error?.name === 'AbortError' ? '在线用户列表加载超时，请重试' : error?.message || '在线用户列表加载失败');
      } finally {
        window.clearTimeout(timeout);
        if (active) setLoading(false);
      }
    })();
    return () => { active = false; window.clearTimeout(timeout); controller.abort(); };
  }, [selection, page, pageSize, query, refresh]);

  const columns = [
    { title: '用户账号', key: 'account', width: 250, render: (_: unknown, user: OnlineUser) => <div className="online-user-account"><strong>{user.email || '未设置邮箱'}</strong><span>ID {user.id}</span></div> },
    { title: '最后有效定位', key: 'location', width: 230, render: (_: unknown, user: OnlineUser) => <div className="online-user-location"><span>{onlineUserLocation(user)}</span>{onlineUserLocationNote(user, unknownReasons) && <small>{onlineUserLocationNote(user, unknownReasons)}</small>}</div> },
    { title: '最近在线', dataIndex: 'last_seen_at', key: 'last_seen_at', width: 170, render: (value: number) => time(value) },
    { title: '最后位置记录时间', dataIndex: 'location_seen_at', key: 'location_seen_at', width: 170, render: (value: number) => time(value) },
  ];

  return <Modal title={`${selection.title} · 在线用户`} open onCancel={onClose} width={960} footer={<Button onClick={onClose}>关闭</Button>} className="online-users-modal">
    <div className="online-users-toolbar">
      <Input.Search aria-label="在当前地区精确搜索邮箱或用户 ID" placeholder="精确搜索邮箱或用户 ID" allowClear value={search} onChange={(event) => { setSearch(event.target.value); if (!event.target.value) { setQuery(''); setPage(1); } }} onSearch={(value) => { setQuery(value.trim()); setPage(1); }} />
      <Space><Tag>{total.toLocaleString('zh-CN')} 人</Tag><Button icon={<ReloadOutlined spin={loading} />} disabled={loading} onClick={() => setRefresh((value) => value + 1)}>刷新</Button></Space>
    </div>
    <p className="online-users-note">近 {Math.round((result?.online_window_seconds || 600) / 60)} 分钟有流量活动的用户，每个账号只计一次。地区按最后有效定位归属并持续保留，不保证是当前位置；未知用户的记录时间可能对应未能定位的上报。列表按查询时刻更新，人数可能与地图上一轮统计略有变化。</p>
    {error && <Alert type="error" showIcon message="暂时无法获取在线用户" description={error} action={<Button size="small" onClick={() => setRefresh((value) => value + 1)}>重试</Button>} />}
    <Table<OnlineUser> rowKey="id" columns={columns} dataSource={result?.users || []} loading={loading} size="middle" scroll={{ x: 820 }} locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={error ? '列表未加载，请重试' : query ? '当前地区没有匹配的在线用户' : '该地区当前没有在线用户'} /> }} pagination={{ current: page, pageSize, total, showSizeChanger: true, pageSizeOptions: [20, 50, 100], showTotal: (count) => `共 ${count.toLocaleString('zh-CN')} 人`, onChange: (nextPage, nextSize) => { setPage(nextSize !== pageSize ? 1 : nextPage); setPageSize(nextSize); } }} />
    {result?.generated_at ? <p className="online-users-updated">查询时间：{time(result.generated_at)}</p> : null}
  </Modal>;
}
