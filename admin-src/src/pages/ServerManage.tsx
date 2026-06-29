import React, { useEffect, useMemo, useState } from 'react';
import {
  Badge,
  Button,
  Card,
  Drawer,
  Dropdown,
  Form,
  Input,
  InputNumber,
  Popconfirm,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Tooltip,
  message,
} from 'antd';
import {
  CopyOutlined,
  DeleteOutlined,
  DownOutlined,
  EditOutlined,
  MenuOutlined,
  PlusOutlined,
  QuestionCircleOutlined,
  UserOutlined,
} from '@ant-design/icons';
import { apiGet, apiPost, safeJsonParse } from '../lib/api';
import { moveItem } from '../lib/drag';
import JsonModal from '../components/JsonModal';

const serverTypes = ['v2node', 'shadowsocks', 'vmess', 'trojan', 'hysteria', 'tuic', 'vless', 'anytls'];
const v2nodeProtocols = ['anytls', 'hysteria2', 'juicity', 'mieru', 'shadowsocks', 'trojan', 'tuic', 'vless', 'vmess'];
const genericNetworks = ['tcp', 'ws', 'grpc', 'kcp', 'httpupgrade', 'xhttp'];
const ssNetworks = ['tcp', 'http'];
const ciphers = ['aes-128-gcm', 'aes-192-gcm', 'aes-256-gcm', 'chacha20-ietf-poly1305', '2022-blake3-aes-128-gcm', '2022-blake3-aes-256-gcm'];
const congestionOptions = ['cubic', 'new_reno', 'bbr'];
const mieruTransportOptions = ['TCP', 'UDP'];
const externalV2nodeProtocols = ['juicity', 'mieru'];
const udpRelayOptions = ['native', 'quic'];

const typeColors: Record<string, string> = {
  shadowsocks: '#489851',
  vmess: '#CB3180',
  trojan: '#EAB854',
  hysteria: '#1A1A1A',
  tuic: '#9400D3',
  vless: '#4080FF',
  anytls: '#FF8C00',
  v2node: '#FF0000',
};

const statusMap: Record<number, any> = { 0: 'error', 1: 'warning', 2: 'success' };

function listValue(value: any): string[] {
  const parsed = safeJsonParse(value, value);
  if (Array.isArray(parsed)) return parsed.map(String);
  if (parsed === undefined || parsed === null || parsed === '') return [];
  return String(parsed).split(/[,\n]/).map((item) => item.trim()).filter(Boolean);
}

function maybeJSON(value: any) {
  if (value === undefined || value === null || value === '') return null;
  return safeJsonParse(value, value);
}

function formatTextJSON(value: any) {
  if (value === undefined || value === null || value === '') return '';
  if (typeof value === 'string') return value;
  return JSON.stringify(value, null, 2);
}

function parseJSONDeep(value: any, fallback: any = value) {
  let current = value;
  for (let i = 0; i < 3; i += 1) {
    if (typeof current !== 'string') return current;
    const trimmed = current.trim();
    if (!trimmed) return fallback;
    try {
      current = JSON.parse(trimmed);
    } catch {
      return i === 0 ? safeJsonParse(current, fallback) : current;
    }
  }
  return current;
}

function jsonObjectFromField(value: any) {
  const parsed = parseJSONDeep(value, {});
  return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed : {};
}

function readPath(obj: any, path: string) {
  return path.split('.').reduce((current, key) => {
    if (current === undefined || current === null) return undefined;
    const match = key.match(/^(.+)\[(\d+)\]$/);
    if (match) {
      const value = current[match[1]];
      const index = Number(match[2]);
      return Array.isArray(value) ? value[index] : undefined;
    }
    return current[key];
  }, obj);
}

function nonEmpty(value: any) {
  return value !== undefined && value !== null && value !== '';
}

function settingDisplayValue(obj: Record<string, any>, fieldKey: string, aliases: string[] = []) {
  for (const key of [fieldKey, ...aliases]) {
    const value = key.includes('.') || key.includes('[') ? readPath(obj, key) : obj[key];
    if (nonEmpty(value)) return value;
  }
  return undefined;
}

const realityAliases: Record<string, string[]> = {
  server_name: ['serverName', 'sni', 'server_names[0]', 'serverNames[0]', 'reality.server_name', 'reality.serverName', 'reality.server_names[0]', 'reality.serverNames[0]', 'realitySettings.server_name', 'realitySettings.serverName', 'realitySettings.server_names[0]', 'realitySettings.serverNames[0]', 'settings.reality.server_name', 'settings.reality.serverName', 'settings.reality.serverNames[0]'],
  dest: ['server_address', 'serverAddress', 'target', 'address', 'reality.dest', 'reality.server_address', 'reality.serverAddress', 'realitySettings.dest', 'realitySettings.serverAddress', 'settings.reality.dest'],
  server_port: ['serverPort', 'target_port', 'targetPort', 'reality.server_port', 'reality.serverPort', 'reality.target_port', 'reality.targetPort', 'realitySettings.server_port', 'realitySettings.serverPort', 'settings.reality.serverPort'],
  xver: ['proxy_protocol', 'proxyProtocol', 'reality.xver', 'realitySettings.xver', 'settings.reality.xver'],
  private_key: ['privateKey', 'reality.private_key', 'reality.privateKey', 'realitySettings.private_key', 'realitySettings.privateKey', 'settings.reality.privateKey'],
  public_key: ['publicKey', 'pbk', 'reality.public_key', 'reality.publicKey', 'reality.pbk', 'realitySettings.public_key', 'realitySettings.publicKey', 'realitySettings.pbk', 'settings.reality.publicKey'],
  short_id: ['shortId', 'sid', 'short_ids[0]', 'shortIds[0]', 'reality.short_id', 'reality.shortId', 'reality.sid', 'reality.short_ids[0]', 'reality.shortIds[0]', 'realitySettings.short_id', 'realitySettings.shortId', 'realitySettings.short_ids[0]', 'realitySettings.shortIds[0]', 'settings.reality.shortIds[0]'],
  fingerprint: ['fp', 'client_fingerprint', 'clientFingerprint', 'utls.fingerprint', 'reality.fingerprint', 'reality.fp', 'realitySettings.fingerprint', 'realitySettings.fp'],
  allow_insecure: ['allowInsecure', 'insecure', 'reality.allow_insecure', 'reality.allowInsecure', 'realitySettings.allowInsecure'],
};

function mergeAliasField(target: Record<string, any>, sources: Record<string, any>[], fieldKey: string) {
  if (nonEmpty(target[fieldKey])) return;
  for (const source of sources) {
    const value = settingDisplayValue(source, fieldKey, realityAliases[fieldKey] || []);
    if (nonEmpty(value)) {
      target[fieldKey] = value;
      return;
    }
  }
}

