import { useState } from 'react';
import { Tabs } from 'antd';
import { ClusterOutlined, DeploymentUnitOutlined, ShareAltOutlined } from '@ant-design/icons';
import ServerManage from './ServerManage';
import ClientEntryUserPolicyPage from './ClientEntryUserPolicyPage';
import ClientEntryPage from './ClientEntryPage';
import './ServerManagePage.css';

type ServerTab = 'nodes' | 'assignment' | 'client-entry';

export default function ServerManagePage({ initialTab = 'nodes' }: { initialTab?: ServerTab }) {
  const [activeTab, setActiveTab] = useState<string>(initialTab);
  return <div className="legacy-page server-management-page">
    <div className="content-heading">节点管理</div>
    <div className="server-management-content">
      <Tabs activeKey={activeTab} onChange={setActiveTab} destroyOnHidden items={[
        { key: 'nodes', label: <span><DeploymentUnitOutlined aria-hidden /> 节点列表</span>, children: <ServerManage embedded /> },
        { key: 'assignment', label: <span><ShareAltOutlined aria-hidden /> 入口分配</span>, children: <ClientEntryUserPolicyPage embedded /> },
        { key: 'client-entry', label: <span><ClusterOutlined aria-hidden /> 客户端入口</span>, children: <ClientEntryPage embedded /> },
      ]} />
    </div>
  </div>;
}
