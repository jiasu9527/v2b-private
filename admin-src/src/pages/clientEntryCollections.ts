export type EntryCollectionMember = { kind: 'policy' | 'split_group'; id: number };
export type EntryCollection = {
  id: number;
  name: string;
  entry_host: string;
  version: number;
  items: EntryCollectionMember[];
};
export type EntryCollectionOption = {
  value: string;
  label: string;
  item: EntryCollectionMember;
  entry_host: string;
  collection_id?: number;
};

export function entryCollectionMemberKey(item: EntryCollectionMember) {
  return `${item.kind}:${Number(item.id)}`;
}

export function entryCollectionMember(row: any): EntryCollectionMember {
  return row?.__row_kind === 'split_group'
    ? { kind: 'split_group', id: Number(row.__split_group.id) }
    : { kind: 'policy', id: Number(row.id) };
}

export function canJoinEntryCollection(row: any) {
  return row?.__row_kind === 'split_group' || !row?.action || row.action === 'override';
}

export function availableEntryCollectionOptions(options: EntryCollectionOption[], collectionID?: number) {
  return options.filter((option) => !option.collection_id || option.collection_id === collectionID);
}

export function attachEntryCollections(rows: any[], collections: EntryCollection[]) {
  const owners = new Map<string, EntryCollection>();
  const availableRows = new Set(rows.map((row) => entryCollectionMemberKey(entryCollectionMember(row))));
  collections.forEach((collection) => collection.items.forEach((item) => {
    const key = entryCollectionMemberKey(item);
    if (!availableRows.has(key)) throw new Error('入口合集成员与规则列表不同步，请刷新后重试');
    if (owners.has(key)) throw new Error('入口合集成员重复，请刷新后重试');
    owners.set(key, collection);
  }));
  return rows.map((row) => ({ ...row, __entry_collection: owners.get(entryCollectionMemberKey(entryCollectionMember(row))) }));
}

export function parseEntryCollections(value: unknown): EntryCollection[] {
  if (!Array.isArray(value)) throw new Error('入口合集数据格式异常，请刷新重试');
  const ids = new Set<number>();
  return value.map((raw) => {
    const id = Number(raw?.id);
    const version = Number(raw?.version);
    if (!Number.isSafeInteger(id) || id <= 0 || ids.has(id) || !Number.isSafeInteger(version) || version <= 0 || !String(raw?.name || '').trim() || !String(raw?.entry_host || '').trim() || !Array.isArray(raw?.items)) {
      throw new Error('入口合集数据不完整，请刷新重试');
    }
    ids.add(id);
    const items = raw.items.map((item: any): EntryCollectionMember => {
      const memberID = Number(item?.id);
      if (!['policy', 'split_group'].includes(item?.kind) || !Number.isSafeInteger(memberID) || memberID <= 0) throw new Error('入口合集成员无效，请刷新重试');
      return { kind: item.kind, id: memberID };
    });
    return { id, version, name: String(raw.name), entry_host: String(raw.entry_host), items };
  });
}
