export const AI_DEFAULT_MODELS = ['gpt-6.1-sol', 'claude-fable-5-1', 'deepseek-v4.1-flash', 'glm-5.3'];
export type AIAccessMode = 'standard' | 'full';
export const AI_DEFAULT_ACCESS_MODE: AIAccessMode = 'full';
export type AIReasoningEffort = 'auto' | 'none' | 'minimal' | 'low' | 'medium' | 'high' | 'xhigh';
export type AIReasoningChoice = 'default' | AIReasoningEffort;
export const AI_REASONING_LABELS: Record<AIReasoningEffort, string> = { auto: '中转默认', none: '不启用推理', minimal: '极低', low: '低', medium: '中', high: '高', xhigh: '极高' };
export const AI_REASONING_OPTIONS = Object.entries(AI_REASONING_LABELS).map(([value, label]) => ({ value: value as AIReasoningEffort, label }));
export type AIFallbackRelay = { id: string; name: string; base_url: string; models: string[]; enabled: boolean; has_api_key: boolean };
export type AIFallbackRelayForm = { id?: string; name: string; base_url: string; models: string[]; enabled: boolean; api_key: string; clear_api_key: boolean };
export type AIModelEndpoint = { base_url: string; has_api_key: boolean };
export type AIModelEndpointForm = { mode: 'shared' | 'dedicated'; base_url: string; api_key: string; clear_api_key: boolean };
export type AIModelEndpointSave = { base_url: string; api_key: string; clear_api_key: boolean };
export type AIConfig = { enabled: boolean; base_url?: string; models: string[]; has_api_key: boolean; model_endpoints?: Record<string, AIModelEndpoint>; ready_models?: string[]; reasoning_efforts?: Record<string, string>; failover_enabled?: boolean; failover_timeout_seconds?: number; fallback_relays?: AIFallbackRelay[] };
const hasOwn = (value: object, key: string) => Object.prototype.hasOwnProperty.call(value, key);
export function aiModelEndpointForms(models: string[], endpoints: Record<string, AIModelEndpoint> = {}): Record<string, AIModelEndpointForm> {
  return Object.fromEntries([...new Set([...models, ...Object.keys(endpoints)])].map((model) => {
    const saved = hasOwn(endpoints, model) ? endpoints[model] : undefined;
    return [model, { mode: saved ? 'dedicated' : 'shared', base_url: saved?.base_url || '', api_key: '', clear_api_key: false }];
  }));
}
export function aiNormalizeEndpointURL(value: string): string {
  const raw = value.trim().replace(/\/+$/, '');
  if (!raw) return '';
  let url: URL;
  try { url = new URL(raw); } catch { throw new Error('请输入有效的中转 Base URL'); }
  const loopback = url.hostname === 'localhost' || url.hostname === '[::1]' || /^127(?:\.\d{1,3}){3}$/.test(url.hostname);
  if (url.username || url.password || url.search || url.hash || (url.protocol !== 'https:' && !(url.protocol === 'http:' && loopback))) throw new Error('Base URL 必须使用 HTTPS，且不能包含密钥、查询参数或片段');
  // Preserve the administrator's host spelling and path: Go normalizes only
  // these suffixes, not WHATWG URL's host casing/default ports/path segments.
  const base = raw.endsWith('/chat/completions') ? raw.slice(0, -'/chat/completions'.length) : raw;
  if (new URL(base).pathname === '/') return `${base.replace(/\/+$/, '')}/v1`;
  return base;
}
export function aiPrepareModelEndpoints(values: Record<string, AIModelEndpointForm> = {}, previous: Record<string, AIModelEndpoint> = {}, models: string[] = [], enabled = false): Record<string, AIModelEndpointSave> {
  const entries: [string, AIModelEndpointSave][] = [];
  for (const [model, entry] of Object.entries(values)) {
    if (!entry || entry.mode === 'shared') continue;
    if (entry.mode !== 'dedicated') throw new Error(`${model}：请选择默认中转或独立中转`);
    if (!models.includes(model)) throw new Error(`${model} 已从模型列表移除，请先将它切回默认中转以确认删除独立配置`);
    const saved = hasOwn(previous, model) ? previous[model] : undefined;
    const base = aiNormalizeEndpointURL(String(entry.base_url || ''));
    if (!base) throw new Error(`${model}：请填写独立 Base URL`);
    const key = String(entry.api_key || '').trim();
    const clear = !!entry.clear_api_key;
    if (key && clear) throw new Error(`${model}：清除 Key 和填写新 Key 不能同时使用`);
    if (saved?.has_api_key && base !== aiNormalizeEndpointURL(saved.base_url) && !key && !clear) throw new Error(`${model}：修改地址后请重新输入 API Key 或明确清除旧 Key`);
    if (enabled && !key && (!saved?.has_api_key || clear)) throw new Error(`${model}：启用前请填写独立 API Key，或切回默认中转`);
    entries.push([model, { base_url: base, api_key: key, clear_api_key: clear }]);
  }
  return Object.fromEntries(entries);
}
export function aiModelReady(config: AIConfig, model: string): boolean {
  if (!config.enabled || !config.models.includes(model)) return false;
  // The safe readiness list also works for staff, whose endpoint details are hidden.
  if (Array.isArray(config.ready_models)) return config.ready_models.includes(model);
  if (config.model_endpoints && hasOwn(config.model_endpoints, model)) return !!config.model_endpoints[model]?.has_api_key;
  return !!config.has_api_key;
}
export const AI_MAX_FALLBACK_RELAYS = 4;
export const AI_DEFAULT_FAILOVER_TIMEOUT = 30;
export function aiFallbackRelayForms(relays: AIFallbackRelay[] = []): AIFallbackRelayForm[] {
  return relays.map((relay) => ({ id: relay.id, name: relay.name, base_url: relay.base_url, models: [...relay.models], enabled: relay.enabled, api_key: '', clear_api_key: false }));
}
export function aiPrepareFallbackRelays(values: AIFallbackRelayForm[] = [], previous: AIFallbackRelay[] = [], models: string[] = []): AIFallbackRelayForm[] {
  if (values.length > AI_MAX_FALLBACK_RELAYS) throw new Error('最多配置 4 个备用中转');
  const ids = new Set<string>();
  return values.map((relay, index) => {
    const id = relay.id || '';
    const saved = id ? previous.find((item) => item.id === id) : undefined;
    if (id && (!saved || ids.has(id))) throw new Error(`备用中转 ${index + 1} 的配置已变化，请刷新后重试`);
    if (id) ids.add(id);
    const apiKey = String(relay.api_key || '').trim();
    const clear = !!relay.clear_api_key;
    if (clear && apiKey) throw new Error(`备用中转 ${index + 1}：清除 Key 和填写新 Key 不能同时使用`);
    const baseURL = String(relay.base_url || '').trim();
    if (saved?.has_api_key && baseURL !== saved.base_url && !apiKey && !clear) throw new Error(`备用中转 ${index + 1}：修改地址后请重新输入 API Key 或明确清除旧 Key`);
    if (relay.enabled && !apiKey && (!saved?.has_api_key || clear)) throw new Error(`备用中转 ${index + 1}：启用前请填写 API Key，或先停用该中转`);
    const supportedModels = [...new Set((relay.models || []).map((model) => model.trim()).filter(Boolean))];
    if (supportedModels.some((model) => !models.includes(model))) throw new Error(`备用中转 ${index + 1}：请选择当前全局模型列表中的模型，或清空以继承全部模型`);
    return { ...(id ? { id } : {}), name: String(relay.name || '').trim(), base_url: baseURL, models: supportedModels, enabled: !!relay.enabled, api_key: apiKey, clear_api_key: clear };
  });
}
export type AISession = { id: number; title: string; model: string; created_by: number; status: string; created_at: number; updated_at: number; access_mode?: AIAccessMode; access_version?: number; full_access_by?: number; reasoning_effort?: AIReasoningChoice; source?: string };
export type AITelegramConfig = { enabled: boolean; default_model: string; default_access_mode: AIAccessMode; stream_updates: boolean; notify_completion: boolean };
export type AITelegramBinding = { actor_id: number; email: string; telegram_id: number | string; active: boolean; session_id?: number; bound_at: number };
export type AITelegramStatus = { config: AITelegramConfig; bindings: AITelegramBinding[]; my_binding?: AITelegramBinding | null; models: string[]; bot_ready: boolean };
export type AITelegramBindingCode = { code: string; expires_at: number; command: string };
export function aiTelegramConfig(value: unknown): AITelegramConfig {
  const config = value as AITelegramConfig | null;
  if (!config || typeof config.enabled !== 'boolean' || typeof config.default_model !== 'string' ||
      !['standard', 'full'].includes(config.default_access_mode) || typeof config.stream_updates !== 'boolean' || typeof config.notify_completion !== 'boolean') {
    throw new Error('Telegram 配置格式不正确，请刷新后重试');
  }
  return { enabled: config.enabled, default_model: config.default_model, default_access_mode: config.default_access_mode, stream_updates: config.stream_updates, notify_completion: config.notify_completion };
}
export function aiTelegramBindings(status: AITelegramStatus | null, actorID: number, isAdmin: boolean): AITelegramBinding[] {
  if (!status || !isAdmin && (!Number.isSafeInteger(actorID) || actorID <= 0)) return [];
  const items = [...(status.bindings || []), ...(status.my_binding ? [status.my_binding] : [])];
  const seen = new Set<number>();
  return items.filter((item) => {
    if (!Number.isSafeInteger(item.actor_id) || item.actor_id <= 0 || seen.has(item.actor_id) || !isAdmin && item.actor_id !== actorID) return false;
    seen.add(item.actor_id);
    return true;
  });
}
export function aiTelegramActorID(value: unknown): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value <= 0) throw new Error('请输入有效的后台操作人 ID');
  return value;
}
export type AIMessage = { id: number; session_id: number; turn_id: number; role: string; content: string; tool_calls?: { id: string; function: { name: string; arguments: string } }[]; tool_call_id?: string; created_at: number };
export type AITurn = { id: number; status: string; error: string; created_at: number; completed_at: number; draft_content?: string; draft_round?: number; draft_version?: number; access_mode?: AIAccessMode; stop_requested?: boolean; reasoning_effort?: AIReasoningEffort };
export type AIAction = { id: number; session_id: number; actor_id: number; approved_by?: number; rejected_by?: number; approval_mode?: 'manual' | 'full_access'; authorization_turn_id?: number; tool_call_id: string; kind: string; target_id: number; target_type: string; server_label: string; payload: Record<string, unknown>; reason: string; status: string; expires_at: number; claimed_at: number; completed_at: number; output: string; error: string; created_at: number; updated_at: number };
export type AISessionDetail = { session: AISession; messages: AIMessage[]; turns: AITurn[]; actions: AIAction[]; messages_truncated?: boolean };
export type AIServer = { id: number; name: string; host: string; port: number; username: string; host_key_sha256: string; enabled: boolean; version: number; has_password: boolean; has_private_key: boolean; has_key_passphrase: boolean; created_at: number; updated_at: number };
export type AIFingerprint = { host: string; port: number; fingerprint: string; key_type: string };
export function aiReasoningEffort(value: unknown): AIReasoningEffort {
  return AI_REASONING_OPTIONS.some((option) => option.value === value) ? value as AIReasoningEffort : 'auto';
}
export function aiSessionReasoning(session?: AISession | null): AIReasoningChoice {
  return AI_REASONING_OPTIONS.some((option) => option.value === session?.reasoning_effort) ? session!.reasoning_effort as AIReasoningEffort : 'default';
}
export function aiReasoningDefaults(models: string[], values?: Record<string, unknown> | null): Record<string, AIReasoningEffort> {
  return Object.fromEntries(models.map((model) => [model, aiReasoningEffort(values && Object.prototype.hasOwnProperty.call(values, model) ? values[model] : undefined)]));
}
export function aiReasoningDefaultLabel(config: AIConfig, model: string): string {
  return `跟随设置（${AI_REASONING_LABELS[aiReasoningDefaults([model], config.reasoning_efforts)[model]]}）`;
}
export function aiActionStatus(action: AIAction, now = Date.now()): string {
  return action.status === 'pending' && action.expires_at * 1000 <= now ? 'expired' : action.status;
}
export function aiCanApprove(action: AIAction, enabled: boolean, now = Date.now()): boolean {
  return enabled && aiActionStatus(action, now) === 'pending';
}
export function aiSessionBusy(detail: AISessionDetail | null): boolean {
  return !!detail && (detail.session.status === 'running' || detail.turns.some((turn) => turn.status === 'running') || detail.actions.some((action) => action.status === 'running'));
}
export function aiAccessMode(session?: AISession | null): AIAccessMode {
  return session?.access_mode === 'full' ? 'full' : 'standard';
}
export function aiCanReadSession(session: AISession, actorID: number, isAdmin: boolean): boolean {
  return isAdmin || actorID > 0 && session.created_by === actorID;
}
export function aiOwnsFullAccess(session: AISession | null | undefined, actorID: number): boolean {
  return aiAccessMode(session) === 'full' && actorID > 0 && session?.full_access_by === actorID;
}

