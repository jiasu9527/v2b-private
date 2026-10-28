type UACondition = { field: 'ua'; operator: string; values?: string[] };

function decodeConditions(value: unknown): any[] | undefined {
  let parsed = value;
  for (let attempt = 0; attempt < 3 && typeof parsed === 'string'; attempt += 1) {
    try { parsed = JSON.parse(parsed); } catch { return undefined; }
  }
  // Conversion must never silently discard a malformed or unsupported condition.
  if (!Array.isArray(parsed) || parsed.some((item) => !item || typeof item !== 'object' || Array.isArray(item))) return undefined;
  return parsed;
}

function normalizeUA(condition: any): UACondition | undefined {
  if (String(condition?.field || '').trim().toLowerCase() !== 'ua') return undefined;
  let operator = String(condition.operator || '').trim().toLowerCase();
  if (operator === 'is_empty') operator = 'empty';
  if (operator === 'is_not_empty') operator = 'not_empty';
  if (operator === 'empty' || operator === 'not_empty') return { field: 'ua', operator };
  if (!['contains_any', 'excludes_any'].includes(operator) || !Array.isArray(condition.values) || !condition.values.length) return undefined;
  if (condition.values.some((value: unknown) => typeof value !== 'string' || !value.trim())) return undefined;
  return { field: 'ua', operator, values: condition.values.map((value: string) => value.trim()) };
}

export function splitUserIDRange(value: unknown): { min: number; max: number } | undefined {
  const conditions = decodeConditions(value);
  if (!conditions?.length) return undefined;
  let range: { min: number; max: number } | undefined;
  for (const condition of conditions) {
    const field = String(condition.field || '').trim().toLowerCase();
    if (field === 'ua' && normalizeUA(condition)) continue;
    if (field !== 'user_id' || String(condition.operator || '').trim().toLowerCase() !== 'between' || range) return undefined;
    const { min, max } = condition;
    if (![min, max].every((number) => (typeof number === 'number' || (typeof number === 'string' && /^\d+$/.test(number.trim()))) && Number.isSafeInteger(Number(number)) && Number(number) > 0)) return undefined;
    if (Number(min) > Number(max)) return undefined;
    range = { min: Number(min), max: Number(max) };
  }
  return range;
}

// A converted policy keeps its ID range as snapshot provenance. Only UA remains
// a per-request guard; the fixed assignment, not the old range, selects users.
export function splitUAConditions(value: unknown): UACondition[] {
  return (decodeConditions(value) || []).map(normalizeUA).filter((condition): condition is UACondition => Boolean(condition));
}
