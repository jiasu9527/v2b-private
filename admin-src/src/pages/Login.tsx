import React, { useState } from 'react';
import { Button, Card, Form, Input, Typography, message } from 'antd';
import { getSettings, passportLogin, setAuth } from '../lib/api';

export default function Login({ onDone }: { onDone: () => void }) {
  const [loading, setLoading] = useState(false);
  const settings = getSettings();
  const submit = async (values: any) => {
    setLoading(true);
    try {
      const res = await passportLogin(values.email, values.password);
      const data = res.data || res;
      if (!data.is_admin) throw new Error('当前账号不是管理员');
      setAuth(data.auth_data || data.token);
      message.success('登录成功');
      onDone();
    } catch (e:any) { message.error(e.message || '登录失败'); }
    finally { setLoading(false); }
  };
  return <div className="login-page">
    <Card className="login-card">
      <Typography.Title level={3}>{settings.title || 'Forest'} 管理后台</Typography.Title>
      <Typography.Paragraph type="secondary">请输入管理员账号登录</Typography.Paragraph>
      <Form layout="vertical" onFinish={submit}>
        <Form.Item name="email" label="邮箱" rules={[{ required: true }]}><Input autoComplete="username" /></Form.Item>
        <Form.Item name="password" label="密码" rules={[{ required: true }]}><Input.Password autoComplete="current-password" /></Form.Item>
        <Button type="primary" htmlType="submit" loading={loading} block>登录</Button>
      </Form>
    </Card>
  </div>;
}