// API snapshots are JSON values. Compare fields without serializing large tool
// results, and keep unchanged records so transcript memoization can skip them.
// IDs or timestamps alone are not enough: output can change within one second.
function aiSnapshotEqual(previous: unknown, incoming: unknown): boolean {
  if (Object.is(previous, incoming)) return true;
  if (previous === null || incoming === null || typeof previous !== 'object' || typeof incoming !== 'object') return false;
  if (Array.isArray(previous) || Array.isArray(incoming)) {
    return Array.isArray(previous) && Array.isArray(incoming) && previous.length === incoming.length && previous.every((value, index) => aiSnapshotEqual(value, incoming[index]));
  }
  const keys = Object.keys(previous);
  if (keys.length !== Object.keys(incoming).length) return false;
  return keys.every((key) => Object.prototype.hasOwnProperty.call(incoming, key) && aiSnapshotEqual(previous[key], incoming[key]));
}

function aiReuseSnapshotItems<T extends { id: number }>(previous: T[], incoming: T[], merge?: (before: T | undefined, next: T) => T): T[] {
  if (previous === incoming) return previous;
  const byID = new Map(previous.map((item) => [item.id, item]));
  let sameOrder = previous.length === incoming.length;
  const next = incoming.map((item, index) => {
    const before = byID.get(item.id);
    const candidate = merge ? merge(before, item) : item;
    const value = before && aiSnapshotEqual(before, candidate) ? before : candidate;
    if (value !== previous[index]) sameOrder = false;
    return value;
  });
  return sameOrder ? previous : next;
}

