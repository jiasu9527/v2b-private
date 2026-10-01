import React, { useState } from 'react';
import { Tabs, Typography } from 'antd';
import { AppstoreOutlined, DatabaseOutlined, ShoppingCartOutlined, LineChartOutlined } from '@ant-design/icons';
import AppleIDProducts from './AppleIDProducts';
import AppleIDInventory from './AppleIDInventory';
import AppleIDOrders from './AppleIDOrders';
import AppleIDFinance from './AppleIDFinance';
import './AppleIDPage.css';

export default function AppleIDPage({ initialTab = 'products' }: { initialTab?: string }) {
  const [activeTab, setActiveTab] = useState(initialTab);
  return <div className="legacy-page apple-id-page">
    <div className="content-heading">独享 Apple ID</div>
    <div className="apple-id-content">
      <Typography.Paragraph type="secondary" className="apple-id-intro">
        管理独享账号的商品、库存、售后订单与流水统计。
      </Typography.Paragraph>
      <Tabs activeKey={activeTab} onChange={setActiveTab} destroyOnHidden items={[
        { key: 'products', label: <span><AppstoreOutlined /> 商品管理</span>, children: <AppleIDProducts /> },
        { key: 'inventory', label: <span><DatabaseOutlined /> 库存管理</span>, children: <AppleIDInventory /> },
        { key: 'orders', label: <span><ShoppingCartOutlined /> 订单管理</span>, children: <AppleIDOrders /> },
        { key: 'finance', label: <span><LineChartOutlined /> 流水统计</span>, children: <AppleIDFinance /> },
      ]} />
    </div>
  </div>;
}
