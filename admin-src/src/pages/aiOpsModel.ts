export const AI_DEFAULT_MODELS = ['gpt-6.1-sol', 'claude-fable-5-1', 'deepseek-v4.1-flash', 'glm-5.3'];
export type AIConfig = { enabled: boolean; base_url: string; models: string[]; has_api_key: boolean };
export type AISession = { id: number; title: string; model: string; created_by: number; status: string; created_at: number; updated_at: number };
export type AIMessage = { id: number; session_id: number; turn_id: number; role: string; content: string; tool_calls?: { id: string; function: { name: string; arguments: string } }[]; tool_call_id?: string; created_at: number };
export type AITurn = { id: number; status: string; error: string; created_at: number; completed_at: number };
export type AIAction = { id: number; session_id: number; actor_id: number; approved_by?: number; rejected_by?: number; tool_call_id: string; kind: string; target_id: number; target_type: string; server_label: string; payload: Record<string, unknown>; reason: string; status: string; expires_at: number; claimed_at: number; completed_at: number; output: string; error: string; created_at: number; updated_at: number };
export type AISessionDetail = { session: AISession; messages: AIMessage[]; turns: AITurn[]; actions: AIAction[] };
export type AIServer = { id: number; name: string; host: string; port: number; username: string; host_key_sha256: string; enabled: boolean; version: number; has_password: boolean; has_private_key: boolean; has_key_passphrase: boolean; created_at: number; updated_at: number };
export type AIFingerprint = { host: string; port: number; fingerprint: string; key_type: string };
export function aiActionStatus(action: AIAction, now = Date.now()): string {
  return action.status === 'pending' && action.expires_at * 1000 <= now ? 'expired' : action.status;
}
export function aiCanApprove(action: AIAction, enabled: boolean, now = Date.now()): boolean {
  return enabled && aiActionStatus(action, now) === 'pending';
}
export function aiSessionBusy(detail: AISessionDetail | null): boolean {
  return !!detail && (detail.session.status === 'running' || detail.turns.some((turn) => turn.status === 'running') || detail.actions.some((action) => action.status === 'running'));
}
export function aiLatestTurn(turns: AITurn[]): AITurn | undefined {
  return turns.reduce<AITurn | undefined>((latest, turn) => !latest || turn.id > latest.id ? turn : latest, undefined);
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
