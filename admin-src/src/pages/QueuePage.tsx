import React, { useEffect, useRef, useState } from 'react';
import { Badge, Spin, Table, message } from 'antd';
import { CheckCircleOutlined, CloseCircleOutlined } from '@ant-design/icons';
import { apiGet } from '../lib/api';

const queueNames: Record<string, string> = {
  order_handle: '订单队列',
  send_email: '邮件队列',
  send_email_mass: '邮件群发队列',
  send_telegram: 'Telegram消息队列',
  stat: '统计队列',
  stat_refresh: '统计刷新队列',
  traffic_fetch: '流量消费队列',
};

export default function QueuePage() {
  const [stats, setStats] = useState<any>(null);
  const [work, setWork] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const timer = useRef<number>();

  const load = async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const [s, w] = await Promise.all([apiGet('/system/getQueueStats'), apiGet('/system/getQueueWorkload')]);
      setStats(s.data || {});
      setWork(w.data || []);
    } catch (e: any) {
      if (!silent) message.error(e.message || '加载失败');
    } finally {
      if (!silent) setLoading(false);
    }
  };

  useEffect(() => {
    load();
    timer.current = window.setInterval(() => load(true), 3000);
    return () => window.clearInterval(timer.current);
  }, []);

  const workload = (work || []).filter((item) => (item.queue || item.name) !== 'default');
  const columns: any[] = [
    { title: '队列名称', dataIndex: 'queue', render: (_: any, row: any) => row.name || queueNames[row.queue] || row.queue },
    { title: '作业量', dataIndex: 'processes' },
    { title: '任务量', dataIndex: 'length' },
    { title: '占用时间', dataIndex: 'wait', align: 'right', render: (value: any) => `${value || 0}s` },
  ];

  return <div className="legacy-page queue-page">
    <div className="content-heading">队列监控</div>
    <Spin spinning={loading || !stats}>
      <div className="block block-rounded">
        <div className="block-header block-header-default"><h3 className="block-title">总览</h3></div>
        <div className="block-content p-0">
          <div className="row no-gutters">
            <div className="col-lg-6 col-xl-3 border-right p-4 border-bottom"><div><div>当前作业量</div><div className="mt-4 font-size-h3">{stats?.jobsPerMinute || 0}</div></div></div>
            <div className="col-lg-6 col-xl-3 border-right p-4 border-bottom"><div><div>近一小时处理量</div><div className="mt-4 font-size-h3">{stats?.recentJobs || 0}</div></div></div>
            <div className="col-lg-6 col-xl-3 border-right p-4 border-bottom"><div><div>7日内报错数量</div><div className="mt-4 font-size-h3">{stats?.failedJobs || 0}</div></div></div>
            <div className="col-lg-6 col-xl-3 p-4 border-bottom overflow-hidden position-relative"><div><div>状态</div><div className="mt-4 font-size-h3">{stats ? (stats.status ? '运行中' : '未启动') : '-'}</div>{stats && (stats.status ? <CheckCircleOutlined className="text-success queue-bg-icon" /> : <CloseCircleOutlined className="text-danger queue-bg-icon" />)}</div></div>
          </div>
        </div>
      </div>
      <div className="block block-rounded">
        <div className="block-header block-header-default"><h3 className="block-title">当前作业详情</h3><div className="block-options"><Badge status={stats?.status ? 'success' : 'error'} text={stats?.status ? '自动刷新中' : '未运行'} /></div></div>
        <div className="block-content p-0"><Table className="forest-table" rowKey={(row) => row.queue || row.name} columns={columns} dataSource={workload} pagination={false} /></div>
      </div>
    </Spin>
  </div>;
}