export function aiMergeSessionAccess(previous: AISession | null | undefined, incoming: AISession): AISession {
  if (!previous || previous.id !== incoming.id) return incoming;
  const next = (previous.access_version || 0) > (incoming.access_version || 0)
    ? { ...incoming, access_mode: previous.access_mode, access_version: previous.access_version, full_access_by: previous.full_access_by }
    : incoming;
  return aiSnapshotEqual(previous, next) ? previous : next;
}
export function aiMergeSessionList(items: AISession[], incoming: AISession): AISession[] {
  const index = items.findIndex((item) => item.id === incoming.id);
  if (index < 0) return items;
  const session = aiMergeSessionAccess(items[index], incoming);
  if (session === items[index]) return items;
  const next = items.slice();
  next[index] = session;
  return next;
}
export function aiLatestTurn(turns: AITurn[]): AITurn | undefined {
  return turns.reduce<AITurn | undefined>((latest, turn) => !latest || turn.id > latest.id ? turn : latest, undefined);
}
export function aiToolResultName(messages: AIMessage[], index: number): string {
  const result = messages[index];
  if (!result || result.role !== 'tool' || !result.tool_call_id) return '';
  for (let i = index - 1; i >= 0; i -= 1) {
    const message = messages[i];
    if (message.session_id !== result.session_id || message.turn_id !== result.turn_id || message.role !== 'assistant') continue;
    if (message.tool_calls?.length) return message.tool_calls.find((call) => call.id === result.tool_call_id)?.function.name || '';
  }
  return '';
}
export type AIConversationTurn = { key: string; turnID: number; messages: AIMessage[]; actions: AIAction[]; turn?: AITurn };
export function aiConversationTurns(detail: AISessionDetail | null): AIConversationTurn[] {
  if (!detail) return [];
  const groups = new Map<number, AIConversationTurn>();
  const turns = new Map(detail.turns.map((turn) => [turn.id, turn]));
  const callTurns = new Map<string, Set<number>>();
  const group = (turnID: number) => {
    let value = groups.get(turnID);
    if (!value) {
      value = { key: `${detail.session.id}:${turnID}`, turnID, messages: [], actions: [], turn: turns.get(turnID) };
      groups.set(turnID, value);
    }
    return value;
  };
  for (const message of detail.messages) {
    if (message.session_id !== detail.session.id) continue;
    group(message.turn_id).messages.push(message);
    for (const call of message.tool_calls || []) {
      const ids = callTurns.get(call.id) || new Set<number>();
      ids.add(message.turn_id);
      callTurns.set(call.id, ids);
    }
  }
  for (const action of detail.actions) {
    if (action.session_id !== detail.session.id) continue;
    const namespacedTurn = Number(/^turn:(\d+):/.exec(action.tool_call_id)?.[1] || 0);
    const authorizedTurn = action.authorization_turn_id || 0;
    const matchingTurns = callTurns.get(action.tool_call_id);
    // Old actions may predate turn IDs. Keep ambiguous or truncated history
    // visible in its own group rather than attaching it to an unrelated reply.
    const turnID = Number.isSafeInteger(authorizedTurn) && authorizedTurn > 0 ? authorizedTurn
      : Number.isSafeInteger(namespacedTurn) && namespacedTurn > 0 ? namespacedTurn
        : !detail.messages_truncated && matchingTurns?.size === 1 ? [...matchingTurns][0] : 0;
    group(turnID).actions.push(action);
  }
  for (const turn of detail.turns) {
    if (turn.status === 'running') group(turn.id);
  }
  return [...groups.values()].sort((a, b) => a.turnID - b.turnID).map((value) => ({
    ...value,
    messages: value.messages.sort((a, b) => a.id - b.id),
    actions: value.actions.sort((a, b) => a.id - b.id),
  }));
}
export function aiParseModels(value: string): string[] {
  return [...new Set(value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean))];
}
export function aiRequestID(): string {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID();
  const bytes = new Uint8Array(16); globalThis.crypto.getRandomValues(bytes);
  return Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('');
}
export function aiFingerprintMatches(observed: AIFingerprint | null, host: unknown, port: unknown): boolean {
  return !!observed && observed.host === String(host || '').trim() && observed.port === Number(port || 22);
}