function normalizeTLSSettings(row: any) {
  const snake = jsonObjectFromField(row?.tls_settings);
  const camel = jsonObjectFromField(row?.tlsSettings);
  const reality = jsonObjectFromField(row?.reality);
  const realitySnake = jsonObjectFromField(row?.reality_settings);
  const realityCamel = jsonObjectFromField(row?.realitySettings);
  const settings = jsonObjectFromField(row?.settings);
  const merged: Record<string, any> = { ...camel, ...snake };
  const sources = [merged, snake, camel, reality, realitySnake, realityCamel, settings, row || {}];
  Object.keys(realityAliases).forEach((fieldKey) => mergeAliasField(merged, sources, fieldKey));
  return Object.keys(merged).length ? merged : row?.tls_settings ?? row?.tlsSettings;
}

function typeTag(type: string, text?: React.ReactNode) {
  return <Tag color={typeColors[type] || undefined}>{text || type}</Tag>;
}


function normalizeInitial(row: any) {
  const type = row?.type || 'v2node';
  const protocol = row?.protocol || (type === 'v2node' ? 'vless' : undefined);
  return {
    ...row,
    type,
    protocol,
    rate: row?.rate ?? 1,
    show: row?.show ?? 1,
    tls: row?.tls ?? 0,
    network: row?.network ?? 'tcp',
    group_id: listValue(row?.group_id),
    route_id: listValue(row?.route_id),
    tags: listValue(row?.tags),
    network_settings: formatTextJSON(row?.network_settings ?? row?.networkSettings),
    tls_settings: formatTextJSON(normalizeTLSSettings(row)),
    encryption_settings: formatTextJSON(row?.encryption_settings),
    padding_scheme: formatTextJSON(row?.padding_scheme),
    ddns_settings: formatTextJSON(row?.ddns_settings),
  };
}

function normalizePayload(values: any, edit: any) {
  const type = values.type || edit?.type;
  const payload: any = { ...edit, ...values };
  delete payload.type;
  delete payload.available_status;
  delete payload.online;
  delete payload.last_check_at;
  delete payload.last_push_at;
  delete payload.entry_group_name;
  delete payload.entry_group_names;
  delete payload.entry_group_options;
  delete payload.install_command;
  delete payload.send_through;
  payload.group_id = listValue(payload.group_id);
  payload.route_id = listValue(payload.route_id);
  payload.tags = listValue(payload.tags);
  ['network_settings', 'tls_settings', 'encryption_settings', 'padding_scheme', 'ddns_settings'].forEach((key) => {
    if (payload[key] !== undefined) payload[key] = maybeJSON(payload[key]);
  });
  if (payload.show === true) payload.show = 1;
  if (payload.show === false) payload.show = 0;
  if (type !== 'v2node') {
    delete payload.protocol;
    delete payload.listen_ip;
  }
  return payload;
}

function baseDefaults(type: string) {
  const common: any = { type, show: 1, rate: 1, port: '443', server_port: 443, group_id: [], route_id: [], tags: [] };
  if (type === 'v2node') return { ...common, protocol: 'vless', network: 'tcp', tls: 0, disable_sni: 0, zero_rtt_handshake: 0, flow: null, ddns_settings: '', install_command: '' };
  if (type === 'vless') return { ...common, network: 'tcp', tls: 0, flow: null };
  if (type === 'vmess') return { ...common, network: 'tcp', tls: 0 };
  if (type === 'trojan') return { ...common, network: 'tcp', allow_insecure: 0, server_name: '' };
  if (type === 'shadowsocks') return { ...common, cipher: 'aes-128-gcm' };
  if (type === 'hysteria') return { ...common, version: 2, up_mbps: 100, down_mbps: 100, insecure: 0 };
  if (type === 'tuic') return { ...common, insecure: 0, disable_sni: 0, udp_relay_mode: 'native', zero_rtt_handshake: 0, congestion_control: 'cubic' };
  if (type === 'anytls') return { ...common, network: undefined, insecure: 0, padding_scheme: '' };
  return common;
}

type ProtocolFieldItem = { key: string; node: React.ReactNode; span?: 12 | 24; hidden?: boolean };

type ChildEditorType = 'network_settings' | 'tls_settings' | 'encryption_settings' | 'padding_scheme';

function addField(fields: ProtocolFieldItem[], key: string, node: React.ReactNode, span: 12 | 24 = 12) {
  fields.push({ key, node, span });
}

function addHiddenField(fields: ProtocolFieldItem[], name: string) {
  fields.push({ key: `${name}_hidden`, hidden: true, node: <Form.Item name={name} hidden><Input.TextArea /></Form.Item> });
}

function editLink(label: string, onClick: () => void) {
  return <span>{label} <a onClick={(event) => { event.preventDefault(); event.stopPropagation(); onClick(); }}>编辑配置</a></span>;
}

