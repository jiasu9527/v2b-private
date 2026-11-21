// Parse before the ordinary AI chat transport. This module must never send or
// log the source text. Only the extracted fields may go to the isolated
// /web-login/run endpoint. Ambiguous input must not fall back to ordinary chat.
export type AIWebLoginText =
  | { kind: 'chat' }
  | { kind: 'invalid'; message: string }
  | { kind: 'login'; url: string; target_url?: string; username: string; password: string; task: string };

type Field = 'url' | 'target_url' | 'username' | 'password' | 'task';
type Fields = Partial<Record<Field, string>>;
type Mark = { kind: Field; start: number; prefix: string; valueStart: number };

const loginHead = /^\s*\/?\s*(?:网页登录|登录网页|浏览器登录|browser[ \t]+login|web[ \t]+login)(?=[\s:：,，。]|https?:\/\/|$)/i;
const loginIntent = /登录|登陆|登入|登錄|\b(?:sign|log)[ -]+in\b/i;
const urlHint = /https?:\/\//i;
const urlWithUserInfo = /https?:\/\/[^/?#\s，；。！？、,;<>"']+@/i;
const unmistakableSSH = /^\s*(?:(?:麻烦|请|帮我|给我|用|把|需要|我要|我想|先)[ \t]*)*(?:(?:通过|登录|登陆|连接|连上|登记|导入|添加|录入|注册|配置|部署|检查|查看)[ \t]*)*\b(?:ssh|sftp|scp)\b/i;
const labelKinds: Record<Field, readonly string[]> = {
  url: ['登录页面地址', '网站登录地址', '登录地址', '登陆地址', '网站地址', '登录网址', '站点地址', '网址', 'login_url', 'url'],
  target_url: ['目标页面地址', '目标页面', '目标网址', '目标地址', '页面地址', 'target_url', 'target url'],
  username: ['登录用户名', '登录账号', '登陆账号', '用户名', '账户名', '账号', '帐号', '账户', 'username', 'user name', 'account', 'user'],
  password: ['网页登录密码', '登录密码', '登陆密码', '登录口令', '密码', '口令', 'password', 'passwd', 'pwd'],
  task: ['查询任务', '查询内容', '要做什么', '想查看', '任务', 'task', 'question'],
};
const fieldByLabel = new Map<string, Field>();
for (const [kind, labels] of Object.entries(labelKinds)) {
  for (const label of labels) fieldByLabel.set(label, kind as Field);
}
const labelPattern = [...fieldByLabel.keys()].sort((a, b) => b.length - a.length).join('|');
const quoteEnd: Readonly<Record<string, string>> = { '"': '"', "'": "'", '`': '`', '“': '”', '‘': '’', '「': '」' };
const taskAction = /^(?:(?:然后|接着|你|再|请|帮我|给我)[ \t]*)*(?:查一下|查下|查查|查找|查询|找出|查看|看看|看下|看一眼|看一下|检查|核对|告诉我|读取|读一下|搜索|列出|判断|分析|统计|总结|确认|验证|检测|read\b|check\b|find\b|show\b|list\b|inspect\b|summari[sz]e\b)/i;
const invalid = (message: string): AIWebLoginText => ({ kind: 'invalid', message });
const invalidFields = '未能明确识别网页登录资料。请在同一条消息里写清网址、账号、密码和要查询的内容；未发送到普通聊天。';
const invalidTarget = '一次只支持登录一个网站。如果需要读取指定页面，请把第二个链接明确写为「目标页面」；未发送到普通聊天。';
const missingField = (field: '网址' | '账号' | '密码' | '账号和密码' | '要查询的内容') =>
  invalid(`没识别到${field}。请在这条消息里补上再发送；未发送到普通聊天。`);

function quotedRanges(line: string): Array<[number, number]> {
  const ranges: Array<[number, number]> = [];
  for (let start = 0; start < line.length; start++) {
    const endQuote = quoteEnd[line[start]];
    if (!endQuote || (start > 0 && !/[ \t,，;；:=：]/.test(line[start - 1]))) continue;
    const end = line.indexOf(endQuote, start + 1);
    ranges.push([start, end === -1 ? line.length : end + 1]);
    if (end === -1) break;
    start = end;
  }
  return ranges;
}

function marksIn(line: string): Mark[] {
  const ranges = quotedRanges(line);
  const pattern = new RegExp(`(^|[ \\t,，;；])(${labelPattern})`, 'gi');
  const marks: Mark[] = [];
  for (const match of line.matchAll(pattern)) {
    const prefix = match[1];
    const label = match[2];
    const start = match.index! + prefix.length;
    const after = start + label.length;
    if (ranges.some(([left, right]) => start >= left && start < right)) continue;
    if (/^[a-z]/i.test(label) && after < line.length && !/[ \t:：=]/.test(line[after])) continue;
    const separator = line.slice(after).match(/^[ \t]*(?:[:：=][ \t]*|是[ \t]*)?/);
    marks.push({ kind: fieldByLabel.get(label.toLowerCase())!, start, prefix, valueStart: after + (separator?.[0].length || 0) });
  }
  return marks;
}

function splitLooseTask(raw: string): [string, string?] {
  // Punctuation inside a value is preserved unless it clearly introduces an
  // instruction, e.g. "密码 demo，查下证书". Quoted values can include spaces,
  // punctuation and words that resemble an instruction.
  const ranges = quotedRanges(raw);
  for (const match of raw.matchAll(/[ \t]*[,，;；。][ \t]*|[ \t]+/g)) {
    const start = match.index!;
    const end = start + match[0].length;
    const before = raw.slice(0, start).trim();
    if (!before || ranges.some(([left, right]) => end > left && start < right)) continue;
    const after = raw.slice(end);
    if (!taskAction.test(after)) continue;
    if (!/[ ,，;；。]/.test(match[0][0]) || /[,，;；。]/.test(match[0]) || !/[ \t]/.test(before) || !!quoteEnd[before[0]]) {
      return [raw.slice(0, start), after];
    }
  }
  return [raw];
}

function unquote(input: string): { value: string; quoted: boolean } | undefined {
  const raw = input.trim();
  if (!raw) return;
  const endQuote = quoteEnd[raw[0]];
  if (!endQuote) return { value: raw, quoted: false };
  if (raw.length < 2 || !raw.endsWith(endQuote) || raw.indexOf(endQuote, 1) !== raw.length - 1) return;
  const value = raw.slice(1, -1);
  return value.trim() ? { value, quoted: true } : undefined;
}

function credential(input: string, maxBytes: number): string | undefined {
  const out = unquote(input);
  if (!out || (!out.quoted && /[ \t]/.test(out.value)) || /[\p{Cc}\p{Cf}]/u.test(out.value)) return;
  return new TextEncoder().encode(out.value).length <= maxBytes ? out.value : undefined;
}

function subjectURL(input: string): string | undefined {
  const trimmed = input.trim();
  const markdown = trimmed.match(/^\[[^\]\n]*\]\((https?:\/\/[^\s]+)\)$/i);
  const value = markdown?.[1] || unquote(trimmed)?.value;
  if (value && /^https?:\/\/\S+$/i.test(value)) return value;
}

