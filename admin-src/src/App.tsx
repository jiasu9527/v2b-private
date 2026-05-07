import React, { useEffect, useMemo, useState } from 'react';
import { Layout, Menu, Button, ConfigProvider, theme, Typography } from 'antd';
import {
  AppstoreOutlined, DashboardOutlined, UserOutlined, ShoppingCartOutlined, ClusterOutlined,
  SettingOutlined, FileTextOutlined, GiftOutlined, CreditCardOutlined, QuestionCircleOutlined,
  MessageOutlined, MenuFoldOutlined, MenuUnfoldOutlined, LogoutOutlined, DeploymentUnitOutlined,
  ShareAltOutlined, DatabaseOutlined, BellOutlined
} from '@ant-design/icons';
import zhCN from 'antd/locale/zh_CN';
import Login from './pages/Login';
import Dashboard from './pages/Dashboard';
import UserPage from './pages/UserPage';
import OrderPage from './pages/OrderPage';
import ServerManage from './pages/ServerManage';
import TicketPage from './pages/TicketPage';
import ConfigPage from './pages/ConfigPage';
import QueuePage from './pages/QueuePage';
import InviteCampaignPage from './pages/InviteCampaignPage';
import GenericResourcePage from './pages/GenericResourcePage';
import { checkLogin, clearAuth, getAuth, getSettings } from './lib/api';

const { Header, Sider, Content } = Layout;

const menu = [
  { key: '/dashboard', icon: <DashboardOutlined />, label: '仪表盘' },
  { key: '/user', icon: <UserOutlined />, label: '用户管理' },
  { key: '/plan', icon: <AppstoreOutlined />, label: '套餐管理' },
  { key: '/order', icon: <ShoppingCartOutlined />, label: '订单管理' },
  { key: 'server', icon: <ClusterOutlined />, label: '节点系统', children: [
    { key: '/server/manage', icon: <DeploymentUnitOutlined />, label: '节点管理' },
    { key: '/server/group', icon: <DatabaseOutlined />, label: '权限组' },
    { key: '/server/route', icon: <ShareAltOutlined />, label: '路由规则' },
    { key: '/server/client-entry', icon: <ShareAltOutlined />, label: '客户端入口' },
  ]},
  { key: '/config/system', icon: <SettingOutlined />, label: '系统配置' },
  { key: '/config/payment', icon: <CreditCardOutlined />, label: '支付接口' },
  { key: '/ticket', icon: <MessageOutlined />, label: '工单管理' },
  { key: '/notice', icon: <BellOutlined />, label: '公告管理' },
  { key: '/coupon', icon: <GiftOutlined />, label: '优惠券' },
  { key: '/giftcard', icon: <GiftOutlined />, label: '礼品卡' },
  { key: '/knowledge', icon: <QuestionCircleOutlined />, label: '知识库' },
  { key: '/queue', icon: <DatabaseOutlined />, label: '队列监控' },
  { key: '/invite-campaign', icon: <ShareAltOutlined />, label: '邀请任务' },
];

function normalizePath() {
  const settings = getSettings();
  const adminPath = String(settings.secure_path || location.pathname.split('/').filter(Boolean)[0] || '').replace(/^\/+|\/+$/g, '');
  let p = location.pathname;
  if (adminPath && p.startsWith(`/${adminPath}`)) p = p.slice(adminPath.length + 1) || '/dashboard';
  if (p === '/' || p === '') return '/dashboard';
  return p;
}

function Page({ path }: { path: string }) {
  if (path === '/dashboard') return <Dashboard />;
  if (path === '/user') return <UserPage />;
  if (path === '/plan') return <GenericResourcePage name="plans" />;
  if (path === '/order') return <OrderPage />;
  if (path === '/server/manage') return <ServerManage />;
  if (path === '/server/group') return <GenericResourcePage name="serverGroups" />;
  if (path === '/server/route') return <GenericResourcePage name="serverRoutes" />;
  if (path === '/server/client-entry') return <GenericResourcePage name="clientEntry" />;
  if (path === '/config/system') return <ConfigPage />;
  if (path === '/config/payment') return <GenericResourcePage name="payments" />;
  if (path === '/ticket') return <TicketPage />;
  if (path === '/notice') return <GenericResourcePage name="notices" />;
  if (path === '/coupon') return <GenericResourcePage name="coupons" />;
  if (path === '/giftcard') return <GenericResourcePage name="giftcards" />;
  if (path === '/knowledge') return <GenericResourcePage name="knowledge" />;
  if (path === '/queue') return <QueuePage />;
  if (path === '/invite-campaign') return <InviteCampaignPage />;
  return <Dashboard />;
}

export default function App() {
  const [authed, setAuthed] = useState(!!getAuth());
  const [collapsed, setCollapsed] = useState(false);
  const [path, setPath] = useState(normalizePath());
  const settings = getSettings();
  const title = settings.title || 'Forest';
  const logo = settings.logo;
  const adminPath = String(settings.secure_path || location.pathname.split('/').filter(Boolean)[0] || 'localadmin').replace(/^\/+|\/+$/g, '');

  useEffect(() => { if (getAuth()) checkLogin().then((r)=>{ if (!r.data?.is_admin) { clearAuth(); setAuthed(false); } }).catch(()=>setAuthed(false)); }, []);
  useEffect(() => { const f=()=>setPath(normalizePath()); window.addEventListener('popstate',f); return()=>window.removeEventListener('popstate',f); }, []);

  const selected = useMemo(() => [path], [path]);
  const navigate = (key: string) => { if (!key.startsWith('/')) return; history.pushState(null, '', `/${adminPath}${key}`); setPath(key); };
  if (!authed) return <ConfigProvider locale={zhCN}><Login onDone={() => setAuthed(true)} /></ConfigProvider>;

  return <ConfigProvider locale={zhCN} theme={{ algorithm: theme.defaultAlgorithm, token: { colorPrimary: '#343a40', borderRadius: 4 } }}>
    <Layout className="admin-layout">
      <Sider width={238} collapsible collapsed={collapsed} trigger={null} className="admin-sider">
        <div className="brand"><div className="brand-mark">{logo ? <img src={logo} alt="logo" /> : 'F'}</div>{!collapsed && <Typography.Text strong>{title}</Typography.Text>}</div>
        <Menu mode="inline" selectedKeys={selected} defaultOpenKeys={['server']} items={menu as any} onClick={({ key }) => navigate(String(key))} />
      </Sider>
      <Layout>
        <Header className="admin-header"><Button type="text" icon={collapsed ? <MenuUnfoldOutlined/> : <MenuFoldOutlined/>} onClick={()=>setCollapsed(!collapsed)} /><div className="header-spacer"/><Button icon={<LogoutOutlined/>} onClick={()=>{clearAuth();setAuthed(false)}}>退出</Button></Header>
        <Content className="admin-content"><Page path={path} /></Content>
      </Layout>
    </Layout>
  </ConfigProvider>;
}
