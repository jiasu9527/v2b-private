export type AppleIDImportFormat = 'lines' | 'json';
export type AppleIDCredential = { account: string; password: string };

// Never include input fragments in validation errors: imported text contains credentials.
export function parseAppleIDImport(input: string, format: AppleIDImportFormat): AppleIDCredential[] {
  let values: unknown[];
  if (format === 'json') {
    let parsed: unknown;
    try {
      parsed = JSON.parse(input);
    } catch {
      throw new Error('JSON 格式不正确，请检查引号、逗号和括号。');
    }
    if (!Array.isArray(parsed)) throw new Error('JSON 必须为账号对象数组。');
    values = parsed;
  } else {
    const lines = input.split(/\r?\n/).filter((line) => line.trim() !== '');
    values = lines.map((line, index) => {
      const separator = line.indexOf('\t');
      if (separator < 0) throw new Error(`第 ${index + 1} 条缺少 TAB 分隔符。`);
      return { account: line.slice(0, separator), password: line.slice(separator + 1) };
    });
  }
  if (values.length === 0) throw new Error('请至少输入一个账号。');
  if (values.length > 500) throw new Error('单次最多导入 500 个账号，请分批导入。');
  const seen = new Set<string>();
  return values.map((value: any, index) => {
    if (!value || typeof value.account !== 'string' || !value.account.trim() || typeof value.password !== 'string' || value.password === '') {
      throw new Error(`第 ${index + 1} 条的账号或密码为空，或字段类型不正确。`);
    }
    const account = value.account.trim();
    const fingerprint = account.toLowerCase();
    if (seen.has(fingerprint)) throw new Error(`第 ${index + 1} 条账号重复，请移除后再导入。`);
    seen.add(fingerprint);
    return { account, password: value.password };
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
