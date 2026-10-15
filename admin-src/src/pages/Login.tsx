import React, { useState } from 'react';
import { Button, Card, Form, Input, Typography, message } from 'antd';
import { checkLogin, clearAuth, getSettings, passportLogin, setAdminUserInfo, setAuth, type AnyRecord } from '../lib/api';

export default function Login({ onDone }: { onDone: (user: AnyRecord) => void }) {
  const [loading, setLoading] = useState(false);
  const settings = getSettings();
  const submit = async (values: any) => {
    setLoading(true);
    try {
      const res = await passportLogin(values.email, values.password);
      const data = res.data || res;
      if (!(data.auth_data || data.token)) throw new Error('登录返回缺少有效会话');
      setAuth(data.auth_data || data.token);
      const checked = await checkLogin();
      const identity = checked.data || {};
      if (!identity.is_admin && !identity.is_support) throw new Error('当前账号没有管理员或员工工作台权限');
      const user = { ...identity, email: identity.email || values.email };
      setAdminUserInfo(user);
      message.success('登录成功');
      onDone(user);
    } catch (e:any) { clearAuth(); message.error(e.message || '登录失败'); }
    finally { setLoading(false); }
  };
  return <div className="login-page">
    <Card className="login-card">
      <Typography.Title level={3}>{settings.title || 'Forest'} 管理后台</Typography.Title>
      <Typography.Paragraph type="secondary">请输入管理员或获授权的员工账号登录</Typography.Paragraph>
      <Form layout="vertical" onFinish={submit}>
        <Form.Item name="email" label="邮箱" rules={[{ required: true }]}><Input aria-label="邮箱" autoComplete="username" /></Form.Item>
        <Form.Item name="password" label="密码" rules={[{ required: true }]}><Input.Password aria-label="密码" autoComplete="current-password" /></Form.Item>
        <Button type="primary" htmlType="submit" loading={loading} block>登录</Button>
      </Form>
    </Card>
  </div>;
}
