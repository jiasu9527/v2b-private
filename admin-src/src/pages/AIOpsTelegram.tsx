import React, { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Descriptions, Form, InputNumber, Modal, Select, Space, Spin, Switch, Table, Tag, Typography, message } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { apiGet, apiJsonPost } from '../lib/api';
import { aiTelegramActorID, aiTelegramBindings, aiTelegramConfig, type AITelegramBinding, type AITelegramBindingCode, type AITelegramConfig, type AITelegramStatus } from './aiOpsModel';

const { Text, Paragraph } = Typography;
const timestamp = (value?: number) => value ? new Date(value * 1000).toLocaleString('zh-CN', { hour12: false }) : '—';
const accessLabel = (value?: string) => value === 'full' ? '完全访问' : '逐次确认';

export default function AIOpsTelegram({ active, isAdmin, actorID }: { active: boolean; isAdmin: boolean; actorID: number }) {
  const [status, setStatus] = useState<AITelegramStatus | null>(null);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState('');
  const [error, setError] = useState('');
  const [code, setCode] = useState<(AITelegramBindingCode & { actor_id: number }) | null>(null);
  const [revoking, setRevoking] = useState<AITelegramBinding | null>(null);
  const [revokeError, setRevokeError] = useState('');
  const [now, setNow] = useState(Date.now());
  const [configForm] = Form.useForm<AITelegramConfig>();
  const [bindingForm] = Form.useForm<{ actor_id: number }>();
  const alive = useRef(true), sequence = useRef(0), mutation = useRef(false);

  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; sequence.current += 1; };
  }, []);
  const load = async () => {
    if (!active) return;
    const current = ++sequence.current;
    setLoading(true);
    try {
      const response = await apiGet('/ai-ops/telegram/status', {}, { cache: 'no-store' });
      if (!alive.current || current !== sequence.current) return;
      const data = response?.data as AITelegramStatus;
      if (!data || !Array.isArray(data.bindings) || !Array.isArray(data.models) || data.models.some((item) => typeof item !== 'string') || typeof data.bot_ready !== 'boolean') throw new Error('Telegram 状态数据格式不正确');
      const config = aiTelegramConfig(data.config);
      // The server enforces roles too. Never retain another operator's binding
      // in this staff view if a stale or overbroad response is returned.
      const bindings = aiTelegramBindings(data, actorID, isAdmin);
      setStatus({ ...data, config, bindings, my_binding: bindings.find((item) => item.actor_id === actorID) });
      if (isAdmin) configForm.setFieldsValue(config);
      setError('');
    } catch (cause: any) {
      if (alive.current && current === sequence.current) setError(cause?.message || '读取 Telegram 状态失败');
    } finally { if (alive.current && current === sequence.current) setLoading(false); }
  };
  useEffect(() => {
    setCode(null); setRevoking(null); setRevokeError('');
    if (active) void load(); else { sequence.current += 1; setLoading(false); }
  }, [active, actorID, isAdmin]);
  useEffect(() => {
    if (!code) return;
    setNow(Date.now());
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [code]);

  const begin = (kind: string) => {
    if (!active || mutation.current) return false;
    mutation.current = true; sequence.current += 1; setLoading(false); setBusy(kind);
    return true;
  };
  const finish = () => { mutation.current = false; if (alive.current) setBusy(''); };
  const saveConfig = async () => {
    if (!isAdmin || !status || !begin('config')) return;
    try {
      const values = aiTelegramConfig(await configForm.validateFields());
      if (values.enabled && !status.bot_ready) throw new Error('现有 Telegram Bot 尚未就绪，请先在面板 Telegram 设置中完成配置');
      if (values.enabled && values.default_model && !status.models.includes(values.default_model)) throw new Error('请选择当前已配置的 AI 模型');
      if (values.enabled && !values.default_model) throw new Error('请选择 Telegram 默认模型');
      const response = await apiJsonPost('/ai-ops/telegram/config', values);
      if (!alive.current) return;
      const config = aiTelegramConfig(response?.data);
      setStatus((previous) => previous ? { ...previous, config } : previous);
      configForm.setFieldsValue(config);
      message.success('Telegram AI 配置已保存');
    } catch (cause: any) { if (alive.current && !cause?.errorFields) message.error(cause?.message || '保存 Telegram 配置失败'); }
    finally { finish(); }
  };
  const generateCode = async () => {
    if (!isAdmin || !status || !begin('code')) return;
    setCode(null);
    try {
      const values = await bindingForm.validateFields();
      const targetID = aiTelegramActorID(values.actor_id);
      const response = await apiJsonPost('/ai-ops/telegram/binding-code', { actor_id: targetID });
      if (!alive.current) return;
      const value = response?.data as AITelegramBindingCode;
      if (!value || typeof value.code !== 'string' || !value.code || typeof value.command !== 'string' || !value.command || !Number.isSafeInteger(value.expires_at) || value.expires_at * 1000 <= Date.now()) throw new Error('绑定码响应无效或已过期，请重新生成');
      setCode({ ...value, actor_id: targetID });
    } catch (cause: any) { if (alive.current && !cause?.errorFields) message.error(cause?.message || '生成绑定码失败'); }
    finally { finish(); }
  };
  const updateWebhook = async () => {
    if (!isAdmin || !status?.bot_ready || !begin('webhook')) return;
    try {
      await apiJsonPost('/ai-ops/telegram/webhook', {});
      if (alive.current) message.success('Webhook 已更新；请通过 Bot 私聊验证收发');
    } catch (cause: any) { if (alive.current) message.error(cause?.message || '更新 Webhook 失败'); }
    finally { finish(); }
  };
  const revoke = async () => {
    if (!revoking || !revoking.active || !isAdmin && revoking.actor_id !== actorID || !begin('revoke')) return;
    const targetID = revoking.actor_id;
    setRevokeError('');
    try {
      const response = await apiJsonPost('/ai-ops/telegram/revoke', { actor_id: aiTelegramActorID(targetID) });
      if (!alive.current) return;
      if (response?.data !== true) throw new Error('撤销状态未确认，请刷新后核对');
      setStatus((previous) => previous ? {
        ...previous,
        bindings: previous.bindings.map((item) => item.actor_id === targetID ? { ...item, active: false, session_id: 0 } : item),
        my_binding: previous.my_binding?.actor_id === targetID ? { ...previous.my_binding, active: false, session_id: 0 } : previous.my_binding,
      } : previous);
      if (code?.actor_id === targetID) setCode(null);
      setRevoking(null); message.success('Telegram 绑定已撤销'); await load();
    } catch (cause: any) { if (alive.current) setRevokeError(cause?.message || '撤销失败，请刷新后重试'); }
    finally { finish(); }
  };

  const bindings = aiTelegramBindings(status, actorID, isAdmin);
  const remaining = code ? Math.max(0, Math.ceil((code.expires_at * 1000 - now) / 1000)) : 0;
  const columns = [
    { title: '后台操作人', key: 'actor', render: (_: unknown, binding: AITelegramBinding) => <Space direction="vertical" size={0}><Text>#{binding.actor_id}{binding.actor_id === actorID ? '（我）' : ''}</Text><Text type="secondary">{binding.email || '—'}</Text></Space> },
    { title: 'Telegram ID', dataIndex: 'telegram_id', render: (value: number | string) => <Text copyable={value ? { text: String(value) } : false}>{value || '—'}</Text> },
    { title: '状态', dataIndex: 'active', render: (value: boolean) => <Tag color={value ? 'success' : 'default'}>{value ? '已绑定' : '已撤销'}</Tag> },
    { title: '当前会话', dataIndex: 'session_id', render: (value?: number) => value ? `#${value}` : '—' },
    { title: '绑定时间', dataIndex: 'bound_at', render: (value: number) => timestamp(value) },
    { title: '操作', key: 'actions', render: (_: unknown, binding: AITelegramBinding) => (isAdmin || binding.actor_id === actorID) && <Button danger type="link" disabled={!binding.active || !!busy || loading} onClick={() => { setRevoking(binding); setRevokeError(''); }}>撤销绑定</Button> },
  ];

  return <Space direction="vertical" size="middle" style={{ width: '100%', paddingBottom: 16 }}>
    <Card size="small" title="Telegram AI 运维" extra={<Button icon={<ReloadOutlined />} aria-label="刷新 Telegram 状态" loading={loading} disabled={!!busy} onClick={() => void load()}>刷新</Button>}>
      <Space wrap style={{ marginBottom: 12 }}>
        <Tag color={!status ? 'default' : status.bot_ready ? 'success' : 'warning'}>{!status ? '正在读取 Bot 状态' : status.bot_ready ? '现有 Bot 配置已就绪' : '现有 Bot 配置未就绪'}</Tag>
        {status && <Tag color={status.config.enabled ? 'processing' : 'default'}>{status.config.enabled ? 'Telegram AI 已启用' : 'Telegram AI 未启用'}</Tag>}
      </Space>
      <Paragraph style={{ marginBottom: 0 }}>复用面板现有的 Telegram Bot，无需填写新 Token。仅支持与 Bot 私聊，不接收群聊指令。Telegram 对话会保存到现有会话历史，并标记为 TG。Bot 配置就绪仅代表已有配置可用，不代表 Webhook 已验证。</Paragraph>
    </Card>
    {error && <Alert type="error" showIcon message={error} action={<Button size="small" disabled={!!busy || loading} onClick={() => void load()}>重试</Button>} />}
    {status && !status.bot_ready && <Alert type="warning" showIcon message="现有 Telegram Bot 尚未就绪" description={isAdmin ? '请先在面板已有的 Telegram 设置中完成 Bot 配置，再刷新本页；这里不重复保存 Bot Token。' : '请联系管理员完成面板 Telegram Bot 配置。'} />}
    <Spin spinning={loading && !status}>
      <Space direction="vertical" size="middle" style={{ width: '100%' }}>
        {isAdmin ? <Card size="small" title="Telegram AI 设置">
          <Alert type="info" showIcon message="首次接入请更新 Webhook" description="旧 Webhook 不具备 AI 指令所需的来源验证。确认现有 Bot 已配置后，保存启用设置，再点击“更新 Webhook”添加验证；随后生成绑定码并在 Bot 私聊中验证。" style={{ marginBottom: 16 }} />
          <Form form={configForm} layout="vertical" disabled={!status || !!busy || loading}>
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(210px, 1fr))', gap: '0 20px' }}>
              <Form.Item name="enabled" label="启用 Telegram AI" valuePropName="checked"><Switch /></Form.Item>
              <Form.Item name="default_model" label="新会话默认模型"><Select placeholder="选择已配置的模型" options={(status?.models || []).map((value) => ({ value, label: value }))} /></Form.Item>
              <Form.Item name="default_access_mode" label="新会话默认权限" rules={[{ required: true, message: '请选择默认权限' }]}><Select options={[{ value: 'full', label: '完全访问' }, { value: 'standard', label: '逐次确认' }]} /></Form.Item>
              <Form.Item name="stream_updates" label="流式更新回复" valuePropName="checked"><Switch /></Form.Item>
              <Form.Item name="notify_completion" label="任务完成通知" valuePropName="checked"><Switch /></Form.Item>
            </div>
            <Paragraph type="secondary">默认模型和权限用于新建 Telegram 会话，不会覆盖已有会话。完全访问会按绑定操作人的权限自动执行操作；逐次确认模式下，完整操作卡发送成功后可在 Bot 私聊中点击批准或拒绝，也可在网页处理。修改全局模型请使用 AI 运维“设置”。</Paragraph>
            <Space wrap><Button type="primary" loading={busy === 'config'} onClick={() => void saveConfig()}>保存 Telegram 设置</Button><Button disabled={!status?.bot_ready || !!busy || loading} loading={busy === 'webhook'} onClick={() => void updateWebhook()}>更新 Webhook</Button></Space>
          </Form>
        </Card> : status && <Card size="small" title="当前使用设置"><Descriptions size="small" column={{ xs: 1, sm: 2 }}>
          <Descriptions.Item label="新会话默认模型">{status.config.default_model || '未设置'}</Descriptions.Item>
          <Descriptions.Item label="新会话默认权限">{accessLabel(status.config.default_access_mode)}</Descriptions.Item>
          <Descriptions.Item label="流式更新">{status.config.stream_updates ? '开启' : '关闭'}</Descriptions.Item>
          <Descriptions.Item label="完成通知">{status.config.notify_completion ? '开启' : '关闭'}</Descriptions.Item>
        </Descriptions></Card>}
        {isAdmin && <Card size="small" title="生成一次性绑定码">
          <Paragraph type="secondary">填写管理员或员工的后台操作人 ID，为该账号生成有效期 10 分钟、只能使用一次的绑定码。由对应人员在与 Bot 的私聊中发送完整命令，绑定后将以该后台账号的权限操作。</Paragraph>
          <Form form={bindingForm} layout="inline" disabled={!status || !!busy || loading} initialValues={{ actor_id: actorID > 0 ? actorID : undefined }}>
            <Form.Item name="actor_id" label="后台操作人 ID" rules={[{ required: true, message: '请输入后台操作人 ID' }, { validator: async (_, value) => { aiTelegramActorID(value); } }]}><InputNumber min={1} max={Number.MAX_SAFE_INTEGER} precision={0} placeholder="管理员／员工 ID" style={{ width: 210 }} /></Form.Item>
            <Form.Item><Button type="primary" loading={busy === 'code'} onClick={() => void generateCode()}>生成绑定码</Button></Form.Item>
          </Form>
        </Card>}
        <Card size="small" title={isAdmin ? '后台账号与 Telegram 绑定' : '我的 Telegram 绑定'}>
          {!isAdmin && <Paragraph type="secondary">仅显示本人的绑定。请向管理员领取一次性绑定命令；你可以随时撤销自己的绑定。</Paragraph>}
          <Table<AITelegramBinding> rowKey="actor_id" size="small" dataSource={bindings} columns={columns} loading={loading && !!status} pagination={bindings.length > 10 ? { pageSize: 10, showSizeChanger: false } : false} scroll={{ x: 850 }} locale={{ emptyText: status ? '暂无绑定' : '尚未读取绑定信息' }} />
        </Card>
      </Space>
    </Spin>
    <Card size="small" title="私聊命令">
      <Descriptions size="small" column={{ xs: 1, md: 2 }}>
        <Descriptions.Item label={<Text code>/ai</Text>}>进入 AI 模式，随后直接发送任务</Descriptions.Item>
        <Descriptions.Item label={<Text code>/ainew</Text>}>下一条消息使用新的 Telegram 运维会话</Descriptions.Item>
        <Descriptions.Item label={<Text code>/aisessions</Text>}>查看会话列表</Descriptions.Item>
        <Descriptions.Item label={<Text code>/aimodel</Text>}>查看或切换可用模型</Descriptions.Item>
        <Descriptions.Item label={<Text code>/aidetails</Text>}>查看当前会话状态与操作摘要</Descriptions.Item>
        <Descriptions.Item label={<Text code>/aistop</Text>}>停止当前 AI 任务</Descriptions.Item>
        <Descriptions.Item label={<Text code>/aiexit</Text>}>退出 AI 输入模式，不会自动停止已有任务</Descriptions.Item>
      </Descriptions>
    </Card>
    <Modal title="一次性 Telegram 绑定命令" open={isAdmin && active && !!code} onCancel={() => setCode(null)} footer={<Button type="primary" onClick={() => setCode(null)}>已保存命令</Button>} destroyOnHidden>
      {code && <Space direction="vertical" size="middle" style={{ width: '100%' }}>
        <Text>后台操作人 #{code.actor_id}</Text>
        <Alert showIcon type={remaining > 0 ? 'info' : 'warning'} message={remaining > 0 ? `剩余 ${Math.floor(remaining / 60)} 分 ${remaining % 60} 秒；使用一次后失效` : '绑定码已过期，请关闭并重新生成'} description={`有效至 ${timestamp(code.expires_at)}。只将命令交给这个后台账号的实际使用人，不要发送到群聊。`} />
        {remaining > 0 && <Paragraph code copyable={{ text: code.command }} style={{ overflowWrap: 'anywhere', marginBottom: 0 }}>{code.command}</Paragraph>}
        <Text type="secondary">让对应人员在现有 Bot 的私聊窗口发送上方命令；完成后刷新绑定列表核对。关闭窗口后本页不再显示该命令。</Text>
      </Space>}
    </Modal>
    <Modal title="撤销 Telegram 绑定" open={active && !!revoking && (isAdmin || revoking.actor_id === actorID)} onCancel={() => { if (!busy) { setRevoking(null); setRevokeError(''); } }} onOk={() => void revoke()} okText="确认撤销并停止 TG 任务" okButtonProps={{ danger: true }} confirmLoading={busy === 'revoke'} cancelButtonProps={{ disabled: !!busy }} maskClosable={!busy} closable={!busy} destroyOnHidden>
      <Paragraph>确认撤销操作人 #{revoking?.actor_id}（{revoking?.email || '未提供邮箱'}）的 Telegram 绑定？</Paragraph>
      <Alert type="warning" showIcon message="将停止该绑定正在进行的 Telegram AI 任务" description="撤销后不能再从该 Telegram 账号继续操作，必须重新领取绑定码。已有会话记录保留；已经启动的远程命令不保证停止或回滚。" />
      {revokeError && <Alert type="error" showIcon message={revokeError} style={{ marginTop: 12 }} />}
    </Modal>
  </Space>;
}
