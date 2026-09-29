import React, { useState } from 'react';
import { Tabs, Typography } from 'antd';
import { AppstoreOutlined, DatabaseOutlined, ShoppingCartOutlined } from '@ant-design/icons';
import AppleIDProducts from './AppleIDProducts';
import AppleIDInventory from './AppleIDInventory';
import AppleIDOrders from './AppleIDOrders';
import './AppleIDPage.css';

export default function AppleIDPage() {
  const [activeTab, setActiveTab] = useState('products');
  return <div className="legacy-page apple-id-page">
    <div className="content-heading">独享 Apple ID</div>
    <div className="apple-id-content">
      <Typography.Paragraph type="secondary" className="apple-id-intro">
        管理独享账号的商品、库存与售后订单。
      </Typography.Paragraph>
      <Tabs activeKey={activeTab} onChange={setActiveTab} destroyOnHidden items={[
        { key: 'products', label: <span><AppstoreOutlined /> 商品管理</span>, children: <AppleIDProducts /> },
        { key: 'inventory', label: <span><DatabaseOutlined /> 库存管理</span>, children: <AppleIDInventory /> },
        { key: 'orders', label: <span><ShoppingCartOutlined /> 订单管理</span>, children: <AppleIDOrders /> },
      ]} />
    </div>
  </div>;
}
