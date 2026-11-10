import React, { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Col, Descriptions, Form, Input, InputNumber, Popconfirm, Row, Select, Space, Switch, Table, Tabs, Tag, Typography, message } from 'antd';
import { CopyOutlined, KeyOutlined, ReloadOutlined } from '@ant-design/icons';
import { apiGet, apiJsonPost } from '../lib/api';
import './NewAPIIntegrationPage.css';

type IntegrationConfig = {
  enabled: boolean;
  api_url: string;
  site_url: string;
  client_id: string;
  merchant_id: string;
  admin_user_id: number;
  min_amount_fen: number;
  max_amount_fen: number;
  daily_limit_fen: number;
  revision: number;
  has_client_secret: boolean;
  has_merchant_key: boolean;
  has_admin_token: boolean;
  authorize_url?: string;
  token_url?: string;
  userinfo_url?: string;
  epay_url?: string;
  callback_url?: string;
};
type ConfigForm = {
  enabled: boolean;
  api_url: string;
  site_url: string;
  client_id: string;
  client_secret?: string;
  merchant_id: string;
  merchant_key?: string;
  admin_token?: string;
  admin_user_id?: number;
  min_amount_yuan: string;
  max_amount_yuan: string;
  daily_limit_yuan: string;
};
type Transfer = {
  id: number;
  remote_trade_no: string;
  user_id: number;
  remote_user_id: number;
  amount_fen: number;
  status: string;
  attempts: number;
  last_error?: string;
  created_at: number;
  completed_at?: number;
};
type Binding = { user_id: number; email: string; remote_user_id: number; created_at: number };
type ResultPage<T> = { items: T[]; total: number; current: number; page_size: number };
type TransferFilter = { user_id: string; trade_no: string; status?: string };

const { Text, Paragraph } = Typography;
const emptyFilter: TransferFilter = { user_id: '', trade_no: '' };
const newAPIStatusLabels: Record<string, { label: string; color?: string }> = {
  pending: { label: '处理中 / 待核对', color: 'processing' },
  succeeded: { label: '已到账', color: 'success' },
};

// All money crossing the API boundary is integral CNY fen, not a floating-point
// yuan calculation and never a quota calculated by the browser.
export function newAPIAmountToFen(value: unknown): number {
  const raw = typeof value === 'string' || typeof value === 'number' ? String(value).trim() : '';
  if (!/^\d+(?:\.\d{1,2})?$/.test(raw)) throw new Error('金额必须是最多两位小数的正数');
  const [whole, fraction = ''] = raw.split('.');
  const fen = BigInt(whole) * 100n + BigInt(fraction.padEnd(2, '0'));
  if (fen < 1n || fen > BigInt(Number.MAX_SAFE_INTEGER)) throw new Error('金额超出允许范围');
  return Number(fen);
}

export function newAPIFenToYuan(value: unknown): string {
  if (!Number.isSafeInteger(value) || Number(value) < 0) throw new Error('服务端返回的金额无效');
  const fen = BigInt(Number(value));
  return `${fen / 100n}.${String(fen % 100n).padStart(2, '0')}`;
}

export function newAPITransferStatus(status: string): { label: string; color?: string } {
  return newAPIStatusLabels[status] || { label: `未知状态（${status || '未返回'}）`, color: 'default' };
}

export function newAPIRetryAllowed(status: string): boolean {
  return status === 'pending';
}

function httpsOrigin(value: string, label: string): string {
  const raw = String(value || '').trim();
  if (!raw) return '';
  try {
    const url = new URL(raw);
    if (url.protocol !== 'https:' || url.username || url.password || url.search || url.hash || url.pathname !== '/') throw new Error();
    return url.origin;
  } catch { throw new Error(`${label}需填写 HTTPS 站点地址，不包含路径、查询参数或密码`); }
}

