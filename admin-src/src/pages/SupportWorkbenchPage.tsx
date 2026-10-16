import React, { useState } from 'react';
import { Tabs } from 'antd';
import { AppstoreOutlined, AuditOutlined, SearchOutlined, SafetyCertificateOutlined } from '@ant-design/icons';
import SupportOverview from './SupportOverview';
import SupportUserSearch from './SupportUserSearch';
import SupportOperations from './SupportOperations';
import AppleIDPage from './AppleIDPage';
import './SupportPage.css';

export default function SupportWorkbenchPage({ initialTab = 'overview', appleInitialTab = 'products' }: { initialTab?: 'overview' | 'apple-id'; appleInitialTab?: string }) {
  const [tab, setTab] = useState<string>(initialTab);
  return <div className="legacy-page support-workbench"><div className="content-heading">员工运维工作台</div><div className="support-content"><Tabs activeKey={tab} onChange={setTab} destroyOnHidden items={[
    { key: 'overview', label: <span><SafetyCertificateOutlined aria-hidden /> 入口状态</span>, children: <SupportOverview /> },
    { key: 'users', label: <span><SearchOutlined aria-hidden /> 精准查询用户</span>, children: <SupportUserSearch /> },
    { key: 'apple-id', label: <span><AppstoreOutlined aria-hidden /> 独享 Apple ID</span>, children: <AppleIDPage embedded initialTab={appleInitialTab} /> },
    { key: 'operations', label: <span><AuditOutlined aria-hidden /> 我的操作记录</span>, children: <SupportOperations /> },
  ]} /></div></div>;
}
