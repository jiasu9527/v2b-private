import React, { useEffect, useRef, useState } from 'react';
import { Alert, Button, ConfigProvider, Space, Spin, Typography, theme } from 'antd';
import zhCN from 'antd/locale/zh_CN';
import AIOpsPage from './pages/AIOpsPage';
import { authenticateTelegramMiniApp, clearAuth, getTelegramMiniIdentity, invalidateTelegramMiniSession, setTelegramMiniSession, type TelegramMiniSession } from './lib/api';
import { miniSafeInset, miniViewportHeight, telegramMiniTheme, type TelegramThemeParams } from './lib/telegramMiniTheme';
import './TelegramMiniApp.css';

type TelegramWebApp = {
  initData?: string; colorScheme?: string; themeParams?: TelegramThemeParams; viewportHeight?: number; viewportStableHeight?: number;
  safeAreaInset?: { top?: number; bottom?: number; left?: number; right?: number };
  contentSafeAreaInset?: { top?: number; bottom?: number; left?: number; right?: number };
  ready?: () => void; expand?: () => void; close?: () => void;
  setHeaderColor?: (color: string) => void;
  setBackgroundColor?: (color: string) => void;
  setBottomBarColor?: (color: string) => void;
  isVersionAtLeast?: (version: string) => boolean;
  onEvent?: (name: string, callback: () => void) => void;
  offEvent?: (name: string, callback: () => void) => void;
};

