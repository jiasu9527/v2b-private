import React from 'react';
import { Button, Checkbox, Form, Input, InputNumber, Select, Space, Switch, Typography, type FormInstance } from 'antd';
import { ArrowDownOutlined, ArrowUpOutlined, DeleteOutlined, PlusOutlined } from '@ant-design/icons';
import { AI_DEFAULT_FAILOVER_TIMEOUT, AI_MAX_FALLBACK_RELAYS, type AIConfig, type AIFallbackRelay, type AIFallbackRelayForm } from './aiOpsModel';

const { Text, Paragraph } = Typography;
type RelayField = { name: number; key: number };

function FallbackRelayRow({ field, count, saved, models, busy, move, remove }: {
  field: RelayField;
  count: number;
  saved?: AIFallbackRelay;
  models: string[];
  busy: boolean;
  move: (from: number, to: number) => void;
  remove: (index: number) => void;
}) {
  return <section className="ai-ops-relay" aria-label={`备用中转 ${field.name + 1}`}>
    <div className="ai-ops-relay-head"><Text strong>备用中转 {field.name + 1}</Text><Space size={2} wrap>
      <Button size="small" type="text" icon={<ArrowUpOutlined />} aria-label={`上移备用中转 ${field.name + 1}`} disabled={busy || field.name === 0} onClick={() => move(field.name, field.name - 1)}>上移</Button>
      <Button size="small" type="text" icon={<ArrowDownOutlined />} aria-label={`下移备用中转 ${field.name + 1}`} disabled={busy || field.name === count - 1} onClick={() => move(field.name, field.name + 1)}>下移</Button>
      <Button size="small" type="text" danger icon={<DeleteOutlined />} aria-label={`移除备用中转 ${field.name + 1}`} onClick={() => remove(field.name)}>移除</Button>
    </Space></div>
    <Form.Item name={[field.name, 'id']} hidden><Input /></Form.Item>
    <div className="ai-ops-relay-identity">
      <Form.Item name={[field.name, 'name']} label="名称" rules={[{ required: true, whitespace: true, message: '请输入备用中转名称' }, { max: 100, message: '名称最多 100 字' }]}><Input autoComplete="off" maxLength={100} placeholder="例如：备用线路一" /></Form.Item>
      <Form.Item name={[field.name, 'enabled']} label="启用此中转" valuePropName="checked"><Switch /></Form.Item>
    </div>
    <Form.Item name={[field.name, 'base_url']} label="备用 Base URL" rules={[{ required: true, whitespace: true, message: '请输入备用中转 Base URL' }]} extra="修改地址后需重新填写 API Key，或明确清除旧 Key 并停用该中转。"><Input placeholder="https://backup.example.com/v1" autoComplete="off" /></Form.Item>
    <Form.Item name={[field.name, 'api_key']} label={`备用 API Key · ${saved?.has_api_key ? '已保存（留空保持不变）' : '未配置'}`}><Input.Password autoComplete="new-password" placeholder={saved?.has_api_key ? '留空保留该中转的 API Key' : '输入该中转的 API Key'} /></Form.Item>
    <Form.Item name={[field.name, 'clear_api_key']} valuePropName="checked"><Checkbox>清除该中转已有 API Key</Checkbox></Form.Item>
    <Form.Item name={[field.name, 'models']} label="支持的模型" extra="留空继承上方全部模型；只会为当前模型选择支持它的备用中转，不会更换模型。"><Select mode="multiple" allowClear options={models.map((value) => ({ value, label: value }))} placeholder="全部模型（继承全局设置）" /></Form.Item>
  </section>;
}

export default function AIOpsRelaySettings({ config, form, models, busy }: { config: AIConfig; form: FormInstance; models: string[]; busy: boolean }) {
  const failoverEnabled = Form.useWatch('failover_enabled', { form, preserve: true });
  const relays: AIFallbackRelayForm[] = Form.useWatch('fallback_relays', { form, preserve: true }) || [];
  return <section className="ai-ops-relay-settings" aria-label="多中转灾备设置">
    <Text strong>多中转灾备</Text>
    <Paragraph type="secondary">主中转遇到网络超时、限流或服务异常时，按下方顺序切换可用的备用中转，继续使用同一个模型。切换不会重复执行已经完成的操作。</Paragraph>
    <div className="ai-ops-failover-controls">
      <Form.Item name="failover_enabled" label="自动切换备用中转" valuePropName="checked"><Switch /></Form.Item>
      <Form.Item name="failover_timeout_seconds" label="单站请求超时（秒）" rules={[{ required: true, type: 'number', min: 10, max: 90, message: '请填写 10–90 秒' }]} extra="单个中转未在此时间内完整返回，将按顺序尝试备用中转。"><InputNumber min={10} max={90} precision={0} placeholder={String(AI_DEFAULT_FAILOVER_TIMEOUT)} disabled={busy || !failoverEnabled} /></Form.Item>
    </div>
    <Form.List name="fallback_relays" rules={[{ validator: (_, values) => (values || []).length <= AI_MAX_FALLBACK_RELAYS ? Promise.resolve() : Promise.reject(new Error('最多配置 4 个备用中转')) }]}>
      {(fields, { add, remove, move }, { errors }) => <>
        {fields.map((field) => <FallbackRelayRow key={field.key} field={field} count={fields.length} saved={config.fallback_relays?.find((relay) => relay.id === relays[field.name]?.id)} models={models} busy={busy} move={move} remove={remove} />)}
        <Button className="ai-ops-relay-add" icon={<PlusOutlined />} disabled={busy || fields.length >= AI_MAX_FALLBACK_RELAYS} onClick={() => add({ name: '', base_url: '', models: [], enabled: true, api_key: '', clear_api_key: false })}>添加备用中转（{fields.length}/{AI_MAX_FALLBACK_RELAYS}）</Button>
        <Form.ErrorList errors={errors} />
      </>}
    </Form.List>
  </section>;
}
