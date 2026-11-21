import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Form, Input, Select, Space, Spin, Tag, Typography } from 'antd';
import { GlobalOutlined, LockOutlined } from '@ant-design/icons';
import { aiOpsRequestHeaders, aiOpsRequestURL } from '../lib/api';
import './AIOpsWebLogin.css';

type LoginFields = {
  url: string;
  target_url?: string;
  username: string;
  password: string;
  task: string;
  model?: string;
};
type LoginResult = {
  origin: string;
  page_title: string;
  summary: string;
  model: string;
};
type Availability = 'loading' | 'enabled' | 'disabled' | 'error';

const base = '/ai-ops/web-login';
const { Text, Title, Paragraph } = Typography;
const urlError = '请输入公网 HTTPS 域名（443 端口），不要带账号信息、查询参数或 # 片段';
const privateSuffixes = new Set(['arpa', 'corp', 'example', 'home', 'internal', 'invalid', 'lan', 'local', 'localdomain', 'localhost', 'onion', 'test']);
const byteLength = (value: string) => new TextEncoder().encode(value).length;
const credentialHasControl = (value: string) => /[\x00-\x1f\x7f-\x9f]/.test(value);

export function aiOpsLoginURL(input: string): URL | null {
  const value = input.trim();
  const authority = /^https:\/\/[^/?#@]+/i.exec(value);
  if (!authority || (authority[0].length < value.length && value[authority[0].length] !== '/') ||
    /[?#\s\p{Cc}\p{Cf}\\]/u.test(value) || value.length > 2048) return null;
  const path = value.slice(authority[0].length);
  if (/(?:^|\/)\.{1,2}(?:\/|$)/.test(path) || /%(?![a-f0-9]{2})/i.test(path)) return null;
  // Match the server's URL boundary before browser canonicalization can
  // silently reinterpret an escaped separator or dot segment.
  for (const hex of path.matchAll(/%([a-f0-9]{2})/gi)) {
    const octet = Number.parseInt(hex[1], 16);
    if (octet < 0x80 && !/[a-zA-Z0-9 _~-]/.test(String.fromCharCode(octet))) return null;
  }
  try {
    const url = new URL(value);
    if (url.protocol !== 'https:' || url.username || url.password || url.port || url.search || url.hash || url.href.length > 2048) return null;
    const labels = url.hostname.split('.');
    const suffix = labels[labels.length - 1];
    if (url.hostname.length > 253 || labels.length < 2 || !/^[a-z][a-z0-9-]+$/.test(suffix) ||
      privateSuffixes.has(suffix) || labels.some((label) => !/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(label))) return null;
    return url;
  } catch {
    return null;
  }
}

async function webLoginRequest(path: '/status' | '/run', signal: AbortSignal, fields?: LoginFields): Promise<Response> {
  return fetch(aiOpsRequestURL(base + path), {
    method: fields ? 'POST' : 'GET',
    headers: {
      ...aiOpsRequestHeaders(),
      Accept: 'application/json',
      ...(fields ? { 'Content-Type': 'application/json' } : {}),
    },
    body: fields ? JSON.stringify(fields) : undefined,
    credentials: 'omit',
    redirect: 'error',
    cache: 'no-store',
    referrerPolicy: 'no-referrer',
    signal,
  });
}

// Upstream text is untrusted data; present only a bounded whitelist of error
// codes rather than echoing response.body, URLs or the submitted credentials.
const runErrors: ReadonlyMap<string, string> = new Map([
  ['invalid_target', '网站地址不符合要求，或登录跳转离开了该网站。凭据不会提交到其他域名。'],
  ['unsupported_login', '没有识别到可安全提交的账号密码表单。可以改填登录页的完整 HTTPS 地址后再试。'],
  ['login_failed', '未能确认登录成功，请核对账号密码和网站的登录方式。系统没有自动重复提交。'],
  ['verification_required', '网站要求验证码、扫码或第三方验证，本次已停止。'],
  ['no_content', '未读取到可分析的网页正文，请核对登录后页面地址和账号权限。'],
  ['model_unavailable', '网页任务未获得模型回复，请检查 AI 运维的模型配置。'],
]);

async function runFailure(response: Response) {
  const payload = await response.json().catch(() => null);
  if (typeof payload?.code === 'string' && runErrors.has(payload.code)) return runErrors.get(payload.code)!;
  switch (response.status) {
    case 400: return '提交资料或网址不符合要求，请核对后再试。';
    case 401:
    case 403: return '当前后台会话无权运行，或已过期，请重新登录后台。';
    case 404:
    case 503: return '网页登录组件暂不可用，请核对服务器配置。';
    case 422: return '未完成登录或读取。请核对网址、账号和验证方式；未自动重试。';
    case 409:
    case 429: return '已有任务在运行，请等任务结束后再提交。';
    case 408:
    case 504: return '执行超时，未自动重试。网站可能已收到登录请求，请确认后再操作。';
    default: return '执行未完成，未自动重试。请核对网站状态后再试。';
  }
}

function responseResult(payload: unknown, expectedOrigin: string): LoginResult | null {
  const result = (payload as { data?: Partial<LoginResult> } | null)?.data;
  if (!result || result.origin !== expectedOrigin || typeof result.page_title !== 'string' ||
    typeof result.model !== 'string' || typeof result.summary !== 'string' || !result.summary.trim() || result.summary.length > 24_000) return null;
  return { origin: result.origin, page_title: result.page_title.slice(0, 220), model: result.model.slice(0, 120), summary: result.summary };
}

export default function AIOpsWebLogin({ models }: { models: string[] }) {
  const [form] = Form.useForm<LoginFields>();
  const [availability, setAvailability] = useState<Availability>('loading');
  const [refresh, setRefresh] = useState(0);
  const [running, setRunning] = useState(false);
  const [feedback, setFeedback] = useState('');
  const [result, setResult] = useState<LoginResult | null>(null);
  const currentRun = useRef<AbortController | null>(null);
  const secure = typeof window !== 'undefined' && window.location.protocol === 'https:';

  const stopWaiting = useCallback(() => {
    currentRun.current?.abort();
    currentRun.current = null;
  }, []);

  useEffect(() => {
    if (!secure) return;
    const controller = new AbortController();
    setAvailability('loading');
    void (async () => {
      try {
        const response = await webLoginRequest('/status', controller.signal);
        const payload = response.ok ? await response.json() : null;
        if (!controller.signal.aborted) setAvailability(
          typeof payload?.data?.enabled !== 'boolean' ? 'error' : payload.data.enabled ? 'enabled' : 'disabled',
        );
      } catch {
        if (!controller.signal.aborted) setAvailability('error');
      }
    })();
    return () => controller.abort();
  }, [secure, refresh]);

  useEffect(() => {
    const handlePageHide = () => {
      stopWaiting();
      form.resetFields();
      setRunning(false);
      setResult(null);
      setFeedback('');
    };
    window.addEventListener('pagehide', handlePageHide);
    return () => {
      window.removeEventListener('pagehide', handlePageHide);
      stopWaiting();
    };
  }, [form, stopWaiting]);

  const run = async (fields: LoginFields) => {
    if (!secure || availability !== 'enabled' || currentRun.current) return;
    const url = aiOpsLoginURL(fields.url);
    const target = fields.target_url?.trim() ? aiOpsLoginURL(fields.target_url) : undefined;
    if (!url || target === null || (target && target.origin !== url.origin)) {
      setFeedback('登录地址和目标页面必须属于同一个 HTTPS 网站。');
      return;
    }
    const username = fields.username.trim();
    const task = fields.task.trim();
    if (!username || !task || !fields.password?.trim() || byteLength(username) > 320 || byteLength(fields.password) > 1024 ||
      byteLength(task) > 4000 || credentialHasControl(username) || credentialHasControl(fields.password) ||
      (fields.model && !models.includes(fields.model))) {
      setFeedback('请输入账号、密码和任务；如已更换模型，请重新选择。');
      return;
    }

    const controller = new AbortController();
    currentRun.current = controller;
    setRunning(true);
    setFeedback('');
    setResult(null);
    try {
      const pending = webLoginRequest('/run', controller.signal, {
        url: url.href, target_url: target?.href, username, password: fields.password, task, model: fields.model,
      });
      // Clear the live field immediately; no password is kept in request/retry
      // state, chat history or a browser storage adapter.
      form.setFieldValue('password', '');
      const response = await pending;
      if (!response.ok) {
        const explanation = await runFailure(response);
        if (!controller.signal.aborted) setFeedback(explanation);
        return;
      }
      const next = responseResult(await response.json(), url.origin);
      if (controller.signal.aborted) return;
      if (next) setResult(next);
      else setFeedback('服务器未返回有效的网页分析结果。');
    } catch {
      if (!controller.signal.aborted) setFeedback('连接中断，未自动重试。网站可能已收到登录请求，请确认后再操作。');
    } finally {
      if (currentRun.current === controller) {
        currentRun.current = null;
        setRunning(false);
      }
    }
  };

  return (
    <section className="ai-ops-web-login" aria-label="AI 运维网页登录">
      <Card>
        <div className="ai-ops-web-login-heading">
          <GlobalOutlined aria-hidden />
          <Title level={4}>网页登录与只读查询</Title>
          <Tag color="blue">只读</Tag>
        </div>
        <Paragraph type="secondary">
          填写网站、账号和查询任务，AI 运维在独立浏览器中完成常见账号密码登录，再读取指定页面或登录后的页面。
        </Paragraph>
        {!secure ? <Alert showIcon type="warning" message="请使用 HTTPS 打开后台后再提交网页登录资料。" /> :
          availability === 'loading' ? <Space role="status"><Spin size="small" /><Text>正在检查网页登录组件…</Text></Space> :
            availability === 'error' ? <Alert showIcon type="error" message="读取网页登录状态失败" action={<Button size="small" onClick={() => setRefresh((n) => n + 1)}>重试检查</Button>} /> :
              availability === 'disabled' ? <Alert showIcon type="info" message="服务器尚未启用网页登录组件" description="浏览器执行器部署并启用后，才可从这里提交网页登录任务。" /> : (
                <Form form={form} name="ai-ops-web-login" layout="vertical" preserve={false} disabled={running} autoComplete="off" onFinish={(fields) => void run(fields)}>
                  <div className="ai-ops-web-login-fields">
                    <Form.Item<LoginFields> name="url" label="网站或登录地址"
                      rules={[{ required: true, message: '请输入网址' }, { validator: (_, value) => !value || aiOpsLoginURL(value) ? Promise.resolve() : Promise.reject(new Error(urlError)) }]}>
                      <Input maxLength={2048} placeholder="https://dashboard.example.com/login" autoComplete="off" spellCheck={false} />
                    </Form.Item>
                    <Form.Item<LoginFields> name="target_url" label="要读取的页面地址（可选）" dependencies={['url']}
                      rules={[({ getFieldValue }) => ({ validator: (_, value) => {
                        if (!value?.trim()) return Promise.resolve();
                        const target = aiOpsLoginURL(value);
                        const login = aiOpsLoginURL(getFieldValue('url') || '');
                        if (!target) return Promise.reject(new Error(urlError));
                        return login && login.origin === target.origin ? Promise.resolve() : Promise.reject(new Error('目标页面和登录地址必须属于同一个网站'));
                      } })]}>
                      <Input maxLength={2048} placeholder="留空时读取登录后显示的页面" autoComplete="off" spellCheck={false} />
                    </Form.Item>
                    <Form.Item<LoginFields> name="username" label="该网站的账号"
                      rules={[{ required: true, whitespace: true, message: '请输入该网站的账号' }, { max: 320 }]}>
                      <Input maxLength={320} autoComplete="off" spellCheck={false} />
                    </Form.Item>
                    <Form.Item<LoginFields> name="password" label="该网站的密码"
                      rules={[{ required: true, message: '请输入该网站的密码' }, { max: 1024 }]}>
                      <Input.Password maxLength={1024} autoComplete="new-password" visibilityToggle={false} prefix={<LockOutlined />} spellCheck={false} />
                    </Form.Item>
                  </div>
                  <Form.Item<LoginFields> name="task" label="想查看什么"
                    rules={[{ required: true, whitespace: true, message: '请描述要查询的信息' }, { max: 2000 }]}>
                    <Input.TextArea rows={3} maxLength={2000} placeholder="例如：查看证书状态和到期日期，列出尚未续期的项目。" autoComplete="off" />
                  </Form.Item>
                  {models.length > 0 && <Form.Item<LoginFields> className="ai-ops-web-login-model" name="model" label="分析模型（可选）">
                    <Select allowClear options={models.map((model) => ({ value: model, label: model }))} placeholder="使用默认可用模型" />
                  </Form.Item>}
                  <Paragraph type="secondary" className="ai-ops-web-login-note">
                    账号密码只用于本次登录，不进模型或运维聊天记录。页面内容会发送给配置的 AI 中转进行分析，请勿查询含密钥等敏感内容的页面。遇到跨域登录、验证码或扫码会停止。
                  </Paragraph>
                  <Space wrap>
                    <Button type="primary" htmlType="submit" loading={running}>登录并读取</Button>
                    {running && <Button disabled={false} onClick={() => {
                      stopWaiting();
                      setRunning(false);
                      setFeedback('已停止等待；网站可能已收到登录请求。');
                    }}>停止等待</Button>}
                    {running && <Text type="secondary" role="status">正在登录并读取网页，较慢时请耐心等待。</Text>}
                  </Space>
                </Form>
              )}
        {feedback && <Alert className="ai-ops-web-login-feedback" showIcon type="warning" message={feedback} role="status" />}
      </Card>
      {result && <Card title="网页查询结果" aria-live="polite">
        <Space wrap>
          <Text strong>{result.page_title || '网页'}</Text>
          <Text type="secondary">{result.origin}</Text>
          {result.model && <Tag>{result.model}</Tag>}
        </Space>
        <div className="ai-ops-web-login-result">{result.summary}</div>
      </Card>}
    </section>
  );
}
