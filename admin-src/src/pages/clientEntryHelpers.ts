export type ClientEntryServerOption = { value: string; label: string };

export function memberKey(member: any) {
  const serverType = String(member?.server_type || member?.serverType || '').trim();
  const serverID = String(member?.server_id ?? member?.serverID ?? '').trim();
  return serverType && serverID ? `${serverType}:${serverID}` : '';
}

export function splitMemberKey(value: any) {
  const raw = String(value || '').trim();
  const index = raw.indexOf(':');
  if (index <= 0) return null;
  const serverType = raw.slice(0, index).trim();
  const serverID = Number(raw.slice(index + 1));
  if (!serverType || !Number.isFinite(serverID) || serverID <= 0) return null;
  return { server_type: serverType, server_id: serverID };
}

function isVisibleNode(row: any) {
  const raw = row?.show;
  if (raw === undefined || raw === null || raw === '') return true;
  if (typeof raw === 'boolean') return raw;
  const text = String(raw).trim().toLowerCase();
  return text !== '0' && text !== 'false' && text !== 'hidden' && text !== 'hide';
}

function nodeLabel(row: any) {
  const type = String(row?.type || '').trim();
  const id = row?.id ?? row?.server_id ?? '';
  const name = String(row?.name || row?.remarks || '').trim() || `${type || 'node'} #${id}`;
  const host = String(row?.host || '').trim();
  const port = String(row?.port || row?.server_port || '').trim();
  const endpoint = host ? `${host}${port ? `:${port}` : ''}` : '';
  return [name, type && `/${type}`, id && `#${id}`, endpoint && `(${endpoint})`].filter(Boolean).join(' ');
}

export function buildVisibleServerOptions(nodes: any[]) {
  return nodes.filter(isVisibleNode).map((row) => {
    const value = memberKey({ server_type: row?.type, server_id: row?.id });
    if (!value) return null;
    return { value, label: nodeLabel(row) };
  }).filter(Boolean) as ClientEntryServerOption[];
}

export function visibleMemberNames(row: any, visibleServerOptionMap: Record<string, string>) {
  return (Array.isArray(row?.members) ? row.members : [])
    .map((member: any) => {
      const key = memberKey(member);
      return key && visibleServerOptionMap[key] ? visibleServerOptionMap[key] : '';
    })
    .filter(Boolean);
}

export function normalizeEntryForVisibleMembers(row: any = {}, visibleMemberKeys?: Set<string>) {
  const members = (Array.isArray(row.members) ? row.members : [])
    .map(memberKey)
    .filter((key) => key && (!visibleMemberKeys || visibleMemberKeys.has(key)));
  return { ...row, members };
}
