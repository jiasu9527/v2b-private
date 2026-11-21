import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import https from 'node:https';
import http from 'node:http';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { chromium } from 'playwright';
import { runWorkflow } from './workflow.mjs';

const browserTest = process.env.FOREST_WEB_LOGIN_TEST_BROWSER === '1';
const executablePath = process.env.FOREST_WEB_LOGIN_TEST_EXECUTABLE;
const username = 'fixture-browser-user';
const password = 'fixture & site secret';

test('Chromium on local HTTPS: normal and two-step login, no unsafe submission', {skip: !browserTest, timeout: 60_000}, async (t) => {
  const temp = await mkdtemp(path.join(os.tmpdir(), 'forest-browser-fixture-'));
  t.after(() => rm(temp, { recursive: true, force: true }));
  execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1', '-subj', '/CN=localhost',
    '-keyout', path.join(temp, 'key.pem'), '-out', path.join(temp, 'cert.pem')], { stdio:'ignore' });
  const cookie = 'forest_fixture=authenticated';
  const requests = [];
  const form = (formPath, fields, action, method = 'post') =>
    '<form action="' + formPath + '" method="' + method + '">' + fields + '<button type="submit">' + action + '</button></form>';
  const userField = '<label>用户名 <input autocomplete="username" name="username"></label>';
  const passwordField = '<label>密码 <input type="password" autocomplete="current-password" name="password"></label>';
  const server = https.createServer({ key:await readFile(path.join(temp,'key.pem')), cert:await readFile(path.join(temp,'cert.pem')) }, async (req, res) => {
    if (req.url === '/favicon.ico') {res.writeHead(404).end(); return;}
    requests.push(req.method + ' ' + req.url);
    const html = (body, code = 200) => res.writeHead(code, {'Content-Type':'text/html; charset=utf-8'}).end('<!doctype html>' + body);
    const readBody = async () => {
      const chunks = [];
      for await (const chunk of req) chunks.push(chunk);
      return new URLSearchParams(Buffer.concat(chunks).toString());
    };
    switch (req.url) {
    case '/home':
      return html('<a href="/sign-in">登录</a>');
    case '/sign-in':
      return html(form('/auth', userField + passwordField, '登录'));
    case '/custom':
      return html(userField + passwordField + '<button type="button">登录</button>' +
        '<script>document.querySelector("button").onclick=async()=>{const u=document.querySelector("input[name=username]").value,p=document.querySelector("input[name=password]").value;' +
        'const result=await fetch("/auth-json",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({user:u,password:p})});if(result.ok)location.href="/landing"};</script>');
    case '/auth-json': {
      const chunks = [];
      for await (const chunk of req) chunks.push(chunk);
      const fields = JSON.parse(Buffer.concat(chunks).toString());
      if (req.method !== 'POST' || fields.user !== username || fields.password !== password) return html('invalid', 403);
      return res.writeHead(200, {'Set-Cookie':cookie+'; Secure; HttpOnly; Path=/'}).end('ok');
    }
    case '/two':
      return html(form('/identify', userField, '下一步'));
    case '/identify':
      if (req.method !== 'POST' || (await readBody()).get('username') !== username) return html('invalid', 403);
      return res.writeHead(303, {Location:'/two-password'}).end();
    case '/two-password':
      return html(form('/auth', passwordField, 'Sign in'));
    case '/auth': {
      if (req.method !== 'POST' || (await readBody()).get('password') !== password) return html('invalid', 403);
      return res.writeHead(303, {'Set-Cookie': cookie+'; Secure; HttpOnly; Path=/', Location:'/landing'}).end();
    }
    case '/landing':
      if (!req.headers.cookie?.includes(cookie)) return html('need login',403);
      return html('<title>Fixture landing</title><main>已登录测试站点</main>');
    case '/report':
      if (!req.headers.cookie?.includes(cookie)) return html('need login',403);
      return html('<title>' + username + ' report</title><main>三台站点在线<br>' + password +
        '<form>不应该读取的表单</form><span style="display:none">不读取隐藏正文</span></main>' +
        '<script>fetch("/should-not-mutate",{method:"POST",body:"unused"}).catch(()=>{})</script>');
    case '/get':
      return html(form('/must-not-submit', userField + passwordField, '登录', 'get'));
    case '/mfa':
      return html('<h1>请输入验证码</h1>' + form('/must-not-submit', userField+passwordField+
        '<input name="otp" autocomplete="one-time-code">', '登录'));
    case '/cross':
      return html(form('https://another.example.test/submit', userField + passwordField, '登录'));
    case '/replay':
      return html(form('/auth-redirect', userField + passwordField, '登录'));
    case '/auth-redirect':
      if (req.method !== 'POST' || (await readBody()).get('password') !== password) return html('invalid',403);
      return res.writeHead(307, {Location:'/must-not-replay'}).end();
    default:
      return html('unexpected',404);
    }
  });
  await new Promise((resolve) => server.listen(0,'127.0.0.1',resolve));
  t.after(() => { server.close(); server.closeAllConnections(); });
  // A synthetic HTTPS origin through a local pinned CONNECT tunnel, with
  // the exact production Chromium network flags. No public host is contacted.
  const origin = 'https://login.example.test';
  const tunnel = http.createServer((_, res) => res.writeHead(403).end());
  const sockets = new Set();
  const attemptedTunnelHosts = [];
  tunnel.on('connect', (req,socket,head) => {
    attemptedTunnelHosts.push(req.url);
    sockets.add(socket);
    socket.on('close', () => sockets.delete(socket));
    if (req.url !== 'login.example.test:443') { socket.end('HTTP/1.1 403 Forbidden\r\n\r\n'); return; }
    const upstream=net.connect({host:'127.0.0.1', port:server.address().port}, () => {
      socket.write('HTTP/1.1 200 Connection Established\r\n\r\n');
      if (head.length) upstream.write(head);
      socket.pipe(upstream).pipe(socket);
    });
    upstream.on('error', () => socket.destroy());
    socket.on('error', () => upstream.destroy());
    socket.on('close', () => upstream.destroy());
  });
  await new Promise((resolve) => tunnel.listen(0,'127.0.0.1',resolve));
  t.after(() => { for (const socket of sockets) socket.destroy(); tunnel.close(); });
  const browser = await chromium.launch({
    headless:true, chromiumSandbox:true,
    proxy:{server:'http://127.0.0.1:'+tunnel.address().port,bypass:'<-loopback>'},
    args:['--disable-quic','--force-webrtc-ip-handling-policy=disable_non_proxied_udp',
      '--disable-background-networking','--disable-domain-reliability','--disable-sync',
      '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1'],
    ...(executablePath ? {executablePath} : {}),
  });
  t.after(() => browser.close());
  const run = async (start, target) => {
    // Trust the synthetic self-signed certificate only inside this test.
    const context = await browser.newContext({ignoreHTTPSErrors:true, serviceWorkers:'block'});
    try { return await runWorkflow(context,{ origin, url:origin+start, username, password, target:target&&origin+target }); }
    finally { await context.close(); }
  };
  const first = await run('/home','/report');
  assert.deepEqual(first,{status:'ok',title:'〔账号信息已隐藏〕 report',text:'三台站点在线\n〔账号信息已隐藏〕'});
  const second = await run('/two');
  assert.deepEqual(second,{status:'ok',title:'Fixture landing',text:'已登录测试站点'});
  assert.deepEqual(await run('/custom'),second);
  for (const [path, status] of [['/get','blocked'], ['/cross','blocked'], ['/mfa','verification_required'], ['/replay','blocked']])
    await assert.rejects(run(path), (err) => err.status===status);
  assert.equal(requests.filter((v) => v==='POST /auth').length,2);
  assert.equal(requests.filter((v) => v==='POST /identify').length,1);
  assert.equal(requests.filter((v) => v==='POST /auth-json').length,1);
  assert(attemptedTunnelHosts.includes('login.example.test:443'));
  assert.equal(requests.filter((v) => v==='POST /auth-redirect').length,1);
  assert.equal(requests.some((v)=>/must-not-submit|must-not-replay|should-not-mutate/.test(v)),false);
});
