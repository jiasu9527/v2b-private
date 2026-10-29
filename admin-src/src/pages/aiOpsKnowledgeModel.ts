export const AI_KNOWLEDGE_CATEGORIES = [
  { value: 'architecture', label: '架构' }, { value: 'server', label: '服务器' },
  { value: 'network', label: '网络与入口' }, { value: 'deployment', label: '部署' },
  { value: 'troubleshooting', label: '故障排查' }, { value: 'other', label: '其他' },
] as const;
export type AIKnowledgeCategory = typeof AI_KNOWLEDGE_CATEGORIES[number]['value'];
export type AIKnowledgeStatus = 'active' | 'draft' | 'disabled';
export const AI_KNOWLEDGE_STATUSES: { value: AIKnowledgeStatus; label: string }[] = [
  { value: 'active', label: '已发布' }, { value: 'draft', label: '草稿' }, { value: 'disabled', label: '已停用' },
];
export type AIKnowledgeEntry = {
  id: number; title: string; summary: string; content?: string; category: AIKnowledgeCategory;
  status: AIKnowledgeStatus; pinned: boolean; version: number; source_kind: 'manual' | 'ai';
  source_note: string; source_session_id: number; source_turn_id: number;
  created_by: number; updated_by: number; created_at: number; updated_at: number;
};
export type AIKnowledgeValues = { title: string; summary: string; content: string; category: AIKnowledgeCategory; status: AIKnowledgeStatus; pinned: boolean };
export type AIKnowledgeList = { data: AIKnowledgeEntry[]; total: number; current: number; page_size: number };
export function aiKnowledgeCanRead(entry: AIKnowledgeEntry, actorID: number, isAdmin: boolean): boolean {
  return isAdmin || entry.status === 'active' || entry.status === 'draft' && actorID > 0 && entry.created_by === actorID;
}
export function aiKnowledgeDetail(value: unknown): AIKnowledgeEntry {
  const entry = value as AIKnowledgeEntry;
  if (!entry || !Number.isSafeInteger(entry.id) || entry.id <= 0 || !Number.isSafeInteger(entry.version) || entry.version <= 0 || typeof entry.title !== 'string' || typeof entry.content !== 'string' || !AI_KNOWLEDGE_STATUSES.some((item) => item.value === entry.status)) throw new Error('知识详情格式不正确，请刷新后重试');
  return entry;
}
export function aiKnowledgeList(value: unknown): AIKnowledgeList {
  const list = value as AIKnowledgeList;
  if (!list || !Array.isArray(list.data) || !Number.isSafeInteger(list.total) || list.total < 0 || !Number.isSafeInteger(list.current) || list.current < 1 || !Number.isSafeInteger(list.page_size) || list.page_size < 1 || list.data.some((entry) => !entry || !Number.isSafeInteger(entry.id) || entry.id <= 0 || !Number.isSafeInteger(entry.version) || entry.version < 1 || typeof entry.title !== 'string' || !AI_KNOWLEDGE_STATUSES.some((item) => item.value === entry.status))) throw new Error('知识列表格式不正确，请刷新后重试');
  return list;
}
export function aiKnowledgeValues(entry?: AIKnowledgeEntry | null): AIKnowledgeValues {
  return { title: entry?.title || '', summary: entry?.summary || '', content: entry?.content || '', category: entry?.category || 'other', status: entry?.status || 'draft', pinned: !!entry?.pinned };
}
export function aiKnowledgeSavePayload(values: AIKnowledgeValues, existing: AIKnowledgeEntry | null, isAdmin: boolean) {
  if (existing && !isAdmin) throw new Error('员工只能新建草稿，不能修改已有资料');
  if (existing && (!Number.isSafeInteger(existing.id) || existing.id < 1 || !Number.isSafeInteger(existing.version) || existing.version < 1)) throw new Error('资料版本缺失，请刷新后重试');
  const title = String(values.title || '').trim(), summary = String(values.summary || '').trim(), content = String(values.content || '');
  if (!title || [...title].length > 120 || [...summary].length > 500 || !content.trim() || [...content].length > 12000) throw new Error('请检查标题、摘要和正文长度');
  if (!AI_KNOWLEDGE_CATEGORIES.some((item) => item.value === values.category)) throw new Error('请选择有效的资料类别');
  if (isAdmin && !AI_KNOWLEDGE_STATUSES.some((item) => item.value === values.status)) throw new Error('请选择有效的发布状态');
  return { ...(existing ? { id: existing.id, expected_version: existing.version } : {}), title, summary, content, category: values.category, status: isAdmin ? values.status : 'draft', pinned: isAdmin && !!values.pinned };
}