export function newAPIConfigFormValues(config: IntegrationConfig): ConfigForm {
  return {
    enabled: !!config.enabled,
    api_url: config.api_url || 'https://apizhuan.com',
    site_url: config.site_url || '',
    client_id: config.client_id || 'forest',
    merchant_id: config.merchant_id || 'forest-wallet',
    admin_user_id: config.admin_user_id || undefined,
    min_amount_yuan: newAPIFenToYuan(config.min_amount_fen ?? 100),
    max_amount_yuan: newAPIFenToYuan(config.max_amount_fen ?? 100000),
    daily_limit_yuan: newAPIFenToYuan(config.daily_limit_fen ?? 500000),
    // Secrets never hydrate from a GET response, even if a future API regresses.
    client_secret: '', merchant_key: '', admin_token: '',
  };
}

export function newAPIConfigResult(value: unknown, previousRevision?: number): IntegrationConfig {
  const config = value as IntegrationConfig;
  if (!config || !Number.isSafeInteger(config.revision) || config.revision < 0 ||
      (previousRevision !== undefined && config.revision !== previousRevision + 1) ||
      !Number.isSafeInteger(config.admin_user_id) || config.admin_user_id < 0 ||
      ['enabled', 'has_client_secret', 'has_merchant_key', 'has_admin_token'].some((key) => typeof config[key] !== 'boolean') ||
      ['api_url', 'site_url', 'client_id', 'merchant_id'].some((key) => typeof config[key] !== 'string') ||
      ['min_amount_fen', 'max_amount_fen', 'daily_limit_fen'].some((key) => !Number.isSafeInteger(config[key]) || config[key] < 1) ||
      config.min_amount_fen > config.max_amount_fen || config.max_amount_fen > config.daily_limit_fen) {
    throw new Error('服务端未返回有效配置及版本，请重新读取配置后核对');
  }
  return config;
}

export function newAPIConfigPayload(values: ConfigForm, config: IntegrationConfig): Record<string, unknown> {
  if (!Number.isSafeInteger(config.revision) || config.revision < 0) throw new Error('配置版本无效，请重新读取后再保存');
  const minimum = newAPIAmountToFen(values.min_amount_yuan);
  const maximum = newAPIAmountToFen(values.max_amount_yuan);
  const daily = newAPIAmountToFen(values.daily_limit_yuan);
  if (minimum > maximum || maximum > daily) throw new Error('限额需满足：单笔最低 ≤ 单笔最高 ≤ 每人滚动 24 小时限额');
  const apiURL = httpsOrigin(values.api_url, 'New API 地址');
  const siteURL = httpsOrigin(values.site_url, '本站地址');
  const adminID = Number(values.admin_user_id || 0);
  if (!Number.isSafeInteger(adminID) || adminID < 0) throw new Error('New API 管理员 ID 必须是非负整数');
  const payload: Record<string, unknown> = {
    enabled: !!values.enabled, api_url: apiURL, site_url: siteURL,
    client_id: String(values.client_id || '').trim(), merchant_id: String(values.merchant_id || '').trim(),
    admin_user_id: adminID, min_amount_fen: minimum, max_amount_fen: maximum,
    daily_limit_fen: daily, expected_revision: config.revision,
  };
  for (const key of ['client_secret', 'merchant_key', 'admin_token'] as const) {
    const secret = String(values[key] || '').trim();
    if (secret) payload[key] = secret; // Blank means preserve, never overwrite with empty.
    if (values.enabled && !secret && !config[`has_${key}`]) throw new Error('启用前请补齐登录密钥、支付密钥和管理员访问令牌');
  }
  if (values.enabled && (!apiURL || !siteURL || !payload.client_id || !payload.merchant_id || adminID <= 0)) {
    throw new Error('启用前请填写两端地址、Client ID、商户 ID 和 New API 管理员 ID');
  }
  return payload;
}

