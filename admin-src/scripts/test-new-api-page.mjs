import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createContext, runInContext } from 'node:vm';
import { test } from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/pages/NewAPIIntegrationPage.tsx', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.React, esModuleInterop: true },
}).outputText;
const config = (revision = 3) => ({
  revision, enabled: false, api_url: 'https://ai.example.test', site_url: 'https://panel.example.test',
  client_id: 'forest', merchant_id: 'wallet', admin_user_id: 1,
  min_amount_fen: 100, max_amount_fen: 100000, daily_limit_fen: 500000,
  has_client_secret: true, has_merchant_key: true, has_admin_token: true,
});

function runtime() {
  const slots = [], effects = [], requests = [], notices = [];
  const values = {};
  let cursor = 0, tree, serverConfig = config(), postResult;
  const react = {
    createElement: (type, props, ...children) => ({ type, props: { ...props, children } }),
    useState(initial) {
      const index = cursor++;
      if (!(index in slots)) slots[index] = typeof initial === 'function' ? initial() : initial;
      return [slots[index], (next) => { slots[index] = typeof next === 'function' ? next(slots[index]) : next; }];
    },
    useRef(initial) {
      const index = cursor++;
      if (!(index in slots)) slots[index] = { current: initial };
      return slots[index];
    },
    useEffect(effect) { const index = cursor++; if (!(index in slots)) { slots[index] = true; effects.push(effect); } },
  };
  const form = {
    setFieldsValue: (next) => Object.assign(values, next),
    setFieldValue: (key, value) => { values[key] = value; },
    getFieldValue: (key) => values[key],
    validateFields: async () => ({ ...values }),
  };
  const antd = Object.fromEntries(['Alert', 'Button', 'Card', 'Col', 'Descriptions', 'InputNumber', 'Popconfirm', 'Row', 'Select', 'Switch', 'Table', 'Tabs', 'Tag'].map((name) => [name, name]));
  antd.Form = Object.assign(function Form() {}, { useForm: () => [form], Item: 'Form.Item' });
  antd.Input = Object.assign(function Input() {}, { Password: 'Input.Password' });
  antd.Space = Object.assign(function Space() {}, { Compact: 'Space.Compact' });
  antd.Typography = { Text: 'Text', Paragraph: 'Paragraph' };
  antd.message = Object.fromEntries(['success', 'error', 'info'].map((name) => [name, (message) => notices.push({ name, message })]));
  const api = {
    async apiGet(path) {
      requests.push({ method: 'GET', path });
      if (requests.filter((x) => x.method === 'GET').length > 1) throw new Error('simulated reload network failure');
      return { data: { ...serverConfig } };
    },
    async apiJsonPost(path, payload) {
      requests.push({ method: 'POST', path, payload });
      assert.equal(payload.expected_revision, serverConfig.revision, 'writes must use the last committed revision');
      serverConfig = { ...serverConfig, revision: serverConfig.revision + 1 };
      return postResult === undefined ? { data: { ...serverConfig } } : postResult;
    },
  };
  const context = createContext({ exports: {}, URL, console, require: (name) => {
    if (name === 'react') return react;
    if (name === 'antd') return antd;
    if (name === '../lib/api') return api;
    return {};
  } });
  runInContext(compiled, context);
  const render = () => { cursor = 0; tree = context.exports.default(); return tree; };
  const nodes = () => {
    const found = [], seen = new Set();
    const visit = (value) => {
      if (!value || typeof value !== 'object' || seen.has(value)) return;
      seen.add(value);
      if ('type' in value && value.props) found.push(value);
      for (const entry of Object.values(value)) visit(entry);
    };
    visit(tree);
    return found;
  };
  const save = () => nodes().find((node) => node.type === 'Button' && node.props.children[0] === '保存配置').props.onClick();
  render();
  for (const effect of effects) effect();
  return { render, nodes, save, values, requests, notices, setPostResult: (value) => { postResult = value; } };
}

const tick = () => new Promise((resolve) => setImmediate(resolve));

test('successful config writes adopt the returned revision without a second GET', async () => {
  const r = runtime();
  await tick(); r.render();
  r.values.client_secret = 'new-client-secret';
  await r.save(); r.render();
  assert.equal(r.requests.filter((x) => x.method === 'GET').length, 1);
  assert.equal(r.values.client_secret, '');
  assert.equal(r.notices.filter((x) => x.name === 'success').length, 1);
  await r.save(); r.render();
  assert.deepEqual(r.requests.filter((x) => x.method === 'POST').map((x) => x.payload.expected_revision), [3, 4]);
  assert.equal(r.notices.filter((x) => x.name === 'success').length, 2);
  assert.equal(r.nodes().filter((node) => node.type === 'Alert' && node.props.type === 'error').length, 0);
});

test('malformed save responses cannot clear entered secrets or report success', async () => {
  const r = runtime();
  await tick(); r.render();
  r.values.client_secret = 'keep-unconfirmed-input';
  r.setPostResult({ data: true });
  await r.save(); r.render();
  assert.equal(r.values.client_secret, 'keep-unconfirmed-input');
  assert.equal(r.notices.filter((x) => x.name === 'success').length, 0);
  assert.match(r.nodes().find((node) => node.type === 'Alert' && node.props.type === 'error').props.message, /重新读取配置后核对/);
});

test('binding rows distinguish multiple remote accounts for the same panel user', () => {
  const r = runtime();
  const table = r.nodes().find((node) => node.type === 'Table' && node.props.scroll.x === 700);
  assert.equal(table.props.rowKey({ user_id: 7, remote_user_id: 11 }), '7:11');
  assert.equal(table.props.rowKey({ user_id: 7, remote_user_id: 12 }), '7:12');
});
