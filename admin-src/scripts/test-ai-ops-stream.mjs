import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {createRequire} from 'node:module';
import {pathToFileURL} from 'node:url';
import {test} from 'node:test';
import ts from 'typescript';
import {renderToStaticMarkup} from 'react-dom/server';
import React from 'react';
const require=createRequire(import.meta.url);
const streamSource=await readFile(new URL('../src/pages/aiOpsStream.ts',import.meta.url),'utf8');
const compiled=ts.transpileModule(streamSource.replace(/^import .*from '\.\.\/lib\/api';\n/,''),{compilerOptions:{module:ts.ModuleKind.ESNext,target:ts.ScriptTarget.ES2020}}).outputText;
const {aiStreamParser}=await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`);
const markdownSource=await readFile(new URL('../src/pages/AIOpsMarkdown.tsx',import.meta.url),'utf8');
let markdownCompiled=ts.transpileModule(markdownSource,{compilerOptions:{module:ts.ModuleKind.ESNext,target:ts.ScriptTarget.ES2020,jsx:ts.JsxEmit.React}}).outputText;
markdownCompiled=markdownCompiled.replace("from 'react'",`from '${pathToFileURL(require.resolve('react')).href}'`);
const {default:AIOpsMarkdown}=await import(`data:text/javascript;base64,${Buffer.from(markdownCompiled).toString('base64')}`);

test('SSE handles chunk boundaries, CRLF, comments, multiple frames and cumulative Chinese content',()=>{
 const events=[];const feed=aiStreamParser(e=>events.push(e));
 feed(': heartbeat\r\n\r\nevent: dra');feed('ft\r\ndata: {"content":"你');feed('好"}\r\n\r\nevent: end\ndata: {"session_id":1}\n\n');
 assert.deepEqual(events,[{event:'draft',data:{content:'你好'}},{event:'end',data:{session_id:1}}]);
});
test('SSE ignores unknown events and rejects malformed or oversized frames',()=>{
 const events=[];const feed=aiStreamParser(e=>events.push(e));
 feed('event: unknown\ndata: invalid\n\n');assert.equal(events.length,0);
 assert.throws(()=>feed('event: draft\ndata: invalid\n\n'),/格式无效/);
 assert.throws(()=>aiStreamParser(()=>{})('a'.repeat(16*1024*1024+1)),/超出限制/);
});
test('stream credentials are only an Authorization header and readers abort on disposal',()=>{
 assert.match(streamSource,/Authorization: getAuth\(\)/);
 assert.match(streamSource,/credentials: 'same-origin', cache: 'no-store', signal/);
 assert.doesNotMatch(streamSource,/auth_data|EventSource|localStorage|sessionStorage/);
 assert.match(streamSource,/reader.cancel\(\)/);
});
test('Markdown renders bold, tables, code and lists while escaping HTML and not loading external media',()=>{
 const content='**正常**\n\n| 入口 | 状态 |\n|---|---|\n| `192.0.2.1` | 正常 |\n\n- 第一项\n\n```sh\necho <test>\n```\n\n<script>alert(1)</script> ![x](https://evil.test/pixel) [x](javascript:alert(1))';
 const html=renderToStaticMarkup(React.createElement(AIOpsMarkdown,{content}));
 assert.match(html,/<strong>正常<\/strong>/);assert.match(html,/<table>/);assert.match(html,/<code>192.0.2.1<\/code>/);assert.match(html,/<li>第一项<\/li>/);
 assert.match(html,/&lt;script&gt;/);assert.doesNotMatch(html,/<script|<img|<a |onclick|<iframe/);
 assert.doesNotMatch(markdownSource,/dangerouslySetInnerHTML|DOMParser|innerHTML/);
});
test('partially streamed markdown remains displayable without executing unclosed code or HTML',()=>{
 for (const content of ['**正在','| 域名 | 状态 |\n|---|---|\n|例子','```sh\n<iframe src=x>']) {
  const html=renderToStaticMarkup(React.createElement(AIOpsMarkdown,{content}));assert.ok(html.length>0);assert.doesNotMatch(html,/<iframe/);
 }
});