export default function TelegramMiniApp() {
  const [phase, setPhase] = useState<'loading' | 'ready' | 'verify' | 'invalid' | 'outside'>('loading');
  const [error, setError] = useState('');
  const [palette, setPalette] = useState(telegramMiniTheme());
  const [roleKey, setRoleKey] = useState('');
  const verifyRef = useRef<() => void>(() => undefined);
  const webApp = (window as any).Telegram?.WebApp as TelegramWebApp | undefined;

  useEffect(() => {
    // Ant Design dialogs may be portalled outside the hidden workspace.
    // Hide those as well while identity is unverified without losing drafts.
    document.body.classList.toggle('telegram-mini-identity-blocked', phase !== 'ready');
    return () => document.body.classList.remove('telegram-mini-identity-blocked');
  }, [phase]);

  useEffect(() => {
    let live = true, invalid = false, verifying = false;
    let auth: TelegramMiniSession | null = null;
    let timer: number | undefined, expiry: number | undefined;
    const controller = new AbortController();
    let stableHeight = window.innerHeight;
    let previousWidth = window.innerWidth;
    const previousColors = new Map<string, string>();
    const updateTheme = () => {
      const next = telegramMiniTheme(webApp?.colorScheme, webApp?.themeParams);
      setPalette(next);
      for (const [name, value] of Object.entries(next.variables)) {
        if (!previousColors.has(name)) previousColors.set(name, document.documentElement.style.getPropertyValue(name));
        document.documentElement.style.setProperty(name, value);
      }
      // Unsupported appearance APIs must never interrupt identity verification.
      const appearance = (version: string, method: ((value: string) => void) | undefined, value: string) => {
        try { if (method && (!webApp?.isVersionAtLeast || webApp.isVersionAtLeast(version))) method.call(webApp, value); } catch { /* Older Telegram clients keep their own chrome. */ }
      };
      appearance('6.1', webApp?.setHeaderColor, next.colors.header_bg_color);
      appearance('6.1', webApp?.setBackgroundColor, next.colors.secondary_bg_color);
      appearance('7.10', webApp?.setBottomBarColor, next.colors.bottom_bar_bg_color);
    };
    const viewport = () => {
      const visual = window.visualViewport?.height || window.innerHeight;
      const height = miniViewportHeight(webApp?.viewportHeight, visual);
      if (height > 0) document.documentElement.style.setProperty('--mini-viewport-height', `${height}px`);
      const active = document.activeElement as HTMLElement | null;
      const editing = active?.tagName === 'TEXTAREA' || (active?.tagName === 'INPUT' && !['button', 'checkbox', 'radio', 'submit'].includes((active as HTMLInputElement).type)) || !!active?.isContentEditable;
      if (Math.abs(window.innerWidth - previousWidth) > 80) stableHeight = height;
      previousWidth = window.innerWidth;
      if (!editing) stableHeight = Math.max(height, window.innerHeight);
      const sdkStable = Number(webApp?.viewportStableHeight);
      const baseline = Math.max(stableHeight, Number.isFinite(sdkStable) ? sdkStable : 0);
      document.body.classList.toggle('telegram-mini-keyboard-open', !!editing && baseline - height > 120);
      for (const side of ['top', 'bottom', 'left', 'right'] as const) {
        const value = miniSafeInset(webApp?.safeAreaInset?.[side]) + miniSafeInset(webApp?.contentSafeAreaInset?.[side]);
        document.documentElement.style.setProperty(`--mini-safe-${side}`, `${value}px`);
      }
    };
    const expire = () => {
      invalid = true; auth = null; clearAuth();
      if (live) { setPhase('invalid'); setError('登录已过期或运维绑定已撤销，请关闭此页面并从 Telegram Bot 重新打开。'); }
    };
    const verify = async () => {
      if (!live || invalid || verifying || !auth || document.visibilityState === 'hidden') return;
      verifying = true;
      try {
        const user = await getTelegramMiniIdentity(controller.signal);
        if (!live || invalid || !auth) return;
        if (user.id !== auth.user.id) { invalidateTelegramMiniSession(); return; }
        auth = { ...auth, user };
        setTelegramMiniSession(auth);
        setRoleKey(`${user.id}:${user.is_admin}:${user.is_staff}`);
        setPhase('ready'); setError('');
      } catch {
        if (live && !invalid) { setPhase('verify'); setError('暂时无法验证运维身份，请检查网络后重试。不会重新使用 Telegram 登录数据自动登录。'); }
      } finally { verifying = false; }
    };
    verifyRef.current = () => { void verify(); };
    const visibility = () => {
      if (invalid || !auth) return;
      setPhase('verify');
      if (document.visibilityState !== 'hidden') void verify();
    };
    document.body.classList.add('telegram-mini-body');
    window.addEventListener('forest-mini-auth-invalid', expire);
    document.addEventListener('visibilitychange', visibility);
    document.addEventListener('focusin', viewport);
    document.addEventListener('focusout', viewport);
    window.addEventListener('resize', viewport);
    window.visualViewport?.addEventListener('resize', viewport);
    for (const event of ['viewportChanged', 'safeAreaChanged', 'contentSafeAreaChanged']) webApp?.onEvent?.(event, viewport);
    webApp?.onEvent?.('themeChanged', updateTheme);
    viewport(); updateTheme(); webApp?.ready?.(); webApp?.expand?.();

    if (!webApp?.initData) setPhase('outside');
    else {
      void (async () => {
        try {
          const next = await authenticateTelegramMiniApp(webApp.initData!, controller.signal);
          if (!live || invalid) return;
          setTelegramMiniSession(next); auth = next;
          expiry = window.setTimeout(() => invalidateTelegramMiniSession(), Math.max(0, next.expires_at * 1000 - Date.now()));
          await verify();
          if (live && !invalid) timer = window.setInterval(() => { void verify(); }, 5000);
        } catch {
          if (live && !invalid) expire();
        }
      })();
    }
    return () => {
      live = false; controller.abort(); clearAuth(); verifyRef.current = () => undefined;
      window.clearInterval(timer); window.clearTimeout(expiry);
      window.removeEventListener('forest-mini-auth-invalid', expire);
      document.removeEventListener('visibilitychange', visibility);
      document.removeEventListener('focusin', viewport);
      document.removeEventListener('focusout', viewport);
      window.removeEventListener('resize', viewport);
      window.visualViewport?.removeEventListener('resize', viewport);
      for (const event of ['viewportChanged', 'safeAreaChanged', 'contentSafeAreaChanged']) webApp?.offEvent?.(event, viewport);
      webApp?.offEvent?.('themeChanged', updateTheme);
      for (const [name, value] of previousColors) {
        if (value) document.documentElement.style.setProperty(name, value);
        else document.documentElement.style.removeProperty(name);
      }
      document.body.classList.remove('telegram-mini-body', 'telegram-mini-keyboard-open');
    };
  }, []);

  return <ConfigProvider locale={zhCN} theme={{ algorithm: palette.dark ? theme.darkAlgorithm : theme.defaultAlgorithm, token: palette.tokens }}>
    <main className={`telegram-mini-app${palette.dark ? ' is-dark' : ''}`}>
      {!!roleKey && (phase === 'ready' || phase === 'verify') && <div className="telegram-mini-workspace" hidden={phase !== 'ready'}><AIOpsPage key={roleKey} miniApp /></div>}
      {phase !== 'ready' && <div className="telegram-mini-state"><Space direction="vertical" size="large" style={{ width: '100%' }}>
        <Typography.Title level={3}>AI 运维</Typography.Title>
        {phase === 'loading' ? <Spin tip="正在验证 Telegram 运维身份…"><div style={{ height: 90 }} /></Spin> : <Alert showIcon type={phase === 'verify' ? 'warning' : 'info'} message={phase === 'outside' ? '请从 Telegram Bot 打开小程序' : phase === 'invalid' ? '需要重新打开 Bot' : '正在验证登录状态'} description={phase === 'outside' ? '先由管理员生成一次性绑定码完成运维身份绑定，再通过 Bot 菜单进入。小程序无需输入后台密码，也不会使用当前浏览器的后台登录。' : error} />}
        {phase === 'verify' && <Button onClick={() => verifyRef.current()}>重新验证连接</Button>}
        {phase === 'invalid' && webApp?.close && <Button onClick={() => webApp.close?.()}>关闭小程序</Button>}
        <Typography.Text type="secondary">关闭窗口不影响已经提交的后台任务；需要终止任务请在对话中点击停止。</Typography.Text>
      </Space></div>}
    </main>
  </ConfigProvider>;
}
