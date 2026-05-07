import React, { useEffect, useState } from 'react';
import { Card, Col, Row, Statistic, Table, message } from 'antd';
import { apiGet, bytes, money } from '../lib/api';

export default function Dashboard() {
  const [status, setStatus] = useState<any>({});
  const [override, setOverride] = useState<any>({});
  const [userRank, setUserRank] = useState<any[]>([]);
  const [serverRank, setServerRank] = useState<any[]>([]);
  const [inviteToday, setInviteToday] = useState<any[]>([]);
  const [inviteLast, setInviteLast] = useState<any[]>([]);

  useEffect(() => { (async () => {
    try {
      const [s, o, u, sv, it, il] = await Promise.all([
        apiGet('/system/getSystemStatus'), apiGet('/stat/getOverride'), apiGet('/stat/getUserTodayRank'), apiGet('/stat/getServerTodayRank'), apiGet('/stat/getInviteTodayRank'), apiGet('/stat/getInviteLastRank')
      ]);
      setStatus(s.data || {}); setOverride(o.data || {}); setUserRank(u.data || []); setServerRank(sv.data || []); setInviteToday(it.data || []); setInviteLast(il.data || []);
    } catch (e: any) { message.error(e.message || '加载仪表盘失败'); }
  })(); }, []);

  const cards = [
    ['用户总数', override.user_total ?? status.user_total ?? '-'],
    ['订单金额', money(override.order_amount ?? override.total_order_amount)],
    ['在线设备', status.online ?? status.online_count ?? '-'],
    ['队列状态', status.horizon || status.schedule ? '运行中' : '未确认'],
  ];
  const inviteColumns = [
    { title: '用户', dataIndex: 'email', render: (v: any, r: any) => v || r.user_email || r.invite_user_email || r.user_id || '-' },
    { title: '邀请数', dataIndex: 'invite_count', render: (v: any, r: any) => v ?? r.count ?? r.total ?? '-' },
    { title: '奖励', dataIndex: 'reward_amount', render: (v: any) => v === undefined ? '-' : money(v) },
  ];

  return <div className="page-stack">
    <Row gutter={[16,16]}>{cards.map(([k,v]) => <Col xs={24} sm={12} lg={6} key={String(k)}><Card><Statistic title={k as string} value={String(v ?? '-')} /></Card></Col>)}</Row>
    <Row gutter={[16,16]}>
      <Col xs={24} xl={12}><Card title="今日用户流量排行"><Table size="small" rowKey={(r,i)=>r.id || `u-${i}`} dataSource={userRank} pagination={false} columns={[{title:'用户',dataIndex:'email'},{title:'上行',dataIndex:'u',render:bytes},{title:'下行',dataIndex:'d',render:bytes}]} /></Card></Col>
      <Col xs={24} xl={12}><Card title="今日节点流量排行"><Table size="small" rowKey={(r,i)=>r.id || `s-${i}`} dataSource={serverRank} pagination={false} columns={[{title:'节点',dataIndex:'name'},{title:'类型',dataIndex:'type'},{title:'流量',render:(_:any,r:any)=>bytes((Number(r.u)||0)+(Number(r.d)||0))}]} /></Card></Col>
      <Col xs={24} xl={12}><Card title="今日邀请排行"><Table size="small" rowKey={(r,i)=>r.id || `it-${i}`} dataSource={inviteToday} pagination={false} columns={inviteColumns as any} /></Card></Col>
      <Col xs={24} xl={12}><Card title="昨日邀请排行"><Table size="small" rowKey={(r,i)=>r.id || `il-${i}`} dataSource={inviteLast} pagination={false} columns={inviteColumns as any} /></Card></Col>
    </Row>
  </div>;
}
