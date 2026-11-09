import React, { forwardRef, useEffect, useImperativeHandle, useRef, useState } from 'react';
import { Button, Input, Typography } from 'antd';
import type { TextAreaRef } from 'antd/es/input/TextArea';
import { SendOutlined, StopOutlined } from '@ant-design/icons';
import { AI_CHAT_SIZE_ERROR, aiMessageTooLarge, aiShouldSendKey } from './aiOpsModel';
import { watchMiniComposerSize } from './aiOpsComposerSize';

export type AIOpsComposerHandle = { setValue: (value: string) => void };
type Props = {
  draftRef: React.MutableRefObject<string>;
  miniApp: boolean;
  compact: boolean;
  loaded: boolean;
  busy: string;
  sessionBusy: boolean;
  unavailable: boolean;
  sendBlocked: boolean;
  isAdmin: boolean;
  onSend: (content: string) => void;
  onStop: () => void;
};

// Keystrokes belong to the composer, not the page containing up to 400 messages
// and their tool/action details. The ref retains drafts across hidden tabs and
// lets selection, presets and successful sends reset them without a debounce.
const AIOpsComposer = forwardRef<AIOpsComposerHandle, Props>(function AIOpsComposer({ draftRef, miniApp, compact, loaded, busy, sessionBusy, unavailable, sendBlocked, isAdmin, onSend, onStop }, ref) {
  const [value, setValue] = useState(draftRef.current);
  // Modern mobile WebViews size text natively without rc-textarea's repeated
  // synchronous measure/render cycle. Older hosts use a frame-scheduled mirror.
  const [supportsNativeSize] = useState(() => typeof CSS !== 'undefined' && typeof CSS.supports === 'function' && CSS.supports('field-sizing', 'content'));
  const nativeSize = miniApp && supportsNativeSize;
  const inputRef = useRef<TextAreaRef>(null);
  const size = useRef<ReturnType<typeof watchMiniComposerSize> | null>(null);
  useEffect(() => {
    const input = inputRef.current?.resizableTextArea?.textArea;
    if (!miniApp || nativeSize || !input) return;
    const current = watchMiniComposerSize(input);
    size.current = current;
    return () => { current.dispose(); size.current = null; };
  }, [miniApp, nativeSize]);
  useEffect(() => { size.current?.refresh(); }, [value]);
  const composing = useRef(false);
  const compositionEndedAt = useRef(0);
  const update = (next: string) => { draftRef.current = next; setValue(next); };
  useImperativeHandle(ref, () => ({ setValue: update }), [draftRef]);
  const disabled = (miniApp && !loaded) || busy === 'send' || sessionBusy;
  const tooLarge = aiMessageTooLarge(value);
  const canSend = !!value.trim() && !tooLarge && !disabled && !unavailable && !busy && !sendBlocked;
  const send = () => { if (canSend) onSend(draftRef.current); };
  const serverHelp = <details className="ai-ops-server-help"><summary>批量登记服务器</summary><div className="ai-ops-server-help-content"><p>可直接粘贴多台 SSH 资料，每次最多 50 台；支持多段文字、单行、表格和 JSON，无需逐台填表。请保留每台的地址、用户名和密码或完整私钥，未填端口默认 22。</p><pre>{'帮我登记以下服务器：\n192.0.2.10:2222\nroot\nexample-password-A!\n\n192.0.2.20\nops\nexample-password-B$'}</pre><p>密码特殊字符请原样粘贴；首尾空格可写成 password: " space-preserved "。后台先识别并加密凭据，AI 只接收服务器信息和登记编号；无法确定归属时会提示补齐，不会猜测密码。</p></div></details>;
  const sizeError = tooLarge && <Typography.Text className="ai-ops-composer-error" type="danger" role="alert">{AI_CHAT_SIZE_ERROR}</Typography.Text>;
  const input = <Input.TextArea ref={inputRef} aria-label="运维消息" aria-invalid={tooLarge || undefined} className={nativeSize ? 'mini-ops-native-size' : undefined} rows={miniApp ? 1 : undefined} value={value} onChange={(event) => update(event.target.value)} onCompositionStart={() => { composing.current = true; }} onCompositionEnd={() => { composing.current = false; compositionEndedAt.current = Date.now(); }} onKeyDown={(event) => {
    if (!aiShouldSendKey({ key: event.key, shiftKey: event.shiftKey, ctrlKey: event.ctrlKey, altKey: event.altKey, metaKey: event.metaKey, repeat: event.repeat, isComposing: event.nativeEvent.isComposing, keyCode: event.keyCode }, composing.current || Date.now() - compositionEndedAt.current < 50)) return;
    event.preventDefault(); send();
  }} placeholder={miniApp ? sessionBusy ? 'AI 正在处理…' : '发送消息或粘贴服务器资料…' : compact ? '描述需求或粘贴多台服务器资料…' : '描述需求或粘贴多台服务器资料；Enter 发送，Shift+Enter 换行'} autoSize={miniApp ? false : { minRows: compact ? 2 : 3, maxRows: compact ? 4 : 8 }} disabled={disabled} />;
  if (miniApp) return <div className="ai-ops-composer mini-ops-composer"><div className="mini-ops-composer-row">{input}{sessionBusy ? <Button className="mini-ops-send-button is-stop" shape="circle" type="primary" danger aria-label="停止当前任务" icon={<StopOutlined />} loading={busy === 'stop'} disabled={!!busy && busy !== 'stop'} onClick={onStop} /> : <Button className="mini-ops-send-button" shape="circle" type="primary" aria-label="发送消息" icon={<SendOutlined />} loading={busy === 'send'} disabled={!canSend} onClick={send} />}</div>{sizeError}{serverHelp}{unavailable && <Typography.Text className="mini-ops-composer-notice" type="secondary">{isAdmin ? '请在设置中启用 AI 并保存 API Key' : '请联系管理员启用 AI 运维'}</Typography.Text>}</div>;
  return <div className="ai-ops-composer">{input}{sizeError}<div className="ai-ops-composer-actions"><div className="ai-ops-send-controls"><Button type="primary" icon={<SendOutlined />} loading={busy === 'send'} disabled={!canSend} onClick={send}>发送</Button>{sessionBusy && <Button danger icon={<StopOutlined />} loading={busy === 'stop'} disabled={!!busy && busy !== 'stop'} onClick={onStop}>停止</Button>}</div><div className="ai-ops-composer-meta">{serverHelp}<Typography.Text className="ai-ops-composer-hint" type="secondary">{unavailable ? isAdmin ? '请先在设置中启用 AI 并保存中转 API Key。' : '请联系管理员启用 AI 运维。' : compact ? 'Enter 发送 · Shift+Enter 换行' : 'Enter 发送 · Shift+Enter 换行 · 工具详情默认折叠'}</Typography.Text></div></div></div>;
});

export default AIOpsComposer;
