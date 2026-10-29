export const AI_DEFAULT_MODELS = ['gpt-6.1-sol', 'claude-fable-5-1', 'deepseek-v4.1-flash', 'glm-5.3'];
export type AIAccessMode = 'standard' | 'full';
export const AI_DEFAULT_ACCESS_MODE: AIAccessMode = 'full';
export type AIReasoningEffort = 'auto' | 'none' | 'minimal' | 'low' | 'medium' | 'high' | 'xhigh';
export type AIReasoningChoice = 'default' | AIReasoningEffort;
export const AI_REASONING_LABELS: Record<AIReasoningEffort, string> = { auto: '中转默认', none: '不启用推理', minimal: '极低', low: '低', medium: '中', high: '高', xhigh: '极高' };
export const AI_REASONING_OPTIONS = Object.entries(AI_REASONING_LABELS).map(([value, label]) => ({ value: value as AIReasoningEffort, label }));
export type AIFallbackRelay = { id: string; name: string; base_url: string; models: string[]; enabled: boolean; has_api_key: boolean };
export type AIFallbackRelayForm = { id?: string; name: string; base_url: string; models: string[]; enabled: boolean; api_key: string; clear_api_key: boolean };
export type AIConfig = { enabled: boolean; base_url?: string; models: string[]; has_api_key: boolean; reasoning_efforts?: Record<string, string>; failover_enabled?: boolean; failover_timeout_seconds?: number; fallback_relays?: AIFallbackRelay[] };
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
export type AISession = { id: number; title: string; model: string; created_by: number; status: string; created_at: number; updated_at: number; access_mode?: AIAccessMode; access_version?: number; full_access_by?: number; reasoning_effort?: AIReasoningChoice };
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
export function aiMergeSessionAccess(previous: AISession | null | undefined, incoming: AISession): AISession {
  if (!previous || previous.id !== incoming.id || (previous.access_version || 0) <= (incoming.access_version || 0)) return incoming;
  return { ...incoming, access_mode: previous.access_mode, access_version: previous.access_version, full_access_by: previous.full_access_by };
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
export function aiApplyDraft(detail: AISessionDetail | null, draft: AIDraft): AISessionDetail | null {
  if (!detail || draft.session_id !== detail.session.id || typeof draft.content !== 'string' || !Number.isInteger(draft.round) || draft.round < 0) return detail;
  const turn = detail.turns.find((item) => item.id === draft.turn_id);
  if (!turn || turn.status !== 'running') return detail;
  const versioned = Number.isSafeInteger(draft.version) && draft.version! >= 0;
  if (versioned ? draft.version! < (turn.draft_version || 0) : draft.round < (turn.draft_round || 0)) return detail;
  if (draft.status && draft.status !== 'running') return { ...detail, turns: detail.turns.map((item) => item.id === turn.id ? { ...item, status: draft.status!, draft_content: '', draft_version: draft.version ?? item.draft_version } : item) };
  if (!versioned && draft.round === (turn.draft_round || 0) && draft.content.length < (turn.draft_content || '').length) return detail;
  return { ...detail, turns: detail.turns.map((item) => item.id === turn.id ? { ...item, draft_round: draft.round, draft_content: draft.content, draft_version: draft.version ?? item.draft_version } : item) };
}
export function aiMergeSnapshot(previous: AISessionDetail | null, incoming: AISessionDetail): AISessionDetail {
  if (!previous || previous.session.id !== incoming.session.id) return incoming;
  let next = { ...incoming, session: aiMergeSessionAccess(previous.session, incoming.session) };
  for (const turn of previous.turns) {
    const incomingTurn = incoming.turns.find((item) => item.id === turn.id);
    if (!incomingTurn || turn.status !== 'running' || incomingTurn.status !== 'running') continue;
    if (turn.draft_version !== undefined && turn.draft_version <= (incomingTurn.draft_version || 0)) continue;
    if (turn.draft_content || turn.draft_version !== undefined) next = aiApplyDraft(next, { session_id: incoming.session.id, turn_id: turn.id, round: turn.draft_round || 0, content: turn.draft_content || '', version: turn.draft_version }) || next;
  }
  return next;
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
