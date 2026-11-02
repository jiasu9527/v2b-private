import React, { forwardRef, useImperativeHandle, useRef, useState } from 'react';
import { Button, Input, Typography } from 'antd';
import { SendOutlined, StopOutlined } from '@ant-design/icons';
import { aiShouldSendKey } from './aiOpsModel';

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
  const composing = useRef(false);
  const compositionEndedAt = useRef(0);
  const update = (next: string) => { draftRef.current = next; setValue(next); };
  useImperativeHandle(ref, () => ({ setValue: update }), [draftRef]);
  const disabled = (miniApp && !loaded) || busy === 'send' || sessionBusy;
  const canSend = !!value.trim() && !disabled && !unavailable && !busy && !sendBlocked;
  const send = () => { if (canSend) onSend(draftRef.current); };
  const input = <Input.TextArea aria-label="运维消息" value={value} onChange={(event) => update(event.target.value)} onCompositionStart={() => { composing.current = true; }} onCompositionEnd={() => { composing.current = false; compositionEndedAt.current = Date.now(); }} onKeyDown={(event) => {
    if (!aiShouldSendKey({ key: event.key, shiftKey: event.shiftKey, ctrlKey: event.ctrlKey, altKey: event.altKey, metaKey: event.metaKey, repeat: event.repeat, isComposing: event.nativeEvent.isComposing, keyCode: event.keyCode }, composing.current || Date.now() - compositionEndedAt.current < 50)) return;
    event.preventDefault(); send();
  }} placeholder={miniApp ? sessionBusy ? 'AI 正在处理…' : '发送消息…' : compact ? '描述问题或操作需求…' : '描述问题或操作需求；Enter 发送，Shift+Enter 换行'} autoSize={miniApp ? { minRows: 1, maxRows: 4 } : { minRows: compact ? 2 : 3, maxRows: compact ? 4 : 8 }} maxLength={12000} disabled={disabled} />;
  if (miniApp) return <div className="ai-ops-composer mini-ops-composer"><div className="mini-ops-composer-row">{input}{sessionBusy ? <Button className="mini-ops-send-button is-stop" shape="circle" type="primary" danger aria-label="停止当前任务" icon={<StopOutlined />} loading={busy === 'stop'} disabled={!!busy && busy !== 'stop'} onClick={onStop} /> : <Button className="mini-ops-send-button" shape="circle" type="primary" aria-label="发送消息" icon={<SendOutlined />} loading={busy === 'send'} disabled={!canSend} onClick={send} />}</div>{unavailable && <Typography.Text className="mini-ops-composer-notice" type="secondary">{isAdmin ? '请在设置中启用 AI 并保存 API Key' : '请联系管理员启用 AI 运维'}</Typography.Text>}</div>;
  return <div className="ai-ops-composer">{input}<div className="ai-ops-composer-actions"><div className="ai-ops-send-controls"><Button type="primary" icon={<SendOutlined />} loading={busy === 'send'} disabled={!canSend} onClick={send}>发送</Button>{sessionBusy && <Button danger icon={<StopOutlined />} loading={busy === 'stop'} disabled={!!busy && busy !== 'stop'} onClick={onStop}>停止</Button>}</div><Typography.Text className="ai-ops-composer-hint" type="secondary">{unavailable ? isAdmin ? '请先在设置中启用 AI 并保存中转 API Key。' : '请联系管理员启用 AI 运维。' : compact ? 'Enter 发送 · Shift+Enter 换行' : 'Enter 发送 · Shift+Enter 换行 · 工具详情默认折叠'}</Typography.Text></div></div>;
});

export default AIOpsComposer;