export type AIDraft = { session_id: number; turn_id: number; round: number; content: string; status?: string; version?: number };
function aiApplyTurnDraft(turn: AITurn, draft: AIDraft): AITurn {
  if (turn.status !== 'running' || typeof draft.content !== 'string' || !Number.isInteger(draft.round) || draft.round < 0) return turn;
  const versioned = Number.isSafeInteger(draft.version) && draft.version! >= 0;
  if (versioned ? draft.version! < (turn.draft_version || 0) : draft.round < (turn.draft_round || 0)) return turn;
  let next: AITurn;
  if (draft.status && draft.status !== 'running') next = { ...turn, status: draft.status, draft_content: '', draft_version: draft.version ?? turn.draft_version };
  else {
    if (!versioned && draft.round === (turn.draft_round || 0) && draft.content.length < (turn.draft_content || '').length) return turn;
    next = { ...turn, draft_round: draft.round, draft_content: draft.content, draft_version: draft.version ?? turn.draft_version };
  }
  return aiSnapshotEqual(turn, next) ? turn : next;
}
export function aiApplyDraft(detail: AISessionDetail | null, draft: AIDraft): AISessionDetail | null {
  if (!detail || draft.session_id !== detail.session.id || typeof draft.content !== 'string' || !Number.isInteger(draft.round) || draft.round < 0) return detail;
  const index = detail.turns.findIndex((item) => item.id === draft.turn_id);
  if (index < 0) return detail;
  const turn = aiApplyTurnDraft(detail.turns[index], draft);
  if (turn === detail.turns[index]) return detail;
  const turns = detail.turns.slice();
  turns[index] = turn;
  return { ...detail, turns };
}
export function aiMergeSnapshot(previous: AISessionDetail | null, incoming: AISessionDetail): AISessionDetail {
  if (!previous || previous.session.id !== incoming.session.id) return incoming;
  const turns = aiReuseSnapshotItems(previous.turns, incoming.turns, (turn, incomingTurn) => {
    if (!turn || turn.status !== 'running' || incomingTurn.status !== 'running') return incomingTurn;
    if (turn.draft_version !== undefined && turn.draft_version <= (incomingTurn.draft_version || 0)) return incomingTurn;
    return turn.draft_content || turn.draft_version !== undefined
      ? aiApplyTurnDraft(incomingTurn, { session_id: incoming.session.id, turn_id: turn.id, round: turn.draft_round || 0, content: turn.draft_content || '', version: turn.draft_version })
      : incomingTurn;
  });
  const next = {
    ...incoming,
    session: aiMergeSessionAccess(previous.session, incoming.session),
    messages: aiReuseSnapshotItems(previous.messages, incoming.messages),
    actions: aiReuseSnapshotItems(previous.actions, incoming.actions),
    turns,
  };
  return aiSnapshotEqual(previous, next) ? previous : next;
}
export function aiVisibleDraft(detail: AISessionDetail | null): AITurn | undefined {
  if (!detail) return undefined;
  const turn = aiLatestTurn(detail.turns);
  if (!turn || turn.status !== 'running' || !turn.draft_content) return undefined;
  if (detail.messages.some((message) => message.turn_id === turn.id && message.role === 'assistant' && message.content === turn.draft_content)) return undefined;
  return turn;
}
export function aiShouldSendKey(event: { key: string; shiftKey?: boolean; ctrlKey?: boolean; altKey?: boolean; metaKey?: boolean; repeat?: boolean; isComposing?: boolean; keyCode?: number }, composing = false): boolean {
  return event.key === 'Enter' && !event.shiftKey && !event.ctrlKey && !event.altKey && !event.metaKey && !event.repeat && !event.isComposing && event.keyCode !== 229 && !composing;
}
export function aiNearBottom(scrollTop: number, clientHeight: number, scrollHeight: number): boolean {
  return scrollHeight - scrollTop - clientHeight <= 80;
}
