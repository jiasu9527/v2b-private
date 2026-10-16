import React, { useEffect, useMemo, useState } from 'react';
import { Layout, Button, ConfigProvider, Dropdown, Space, Spin, theme, Typography } from 'antd';
import {
  AppstoreOutlined, DashboardOutlined, UserOutlined, ShoppingCartOutlined,
  SettingOutlined, FileTextOutlined, GiftOutlined, CreditCardOutlined, QuestionCircleOutlined,
  MessageOutlined, MenuFoldOutlined, MenuUnfoldOutlined, LogoutOutlined, DeploymentUnitOutlined,
  ShareAltOutlined, DatabaseOutlined, BellOutlined, DownOutlined, SafetyCertificateOutlined, GlobalOutlined
} from '@ant-design/icons';
import zhCN from 'antd/locale/zh_CN';
import Login from './pages/Login';
import SupportWorkbenchPage from './pages/SupportWorkbenchPage';
import Dashboard from './pages/Dashboard';
import UserPage from './pages/UserPage';
import OrderPage from './pages/OrderPage';
import AppleIDPage from './pages/AppleIDPage';
import ServerManagePage from './pages/ServerManagePage';
import TicketPage, { TicketDetailPage } from './pages/TicketPage';
import ConfigPage from './pages/ConfigPage';
import QueuePage from './pages/QueuePage';
import InviteCampaignPage from './pages/InviteCampaignPage';
import GenericResourcePage from './pages/GenericResourcePage';
import PlanPage from './pages/PlanPage';
import PaymentPage from './pages/PaymentPage';
import CouponPage from './pages/CouponPage';
import GiftcardPage from './pages/GiftcardPage';
import NoticePage from './pages/NoticePage';
import KnowledgePage from './pages/KnowledgePage';
import ServerRoutePage from './pages/ServerRoutePage';
import SubscribeGuardPage from './pages/SubscribeGuardPage';
import DNSPodPage from './pages/DNSPodPage';
import DNSFailoverPage from './pages/DNSFailoverPage';
import { checkLogin, clearAuth, getAuth, getCurrentUserInfo, getSettings, pathAdminSegment, setAdminUserInfo } from './lib/api';

const { Header, Sider, Content } = Layout;
const MOBILE_QUERY = '(max-width: 768px)';

function getIsMobileViewport() {
  if (typeof window === 'undefined' || !window.matchMedia) return false;
  return window.matchMedia(MOBILE_QUERY).matches;
}

const menu = [
  { type: 'item', key: '/dashboard', icon: <DashboardOutlined />, label: '仪表盘' },
  { type: 'heading', key: 'setting-heading', label: '设置' },
  { type: 'item', key: '/config/system', icon: <SettingOutlined />, label: '系统配置' },
  { type: 'item', key: '/config/payment', icon: <CreditCardOutlined />, label: '支付配置' },
  { type: 'item', key: '/config/subscribe-guard', icon: <SafetyCertificateOutlined />, label: '订阅防护' },
  { type: 'heading', key: 'server-heading', label: '服务器' },
  { type: 'item', key: '/server/manage', icon: <DeploymentUnitOutlined />, label: '节点管理' },
  { type: 'item', key: '/server/group', icon: <DatabaseOutlined />, label: '权限组管理' },
  { type: 'item', key: '/server/route', icon: <ShareAltOutlined />, label: '路由管理' },
  { type: 'item', key: '/dns', icon: <GlobalOutlined />, label: '域名解析' },
  { type: 'item', key: '/dns-failover', icon: <GlobalOutlined />, label: '备用监控' },
  { type: 'heading', key: 'finance-heading', label: '财务' },
  { type: 'item', key: '/plan', icon: <AppstoreOutlined />, label: '订阅管理' },
  { type: 'item', key: '/order', icon: <ShoppingCartOutlined />, label: '订单管理' },
  { type: 'item', key: '/apple-id', icon: <AppstoreOutlined />, label: '独享 Apple ID' },
  { type: 'item', key: '/coupon', icon: <GiftOutlined />, label: '优惠券管理' },
  { type: 'item', key: '/giftcard', icon: <GiftOutlined />, label: '礼品卡管理' },
  { type: 'item', key: '/invite-campaign', icon: <ShareAltOutlined />, label: '活动任务' },
  { type: 'heading', key: 'user-heading', label: '用户' },
  { type: 'item', key: '/user', icon: <UserOutlined />, label: '用户管理' },
  { type: 'item', key: '/notice', icon: <BellOutlined />, label: '公告管理' },
  { type: 'item', key: '/ticket', icon: <MessageOutlined />, label: '工单管理' },
  { type: 'item', key: '/knowledge', icon: <QuestionCircleOutlined />, label: '知识库管理' },
  { type: 'heading', key: 'metric-heading', label: '指标' },
  { type: 'item', key: '/queue', icon: <DatabaseOutlined />, label: '队列监控' },
];

