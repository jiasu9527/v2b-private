import React from 'react';
import { Checkbox, Form, Input, Select, Tag, Typography, type FormInstance } from 'antd';
import type { AIConfig, AIModelEndpointForm } from './aiOpsModel';

const { Text, Paragraph } = Typography;

export default function AIOpsModelEndpointSettings({ config, form, models }: { config: AIConfig; form: FormInstance; models: string[] }) {
  const values: Record<string, AIModelEndpointForm> = Form.useWatch('model_endpoint_forms', { form, preserve: true }) || {};
  // Keep removed models visible until the administrator explicitly drops their
  // override; never silently attach a saved key to a replacement model name.
  const removed = Object.keys(values).filter((model) => !models.includes(model) && values[model]?.mode === 'dedicated');
  return <section className="ai-ops-model-endpoints" aria-label="按模型设置中转">
    <Text strong>各模型独立中转</Text>
    <Paragraph type="secondary">不同模型可以使用不同厂商的 Base URL 和 API Key。未单独配置的模型使用上方默认中转；独立模型不会自动回退到默认中转。需要灾备时请在下方明确配置支持该模型的备用中转。</Paragraph>
    <div className="ai-ops-model-endpoint-grid">{[...models, ...removed].map((model) => {
      const saved = config.model_endpoints && Object.prototype.hasOwnProperty.call(config.model_endpoints, model) ? config.model_endpoints[model] : undefined;
      const dedicated = values[model]?.mode === 'dedicated';
      const isRemoved = !models.includes(model);
      return <section key={model} className="ai-ops-model-endpoint" aria-label={`${model} 中转配置`}>
        <div className="ai-ops-model-endpoint-head"><Text strong>{model}</Text>{isRemoved && <Tag color="warning">已从模型列表移除</Tag>}</div>
        <Form.Item name={['model_endpoint_forms', model, 'mode']} label="连接方式" initialValue={saved ? 'dedicated' : 'shared'}><Select aria-label={`${model} 连接方式`} options={[{ value: 'shared', label: isRemoved ? '确认删除该模型独立配置' : '使用默认中转' }, { value: 'dedicated', label: '独立 URL 与 API Key' }]} /></Form.Item>
        {dedicated && <>
          <Form.Item name={['model_endpoint_forms', model, 'base_url']} label="独立 Base URL" rules={[{ required: true, whitespace: true, message: '请填写此模型的中转地址' }]} extra="修改地址后需重新输入 Key，不会沿用其他模型的凭据。"><Input aria-label={`${model} Base URL`} autoComplete="off" placeholder="https://provider.example.com/v1" /></Form.Item>
          <Form.Item name={['model_endpoint_forms', model, 'api_key']} label={`独立 API Key · ${saved?.has_api_key ? '已保存（留空不变）' : '未配置'}`}><Input.Password aria-label={`${model} API Key`} autoComplete="new-password" placeholder={saved?.has_api_key ? '留空保留此模型已有 Key' : '填写此模型的 API Key'} /></Form.Item>
          <Form.Item name={['model_endpoint_forms', model, 'clear_api_key']} valuePropName="checked"><Checkbox>清除此模型已保存的 Key</Checkbox></Form.Item>
        </>}
      </section>;
    })}</div>
  </section>;
}
