export type AppleIDCredential = { credential: string };

// Never include input fragments in validation errors: imported text contains credentials.
export function parseAppleIDImport(input: string): AppleIDCredential[] {
  const lines = input.split(/\r?\n/).filter((line) => line.trim() !== '');
  if (lines.length === 0) throw new Error('请至少输入一条账号资料。');
  if (lines.length > 500) throw new Error('单次最多导入 500 条账号资料，请分批导入。');
  const seen = new Set<string>();
  return lines.map((line, index) => {
    const fingerprint = line.trim().toLowerCase();
    if (!fingerprint) throw new Error(`第 ${index + 1} 条账号资料为空。`);
    if (seen.has(fingerprint)) throw new Error(`第 ${index + 1} 条账号资料重复，请移除后再导入。`);
    seen.add(fingerprint);
    // Keep the full line, including spaces, separators and security answers.
    return { credential: line };
  });
}

export function appleIDPriceToCents(value: string | number): number {
  const text = String(value ?? '');
  if (!/^\d+(\.\d{1,2})?$/.test(text)) throw new Error('价格必须为正数，最多保留两位小数。');
  const [whole, fraction = ''] = text.split('.');
  const cents = Number(whole) * 100 + Number(fraction.padEnd(2, '0'));
  if (!Number.isSafeInteger(cents) || cents <= 0) throw new Error('请输入大于 0 的有效价格。');
  return cents;
}