function findURLs(input: string): Array<{ start: number; end: number; url: string }> {
  const links: Array<{ start: number; end: number; url: string }> = [];
  const pattern = /https?:\/\/[^\s，；。！？、,;<>"'`“”‘’「」]+/gi;
  for (const match of input.matchAll(pattern)) {
    const start = match.index!;
    const url = match[0];
    // A Markdown link has its own closing delimiter; parentheses elsewhere in
    // URL paths are left intact rather than quietly changing the destination.
    const markdown = input.slice(0, start).match(/\[[^\]\n]*\]\($/);
    links.push({
      start: markdown ? start - markdown[0].length : start,
      end: start + url.length,
      url: markdown && url.endsWith(')') ? url.slice(0, -1) : url,
    });
  }
  return links;
}

function tidyTask(input: string): string {
  const tail = input.trim().replace(/^[,，。:：;； \t]+|[,，:：;； \t]+$/g, '');
  return /^(?:(?:请|帮我|麻烦|请帮我|给我|去|到)[ \t]*)*(?:网页登录|登录网页|浏览器登录|登录|登陆|登入|登錄|浏览器|网站|网页|log[ -]in|sign[ -]in)$/i.test(tail) ? '' : tail;
}

function positional(lines: string[]): AIWebLoginText | undefined {
  const parts = lines.map((line) => line.trim()).filter(Boolean);
  if (parts.length < 3) return;
  const first = findURLs(parts[0]);
  if (first.length !== 1 || parts[0].slice(first[0].end).trim()) return;
  if (tidyTask(parts[0].slice(0, first[0].start)) && !loginIntent.test(parts[0].slice(0, first[0].start))) return;
  const hasLabels = parts.slice(1, 3).some((line) => marksIn(line).length !== 0);
  if (hasLabels || parts.slice(1, 3).some((line) => urlHint.test(line))) return;
  const username = credential(parts[1], 320), password = credential(parts[2], 1024);
  if (!username || !password) return first[0].start === 0 ? undefined : invalid(invalidFields);
  const task = (parts[3] || '').replace(/^(?:查询任务|任务|task)[ \t]*[:：=][ \t]*/i, '');
  if (parts.length === 3 || !task) return missingField('要查询的内容');
  if (parts.length !== 4 || (!taskAction.test(task) && !/^(?:查询任务|任务|task)[ \t]*[:：=]/i.test(parts[3]))) return invalid(invalidFields);
  if (urlHint.test(task)) return invalid(invalidTarget);
  return { kind: 'login', url: first[0].url, username, password, task };
}

export function parseAIWebLoginText(content: string): AIWebLoginText {
  const text = content.replace(/\r\n?/g, '\n').trim();
  if (urlWithUserInfo.test(text)) return invalid('请把网址和账号密码分开写，不要把凭据放进链接；未发送到普通聊天。');
  const head = text.match(loginHead);
  const explicit = !!head;
  // Keep unambiguous SSH workflows on their existing route, but never let an
  // SSH keyword in a page-reading task opt out of secret protection.
  if (!explicit && unmistakableSSH.test(text)) return { kind: 'chat' };
  if (!explicit && !urlHint.test(text)) return { kind: 'chat' };
  if (text.length > 12_000) {
    const fields = /密码|口令|\b(?:password|passwd|pwd)\b/i.test(text);
    if (explicit || fields || positional(text.split('\n'))) return invalid('网页登录资料过长，请缩短任务描述；未发送到普通聊天。');
    return { kind: 'chat' };
  }

  const lines = (head ? text.slice(head[0].length).replace(/^[ \t:：,，。]+/, '') : text).split('\n');
  const direct = positional(lines);
  if (direct) return direct;
  const fields: Fields = {};
  const residual: string[] = [];
  let duplicate = false;
  for (const line of lines) {
    const marks = marksIn(line);
    let before = line.slice(0, marks[0]?.start ?? line.length);
    if (marks[0]?.prefix && marks[0].prefix !== ' ' && marks[0].prefix !== '\t') before = before.slice(0, -1);
    if (before.trim()) residual.push(before);
    for (let i = 0; i < marks.length; i++) {
      const mark = marks[i];
      const next = marks[i + 1];
      const end = next ? next.start - next.prefix.length : line.length;
      let value = line.slice(mark.valueStart, Math.max(mark.valueStart, end));
      if (next?.prefix === ' ' || next?.prefix === '\t') value = value.replace(/[,，;；][ \t]*$/, '');
      if (mark.kind !== 'task') {
        const [field, looseTask] = splitLooseTask(value);
        value = field;
        if (looseTask) residual.push(looseTask);
      }
      if (Object.hasOwnProperty.call(fields, mark.kind)) duplicate = true;
      fields[mark.kind] = value;
    }
  }

  const hasUser = fields.username !== undefined;
  const hasPass = fields.password !== undefined;
  if (!explicit && !(hasUser && hasPass || loginIntent.test(text) && (hasUser || hasPass))) {
    return { kind: 'chat' };
  }
  if (duplicate) return invalid(invalidFields);
  if (!hasUser || !hasPass) return missingField(hasUser ? '密码' : hasPass ? '账号' : '账号和密码');
  const username = credential(fields.username!, 320);
  const password = credential(fields.password!, 1024);
  if (!username || !password) return invalid(invalidFields);

  let url = fields.url === undefined ? undefined : subjectURL(fields.url);
  const target_url = fields.target_url === undefined ? undefined : subjectURL(fields.target_url);
  if ((fields.url !== undefined && !url) || (fields.target_url !== undefined && !target_url)) return invalid(invalidTarget);
  const fragments: string[] = [];
  for (const part of residual) {
    const links = findURLs(part);
    if (links.length > 1 || (links.length && url)) return invalid(invalidTarget);
    if (links.length) {
      const [link] = links;
      url = link.url;
      fragments.push(tidyTask(part.slice(0, link.start)), tidyTask(part.slice(link.end)));
    } else {
      fragments.push(tidyTask(part));
    }
  }
  if (!url) return missingField('网址');
  const task = fields.task?.trim() || fragments.filter(Boolean).join('\n');
  if (!task) return missingField('要查询的内容');
  if (new TextEncoder().encode(task).length > 4000) return invalid(invalidFields);
  if (urlHint.test(task)) return invalid(invalidTarget);
  return target_url ? { kind: 'login', url, username, password, task, target_url } : { kind: 'login', url, username, password, task };
}
