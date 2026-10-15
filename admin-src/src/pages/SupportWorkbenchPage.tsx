import React, { useState } from 'react';
import { Tabs } from 'antd';
import { AuditOutlined, SearchOutlined, SafetyCertificateOutlined } from '@ant-design/icons';
import SupportOverview from './SupportOverview';
import SupportUserSearch from './SupportUserSearch';
import SupportOperations from './SupportOperations';
import './SupportPage.css';

export default function SupportWorkbenchPage() {
  const [tab, setTab] = useState('overview');
  return <div className="legacy-page support-workbench"><div className="content-heading">员工运维工作台</div><div className="support-content"><Tabs activeKey={tab} onChange={setTab} destroyOnHidden items={[
    { key: 'overview', label: <span><SafetyCertificateOutlined aria-hidden /> 入口状态</span>, children: <SupportOverview /> },
    { key: 'users', label: <span><SearchOutlined aria-hidden /> 精准查询用户</span>, children: <SupportUserSearch /> },
    { key: 'operations', label: <span><AuditOutlined aria-hidden /> 我的操作记录</span>, children: <SupportOperations /> },
  ]} /></div></div>;
}