function normalizePath() {
  const normalizeWithSearch = (value: string) => value || '/dashboard';
  if (location.hash.startsWith('#/')) return normalizeWithSearch(location.hash.slice(1));
  const settings = getSettings();
  const adminPath = String(settings.secure_path || pathAdminSegment()).replace(/^\/+|\/+$/g, '');
  let p = location.pathname;
  if (adminPath && p.startsWith(`/${adminPath}`)) p = p.slice(adminPath.length + 1) || '/dashboard';
  if (p.startsWith('/assets/admin-new')) p = p.slice('/assets/admin-new'.length) || '/dashboard';
  if (p === '/' || p === '') return '/dashboard';
  return normalizeWithSearch(`${p}${location.search}`);
}

function Page({ path }: { path: string }) {
  const route = path.split('?')[0];
  if (route === '/dashboard') return <Dashboard />;
  if (route === '/user') return <UserPage />;
  if (route === '/support/staff' || route === '/support/operations') return <UserPage initialTab="staff-operations" />;
  if (route === '/plan') return <PlanPage />;
  if (route === '/order') return <OrderPage />;
  if (route === '/apple-id') return <AppleIDPage />;
  if (route === '/apple-id/finance') return <AppleIDPage initialTab="finance" />;
  if (route === '/server/manage') return <ServerManagePage />;
  if (route === '/server/group') return <GenericResourcePage name="serverGroups" />;
  if (route === '/server/route') return <ServerRoutePage />;
  if (route === '/server/client-entry') return <ServerManagePage initialTab="client-entry" />;
  if (route === '/server/client-entry-user-policy') return <ServerManagePage initialTab="assignment" />;
  if (route === '/dns') return <DNSPodPage />;
  if (route === '/dns-failover') return <DNSFailoverPage />;
  if (route === '/config/system') return <ConfigPage />;
  if (route === '/config/payment') return <PaymentPage />;
  if (route === '/config/subscribe-guard') return <SubscribeGuardPage />;
  const ticketMatch = route.match(/^\/ticket\/(\d+)/);
  if (ticketMatch) return <TicketDetailPage ticketId={ticketMatch[1]} />;
  if (route === '/ticket') return <TicketPage />;
  if (route === '/notice') return <NoticePage />;
  if (route === '/coupon') return <CouponPage />;
  if (route === '/giftcard') return <GiftcardPage />;
  if (route === '/knowledge') return <KnowledgePage />;
  if (route === '/queue') return <QueuePage />;
  if (route === '/invite-campaign') return <InviteCampaignPage />;
  return <Dashboard />;
}