function ProtocolFields({ type, protocol, tls, network, form, onEditChild }: { type: string; protocol?: string; tls?: any; network?: string; form: any; onEditChild: (title: string, type: ChildEditorType) => void }) {
  const fields: ProtocolFieldItem[] = [];
  const actualProtocol = type === 'v2node' ? protocol : type;
  const obfs = Form.useWatch('obfs', form);
  const encryption = Form.useWatch('encryption', form);
  const mieruNetworkSettingsRaw = Form.useWatch('network_settings', form);
  addHiddenField(fields, 'network_settings');
  addHiddenField(fields, 'tls_settings');
  addHiddenField(fields, 'encryption_settings');
  addHiddenField(fields, 'padding_scheme');
  if (type === 'v2node') addHiddenField(fields, 'ddns_settings');

  if (type === 'v2node') {
    addField(fields, 'protocol', <Form.Item name="protocol" label="节点协议" rules={[{ required: true }]}><Select onChange={(value) => {
      const nextValues: Record<string, any> = { protocol: value };
      if (['anytls', 'hysteria2', 'trojan', 'tuic', 'juicity'].includes(value)) nextValues.tls = 1;
      if (value === 'juicity') {
        nextValues.network = undefined;
        nextValues.congestion_control = form.getFieldValue('congestion_control') || 'bbr';
      }
      if (value === 'mieru') {
        nextValues.tls = 0;
        nextValues.network = undefined;
        nextValues.network_settings = JSON.stringify({ transport: 'TCP', mtu: 1400 }, null, 2);
      }
      form.setFieldsValue(nextValues);
    }} options={v2nodeProtocols.map((value) => ({ label: value === 'hysteria2' ? 'Hysteria2' : value.toUpperCase(), value }))} /></Form.Item>);
  }

  if (['v2node', 'vmess', 'vless'].includes(type) || ['vmess', 'vless', 'trojan', 'hysteria2', 'tuic'].includes(String(actualProtocol))) {
    const allowNone = actualProtocol === 'vless' || actualProtocol === 'vmess';
    const allowReality = actualProtocol === 'vless' || (type === 'v2node' && actualProtocol === 'anytls');
    if (actualProtocol !== 'shadowsocks') {
      const needsTLSConfig = Number(tls || 0) !== 0 || ['hysteria2', 'trojan', 'tuic'].includes(String(actualProtocol));
      addField(fields, 'tls', <Form.Item name="tls" label={needsTLSConfig ? editLink('安全性', () => onEditChild('编辑安全性配置', 'tls_settings')) : '安全性'}><Select options={[
        ...(allowNone ? [{ label: '无', value: 0 }] : []),
        { label: 'TLS', value: 1 },
        ...(allowReality ? [{ label: 'Reality', value: 2 }] : []),
      ]} /></Form.Item>);
    }
  }

  if (actualProtocol === 'shadowsocks' || type === 'shadowsocks') {
    addField(fields, 'cipher', <Form.Item name="cipher" label="加密算法"><Select options={ciphers.map((value) => ({ label: value, value }))} /></Form.Item>, 24);
  }

  if (actualProtocol && !externalV2nodeProtocols.includes(String(actualProtocol)) && (type === 'v2node' || !['hysteria2', 'tuic', 'anytls'].includes(String(actualProtocol)))) {
    const networks = actualProtocol === 'shadowsocks' ? ssNetworks : genericNetworks.filter((item) => actualProtocol !== 'trojan' || ['tcp', 'ws', 'grpc'].includes(item));
    addField(fields, 'network', <Form.Item name="network" label={editLink('传输协议', () => onEditChild('编辑协议配置', 'network_settings'))}><Select placeholder="选择传输协议" options={networks.map((value) => ({ label: value === 'ws' ? 'WebSocket' : value === 'http' ? 'HTTP伪装' : value === 'grpc' ? 'gRPC' : value === 'kcp' ? 'mKCP' : value === 'httpupgrade' ? 'HTTPUpgrade' : value.toUpperCase(), value }))} /></Form.Item>, 24);
  }

  if (actualProtocol === 'juicity') {
    addField(fields, 'juicity_notice', <div className="legacy-info-note">Juicity 由 v2node 外部进程托管；v2node 已预留观测日志接口，支持 patched juicity-server 上报用户流量、在线与敏感域名。</div>, 24);
    addField(fields, 'congestion_control', <Form.Item name="congestion_control" label="拥塞控制算法"><Select options={congestionOptions.map((value) => ({ label: value, value }))} /></Form.Item>);
    addField(fields, 'juicity_tls_link', <div className="form-only-link"><a onClick={() => onEditChild('编辑 Juicity TLS 配置', 'tls_settings')}>编辑 TLS 证书配置</a></div>, 24);
  }

  if (actualProtocol === 'mieru') {
    const networkSettings = jsonObjectFromField(mieruNetworkSettingsRaw !== undefined ? mieruNetworkSettingsRaw : form.getFieldValue('network_settings'));
    const updateMieruSetting = (key: string, value: any) => {
      const next = { ...networkSettings, [key]: value };
      if (value === '' || value === null || value === undefined) delete next[key];
      form.setFieldsValue({ network_settings: JSON.stringify(next, null, 2) });
    };
    addField(fields, 'mieru_notice', <div className="legacy-info-note">Mieru 由 v2node 外部进程托管；v2node 会通过 mita get metrics 采集用户级增量流量。</div>, 24);
    addField(fields, 'mieru_transport', <Form.Item label="传输协议"><Select value={networkSettings.transport || 'TCP'} options={mieruTransportOptions.map((value) => ({ label: value, value }))} onChange={(value) => updateMieruSetting('transport', value)} /></Form.Item>);
    addField(fields, 'mieru_mtu', <Form.Item label="MTU"><InputNumber value={Number(networkSettings.mtu || 1400)} min={576} max={9000} style={{ width: '100%' }} onChange={(value) => updateMieruSetting('mtu', value || 1400)} /></Form.Item>);
    addField(fields, 'mieru_multiplexing', <Form.Item label="Multiplexing"><Input value={networkSettings.multiplexing || ''} placeholder="留空使用 Mieru 默认" onChange={(event) => updateMieruSetting('multiplexing', event.target.value)} /></Form.Item>, 24);
  }

  if (actualProtocol === 'hysteria2' || type === 'hysteria') {
    addField(fields, 'obfs', <Form.Item name="obfs" label="混淆方式obfs"><Select allowClear options={[{ label: '无', value: null }, { label: 'salamander', value: 'salamander' }]} /></Form.Item>);
    if (obfs === 'salamander') addField(fields, 'obfs_password', <Form.Item name="obfs_password" label="混淆密码obfs_password"><Input placeholder="留空自动生成" /></Form.Item>);
    addField(fields, 'up_mbps', <Form.Item name="up_mbps" label="上行带宽"><InputNumber addonAfter="Mbps" placeholder="服务端发送带宽,留空或填0使用BBR" style={{ width: '100%' }} /></Form.Item>, 24);
    addField(fields, 'down_mbps', <Form.Item name="down_mbps" label="下行带宽"><InputNumber addonAfter="Mbps" placeholder="服务端接收带宽,留空或填0使用BBR" style={{ width: '100%' }} /></Form.Item>, 24);
  }

  if (actualProtocol === 'tuic' || type === 'tuic') {
    addField(fields, 'disable_sni', <Form.Item name="disable_sni" label="禁用SNI"><Select options={[{ label: '否', value: 0 }, { label: '是', value: 1 }]} /></Form.Item>);
    addField(fields, 'udp_relay_mode', <Form.Item name="udp_relay_mode" label="数据包中继模式"><Select options={udpRelayOptions.map((value) => ({ label: value, value }))} /></Form.Item>);
    addField(fields, 'congestion_control', <Form.Item name="congestion_control" label="拥塞控制算法"><Select options={congestionOptions.map((value) => ({ label: value, value }))} /></Form.Item>);
    addField(fields, 'zero_rtt_handshake', <Form.Item name="zero_rtt_handshake" label="客户端启用 0-RTT"><Select options={[{ label: '否', value: 0 }, { label: '是', value: 1 }]} /></Form.Item>);
  }

  if (type !== 'v2node' && ['trojan', 'tuic', 'anytls'].includes(type)) {
    if (type === 'anytls') {
      addField(fields, 'insecure', <Form.Item name="insecure" label={<Tooltip title="使用自签名证书需要允许不安全，用户才可以连接">允许不安全 <QuestionCircleOutlined /></Tooltip>}><Select options={[{ label: '否', value: 0 }, { label: '是', value: 1 }]} /></Form.Item>);
      addField(fields, 'server_name', <Form.Item name="server_name" label="服务器名称指示(sni)"><Input placeholder="当节点地址与证书不一致时用于证书验证" /></Form.Item>, 24);
    } else {
      addField(fields, 'server_name', <Form.Item name="server_name" label="服务器名称指示(sni)"><Input placeholder="当节点地址与证书不一致时用于证书验证" /></Form.Item>);
      addField(fields, 'insecure', <Form.Item name="insecure" label="允许不安全"><Select options={[{ label: '否', value: 0 }, { label: '是', value: 1 }]} /></Form.Item>);
    }
  }

  if (actualProtocol === 'anytls' || type === 'anytls') {
    addField(fields, 'padding_scheme_link', <div className="form-only-link"><a onClick={() => onEditChild('编辑填充方案', 'padding_scheme')}>编辑填充方案</a></div>, 24);
  }

  if (actualProtocol === 'vless' || type === 'vless') {
    addField(fields, 'encryption', <Form.Item name="encryption" label={encryption ? editLink('加密方式', () => onEditChild('编辑加密配置', 'encryption_settings')) : '加密方式'}><Select allowClear placeholder="选择加密方式" options={[{ label: '无', value: null }, { label: 'MLKEM768X25519PLUS', value: 'mlkem768x25519plus' }]} /></Form.Item>, 24);
    addField(fields, 'flow', <Form.Item name="flow" label="XTLS流控算法"><Select allowClear placeholder="选择XTLS流控算法" options={[{ label: '无', value: null }, { label: 'xtls-rprx-vision', value: 'xtls-rprx-vision' }]} /></Form.Item>, 24);
  }

  return <>{fields.map(({ key, node, span = 12, hidden }) => hidden ? <React.Fragment key={key}>{node}</React.Fragment> : <div key={key} className={span === 24 ? 'form-col-24' : 'form-col-12'}>{node}</div>)}</>;
}

