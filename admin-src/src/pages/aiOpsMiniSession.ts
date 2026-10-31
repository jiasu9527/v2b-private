import { aiCanReadSession, type AISession } from './aiOpsModel';

export type AIMiniSessionStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>;

const validID = (value: unknown): value is number => typeof value === 'number' && Number.isSafeInteger(value) && value > 0;
const storageKey = (actorID: number) => `forest.ai-ops-mini.last-session.v1.${actorID}`;
const resolveStorage = (storage?: AIMiniSessionStorage | null) => storage === undefined ? typeof window === 'undefined' ? null : window.localStorage : storage;

// This preference contains only a session ID, never credentials or conversation data.
export function aiReadMiniSession(actorID: number, storage?: AIMiniSessionStorage | null): number {
  if (!validID(actorID)) return 0;
  try {
    const value = resolveStorage(storage)?.getItem(storageKey(actorID));
    if (typeof value !== 'string' || !/^[1-9]\d*$/.test(value)) return 0;
    const id = Number(value);
    return validID(id) ? id : 0;
  } catch { return 0; }
}

export function aiWriteMiniSession(actorID: number, sessionID: number, storage?: AIMiniSessionStorage | null): boolean {
  if (!validID(actorID) || !validID(sessionID)) return false;
  try {
    const target = resolveStorage(storage);
    if (!target) return false;
    target.setItem(storageKey(actorID), String(sessionID));
    return true;
  } catch { return false; }
}

export function aiClearMiniSession(actorID: number, storage?: AIMiniSessionStorage | null): boolean {
  if (!validID(actorID)) return false;
  try {
    const target = resolveStorage(storage);
    if (!target) return false;
    target.removeItem(storageKey(actorID));
    return true;
  } catch { return false; }
}

export function aiPickMiniSession(sessions: readonly AISession[], actorID: number, isAdmin: boolean, savedID = 0): AISession | null {
  if (!validID(actorID)) return null;
  const readable = sessions.filter((session) => session && validID(session.id) && validID(session.created_by) && aiCanReadSession(session, actorID, isAdmin));
  const saved = validID(savedID) ? readable.find((session) => session.id === savedID) : undefined;
  if (saved) return saved;
  const updated = (session: AISession) => Number.isFinite(session.updated_at) && session.updated_at > 0 ? session.updated_at : 0;
  // Administrators may explicitly reopen others' sessions, but never land in one by default.
  return readable.reduce<AISession | null>((latest, session) => {
    if (session.created_by !== actorID) return latest;
    if (!latest || updated(session) > updated(latest) || updated(session) === updated(latest) && session.id > latest.id) return session;
    return latest;
  }, null);
}