export default function App() {
  const [authed, setAuthed] = useState(false);
  const [authChecking, setAuthChecking] = useState(!!getAuth());
  const [isMobile, setIsMobile] = useState(getIsMobileViewport);
  const [collapsed, setCollapsed] = useState(() => getIsMobileViewport());
  const [adminUser, setAdminUser] = useState<Record<string, any>>({});
  const [path, setPath] = useState(normalizePath());
  const settings = getSettings();
  const title = settings.title || 'Forest';
  const logo = settings.logo;
  const adminPath = String(settings.secure_path || pathAdminSegment() || 'localadmin').replace(/^\/+|\/+$/g, '');

  useEffect(() => {
    if (!window.matchMedia) return;
    const media = window.matchMedia(MOBILE_QUERY);
    const syncViewport = () => {
      const mobile = media.matches;
      setIsMobile(mobile);
      if (mobile) setCollapsed(true);
      else setCollapsed(false);
    };
    syncViewport();
    if (media.addEventListener) media.addEventListener('change', syncViewport);
    else media.addListener(syncViewport);
    return () => {
      if (media.removeEventListener) media.removeEventListener('change', syncViewport);
      else media.removeListener(syncViewport);
    };
  }, []);

  useEffect(() => {
    let mounted = true;
    let checking = false;
    const rejectIdentity = () => {
      clearAuth();
      if (mounted) { setAdminUser({}); setAuthed(false); setAuthChecking(false); }
    };
    const verify = async () => {
      const token = getAuth();
      if (!token || checking) return;
      checking = true;
      try {
        const response = await checkLogin();
        if (!mounted || token !== getAuth()) return;
        const identity = response.data || {};
        if (!identity.is_admin && !identity.is_staff) { rejectIdentity(); return; }
        setAdminUserInfo(identity); setAdminUser(identity); setAuthed(true);
        // Staff identity uses only the dedicated checkLogin DTO, never administrator/user-detail endpoints.
        if (identity.is_admin) {
          try {
            const info = await getCurrentUserInfo();
            if (mounted && token === getAuth()) {
              const user = { ...(info.data || {}), ...identity };
              setAdminUserInfo(user); setAdminUser(user);
            }
          } catch { /* checkLogin remains the authority; optional profile data is not needed. */ }
        }
      } catch { if (mounted && token === getAuth()) rejectIdentity(); }
      finally { checking = false; if (mounted) setAuthChecking(false); }
    };
    const onVisible = () => { if (document.visibilityState === 'visible') verify(); };
    window.addEventListener('forest-auth-invalid', rejectIdentity);
    document.addEventListener('visibilitychange', onVisible);
    verify();
    return () => { mounted = false; window.removeEventListener('forest-auth-invalid', rejectIdentity); document.removeEventListener('visibilitychange', onVisible); };
  }, []);
  useEffect(() => { const f=()=>setPath(normalizePath()); window.addEventListener('popstate',f); window.addEventListener('hashchange',f); return()=>{ window.removeEventListener('popstate',f); window.removeEventListener('hashchange',f); }; }, []);

  const selected = useMemo(() => {
    const route = path.split('?')[0];
    if (route === '/support/staff' || route === '/support/operations') return ['/user'];
    if (route === '/server/client-entry' || route === '/server/client-entry-user-policy') return ['/server/manage'];
    return [route.startsWith('/ticket/') ? '/ticket' : route === '/apple-id/finance' ? '/apple-id' : route];
  }, [path]);
  const navigate = (key: string) => { if (!key.startsWith('/')) return; history.pushState(null, '', `/${adminPath}${key}`); setPath(key); if (isMobile) setCollapsed(true); };
  const supportOnly = authed && !adminUser.is_admin && !!adminUser.is_staff;
  const staffAppleRoute = path.split('?')[0];
  const staffApplePage = staffAppleRoute === '/apple-id' || staffAppleRoute === '/apple-id/finance';
  const visibleMenu = supportOnly ? [{ type: 'item', key: '/support/workbench', icon: <SafetyCertificateOutlined />, label: '员工工作台' }] : menu;
  const navItems = visibleMenu.map((item) => item.type === 'heading'
    ? <li key={item.key} className="nav-main-heading">{item.label}</li>
    : <li key={item.key} className="nav-main-item"><button className={`nav-main-link ${(supportOnly || selected.includes(item.key)) ? 'active' : ''}`} onClick={() => navigate(item.key)}><span className="nav-main-link-icon">{item.icon}</span>{!collapsed && <span className="nav-main-link-name">{item.label}</span>}</button></li>);
  if (authChecking) return <ConfigProvider locale={zhCN}><div className="login-page"><Spin size="large" tip="正在验证后台权限"><div style={{ width: 240, height: 80 }} /></Spin></div></ConfigProvider>;
  if (!authed) return <ConfigProvider locale={zhCN}><Login onDone={(user) => { setAdminUser(user); setAuthed(true); setAuthChecking(false); if (!user.is_admin) { history.replaceState(null, '', `/${adminPath}/support/workbench`); setPath('/support/workbench'); } }} /></ConfigProvider>;

  const isTicketDetailPath = !supportOnly && /^\/ticket\/\d+/.test(path);
  const adminDisplayName = String(adminUser.email || adminUser.account || adminUser.name || 'Admin');
  const logout = () => {
    clearAuth();
    setAdminUser({});
    setAuthed(false);
  };

  return <ConfigProvider locale={zhCN} theme={{ algorithm: theme.defaultAlgorithm, token: { colorPrimary: '#111111', colorInfo: '#111111', borderRadius: 4, colorLink: '#111111' } }}>
    {isTicketDetailPath ? <Page key={path} path={path} /> :
    <Layout className={`admin-layout${isMobile ? ' admin-layout-mobile' : ''}`}>
      <Sider width={238} collapsedWidth={0} collapsible collapsed={collapsed} trigger={null} className="admin-sider">
        <div className="brand"><div className="brand-mark">{logo ? <img src={logo} alt="logo" /> : 'F'}</div>{!collapsed && <Typography.Text strong>{title}</Typography.Text>}</div>
        <nav className="legacy-nav"><ul className="nav-main">{navItems}</ul></nav>
      </Sider>
      {isMobile && !collapsed && <div className="admin-sider-mask" onClick={() => setCollapsed(true)} />}
      <Layout>
        <Header className="admin-header"><Button type="text" aria-label={collapsed ? '展开菜单' : '收起菜单'} icon={collapsed ? <MenuUnfoldOutlined/> : <MenuFoldOutlined/>} onClick={()=>setCollapsed((value)=>!value)} /><div className="header-spacer"/><Dropdown trigger={['click']} menu={{ items: [{ key: 'logout', icon: <LogoutOutlined />, label: '退出登录', onClick: logout }] }}><Button type="text" className="admin-account-button"><Space size={6}><UserOutlined /><span className="admin-account-name" title={adminDisplayName}>{adminDisplayName}</span><DownOutlined /></Space></Button></Dropdown></Header>
        <Content className="admin-content">{supportOnly ? <SupportWorkbenchPage key={path} initialTab={staffApplePage ? 'apple-id' : 'overview'} appleInitialTab={staffAppleRoute === '/apple-id/finance' ? 'finance' : 'products'} /> : <Page key={path} path={path} />}</Content>
      </Layout>
    </Layout>}
  </ConfigProvider>;
}
