import { aiOpsRequestHeaders, aiOpsRequestURL, clearAuth, invalidateTelegramMiniSession, isTelegramMiniApp } from '../lib/api';

export type AIStreamEvent = { event: string; data: unknown };

// Parse complete SSE frames only. TextDecoder in the reader preserves UTF-8
// boundaries, including Chinese text split between network chunks.
export function aiStreamParser(emit: (event: AIStreamEvent) => void) {
  let buffer = '';
  return (chunk: string) => {
    buffer += chunk;
    if (buffer.length > 16 * 1024 * 1024) throw new Error('实时响应超出限制');
    for (;;) {
      const match = /\r?\n\r?\n/.exec(buffer);
      if (!match) break;
      const frame = buffer.slice(0, match.index);
      buffer = buffer.slice(match.index + match[0].length);
      let event = 'message';
      const data: string[] = [];
      for (const line of frame.split(/\r?\n/)) {
        if (line.startsWith('event:')) event = line.slice(6).trim();
        if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, ''));
      }
      if (!data.length) continue;
      if (!['snapshot', 'draft', 'invalidate', 'end', 'error'].includes(event)) continue;
      let value: unknown;
      try { value = JSON.parse(data.join('\n')); } catch { throw new Error('实时响应格式无效'); }
      emit({ event, data: value });
    }
  };
}

export async function aiReadSessionStream(id: number, signal: AbortSignal, emit: (event: AIStreamEvent) => void): Promise<void> {
  const response = await fetch(`${aiOpsRequestURL('/ai-ops/session/stream')}?id=${encodeURIComponent(id)}`, {
    headers: { ...aiOpsRequestHeaders(), Accept: 'text/event-stream' },
    credentials: isTelegramMiniApp() ? 'omit' : 'same-origin', cache: 'no-store', signal,
  });
  if (response.status === 401) {
    if (isTelegramMiniApp()) invalidateTelegramMiniSession();
    else { clearAuth(); window.dispatchEvent(new Event('forest-auth-invalid')); }
  }
  if (!response.ok || !response.headers.get('content-type')?.includes('text/event-stream') || !response.body) throw new Error('实时连接不可用，将继续自动刷新');
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let ended = false;
  const consume = aiStreamParser((event) => {
    if (event.event === 'error') {
      if (isTelegramMiniApp() && (event.data as { code?: string })?.code === 'auth_invalid') invalidateTelegramMiniSession();
      throw new Error('实时连接已中断，将继续自动刷新');
    }
    emit(event); if (event.event === 'end') ended = true;
  });
  try {
    while (!signal.aborted && !ended) {
      const { done, value } = await reader.read();
      if (done) { consume(decoder.decode()); break; }
      consume(decoder.decode(value, { stream: true }));
    }
  } finally {
    await reader.cancel().catch(() => undefined);
    reader.releaseLock();
  }
}