const networkSamples: Record<string, string> = {
  tcp: JSON.stringify({ acceptProxyProtocol: false, header: { type: 'http', request: { path: ['/'], headers: { Host: ['www.baidu.com', 'www.bing.com'] } }, response: {} } }, null, 4),
  http: JSON.stringify({ acceptProxyProtocol: false, path: '/', Host: 'xtls.github.io' }, null, 4),
  ws: JSON.stringify({ acceptProxyProtocol: false, path: '/', headers: { Host: 'xtls.github.io' } }, null, 4),
  grpc: JSON.stringify({ serviceName: 'GunService' }, null, 4),
  kcp: JSON.stringify({ header: { type: 'none' }, seed: '' }, null, 4),
  httpupgrade: JSON.stringify({ acceptProxyProtocol: false, path: '/', host: 'xtls.github.io' }, null, 4),
  xhttp: JSON.stringify({ path: '/', host: 'xtls.github.io', mode: 'auto', extra: {} }, null, 4),
};

const paddingSample = JSON.stringify([
  'stop=8',
  '0=30-30',
  '1=100-400',
  '2=400-500,c,500-1000,c,500-1000,c,500-1000,c,500-1000',
  '3=9-9,500-1000',
  '4=500-1000',
  '5=500-1000',
  '6=500-1000',
  '7=500-1000',
], null, 4);

function SettingsKV({ form, name, fieldKey, label, placeholder, type = 'input', options, show = true, aliases = [] }: { form: any; name: string; fieldKey: string; label: string; placeholder?: string; type?: 'input' | 'textarea' | 'select' | 'switch'; options?: { label: string; value: any }[]; show?: boolean; aliases?: string[] }) {
  const watchedRaw = Form.useWatch(name, form);
  const raw = watchedRaw !== undefined ? watchedRaw : form.getFieldValue(name);
  const obj = useMemo(() => jsonObjectFromField(raw), [raw]);
  if (!show) return null;
  const displayValue = settingDisplayValue(obj, fieldKey, aliases);
  const setValue = (value: any) => {
    const next = { ...obj, [fieldKey]: value };
    if (value === '' || value === null || value === undefined) delete next[fieldKey];
    form.setFieldsValue({ [name]: Object.keys(next).length ? JSON.stringify(next, null, 2) : '' });
  };
  let input: React.ReactNode = <Input value={displayValue ?? ''} placeholder={placeholder} onChange={(e) => setValue(e.target.value)} />;
  if (type === 'textarea') input = <Input.TextArea rows={3} value={displayValue ?? ''} placeholder={placeholder} onChange={(e) => setValue(e.target.value)} />;
  if (type === 'select') input = <Select allowClear value={displayValue} placeholder={placeholder} options={options || []} onChange={setValue} />;
  if (type === 'switch') input = <Switch checked={displayValue === true || displayValue === '1' || displayValue === 1} onChange={(checked) => setValue(checked ? '1' : '0')} />;
  return <div className="form-group"><label>{label}</label>{input}</div>;
}

function DDNSFields({ form, host }: { form: any; host?: string }) {
  const watchedRaw = Form.useWatch('ddns_settings', form);
  const raw = watchedRaw !== undefined ? watchedRaw : form.getFieldValue('ddns_settings');
  const settings = useMemo(() => jsonObjectFromField(raw), [raw]);
  const ddnsEnabled = settings.ddns_enabled === true || settings.ddns_enabled === '1' || settings.ddns_enabled === 1 || settings.enabled === true || settings.enabled === '1' || settings.enabled === 1;
  const blockEnabled = settings.block_check_enabled === true || settings.block_check_enabled === '1' || settings.block_check_enabled === 1;
  return <div className="form-col-24">
    <div className="legacy-inline-section">
      <div className="legacy-inline-section-title">DDNS / 被墙检测</div>
      <div className="row">
        <div className="col-lg-6"><SettingsKV form={form} name="ddns_settings" fieldKey="ddns_enabled" label="启用DDNS解析更新" type="switch" /></div>
        <div className="col-lg-6"><SettingsKV form={form} name="ddns_settings" fieldKey="block_check_enabled" label="启用被墙检测/换IP" type="switch" /></div>
      </div>
      {ddnsEnabled && <>
        <SettingsKV form={form} name="ddns_settings" fieldKey="cf_record" label="完整记录域名" placeholder={`留空使用连接地址 ${host || ''}`.trim()} />
        <div className="row">
          <div className="col-lg-4"><SettingsKV form={form} name="ddns_settings" fieldKey="cf_record_type" label="记录类型" type="select" options={[{ label: 'A', value: 'A' }, { label: 'AAAA', value: 'AAAA' }]} /></div>
          <div className="col-lg-4"><SettingsKV form={form} name="ddns_settings" fieldKey="cf_ttl" label="TTL" placeholder="留空使用系统默认" /></div>
          <div className="col-lg-4"><SettingsKV form={form} name="ddns_settings" fieldKey="cf_proxied" label="Cloudflare橙云" type="switch" /></div>
        </div>
        <SettingsKV form={form} name="ddns_settings" fieldKey="ddns_interval" label="DDNS检测间隔(分钟)" placeholder="留空使用系统默认" />
      </>}
      {blockEnabled && <>
        <div className="row">
          <div className="col-lg-6"><SettingsKV form={form} name="ddns_settings" fieldKey="ddns_interval" label="被墙检测间隔(分钟)" placeholder="留空使用系统默认" /></div>
          <div className="col-lg-6"><SettingsKV form={form} name="ddns_settings" fieldKey="block_check_url" label="墙检测URL" placeholder="留空使用系统默认" /></div>
        </div>
        <div className="row">
          <div className="col-lg-4"><SettingsKV form={form} name="ddns_settings" fieldKey="block_check_keyword" label="被墙关键词" placeholder="留空=curl失败才算异常" /></div>
          <div className="col-lg-4"><SettingsKV form={form} name="ddns_settings" fieldKey="block_check_timeout" label="检测超时(秒)" placeholder="留空使用系统默认" /></div>
          <div className="col-lg-4"><SettingsKV form={form} name="ddns_settings" fieldKey="block_check_threshold" label="失败阈值" placeholder="留空使用系统默认" /></div>
        </div>
        <SettingsKV form={form} name="ddns_settings" fieldKey="change_ip_curl" label="换IP curl命令" type="textarea" placeholder="例如 curl -fsS 'https://api.xxx/change?token=xxx'" />
        <div className="row">
          <div className="col-lg-6"><SettingsKV form={form} name="ddns_settings" fieldKey="change_ip_wait" label="换IP后等待(秒)" placeholder="留空使用系统默认" /></div>
          <div className="col-lg-6"><SettingsKV form={form} name="ddns_settings" fieldKey="change_ip_cooldown" label="换IP冷却(秒)" placeholder="留空使用系统默认" /></div>
        </div>
      </>}
    </div>
  </div>;
}

