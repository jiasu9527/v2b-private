import React from 'react';

// Deliberately small Markdown subset. Every text fragment is rendered through
// React escaping: no raw HTML, embedded media, scriptable links or HTML parser.
function inline(text: string): React.ReactNode[] {
  const parts = text.split(/(`[^`\n]+`|\*\*[^*\n]+\*\*)/g);
  return parts.map((part, index) => part.startsWith('`') && part.endsWith('`') ? <code key={index}>{part.slice(1, -1)}</code> : part.startsWith('**') && part.endsWith('**') ? <strong key={index}>{part.slice(2, -2)}</strong> : part);
}
function cells(line: string): string[] { return line.trim().replace(/^\|/, '').replace(/\|$/, '').split('|').map((cell) => cell.trim()); }
function separator(line: string): boolean { return line.includes('|') && cells(line).every((cell) => /^:?-{3,}:?$/.test(cell)); }

export default function AIOpsMarkdown({ content }: { content: string }) {
  const lines = content.replace(/\r\n/g, '\n').split('\n');
  const blocks: React.ReactNode[] = [];
  for (let index = 0; index < lines.length;) {
    const start = index;
    const line = lines[index];
    if (!line.trim()) { index++; continue; }
    if (/^\s*```/.test(line)) {
      const code: string[] = [];
      index++;
      while (index < lines.length && !/^\s*```/.test(lines[index])) code.push(lines[index++]);
      if (index < lines.length) index++;
      blocks.push(<pre key={start} className="ai-ops-markdown-code"><code>{code.join('\n')}</code></pre>);
      continue;
    }
    if (line.includes('|') && index + 1 < lines.length && separator(lines[index + 1])) {
      const headers = cells(line); const rows: string[][] = [];
      index += 2;
      while (index < lines.length && lines[index].includes('|') && lines[index].trim()) rows.push(cells(lines[index++]));
      blocks.push(<div key={start} className="ai-ops-markdown-table"><table><thead><tr>{headers.map((cell, col) => <th key={col}>{inline(cell)}</th>)}</tr></thead><tbody>{rows.map((row, r) => <tr key={r}>{headers.map((_, col) => <td key={col}>{inline(row[col] || '')}</td>)}</tr>)}</tbody></table></div>);
      continue;
    }
    const heading = /^(#{1,4})\s+(.+)$/.exec(line);
    if (heading) { blocks.push(<p key={start} className={`ai-ops-markdown-heading level-${heading[1].length}`}>{inline(heading[2])}</p>); index++; continue; }
    if (/^\s*[-*+]\s+/.test(line) || /^\s*\d+\.\s+/.test(line)) {
      const ordered = /^\s*\d+\.\s+/.test(line);
      const pattern = ordered ? /^\s*\d+\.\s+/ : /^\s*[-*+]\s+/;
      const items: React.ReactNode[] = [];
      while (index < lines.length && pattern.test(lines[index])) items.push(<li key={index}>{inline(lines[index++].replace(pattern, ''))}</li>);
      blocks.push(ordered ? <ol key={start}>{items}</ol> : <ul key={start}>{items}</ul>); continue;
    }
    if (/^\s*---+\s*$/.test(line)) { blocks.push(<hr key={start} />); index++; continue; }
    const paragraph = [line]; index++;
    while (index < lines.length && lines[index].trim() && !/^\s*(#{1,4}\s|```|[-*+]\s|\d+\.\s)/.test(lines[index]) && !(lines[index].includes('|') && index + 1 < lines.length && separator(lines[index + 1]))) paragraph.push(lines[index++]);
    blocks.push(<p key={start}>{paragraph.map((text, row) => <React.Fragment key={row}>{row > 0 && <br />}{inline(text)}</React.Fragment>)}</p>);
  }
  return <div className="ai-ops-markdown">{blocks}</div>;
}
