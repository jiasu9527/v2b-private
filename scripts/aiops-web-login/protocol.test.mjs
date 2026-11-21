import assert from 'node:assert/strict';
import test from 'node:test';
import { containsCredentialURL, hideCredentials, loginPayloadContains, parseInput } from './protocol.mjs';

const valid = {
  url: 'https://login.example.test/sign-in',
  target_url: 'https://login.example.test/status',
  username: 'fixture-admin',
  password: 'fixture & pass',
  proxy_url: 'http://127.0.0.1:4000',
};

test('accepts a single explicit HTTPS origin and local transport only', () => {
  assert.equal(parseInput(JSON.stringify(valid)).origin, 'https://login.example.test');
  for (const change of [
    { url: 'http://login.example.test/' }, { url: 'https://user:p@login.example.test/' },
    { url: 'https://login.example.test:4434/' }, { url: 'https://127.0.0.1/' },
    { url: 'https://foo.test/a?token=sensitive' }, { url: 'https://foo.test/#sensitive' },
    { target_url: 'https://other.test/' }, { target_url: false },
    { proxy_url: 'http://evil.test:4000' }, { proxy_url: 'http://127.0.0.1:65536' },
    { password: '' }, { extra: true },
  ]) assert.throws(() => parseInput(JSON.stringify({ ...valid, ...change })), JSON.stringify(change));
});

test('redacts encodings before the output cap', () => {
  const secret = 'fixture & pass';
  const url = new URL('https://site.example/?code=' + encodeURIComponent(secret));
  assert.equal(containsCredentialURL(url, secret), true);
  assert.equal(containsCredentialURL(new URL('https://site.example/'), secret), false);
  assert.equal(hideCredentials('prefix ' + secret, [secret], 11), 'prefix 〔账号信');
  assert.doesNotMatch(hideCredentials(secret + '\n' + encodeURIComponent(secret) + '\nfixture+%26+pass', [secret], 100), /fixture/);
});

test('checks a credential value in JSON, ordinary form or multipart, not substrings', () => {
  const value = 'fixture & pass';
  assert.equal(loginPayloadContains(JSON.stringify({ data: { pass: value } }), value), true);
  assert.equal(loginPayloadContains('pass=fixture+%26+pass', value), true);
  assert.equal(loginPayloadContains('--break\r\nContent-Disposition: form-data; name="pass"\r\n\r\n' + value + '\r\n--break', value), true);
  assert.equal(loginPayloadContains(JSON.stringify({ pass: 'x' + value }), value), false);
  assert.equal(loginPayloadContains('other=wrong', value), false);
});