function ChildEditor({ type, form, tls, network }: { type: ChildEditorType; form: any; tls: any; network?: string }) {
  const watchedTLSSettingsRaw = Form.useWatch('tls_settings', form);
  const watchedEncryptionSettingsRaw = Form.useWatch('encryption_settings', form);
  const tlsSettingsRaw = watchedTLSSettingsRaw !== undefined ? watchedTLSSettingsRaw : form.getFieldValue('tls_settings');
  const encryptionSettingsRaw = watchedEncryptionSettingsRaw !== undefined ? watchedEncryptionSettingsRaw : form.getFieldValue('encryption_settings');
  const tlsSettings = useMemo(() => jsonObjectFromField(tlsSettingsRaw), [tlsSettingsRaw]);
  const encryptionSettings = useMemo(() => jsonObjectFromField(encryptionSettingsRaw), [encryptionSettingsRaw]);

  useEffect(() => {
    if (type === 'tls_settings' && !String(tlsSettingsRaw || '').trim()) {
      form.setFieldsValue({ tls_settings: JSON.stringify({ server_name: '', cert_mode: 'self', provider: '', dns_env: '', reject_unknown_sni: '0', allow_insecure: '0' }, null, 2) });
    }
    if (type === 'encryption_settings' && !String(encryptionSettingsRaw || '').trim()) {
      form.setFieldsValue({ encryption_settings: JSON.stringify({ mode: 'native', rtt: '0rtt', ticket: '600s', server_padding: null, client_padding: null, private_key: null, password: null }, null, 2) });
    }
  }, [type, tlsSettingsRaw, encryptionSettingsRaw, form]);

  if (type === 'network_settings') {
    return <div id="v2ray-protocol" className="legacy-child-editor">
      <div className="form-group"><label>协议详细配置 <a href="https://www.v2ray.com/chapter_02/05_transport.html" target="_blank" rel="noreferrer">参考</a></label><Form.Item name="network_settings" noStyle><Input.TextArea className="code-textarea" rows={18} placeholder={networkSamples[network || 'tcp'] || ''} /></Form.Item></div>
    </div>;
  }
  if (type === 'padding_scheme') {
    return <div id="anytls-padding-scheme" className="legacy-child-editor"><div className="form-group"><Form.Item name="padding_scheme" noStyle><Input.TextArea className="code-textarea" rows={18} placeholder={paddingSample} /></Form.Item></div></div>;
  }
  if (type === 'encryption_settings') {
    const rtt = encryptionSettings.rtt || '0rtt';
    return <div className="legacy-child-editor">
      <SettingsKV form={form} name="encryption_settings" fieldKey="mode" label="Mode" type="select" options={['native', 'xorpub', 'random'].map((value) => ({ label: value, value }))} />
      <div className="row"><div className="col-lg-6"><SettingsKV form={form} name="encryption_settings" fieldKey="rtt" label="RTT" type="select" options={['0rtt', '1rtt'].map((value) => ({ label: value, value }))} /></div><div className="col-lg-6"><SettingsKV form={form} name="encryption_settings" fieldKey="ticket" label="Ticket time" placeholder="最长允许时间" show={rtt === '0rtt'} /></div></div>
      <SettingsKV form={form} name="encryption_settings" fieldKey="server_padding" label="Server Padding" placeholder="留空使用默认值100-111-1111.75-0-111.50-0-3333" />
      <SettingsKV form={form} name="encryption_settings" fieldKey="private_key" label="Private Key" placeholder="留空自动生成，需抗量子加密请自行替换" />
      <SettingsKV form={form} name="encryption_settings" fieldKey="client_padding" label="Client Padding" placeholder="留空使用默认值100-111-1111.75-0-111.50-0-3333" />
      <SettingsKV form={form} name="encryption_settings" fieldKey="password" label="Password" placeholder="留空自动生成，需抗量子加密请自行替换" />
    </div>;
  }
  const tlsNumber = Number(tls || 0);
  return <div className="legacy-child-editor">
    <SettingsKV form={form} name="tls_settings" fieldKey="server_name" label="Server Name(SNI)" placeholder={tlsNumber === 2 ? 'REALITY必填，与后端保持一致' : ''} aliases={realityAliases.server_name} />
    <SettingsKV form={form} name="tls_settings" fieldKey="cert_mode" label="证书模式Cert Mode" type="select" show={tlsNumber === 1} options={[{ label: '自签名', value: 'self' }, { label: 'HTTP申请', value: 'http' }, { label: 'DNS申请', value: 'dns' }, { label: '无证书(关闭TLS)', value: 'none' }]} />
    <SettingsKV form={form} name="tls_settings" fieldKey="provider" label="DNS解析提供商Provider" placeholder="书写格式cloudflare" show={tlsNumber === 1 && tlsSettings.cert_mode === 'dns'} />
    <SettingsKV form={form} name="tls_settings" fieldKey="dns_env" label="DNS env" placeholder="书写格式CF_DNS_API_TOKEN=xxxxxxx如有多条使用逗号,分隔" show={tlsNumber === 1 && tlsSettings.cert_mode === 'dns'} />
    <SettingsKV form={form} name="tls_settings" fieldKey="cert_file" label="证书公钥文件地址Cert File Path" placeholder="留空在/etc/v2node/目录自动生成" show={tlsNumber === 1 && tlsSettings.cert_mode !== 'none'} />
    <SettingsKV form={form} name="tls_settings" fieldKey="key_file" label="证书私钥文件地址Key File Path" placeholder="留空在/etc/v2node/目录自动生成" show={tlsNumber === 1 && tlsSettings.cert_mode !== 'none'} />
    <SettingsKV form={form} name="tls_settings" fieldKey="dest" label="Server Address" placeholder="REALITY目标地址,默认使用SNI" show={tlsNumber === 2} aliases={realityAliases.dest} />
    <SettingsKV form={form} name="tls_settings" fieldKey="server_port" label="Server Port" placeholder="REALITY目标端口,默认443" show={tlsNumber === 2} aliases={realityAliases.server_port} />
    <SettingsKV form={form} name="tls_settings" fieldKey="xver" label="Proxy Protocol" type="select" show={tlsNumber === 2} aliases={realityAliases.xver} options={[0, 1, 2].map((value) => ({ label: String(value), value }))} />
    <SettingsKV form={form} name="tls_settings" fieldKey="private_key" label="Private Key" placeholder="留空自动生成" show={tlsNumber === 2} aliases={realityAliases.private_key} />
    <SettingsKV form={form} name="tls_settings" fieldKey="public_key" label="Public Key" placeholder="留空自动生成" show={tlsNumber === 2} aliases={realityAliases.public_key} />
    <SettingsKV form={form} name="tls_settings" fieldKey="short_id" label="ShortId" placeholder="留空自动生成" show={tlsNumber === 2} aliases={realityAliases.short_id} />
    <SettingsKV form={form} name="tls_settings" fieldKey="fingerprint" label="FingerPrint" type="select" placeholder="TLS指纹默认Chrome" aliases={realityAliases.fingerprint} options={['chrome', 'firefox', 'safari', 'ios', 'android', 'edge', '360', 'qq'].map((value) => ({ label: value === 'ios' ? 'IOS' : value === 'qq' ? 'QQ' : value[0].toUpperCase() + value.slice(1), value }))} />
    <SettingsKV form={form} name="tls_settings" fieldKey="reject_unknown_sni" label="Reject unknown sni" type="switch" show={tlsNumber === 1} />
    <SettingsKV form={form} name="tls_settings" fieldKey="allow_insecure" label="Allow Insecure" type="switch" aliases={realityAliases.allow_insecure} />
    <SettingsKV form={form} name="tls_settings" fieldKey="ech" label="ECH (Encrypted Client Hello)" type="select" placeholder="选择 ECH 模式" options={[{ label: '无', value: '' }, { label: 'Cloudflare', value: 'cloudflare' }, { label: '自定义 SNI', value: 'custom' }]} />
    {tlsSettings.ech === 'cloudflare' && <div className="legacy-success-note">Cloudflare 托管 ECH，密钥由 Cloudflare 自动管理，客户端从 DNS 自动获取配置，服务端无需额外填写</div>}
    {tlsSettings.ech === 'custom' && <>
      <SettingsKV form={form} name="tls_settings" fieldKey="ech_server_name" label="ECH Server Name (伪装域名/外层SNI)" placeholder="留空则关闭自定义 ECH" />
      <SettingsKV form={form} name="tls_settings" fieldKey="ech_key" label="ECH Key (服务端私钥)" placeholder="留空自动生成" />
      <SettingsKV form={form} name="tls_settings" fieldKey="ech_config" label="ECH Config (客户端配置)" placeholder="留空自动生成" />
    </>}
  </div>;
}