export function newAPITransferQuery(filter: TransferFilter, current: number, pageSize: number): Record<string, unknown> {
  const query: Record<string, unknown> = { current, page_size: pageSize };
  const userID = filter.user_id.trim();
  if (userID) {
    if (!/^\d+$/.test(userID) || !Number.isSafeInteger(Number(userID)) || Number(userID) <= 0) throw new Error('用户 ID 必须是正整数');
    query.user_id = Number(userID);
  }
  if (filter.status) query.status = filter.status;
  if (filter.trade_no.trim()) query.trade_no = filter.trade_no.trim();
  return query;
}

function resultPage<T>(value: unknown): ResultPage<T> {
  const page = value as ResultPage<T>;
  if (!page || !Array.isArray(page.items) || !Number.isSafeInteger(page.total) || page.total < 0 || !Number.isSafeInteger(page.current) || page.current < 1 || !Number.isSafeInteger(page.page_size) || page.page_size < 1) {
    throw new Error('服务端分页数据无效，请刷新重试');
  }
  return page;
}

const formatTime = (value?: number) => value && Number.isFinite(value) ? new Date(value * 1000).toLocaleString('zh-CN', { hour12: false }) : '—';
function amountText(value: number) { try { return `¥${newAPIFenToYuan(value)}`; } catch { return '金额无效'; } }