export default function ServerManage() {
  const [rows, setRows] = useState<any[]>([]);
  const [groups, setGroups] = useState<any[]>([]);
  const [routes, setRoutes] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [sortMode, setSortMode] = useState(false);
  const [searchKey, setSearchKey] = useState('');
  const [bulkOpen, setBulkOpen] = useState(false);
  const [addOpen, setAddOpen] = useState(false);
  const [bulkOldHost, setBulkOldHost] = useState('');
  const [bulkNewHost, setBulkNewHost] = useState('');
  const [edit, setEdit] = useState<any>(null);
  const [detail, setDetail] = useState<any>(null);
  const [childEditor, setChildEditor] = useState<{ title: string; type: ChildEditorType } | null>(null);
  const [draggingKey, setDraggingKey] = useState<string | null>(null);
  const [tablePage, setTablePage] = useState({ current: 1, pageSize: 10 });
  const [form] = Form.useForm();
  const watchType = Form.useWatch('type', form);
  const watchProtocol = Form.useWatch('protocol', form);
  const watchTLS = Form.useWatch('tls', form);
  const watchNetwork = Form.useWatch('network', form);
  const watchHost = Form.useWatch('host', form);
  const currentType = watchType || edit?.type || 'v2node';

  const load = async () => {
    setLoading(true);
    try {
      const [nodeRes, groupRes, routeRes] = await Promise.all([
        apiGet('/server/manage/getNodes'),
        apiGet('/server/group/fetch').catch(() => ({ data: [] })),
        apiGet('/server/route/fetch').catch(() => ({ data: [] })),
      ]);
      setRows(nodeRes.data || []);
      setGroups(groupRes.data || []);
      setRoutes(routeRes.data || []);
    } catch (e: any) {
      message.error(e.message || '加载失败');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { load(); }, []);

  const filteredRows = useMemo(() => {
    const key = searchKey.trim();
    if (!key) return rows;
    return rows.filter((row) => JSON.stringify(row).includes(key));
  }, [rows, searchKey]);

  useEffect(() => {
    setTablePage((page) => ({ ...page, current: 1 }));
  }, [searchKey, rows.length]);

  const sortRows = useMemo(() => rows.slice(), [rows]);

  const openEditor = (row?: any, type = 'v2node') => {
    const initial = normalizeInitial(row || baseDefaults(type));
    setEdit(initial);
    form.resetFields();
    form.setFieldsValue(initial);
  };

  const save = async () => {
    const values = await form.validateFields();
    const type = values.type || edit?.type;
    await apiPost(`/server/${type}/save`, normalizePayload(values, edit));
    message.success('保存成功');
    setEdit(null);
    setChildEditor(null);
    load();
  };

  const update = async (row: any, key: string, value: any) => {
    await apiPost(`/server/${row.type}/update`, { id: row.id, [key]: value });
    message.success('已更新');
    load();
  };

  const saveSort = async (nextRows: any[]) => {
    const payload: Record<string, Record<string, number>> = {};
    nextRows.forEach((row, index) => {
      if (!payload[row.type]) payload[row.type] = {};
      payload[row.type][row.id] = index;
    });
    await apiPost('/server/manage/sort', payload);
    message.success('排序已保存');
    load();
  };

  const handleDragStart = (row: any) => {
    setDraggingKey(`${row.type}-${row.id}`);
  };

  const handleDrop = async (targetRow: any) => {
    if (!sortMode || !draggingKey) return;
    const targetKey = `${targetRow.type}-${targetRow.id}`;
    if (draggingKey === targetKey) {
      setDraggingKey(null);
      return;
    }
    const currentRows = sortRows.slice();
    const fromIndex = currentRows.findIndex((row) => `${row.type}-${row.id}` === draggingKey);
    const toIndex = currentRows.findIndex((row) => `${row.type}-${row.id}` === targetKey);
    if (fromIndex < 0 || toIndex < 0) {
      setDraggingKey(null);
      return;
    }
    const nextRows = moveItem(currentRows, fromIndex, toIndex);
    if (!searchKey.trim()) {
      setRows(nextRows);
    } else {
      setRows((prev) => {
        const nextKeys = new Set(nextRows.map((row) => `${row.type}-${row.id}`));
        const nextMap = new Map(nextRows.map((row) => [`${row.type}-${row.id}`, row]));
        const untouched = prev.filter((row) => !nextKeys.has(`${row.type}-${row.id}`));
        return [...nextRows.map((row) => nextMap.get(`${row.type}-${row.id}`) || row), ...untouched];
      });
    }
    setDraggingKey(null);
    await saveSort(nextRows);
  };

  const copy = async (row: any) => {
    await apiPost(`/server/${row.type}/copy`, { id: row.id }, { form: true });
    message.success('已复制');
    load();
  };

  const drop = async (row: any) => {
    await apiPost(`/server/${row.type}/drop`, { id: row.id }, { form: true });
    message.success('已删除');
    load();
  };

  const bulkUpdateHost = async () => {
    const oldHost = bulkOldHost.trim();
    const newHost = bulkNewHost.trim();
    if (!oldHost) return message.error('原地址不能为空');
    if (!newHost) return message.error('新地址不能为空');
    if (oldHost === newHost) return message.error('新旧地址不能相同');
    const res = await apiPost('/server/manage/updateHost', { old_host: oldHost, new_host: newHost });
    const total = res.data?.updated_total || 0;
    message.success(total > 0 ? `已批量修改 ${total} 个节点地址` : '未找到匹配原地址的节点');
    setBulkOpen(false); setBulkOldHost(''); setBulkNewHost(''); load();
  };

  const addOptions = serverTypes.map((type) => ({
    key: type,
    label: type === 'v2node' ? 'V2node' : type === 'vmess' ? 'VMess' : type === 'anytls' ? 'AnyTLS' : type[0].toUpperCase() + type.slice(1),
  }));

  const columns: any[] = sortMode ? [
    { title: '排序', dataIndex: 'sort', width: 100, render: () => <MenuOutlined className="drag-handle" title="拖动排序" /> },
    { title: '节点ID', dataIndex: 'id', width: 150, render: (id: any, row: any) => typeTag(row.type, row.parent_id ? `${id} => ${row.parent_id}` : id) },
    { title: '节点', dataIndex: 'name' },
  ] : [
    { title: '节点ID', dataIndex: 'id', width: 150, filters: serverTypes.map((value) => ({ text: value, value })), onFilter: (value: any, row: any) => row.type === value, render: (id: any, row: any) => typeTag(row.type, row.parent_id ? `${id} => ${row.parent_id}` : id) },
    { title: '显隐', dataIndex: 'show', width: 90, render: (show: any, row: any) => <Switch size="small" checked={!!Number(show)} onClick={() => update(row, 'show', Number(show) ? 0 : 1)} /> },
    { title: <Tooltip title={<div><Badge status="error" /> 未运行<br /><Badge status="warning" /> 无人使用或服务端上报异常<br /><Badge status="success" /> 运行正常</div>}>节点 <QuestionCircleOutlined /></Tooltip>, dataIndex: 'name', render: (name: any, row: any) => <Space><Badge status={statusMap[Number(row.available_status)] || 'default'} /><span>{name}</span></Space> },
    { title: '地址', dataIndex: 'host', render: (_: any, row: any) => <a onClick={() => { navigator.clipboard?.writeText(`${row.host}:${row.port}`); message.success('复制成功'); }}>{row.host}:{row.port}</a> },
    { title: <Tooltip title="根据服务端上报频率而定">人数 <QuestionCircleOutlined /></Tooltip>, dataIndex: 'online', sorter: (a: any, b: any) => Number(a.online || 0) - Number(b.online || 0), width: 130, render: (online: any) => <span><UserOutlined /> {online || 0}</span> },
    { title: <Tooltip title="使用的流量将乘以倍率进行扣除">倍率 <QuestionCircleOutlined /></Tooltip>, dataIndex: 'rate', align: 'center', width: 100, render: (rate: any) => <Tag style={{ minWidth: 60 }}>{rate} x</Tag> },
    { title: '权限组', dataIndex: 'group_id', filters: groups.map((group) => ({ text: group.name, value: group.id })), onFilter: (value: any, row: any) => listValue(row.group_id).includes(String(value)), render: (_: any, row: any) => listValue(row.group_id).map((id) => <Tag key={id}>{groups.find((g) => String(g.id) === String(id))?.name || id}</Tag>) },
    { title: '监听地址', width: 150, render: (_: any, row: any) => row.type === 'v2node' ? <span className="muted-lines">{row.listen_ip || '-'}</span> : '-' },
    { title: '操作', dataIndex: 'action', fixed: 'right', align: 'right', width: 120, render: (_: any, row: any) => <Dropdown trigger={['click']} menu={{ items: [
      { key: 'edit', label: <span><EditOutlined /> 编辑</span>, onClick: () => openEditor(row) },
      { key: 'copy', label: <span><CopyOutlined /> 复制</span>, onClick: () => copy(row) },
      { key: 'detail', label: '详情', onClick: () => setDetail(row) },
      { type: 'divider' as const },
      { key: 'delete', danger: true, label: <Popconfirm title="确认删除？" onConfirm={() => drop(row)}><span><DeleteOutlined /> 删除</span></Popconfirm> },
    ] }}><a>操作 <DownOutlined /></a></Dropdown> },
  ];

  return <div className="legacy-page server-manage-page">
    <div className="content-heading">节点管理</div>
    <Card className="block-card" styles={{ body: { padding: 0 } }}>
      <div className="forest-table-action">
        <span className="add-node-wrap">
          <Button icon={<PlusOutlined />} onClick={() => setAddOpen((open) => !open)} />
          {addOpen && <div className="add-node-menu">
            {addOptions.map((item) => <button key={item.key} type="button" className="add-node-menu-item" onClick={() => { setAddOpen(false); openEditor(undefined, item.key); }}>
              {typeTag(item.key, item.label)}
            </button>)}
          </div>}
        </span>
        <Input placeholder="输入任意关键字搜索" className="ml-2" style={{ width: 200 }} value={searchKey} onChange={(e) => setSearchKey(e.target.value)} />
        <Button className="ml-2" onClick={() => setBulkOpen(!bulkOpen)}>{bulkOpen ? '收起批量修改地址' : '展开批量修改地址'}</Button>
        <Button className="float-right" type="primary" onClick={() => setSortMode(!sortMode)}>{sortMode ? '保存排序' : '编辑排序'}</Button>
      </div>
      {bulkOpen && <div className="bulk-host-editor">
        <Input placeholder="原地址筛选" style={{ width: 220 }} value={bulkOldHost} onChange={(e) => setBulkOldHost(e.target.value)} />
        <Input placeholder="新地址" style={{ width: 220 }} value={bulkNewHost} onChange={(e) => setBulkNewHost(e.target.value)} onPressEnter={bulkUpdateHost} />
        <Button type="primary" loading={loading} onClick={bulkUpdateHost}>批量修改地址</Button>
        <Button onClick={() => setBulkOpen(false)}>取消</Button>
      </div>}
      <Table
        className="forest-table"
        rowKey={(row) => `${row.type}-${row.id}`}
        loading={loading}
        tableLayout="auto"
        columns={columns}
        dataSource={sortMode ? sortRows : filteredRows}
        pagination={!sortMode && {
          current: tablePage.current,
          pageSize: tablePage.pageSize,
          total: filteredRows.length,
          pageSizeOptions: ['10', '50', '100', '500'],
          showSizeChanger: true,
        }}
        scroll={{ x: 1300 }}
        onChange={(pagination: any) => {
          setTablePage({
            current: pagination.current || 1,
            pageSize: pagination.pageSize || tablePage.pageSize,
          });
        }}
        rowClassName={(row) => `${row.parent_id ? 'child_node' : ''} ${sortMode ? 'sortable-row' : ''} ${draggingKey === `${row.type}-${row.id}` ? 'dragging-row' : ''}`}
        onRow={(row) => sortMode ? {
          draggable: true,
          onDragStart: () => handleDragStart(row),
          onDragOver: (event) => event.preventDefault(),
          onDrop: () => handleDrop(row),
          onDragEnd: () => setDraggingKey(null),
        } : {}}
      />
    </Card>

    <Drawer className="legacy-drawer fixed-action-drawer" title={edit?.id ? '编辑节点' : '新建节点'} width="80%" open={!!edit} onClose={() => { setEdit(null); setChildEditor(null); }} maskClosable>
      <div className="fixed-action-drawer-body"><Form form={form} layout="vertical" className="legacy-server-form">
        <div className="form-grid">
          <div className="form-col-8"><Form.Item name="name" label="节点名称" rules={[{ required: true }]}><Input placeholder="请输入节点名称" /></Form.Item></div>
          <div className="form-col-4"><Form.Item name="rate" label="倍率" rules={[{ required: true }]}><InputNumber addonAfter="x" style={{ width: '100%' }} placeholder="请输入节点倍率" /></Form.Item></div>
          <div className="form-col-24"><Form.Item name="tags" label="节点标签"><Select mode="tags" placeholder="输入后回车添加标签" /></Form.Item></div>
          <div className="form-col-24"><Form.Item name="group_id" label="权限组" rules={[{ required: true }]}><Select mode="multiple" placeholder="请选择权限组" options={groups.map((group) => ({ label: group.name, value: String(group.id) }))} /></Form.Item></div>
          <div className="form-col-12"><Form.Item name="host" label={currentType === 'v2node' ? '连接地址' : '节点地址'} rules={[{ required: true }]}><Input placeholder={currentType === 'v2node' || currentType === 'anytls' ? '地址或IP' : '请输入连接地址'} /></Form.Item></div>
          {currentType === 'v2node' && <div className="form-col-12"><Form.Item name="listen_ip" label="监听地址"><Input placeholder="地址或IP默认为0.0.0.0" /></Form.Item></div>}
          <div className="form-col-12"><Form.Item name="port" label="连接端口" rules={[{ required: true }]}><Input placeholder="用户连接端口" /></Form.Item></div>
          <div className="form-col-12"><Form.Item name="server_port" label="服务端端口" rules={[{ required: true }]}><InputNumber style={{ width: '100%' }} placeholder="服务端开放端口" /></Form.Item></div>
          <Form.Item name="type" hidden><Input /></Form.Item>
          <ProtocolFields type={currentType} protocol={watchProtocol} tls={watchTLS} network={watchNetwork} form={form} onEditChild={(title, type) => setChildEditor({ title, type })} />
          <div className="form-col-12"><Form.Item name="parent_id" label="父节点"><Select allowClear placeholder="无" options={rows.filter((row) => row.type === currentType && row.id !== edit?.id).map((row) => ({ label: row.name, value: row.id }))} /></Form.Item></div>
          <div className="form-col-12"><Form.Item name="show" label="显示"><Select options={[{ label: '显示', value: 1 }, { label: '隐藏', value: 0 }]} /></Form.Item></div>
          <div className="form-col-24"><Form.Item name="route_id" label="路由组"><Select mode="multiple" placeholder="请选择路由组" options={routes.map((route) => ({ label: route.remarks || route.name || route.id, value: String(route.id) }))} /></Form.Item></div>
          {currentType === 'v2node' && <DDNSFields form={form} host={watchHost} />}
          {currentType === 'v2node' && <div className="form-col-24"><Form.Item name="install_command" label="一键安装指令"><Input.TextArea rows={4} readOnly style={{ backgroundColor: '#f5f5f5', cursor: 'text' }} /></Form.Item></div>}
        </div>
      </Form></div>
      <div className="forest-drawer-action"><Space><Button onClick={() => { setEdit(null); setChildEditor(null); }}>取消</Button><Button loading={loading} type="primary" onClick={save}>提交</Button></Space></div>
    </Drawer>
    <Drawer className="legacy-drawer legacy-child-drawer" closable={false} title={childEditor?.title} width="80%" open={!!childEditor} onClose={() => setChildEditor(null)} maskClosable>
      {childEditor && <Form form={form} layout="vertical"><ChildEditor type={childEditor.type} form={form} tls={watchTLS} network={watchNetwork} /></Form>}
    </Drawer>
    <JsonModal open={!!detail} title="节点详情" data={detail} onClose={() => setDetail(null)} />
  </div>;
}