export default function NewAPIIntegrationPage() {
  const [form] = Form.useForm<ConfigForm>();
  const [config, setConfig] = useState<IntegrationConfig>();
  const [tab, setTab] = useState('config');
  const [loadingConfig, setLoadingConfig] = useState(false);
  const [saving, setSaving] = useState(false);
  const [checking, setChecking] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [configError, setConfigError] = useState('');
  const [checkResult, setCheckResult] = useState<{ ok: boolean; message: string }>();
  const [transfers, setTransfers] = useState<ResultPage<Transfer>>({ items: [], current: 1, page_size: 20, total: 0 });
  const [bindings, setBindings] = useState<ResultPage<Binding>>({ items: [], current: 1, page_size: 20, total: 0 });
  const [filter, setFilter] = useState<TransferFilter>({ ...emptyFilter });
  const [appliedFilter, setAppliedFilter] = useState<TransferFilter>({ ...emptyFilter });
  const [loadingTransfers, setLoadingTransfers] = useState(false);
  const [loadingBindings, setLoadingBindings] = useState(false);
  const [transferError, setTransferError] = useState('');
  const [bindingError, setBindingError] = useState('');
  const [retrying, setRetrying] = useState<number>();
  const mounted = useRef(true);
  const sequences = useRef({ config: 0, transfers: 0, bindings: 0 });
  const busyWrite = useRef(false);

  const loadConfig = async () => {
    const sequence = ++sequences.current.config;
    setLoadingConfig(true); setConfigError('');
    try {
      const response = await apiGet('/new-api/config', {}, { cache: 'no-store' });
      const next = newAPIConfigResult(response?.data);
      const values = newAPIConfigFormValues(next);
      if (mounted.current && sequence === sequences.current.config) {
        setConfig(next); form.setFieldsValue(values); setDirty(false); setCheckResult(undefined);
      }
    } catch (error: any) { if (mounted.current && sequence === sequences.current.config) setConfigError(error?.message || '读取接入配置失败'); }
    finally { if (mounted.current && sequence === sequences.current.config) setLoadingConfig(false); }
  };

  const loadTransfers = async (query = appliedFilter, current = transfers.current, pageSize = transfers.page_size) => {
    let params: Record<string, unknown>;
    try { params = newAPITransferQuery(query, current, pageSize); }
    catch (error: any) { setTransferError(error.message); return; }
    const sequence = ++sequences.current.transfers;
    setLoadingTransfers(true); setTransferError('');
    try {
      const response = await apiGet('/new-api/transfers', params, { cache: 'no-store' });
      const page = resultPage<Transfer>(response?.data);
      if (mounted.current && sequence === sequences.current.transfers) { setTransfers(page); setAppliedFilter({ ...query }); }
    } catch (error: any) { if (mounted.current && sequence === sequences.current.transfers) setTransferError(error?.message || '读取划转流水失败'); }
    finally { if (mounted.current && sequence === sequences.current.transfers) setLoadingTransfers(false); }
  };

  const loadBindings = async (current = bindings.current, pageSize = bindings.page_size) => {
    const sequence = ++sequences.current.bindings;
    setLoadingBindings(true); setBindingError('');
    try {
      const response = await apiGet('/new-api/bindings', { current, page_size: pageSize }, { cache: 'no-store' });
      const page = resultPage<Binding>(response?.data);
      if (mounted.current && sequence === sequences.current.bindings) setBindings(page);
    } catch (error: any) { if (mounted.current && sequence === sequences.current.bindings) setBindingError(error?.message || '读取用户绑定失败'); }
    finally { if (mounted.current && sequence === sequences.current.bindings) setLoadingBindings(false); }
  };

  useEffect(() => {
    mounted.current = true;
    void loadConfig();
    return () => { mounted.current = false; };
  }, []);

  const save = async () => {
    if (busyWrite.current || loadingConfig || !config) return;
    busyWrite.current = true; setSaving(true); setConfigError(''); setCheckResult(undefined);
    try {
      const values = await form.validateFields();
      const payload = newAPIConfigPayload(values, config);
      const response = await apiJsonPost('/new-api/config', payload);
      const next = newAPIConfigResult(response?.data, config.revision);
      const nextValues = newAPIConfigFormValues(next);
      if (!mounted.current) return;
      // The successful write already returns the committed revision. A second
      // GET can fail after saving and leave the next write using a stale CAS.
      ++sequences.current.config;
      setConfig(next); form.setFieldsValue(nextValues); setDirty(false);
      message.success('接入配置已保存');
    } catch (error: any) { if (mounted.current && !error?.errorFields) setConfigError(error?.message || '保存失败，请重新读取配置后核对'); }
    finally { busyWrite.current = false; if (mounted.current) setSaving(false); }
  };

  const check = async () => {
    if (checking || saving || loadingConfig || dirty) return;
    setChecking(true); setCheckResult(undefined);
    try {
      const response = await apiJsonPost('/new-api/check');
      const data = response?.data;
      if (!data || typeof data.ok !== 'boolean') throw new Error('连通性检查未返回明确结果');
      if (mounted.current) setCheckResult({ ok: data.ok, message: String(data.message || (data.ok ? '连接正常' : '连接检查未通过')) });
    } catch (error: any) { if (mounted.current) setCheckResult({ ok: false, message: error?.message || '连通性检查失败' }); }
    finally { if (mounted.current) setChecking(false); }
  };

  const retry = async (row: Transfer) => {
    if (busyWrite.current || !newAPIRetryAllowed(row.status)) return;
    busyWrite.current = true; setRetrying(row.id);
    try {
      await apiJsonPost('/new-api/retry', { id: row.id });
      if (!mounted.current) return;
      message.success('已请求按原交易核对并补通知；不代表已经到账');
      await loadTransfers();
    } catch (error: any) { if (mounted.current) setTransferError(error?.message || '请求核对失败，请刷新查看原交易'); }
    finally { busyWrite.current = false; if (mounted.current) setRetrying(undefined); }
  };

  const copySecret = async (key: 'client_secret' | 'merchant_key') => {
    const value = String(form.getFieldValue(key) || '');
    if (!value) { message.info('已保存的密钥不回显；如需更换，请先生成或填写新密钥'); return; }
    try { await navigator.clipboard.writeText(value); message.success('已复制当前输入的密钥'); }
    catch { message.error('无法自动复制，请展开输入框后手动复制'); }
  };

  const generateSecret = (key: 'client_secret' | 'merchant_key') => {
    try {
      const bytes = new Uint8Array(32);
      crypto.getRandomValues(bytes);
      form.setFieldValue(key, Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join(''));
      setDirty(true); setCheckResult(undefined);
      message.info('新密钥尚未保存，请先复制到 New API 对应设置');
    } catch { message.error('浏览器不支持安全随机数，请使用 HTTPS 或手动填写随机密钥'); }
  };

  const secretInput = (key: 'client_secret' | 'merchant_key', label: string) => <Form.Item label={label} htmlFor={key} extra={config?.[`has_${key}`] ? '已保存；留空保持不变。更换时需同时更新 New API，保存后不回显。' : '尚未设置。可生成随机密钥，先复制到 New API 后再保存。'}>
    <Space.Compact style={{ width: '100%' }}>
      <Form.Item name={key} noStyle><Input.Password autoComplete="new-password" placeholder={config?.[`has_${key}`] ? '已保存，留空不修改' : '填写或生成密钥'} /></Form.Item>
      <Button aria-label={`生成${label}`} icon={<KeyOutlined />} onClick={() => generateSecret(key)}>生成</Button>
      <Button aria-label={`复制${label}`} icon={<CopyOutlined />} onClick={() => copySecret(key)} />
    </Space.Compact>
  </Form.Item>;

  const urlRows: Array<[keyof IntegrationConfig, string]> = [
    ['authorize_url', '授权地址'], ['token_url', 'Token 地址'], ['userinfo_url', 'UserInfo 地址'],
    ['epay_url', '本站余额支付网关'], ['callback_url', 'New API OIDC 回调地址'],
  ];
  const configuration = <Space direction="vertical" size={16} style={{ width: '100%' }}>
    {configError && <Alert type="error" showIcon message={configError} />}
    <Card title="接入配置" loading={loadingConfig && !config} extra={<Popconfirm title="重新读取会丢弃当前未保存的配置，是否继续？" disabled={!dirty} onConfirm={() => void loadConfig()}><Button icon={<ReloadOutlined />} disabled={saving || checking || loadingConfig} loading={loadingConfig} onClick={() => { if (!dirty) void loadConfig(); }}>重新读取</Button></Popconfirm>}>
      <Form form={form} layout="vertical" disabled={!config || saving || loadingConfig || checking} autoComplete="off" onValuesChange={() => { setDirty(true); setCheckResult(undefined); }}>
        <Form.Item name="enabled" label="启用接入" valuePropName="checked" extra="默认关闭。先完成两端配置并检查连接，再开启用户登录与余额支付。关闭后不再受理新扣款；此前已扣款的待到账订单仍会按原单自动核对并补通知，不自动退款。"><Switch checkedChildren="开启" unCheckedChildren="关闭" /></Form.Item>
        <Row gutter={20}>
          <Col xs={24} lg={12}><Form.Item name="api_url" label="New API 站点地址" rules={[{ required: true, whitespace: true, message: '请输入 New API 地址' }]}><Input placeholder="https://apizhuan.com" /></Form.Item></Col>
          <Col xs={24} lg={12}><Form.Item name="site_url" label="V2B 本站地址" extra="固定用于登录授权和余额付款回跳，不能填写 New API 地址。"><Input placeholder="https://你的V2B域名" /></Form.Item></Col>
          <Col xs={24} lg={12}><Form.Item name="client_id" label="登录 Client ID" rules={[{ required: true, whitespace: true }]}><Input placeholder="forest" /></Form.Item></Col>
          <Col xs={24} lg={12}>{secretInput('client_secret', '登录 Client Secret')}</Col>
          <Col xs={24} lg={12}><Form.Item name="merchant_id" label="余额支付商户 ID" rules={[{ required: true, whitespace: true }]}><Input placeholder="forest-wallet" /></Form.Item></Col>
          <Col xs={24} lg={12}>{secretInput('merchant_key', '余额支付商户密钥')}</Col>
          <Col xs={24} lg={8}><Form.Item name="admin_user_id" label="New API 管理员用户 ID" extra="用于后台核对充值原单及账号归属，不是 V2B 用户 ID。"><InputNumber min={1} precision={0} style={{ width: '100%' }} /></Form.Item></Col>
          <Col xs={24} lg={16}><Form.Item name="admin_token" label="New API 管理员访问令牌（PAT）" extra={config?.has_admin_token ? '已保存；留空不修改。仅服务端使用，读取配置不返回原值。' : '填写 New API 后台创建的专用访问令牌；需读取用户及充值单的权限。'}><Input.Password autoComplete="new-password" placeholder={config?.has_admin_token ? '已保存，留空不修改' : '专用于此接入的访问令牌'} /></Form.Item></Col>
        </Row>
        <Row gutter={20}>
          <Col xs={24} md={8}><Form.Item name="min_amount_yuan" label="单笔最低划转（元）" rules={[{ required: true }]}><InputNumber<string> stringMode min="0.01" precision={2} style={{ width: '100%' }} /></Form.Item></Col>
          <Col xs={24} md={8}><Form.Item name="max_amount_yuan" label="单笔最高划转（元）" rules={[{ required: true }]}><InputNumber<string> stringMode min="0.01" precision={2} style={{ width: '100%' }} /></Form.Item></Col>
          <Col xs={24} md={8}><Form.Item name="daily_limit_yuan" label="每人滚动 24 小时限额（元）" extra="统计当前时刻往前 24 小时的已扣款总额（包含待到账），不在每天零点重置。" rules={[{ required: true }]}><InputNumber<string> stringMode min="0.01" precision={2} style={{ width: '100%' }} /></Form.Item></Col>
        </Row>
        <Paragraph type="secondary">AI 额度兑换价格及折扣在 New API 配置。本模块按充值原单的人民币金额扣本站普通余额，不把人民币金额当作 quota 或 Token 数。</Paragraph>
        <Space wrap>
          <Button type="primary" loading={saving} disabled={!config || loadingConfig || checking} onClick={save}>保存配置</Button>
          <Button loading={checking} disabled={!config || dirty || loadingConfig || saving} onClick={check}>检查已保存配置</Button>
          {dirty && <Text type="warning">配置尚未保存</Text>}
          {config && <Text type="secondary">版本 {config.revision} · 已保存状态：{config.enabled ? '开启' : '关闭'}</Text>}
        </Space>
      </Form>
      {checkResult && <Alert style={{ marginTop: 16 }} showIcon type={checkResult.ok ? 'success' : 'error'} message={checkResult.message} description="此操作仅检查连接和读取权限，不扣用户余额、不充值。" />}
    </Card>
    <Card title="两端对接说明">
      <ol style={{ margin: '0 0 16px', paddingLeft: 22, lineHeight: 1.9 }}>
        <li>填写 New API 地址与 V2B 本站 HTTPS 地址，生成登录密钥及余额支付密钥，先保存为关闭状态。</li>
        <li>在 New API 启用<strong>内置 OIDC</strong>登录，不要使用“通用自定义 OAuth”：使用下方授权、Token、UserInfo 地址及上方 Client ID / Secret。账号按本站用户 ID 关联，不同步密码。</li>
        <li>将 New API 的 <Text code>ServerAddress</Text> 固定为上方已保存的 New API 站点地址（当前为 <Text code>{config?.api_url || 'https://apizhuan.com'}</Text>），用户也需从同一域名打开 AI 站。不要填 V2B 本站域名或使用其他别名，否则登录回调地址会不一致。</li>
        <li>正式启用时设置 <Text code>RegisterEnabled=true</Text>、<Text code>PasswordRegisterEnabled=false</Text>，并关闭不需要的其他 OAuth 登录。总注册开关必须开启才能让本站账号首次通过 OIDC 自动建号；关闭密码注册不会影响该流程。</li>
        <li>在 New API 配置易支付兼容通道：使用本站余额网关和商户 ID / 密钥。这是 V2B 余额支付，不是重新向第三方付款。</li>
        <li>配置专用管理员访问令牌并检查连接；完成联调后再启用。用户从 New API 创建充值原单，回本站确认从自己的 V2B 余额划转。</li>
      </ol>
      <Descriptions size="small" bordered column={1}>
        {urlRows.map(([key, label]) => <Descriptions.Item key={key} label={label}>{config?.[key] ? <Text copyable style={{ wordBreak: 'break-all' }}>{String(config[key])}</Text> : <Text type="secondary">保存完整站点地址后生成</Text>}</Descriptions.Item>)}
      </Descriptions>
      <Paragraph type="secondary" style={{ margin: '12px 0 0' }}>以上地址来自已保存配置；编辑表单不会立即改变生效地址。已有交易按原单核对；待到账期间可同步轮换同一 AI 站、同一商户的密钥，但不能切换 AI 站地址或商户，需先处理未完成划转。</Paragraph>
    </Card>
  </Space>;

  const transferTab = <Space direction="vertical" size={16} style={{ width: '100%' }}>
    <Alert type="info" showIcon message="V2B 普通余额 → AI 额度，单向划转" description="不重复返佣，不混套餐订单。网络超时不等于未到账；待核对记录只能按原单查询和补通知，不能盲退款，也不能换单号再次加额度。" />
    <Card size="small">
      <Space wrap style={{ width: '100%' }}>
        <Input aria-label="V2B 用户 ID 筛选" placeholder="V2B 用户 ID" inputMode="numeric" style={{ width: 170 }} value={filter.user_id} onChange={(e) => setFilter((old) => ({ ...old, user_id: e.target.value }))} onPressEnter={() => void loadTransfers(filter, 1)} allowClear />
        <Input aria-label="交易号筛选" placeholder="交易号" style={{ width: 240 }} value={filter.trade_no} onChange={(e) => setFilter((old) => ({ ...old, trade_no: e.target.value }))} onPressEnter={() => void loadTransfers(filter, 1)} allowClear />
        <Select aria-label="划转状态筛选" placeholder="全部状态" allowClear style={{ width: 150 }} value={filter.status} options={Object.entries(newAPIStatusLabels).map(([value, item]) => ({ value, label: item.label }))} onChange={(status) => setFilter((old) => ({ ...old, status }))} />
        <Button type="primary" loading={loadingTransfers} onClick={() => void loadTransfers(filter, 1)}>查询</Button>
        <Button disabled={loadingTransfers} onClick={() => { setFilter({ ...emptyFilter }); void loadTransfers({ ...emptyFilter }, 1); }}>重置</Button>
        <Button icon={<ReloadOutlined />} disabled={loadingTransfers} onClick={() => void loadTransfers()}>刷新当前结果</Button>
      </Space>
    </Card>
    {transferError && <Alert type="error" showIcon message={transferError} description="当前列表可能仍是上次查询结果，请勿据此判断新查询或操作已成功。" />}
    {JSON.stringify(filter) !== JSON.stringify(appliedFilter) && <Text type="secondary">筛选条件已修改，点击“查询”后生效；翻页使用上次成功查询的条件。</Text>}
    <Table<Transfer> size="small" rowKey="id" loading={loadingTransfers} dataSource={transfers.items} scroll={{ x: 1260 }} columns={[
      { title: 'New API 交易号', key: 'trade_no', width: 250, render: (_, item) => <Space direction="vertical" size={2}><Text copyable style={{ wordBreak: 'break-all' }}>{item.remote_trade_no}</Text><Text type="secondary">本站划转记录 #{item.id}</Text></Space> },
      { title: '用户', key: 'user', width: 140, render: (_, item) => <><div>V2B #{item.user_id}</div><Text type="secondary">New API #{item.remote_user_id}</Text></> },
      { title: '扣款金额', dataIndex: 'amount_fen', width: 125, render: amountText },
      { title: '状态', dataIndex: 'status', width: 135, render: (value: string, item) => { const status = value === 'pending' ? { label: item.last_error ? '待核对' : '处理中', color: item.last_error ? 'warning' : 'processing' } : newAPITransferStatus(value); return <Tag color={status.color}>{status.label}</Tag>; } },
      { title: '通知次数', dataIndex: 'attempts', width: 90 },
      { title: '创建时间', dataIndex: 'created_at', width: 195, render: formatTime },
      { title: '到账时间', dataIndex: 'completed_at', width: 195, render: formatTime },
      { title: '操作', key: 'action', width: 130, render: (_, item) => newAPIRetryAllowed(item.status) ? <Popconfirm title="按原交易核对并补通知？" description="不会再次扣款或创建新充值单；需等待后续核对结果。" onConfirm={() => retry(item)}><Button size="small" loading={retrying === item.id} disabled={retrying !== undefined || saving} >核对 / 补通知</Button></Popconfirm> : '—' },
    ]} expandable={{ rowExpandable: (item) => !!item.last_error, expandedRowRender: (item) => <Alert type="warning" showIcon message="最近处理信息" description={<span style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{item.last_error}</span>} /> }} pagination={{ current: transfers.current, pageSize: transfers.page_size, total: transfers.total, showSizeChanger: true, pageSizeOptions: [20, 50, 100], showTotal: (total) => `共 ${total} 笔`, onChange: (page, size) => void loadTransfers(appliedFilter, page, size) }} />
  </Space>;

  const bindingTab = <Space direction="vertical" size={16} style={{ width: '100%' }}>
    <Alert type="info" showIcon message="付款账号按两端用户 ID 核验" description="仅列出发起 AI 充值后已验证的付款账号关联，不是全部 SSO 登录账号。不会复制全站用户或同步密码；此处不提供解绑或向其他账号划转的操作。" />
    <Button style={{ alignSelf: 'flex-start' }} icon={<ReloadOutlined />} loading={loadingBindings} onClick={() => void loadBindings()}>刷新关联</Button>
    {bindingError && <Alert type="error" showIcon message={bindingError} />}
    <Table<Binding> size="small" rowKey={(item) => `${item.user_id}:${item.remote_user_id}`} loading={loadingBindings} dataSource={bindings.items} scroll={{ x: 700 }} columns={[
      { title: 'V2B 用户 ID', dataIndex: 'user_id', width: 140 },
      { title: '邮箱', dataIndex: 'email', render: (value: string) => <span style={{ overflowWrap: 'anywhere' }}>{value || '—'}</span> },
      { title: 'New API 用户 ID', dataIndex: 'remote_user_id', width: 170 },
      { title: '关联时间', dataIndex: 'created_at', width: 195, render: formatTime },
    ]} pagination={{ current: bindings.current, pageSize: bindings.page_size, total: bindings.total, showSizeChanger: true, pageSizeOptions: [20, 50, 100], showTotal: (total) => `共 ${total} 个关联`, onChange: (page, size) => void loadBindings(page, size) }} />
  </Space>;

  return <div className="legacy-page new-api-integration-page">
    <div className="content-heading">New API 接入</div>
    <Space direction="vertical" size={16} style={{ width: '100%', minWidth: 0, boxSizing: 'border-box', padding: '0 clamp(8px, 2vw, 24px)' }}>
      <Alert type="info" showIcon message="独立 AI 服务，统一账号与余额支付" description="V2B 余额划转到 New API，不影响网站套餐、节点或原有支付通道。接入配置和资金异常核对仅限管理员；划转流水不随普通日志清空。" />
      <Tabs activeKey={tab} onChange={(value) => { setTab(value); if (value === 'transfers') void loadTransfers(); if (value === 'bindings') void loadBindings(); }} items={[
        { key: 'config', label: '接入配置', children: configuration },
        { key: 'transfers', label: '划转流水', children: transferTab },
        { key: 'bindings', label: '付款账号关联', children: bindingTab },
      ]} style={{ width: '100%', minWidth: 0 }} />
    </Space>
  </div>;
}
