import React, { useEffect, useMemo, useState } from 'react';
import {
  Alert, Button, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Spin, Switch, Table, Tag, Tooltip, message,
} from 'antd';
import {
  ArrowLeftOutlined, CloudServerOutlined, PlusOutlined, ReloadOutlined, SearchOutlined, SettingOutlined,
} from '@ant-design/icons';
import { apiGet, apiPost } from '../lib/api';

type DNSPodConfigStatus = {
  configured: boolean;
  secret_id_masked?: string;
  credential_masked?: string;
  source?: string;
  edition?: 'international' | 'china';
  auth_type?: 'tc3' | 'token';
};

const editionOptions = [
  { label: 'DNSPod 国际版', value: 'international' },
  { label: 'DNSPod 中国版', value: 'china' },
];

const authTypeOptions = [
  { label: 'DNSPod 国际版 API Token', value: 'token' },
  { label: '腾讯云 API 3.0', value: 'tc3' },
];

function editionLabel(edition?: string) {
  return edition === 'china' ? 'DNSPod 中国版' : 'DNSPod 国际版';
}

function authTypeLabel(authType?: string) {
  return authType === 'token' ? 'Token API' : 'API 3.0';
}

type DomainRow = {
  id: number;
  name: string;
  status: string;
  dnsStatus: string;
  grade: string;
  gradeTitle: string;
  recordCount: number;
  effectiveDNS: string[];
  updatedOn: string;
  raw: any;
};

type RecordRow = {
  id: number;
  name: string;
  type: string;
  value: string;
  line: string;
  lineId: string;
  status: string;
  ttl: number;
  mx: number;
  weight?: number;
  updatedOn: string;
  raw: any;
};

type LineOption = { label: string; value: string; lineName: string };

const lineNameTranslations: Record<string, string> = {
  default: '默认',
  global: '全球',
  worldwide: '全球',
  china: '中国',
  domestic: '中国大陆',
  mainland: '中国大陆',
  oversea: '海外',
  overseas: '海外',
  'china mobile': '中国移动',
  'china unicom': '中国联通',
  'china telecom': '中国电信',
  'china education': '中国教育网',
  'china education network': '中国教育网',
  cmcc: '中国移动',
  cucc: '中国联通',
  ctcc: '中国电信',
  cernet: '中国教育网',
  mobile: '移动',
  unicom: '联通',
  telecom: '电信',
  education: '教育网',
  asia: '亚洲',
  europe: '欧洲',
  africa: '非洲',
  oceania: '大洋洲',
  'north america': '北美洲',
  'south america': '南美洲',
  'middle east': '中东',
  'hong kong': '中国香港',
  macao: '中国澳门',
  macau: '中国澳门',
  taiwan: '中国台湾',
};

const regionDisplayNames = typeof Intl !== 'undefined' && typeof Intl.DisplayNames === 'function'
  ? new Intl.DisplayNames(['zh-CN'], { type: 'region' })
  : null;

function localizeDNSPodLine(name: string, lineId = '') {
  const rawName = String(name || '').trim();
  const rawID = String(lineId || '').trim();
  const translated = lineNameTranslations[rawName.toLowerCase()] || lineNameTranslations[rawID.toLowerCase()];
  if (translated) return translated;

  const regionCode = /^[a-z]{2}$/i.test(rawID) ? rawID : (/^[a-z]{2}$/i.test(rawName) ? rawName : '');
  if (regionCode && regionDisplayNames) {
    const localized = regionDisplayNames.of(regionCode.toUpperCase());
    if (localized && localized.toUpperCase() !== regionCode.toUpperCase()) return localized;
  }
  return rawName || rawID || '默认';
}

function field(row: any, ...keys: string[]) {
  for (const key of keys) {
    if (row?.[key] !== undefined && row?.[key] !== null) return row[key];
  }
  return undefined;
}

function normalizeDomain(row: any): DomainRow {
  return {
    id: Number(field(row, 'DomainId', 'domain_id', 'id') || 0),
    name: String(field(row, 'Name', 'name') || ''),
    status: String(field(row, 'Status', 'status') || ''),
    dnsStatus: String(field(row, 'DNSStatus', 'dns_status') || ''),
    grade: String(field(row, 'Grade', 'grade') || 'DP_FREE'),
    gradeTitle: String(field(row, 'GradeTitle', 'grade_title') || ''),
    recordCount: Number(field(row, 'RecordCount', 'record_count') || 0),
    effectiveDNS: field(row, 'EffectiveDNS', 'effective_dns') || [],
    updatedOn: String(field(row, 'UpdatedOn', 'updated_on') || ''),
    raw: row,
  };
}

function normalizeRecord(row: any): RecordRow {
  const weight = field(row, 'Weight', 'weight');
  return {
    id: Number(field(row, 'RecordId', 'record_id', 'id') || 0),
    name: String(field(row, 'Name', 'name') || '@'),
    type: String(field(row, 'Type', 'type') || ''),
    value: String(field(row, 'Value', 'value') || ''),
    line: String(field(row, 'Line', 'line') || '默认'),
    lineId: String(field(row, 'LineId', 'line_id') || ''),
    status: String(field(row, 'Status', 'status') || ''),
    ttl: Number(field(row, 'TTL', 'ttl') || 600),
    mx: Number(field(row, 'MX', 'mx') || 0),
    weight: weight === undefined || weight === null ? undefined : Number(weight),
    updatedOn: String(field(row, 'UpdatedOn', 'updated_on') || ''),
    raw: row,
  };
}

function flattenLines(lines: any[], prefix = ''): LineOption[] {
  const result: LineOption[] = [];
  (lines || []).forEach((line) => {
    const lineName = String(field(line, 'LineName', 'line_name', 'name') || '默认');
    const lineId = String(field(line, 'LineId', 'line_id', 'id') || lineName);
    const localizedName = localizeDNSPodLine(lineName, lineId);
    const label = prefix ? `${prefix} / ${localizedName}` : localizedName;
    result.push({ label, value: lineId, lineName });
    const children = field(line, 'SubGroup', 'sub_group');
    if (Array.isArray(children) && children.length) result.push(...flattenLines(children, label));
  });
  return result.filter((item, index, all) => all.findIndex((other) => other.value === item.value) === index);
}

function statusTag(status: string, enabledText = '正常') {
  const normalized = String(status || '').toUpperCase();
  if (['ENABLE', 'ENABLED', 'DNSDONE', 'NORMAL'].includes(normalized)) return <Tag color="success">{enabledText}</Tag>;
  if (['DISABLE', 'DISABLED', 'PAUSE'].includes(normalized)) return <Tag>已暂停</Tag>;
  if (!normalized) return <Tag>未知</Tag>;
  return <Tag color="warning">{status}</Tag>;
}

export default function DNSPodPage() {
  const [config, setConfig] = useState<DNSPodConfigStatus>({ configured: false });
  const [configLoading, setConfigLoading] = useState(true);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [settingsSaving, setSettingsSaving] = useState(false);
  const [domains, setDomains] = useState<DomainRow[]>([]);
  const [domainsLoading, setDomainsLoading] = useState(false);
  const [domainKeyword, setDomainKeyword] = useState('');
  const [domainQuery, setDomainQuery] = useState('');
  const [domainPage, setDomainPage] = useState(1);
  const [domainPageSize, setDomainPageSize] = useState(20);
  const [domainTotal, setDomainTotal] = useState(0);
  const [selectedDomain, setSelectedDomain] = useState<DomainRow | null>(null);
  const [records, setRecords] = useState<RecordRow[]>([]);
  const [recordsLoading, setRecordsLoading] = useState(false);
  const [recordKeyword, setRecordKeyword] = useState('');
  const [recordQuery, setRecordQuery] = useState('');
  const [recordTypeFilter, setRecordTypeFilter] = useState('');
  const [recordPage, setRecordPage] = useState(1);
  const [recordPageSize, setRecordPageSize] = useState(20);
  const [recordTotal, setRecordTotal] = useState(0);
  const [recordTypes, setRecordTypes] = useState<string[]>([]);
  const [lineOptions, setLineOptions] = useState<LineOption[]>([]);
  const [linesLoading, setLinesLoading] = useState(false);
  const [editingRecord, setEditingRecord] = useState<RecordRow | null | undefined>(undefined);
  const [recordSaving, setRecordSaving] = useState(false);
  const [settingsForm] = Form.useForm();
  const [recordForm] = Form.useForm();
  const watchedRecordType = Form.useWatch('record_type', recordForm);
  const watchedAuthType = Form.useWatch('auth_type', settingsForm);

  const loadConfig = async () => {
    setConfigLoading(true);
    try {
      const response = await apiGet('/dns/config');
      setConfig(response.data || { configured: false });
    } catch (error: any) {
      message.error(error.message || '读取 DNSPod 配置失败');
    } finally {
      setConfigLoading(false);
    }
  };

  const loadDomains = async () => {
    if (!config.configured) return;
    setDomainsLoading(true);
    try {
      const response = await apiGet('/dns/domain/list', {
        current: domainPage, page_size: domainPageSize, keyword: domainQuery,
      });
      setDomains((response.data || []).map(normalizeDomain));
      setDomainTotal(Number(response.total || 0));
    } catch (error: any) {
      message.error(error.message || '加载域名失败');
    } finally {
      setDomainsLoading(false);
    }
  };

  const loadRecords = async () => {
    if (!selectedDomain) return;
    setRecordsLoading(true);
    try {
      const response = await apiGet('/dns/record/list', {
        domain: selectedDomain.name,
        domain_id: selectedDomain.id,
        current: recordPage,
        page_size: recordPageSize,
        keyword: recordQuery,
        record_type: recordTypeFilter,
      });
      setRecords((response.data || []).map(normalizeRecord));
      setRecordTotal(Number(response.total || 0));
    } catch (error: any) {
      message.error(error.message || '加载解析记录失败');
    } finally {
      setRecordsLoading(false);
    }
  };

  const loadRecordTypes = async (domain: DomainRow) => {
    try {
      const response = await apiGet('/dns/record/types', { domain_grade: domain.grade });
      setRecordTypes((response.data || []).map((item: any) => String(item)));
    } catch (error: any) {
      message.error(error.message || '加载记录类型失败');
      setRecordTypes([]);
    }
  };

  const loadRecordLines = async (recordType: string, currentLineId = '', currentLineName = '') => {
    if (!selectedDomain || !recordType) return;
    setLinesLoading(true);
    const defaultLine: LineOption = config.auth_type === 'token'
      ? { label: '默认', value: 'default', lineName: 'Default' }
      : { label: '默认', value: '0=0', lineName: '默认' };
    try {
      const response = await apiGet('/dns/record/lines', {
        domain: selectedDomain.name,
        domain_grade: selectedDomain.grade,
        record_type: recordType,
      });
      const loadedOptions = flattenLines(response.data || []);
      const options = loadedOptions.length ? loadedOptions : [defaultLine];
      setLineOptions(options);
      let selected = currentLineId || recordForm.getFieldValue('record_line_id');
      if (!selected && currentLineName) {
        const normalizedLine = currentLineName.trim().toLowerCase();
        selected = options.find((item) => item.lineName.trim().toLowerCase() === normalizedLine || item.label.trim().toLowerCase() === normalizedLine)?.value;
      }
      if (!selected || !options.some((item) => item.value === selected)) selected = options[0]?.value;
      if (selected) recordForm.setFieldValue('record_line_id', selected);
    } catch (error: any) {
      setLineOptions([defaultLine]);
      recordForm.setFieldValue('record_line_id', defaultLine.value);
      message.warning(`${error.message || '加载解析线路失败'}，已使用默认线路`);
    } finally {
      setLinesLoading(false);
    }
  };

  useEffect(() => { loadConfig(); }, []);
  useEffect(() => { if (config.configured) loadDomains(); }, [config.configured, domainPage, domainPageSize, domainQuery]);
  useEffect(() => { if (selectedDomain) loadRecords(); }, [selectedDomain, recordPage, recordPageSize, recordQuery, recordTypeFilter]);
  useEffect(() => {
    if (editingRecord !== undefined && watchedRecordType) loadRecordLines(watchedRecordType, editingRecord?.lineId || '', editingRecord?.line || '');
  }, [watchedRecordType, editingRecord, selectedDomain]);

  const openSettings = () => {
    settingsForm.resetFields();
    settingsForm.setFieldsValue({
      auth_type: config.configured ? (config.auth_type || 'tc3') : 'token',
      api_token: '',
      secret_id: '',
      secret_key: '',
      edition: config.edition || 'international',
      verify: true,
    });
    setSettingsOpen(true);
  };

  const saveSettings = async () => {
    setSettingsSaving(true);
    try {
      const values = await settingsForm.validateFields();
      const response = await apiPost('/dns/config/save', values);
      setConfig(response.data || { configured: true });
      setSettingsOpen(false);
      message.success('DNSPod 配置已保存');
      setDomainPage(1);
      setDomainQuery('');
    } catch (error: any) {
      if (!error?.errorFields) message.error(error.message || '保存 DNSPod 配置失败');
    } finally {
      setSettingsSaving(false);
    }
  };

  const testSettings = async () => {
    setSettingsSaving(true);
    try {
      const values = settingsForm.getFieldsValue();
      await apiPost('/dns/config/test', values);
      message.success('连接 DNSPod 成功');
    } catch (error: any) {
      message.error(error.message || '连接 DNSPod 失败');
    } finally {
      setSettingsSaving(false);
    }
  };

  const clearSettings = async () => {
    setSettingsSaving(true);
    try {
      const response = await apiPost('/dns/config/save', { clear: true });
      setConfig(response.data || { configured: false });
      setDomains([]);
      setSelectedDomain(null);
      setSettingsOpen(false);
      message.success('已清除 DNSPod 配置');
    } catch (error: any) {
      message.error(error.message || '清除 DNSPod 配置失败');
    } finally {
      setSettingsSaving(false);
    }
  };

  const enterDomain = (domain: DomainRow) => {
    setSelectedDomain(domain);
    setRecordKeyword('');
    setRecordQuery('');
    setRecordTypeFilter('');
    setRecordPage(1);
    setRecordTypes([]);
    loadRecordTypes(domain);
  };

  const openRecord = (record?: RecordRow) => {
    setEditingRecord(record || null);
    setLineOptions([]);
    recordForm.resetFields();
    recordForm.setFieldsValue(record ? {
      record_id: record.id,
      sub_domain: record.name,
      record_type: record.type,
      record_line_id: record.lineId,
      value: record.value,
      ttl: record.ttl,
      mx: record.mx,
      weight: record.weight,
    } : {
      sub_domain: '@',
      record_type: recordTypes[0] || 'A',
      ttl: 600,
      mx: 0,
    });
  };

  const saveRecord = async () => {
    if (!selectedDomain) return;
    setRecordSaving(true);
    try {
      const values = await recordForm.validateFields();
      const selectedLine = lineOptions.find((item) => item.value === values.record_line_id);
      await apiPost('/dns/record/save', {
        ...values,
        domain: selectedDomain.name,
        domain_id: selectedDomain.id,
        record_id: editingRecord?.id || 0,
        record_line: selectedLine?.lineName || editingRecord?.line || '默认',
      });
      setEditingRecord(undefined);
      message.success(editingRecord?.id ? '解析记录已更新' : '解析记录已添加');
      loadRecords();
    } catch (error: any) {
      if (!error?.errorFields) message.error(error.message || '保存解析记录失败');
    } finally {
      setRecordSaving(false);
    }
  };

  const toggleRecord = async (record: RecordRow, enabled: boolean) => {
    if (!selectedDomain) return;
    try {
      await apiPost('/dns/record/status', {
        domain: selectedDomain.name,
        domain_id: selectedDomain.id,
        record_id: record.id,
        status: enabled ? 'ENABLE' : 'DISABLE',
      });
      message.success(enabled ? '解析记录已启用' : '解析记录已暂停');
      loadRecords();
    } catch (error: any) {
      message.error(error.message || '修改记录状态失败');
    }
  };

  const deleteRecord = async (record: RecordRow) => {
    if (!selectedDomain) return;
    try {
      await apiPost('/dns/record/delete', { domain: selectedDomain.name, domain_id: selectedDomain.id, record_id: record.id });
      message.success('解析记录已删除');
      loadRecords();
    } catch (error: any) {
      message.error(error.message || '删除解析记录失败');
    }
  };

  const domainColumns = useMemo(() => [
    {
      title: '域名', dataIndex: 'name', minWidth: 220,
      render: (name: string, row: DomainRow) => <a className="dnspod-domain-link" onClick={() => enterDomain(row)}>{name}</a>,
    },
    { title: '状态', dataIndex: 'status', width: 100, render: (value: string) => statusTag(value) },
    {
      title: 'DNS 状态', dataIndex: 'dnsStatus', width: 130,
      render: (value: string) => statusTag(value, 'DNS 正常'),
    },
    { title: '套餐', dataIndex: 'gradeTitle', width: 140, render: (value: string, row: DomainRow) => value || row.grade || '-' },
    { title: '记录数', dataIndex: 'recordCount', width: 100 },
    {
      title: 'DNS 服务器', dataIndex: 'effectiveDNS', minWidth: 260,
      render: (values: string[]) => <Tooltip title={(values || []).join('\n')}><span className="dnspod-muted-cell">{(values || []).join('、') || '-'}</span></Tooltip>,
    },
    { title: '更新时间', dataIndex: 'updatedOn', width: 190, render: (value: string) => value || '-' },
    { title: '操作', key: 'action', fixed: 'right' as const, align: 'right' as const, width: 90, render: (_: any, row: DomainRow) => <a onClick={() => enterDomain(row)}>管理解析</a> },
  ], []);

  const recordColumns = useMemo(() => [
    { title: '主机记录', dataIndex: 'name', width: 150, render: (value: string) => <strong>{value || '@'}</strong> },
    { title: '类型', dataIndex: 'type', width: 90, render: (value: string) => <Tag>{value}</Tag> },
    { title: '线路', dataIndex: 'line', width: 160, render: (value: string, row: RecordRow) => localizeDNSPodLine(value, row.lineId) },
    { title: '记录值', dataIndex: 'value', minWidth: 260, ellipsis: true },
    { title: 'TTL', dataIndex: 'ttl', width: 90 },
    { title: 'MX', dataIndex: 'mx', width: 80, render: (value: number, row: RecordRow) => row.type === 'MX' ? value : '-' },
    { title: '更新时间', dataIndex: 'updatedOn', width: 190, render: (value: string) => value || '-' },
    {
      title: '状态', dataIndex: 'status', width: 90,
      render: (value: string, row: RecordRow) => <Switch size="small" checked={String(value).toUpperCase() === 'ENABLE'} onChange={(checked) => toggleRecord(row, checked)} />,
    },
    {
      title: '操作', key: 'action', fixed: 'right' as const, align: 'right' as const, width: 125,
      render: (_: any, row: RecordRow) => <Space split={<span className="ant-divider ant-divider-vertical" />}>
        <a onClick={() => openRecord(row)}>编辑</a>
        <Popconfirm title="确定删除这条解析记录吗？" description="删除后将立即影响域名解析。" onConfirm={() => deleteRecord(row)}><a className="dnspod-danger-link">删除</a></Popconfirm>
      </Space>,
    },
  ], [selectedDomain, lineOptions]);

  const recordTypeOptions = recordTypes.map((type) => ({ label: type, value: type }));
  const isMX = String(watchedRecordType || '').toUpperCase() === 'MX';

  return <div className="legacy-page dnspod-page">
    <div className="content-heading">域名解析</div>
    <Spin spinning={configLoading}>
      {!config.configured ? <Alert
        className="legacy-alert"
        type="warning"
        showIcon
        message="尚未配置 DNSPod"
        description="可使用 DNSPod 国际版 ID,Token，或腾讯云 API 3.0 SecretId/SecretKey 管理域名与解析记录。"
        action={<Button icon={<SettingOutlined />} onClick={openSettings}>立即配置</Button>}
      /> : null}

      <div className="block block-rounded">
        <div className="dnspod-account-bar">
          <div className="dnspod-account-copy">
            <CloudServerOutlined />
            <div>
              <div className="dnspod-account-title">{editionLabel(config.edition)} {authTypeLabel(config.auth_type)}</div>
              <div className="dnspod-account-subtitle">
                {config.configured ? <>已连接 · {config.credential_masked || config.secret_id_masked || '凭证已配置'}{config.source === 'env' ? ' · 环境变量' : ''}</> : '等待配置账号凭证'}
              </div>
            </div>
          </div>
          <Button icon={<SettingOutlined />} onClick={openSettings}>账号设置</Button>
        </div>

        {selectedDomain ? <>
          <div className="dnspod-view-head">
            <div className="dnspod-view-title">
              <Button type="text" icon={<ArrowLeftOutlined />} onClick={() => setSelectedDomain(null)}>返回域名</Button>
              <span className="dnspod-view-divider" />
              <div><strong>{selectedDomain.name}</strong><span>{selectedDomain.gradeTitle || selectedDomain.grade}</span></div>
            </div>
            <Space wrap>
              <Input
                allowClear
                value={recordKeyword}
                onChange={(event) => setRecordKeyword(event.target.value)}
                onPressEnter={() => { setRecordPage(1); setRecordQuery(recordKeyword.trim()); }}
                placeholder="搜索主机记录或记录值"
                prefix={<SearchOutlined />}
                className="dnspod-search"
              />
              <Select allowClear value={recordTypeFilter || undefined} placeholder="全部类型" options={recordTypeOptions} onChange={(value) => { setRecordPage(1); setRecordTypeFilter(value || ''); }} className="dnspod-type-filter" />
              <Button onClick={() => { setRecordPage(1); setRecordQuery(recordKeyword.trim()); }}>搜索</Button>
              <Button icon={<ReloadOutlined />} onClick={loadRecords}>刷新</Button>
              <Button type="primary" icon={<PlusOutlined />} onClick={() => openRecord()}>添加记录</Button>
            </Space>
          </div>
          <Table
            className="forest-table dnspod-record-table"
            rowKey="id"
            loading={recordsLoading}
            dataSource={records}
            columns={recordColumns}
            scroll={{ x: 1250 }}
            pagination={{
              current: recordPage, pageSize: recordPageSize, total: recordTotal, showSizeChanger: true,
              showTotal: (total) => `共 ${total} 条记录`,
              onChange: (page, size) => { setRecordPage(page); setRecordPageSize(size); },
            }}
          />
        </> : <>
          <div className="dnspod-view-head">
            <div className="dnspod-view-title"><div><strong>全部域名</strong><span>账号下已托管的 DNSPod 域名</span></div></div>
            <Space wrap>
              <Input
                allowClear
                value={domainKeyword}
                onChange={(event) => setDomainKeyword(event.target.value)}
                onPressEnter={() => { setDomainPage(1); setDomainQuery(domainKeyword.trim()); }}
                placeholder="搜索域名"
                prefix={<SearchOutlined />}
                className="dnspod-search"
              />
              <Button onClick={() => { setDomainPage(1); setDomainQuery(domainKeyword.trim()); }}>搜索</Button>
              <Button icon={<ReloadOutlined />} disabled={!config.configured} onClick={loadDomains}>刷新</Button>
            </Space>
          </div>
          <Table
            className="forest-table dnspod-domain-table"
            rowKey={(row) => row.id || row.name}
            loading={domainsLoading}
            dataSource={domains}
            columns={domainColumns}
            scroll={{ x: 1350 }}
            locale={{ emptyText: config.configured ? '当前账号下没有域名' : '请先配置 DNSPod 账号' }}
            pagination={{
              current: domainPage, pageSize: domainPageSize, total: domainTotal, showSizeChanger: true,
              showTotal: (total) => `共 ${total} 个域名`,
              onChange: (page, size) => { setDomainPage(page); setDomainPageSize(size); },
            }}
          />
        </>}
      </div>
    </Spin>

    <Modal
      title="DNSPod 账号设置"
      open={settingsOpen}
      onCancel={() => setSettingsOpen(false)}
      footer={<div className="dnspod-settings-footer">
        <div>{config.configured && config.source !== 'env' ? <Popconfirm title="确定清除 DNSPod 凭证吗？" onConfirm={clearSettings}><Button danger>清除配置</Button></Popconfirm> : null}</div>
        <Space><Button onClick={testSettings} loading={settingsSaving}>测试连接</Button><Button type="primary" onClick={saveSettings} loading={settingsSaving}>保存</Button></Space>
      </div>}
      destroyOnHidden
    >
      {config.source === 'env' ? <Alert className="dnspod-modal-alert" type="info" showIcon message="当前使用环境变量中的 DNSPod 凭证" description="修改后台配置不会覆盖 DNSPOD_AUTH_TYPE、DNSPOD_API_TOKEN、DNSPOD_SECRET_ID、DNSPOD_SECRET_KEY 和 DNSPOD_EDITION。" /> : null}
      <Form form={settingsForm} layout="vertical">
        <Form.Item name="auth_type" label="鉴权方式" rules={[{ required: true, message: '请选择鉴权方式' }]} extra="国际版控制台生成的 ID,Token 与腾讯云 SecretId/SecretKey 是两套独立凭证，不能混填">
          <Select
            options={authTypeOptions}
            onChange={(value) => {
              if (value === 'token') settingsForm.setFieldValue('edition', 'international');
            }}
          />
        </Form.Item>
        {watchedAuthType === 'token' ? <Form.Item
          name="api_token"
          label="API Token"
          preserve={false}
          rules={[
            { required: !(config.configured && config.auth_type === 'token'), message: '请输入 API Token' },
            { pattern: /^\s*[^,\s]+\s*,\s*[^,\s]+\s*$/, message: '格式应为 ID,Token，例如 123456,abcdef' },
          ]}
          extra={config.configured && config.auth_type === 'token' ? `当前：${config.credential_masked || '已配置'}，留空表示不修改` : '请填写 DNSPod 国际版控制台创建的完整 ID,Token'}
        >
          <Input.Password autoComplete="new-password" placeholder={config.configured && config.auth_type === 'token' ? '留空表示不修改' : '例如 123456,abcdef'} />
        </Form.Item> : <>
          <Form.Item name="edition" label="DNSPod 版本" rules={[{ required: true, message: '请选择 DNSPod 版本' }]} extra="国际版与中国版使用不同的 API 接入地址，密钥不能混用">
            <Select options={editionOptions} />
          </Form.Item>
          <Form.Item name="secret_id" label="SecretId" preserve={false} rules={[{ required: !(config.configured && config.auth_type === 'tc3'), message: '请输入 SecretId' }]} extra={config.configured && config.auth_type === 'tc3' ? `当前：${config.credential_masked || config.secret_id_masked || '已配置'}，留空表示不修改` : '在对应腾讯云账号的 API 密钥管理中创建'}>
            <Input autoComplete="off" placeholder={config.configured && config.auth_type === 'tc3' ? '留空表示不修改' : '请输入 DNSPod SecretId'} />
          </Form.Item>
          <Form.Item name="secret_key" label="SecretKey" preserve={false} rules={[{ required: !(config.configured && config.auth_type === 'tc3'), message: '请输入 SecretKey' }]} extra={config.configured && config.auth_type === 'tc3' ? '出于安全原因不会回显，留空表示不修改' : undefined}>
            <Input.Password autoComplete="new-password" placeholder={config.configured && config.auth_type === 'tc3' ? '留空表示不修改' : '请输入 DNSPod SecretKey'} />
          </Form.Item>
        </>}
        <Form.Item name="verify" label="保存前验证" valuePropName="checked"><Switch /></Form.Item>
      </Form>
    </Modal>

    <Modal
      title={editingRecord?.id ? '编辑解析记录' : '添加解析记录'}
      open={editingRecord !== undefined}
      onCancel={() => setEditingRecord(undefined)}
      onOk={saveRecord}
      okText="保存"
      confirmLoading={recordSaving}
      width={760}
      destroyOnHidden
    >
      <Form form={recordForm} layout="vertical" className="modal-grid-form dnspod-record-form">
        <Form.Item name="sub_domain" label="主机记录" rules={[{ required: true, message: '请输入主机记录' }]} extra="根域名填写 @，泛解析填写 *">
          <Input placeholder="例如 www、@、*" />
        </Form.Item>
        <Form.Item name="record_type" label="记录类型" rules={[{ required: true, message: '请选择记录类型' }]}>
          <Select showSearch options={recordTypeOptions.length ? recordTypeOptions : [{ label: 'A', value: 'A' }]} />
        </Form.Item>
        <Form.Item name="record_line_id" label="解析线路" rules={[{ required: true, message: '请选择解析线路' }]}>
          <Select showSearch optionFilterProp="label" loading={linesLoading} options={lineOptions} placeholder={linesLoading ? '正在读取 DNSPod 线路' : '请选择解析线路'} />
        </Form.Item>
        <Form.Item name="ttl" label="TTL" rules={[{ required: true, message: '请输入 TTL' }]}>
          <InputNumber min={1} style={{ width: '100%' }} addonAfter="秒" />
        </Form.Item>
        <Form.Item name="value" label="记录值" rules={[{ required: true, message: '请输入记录值' }]} className="dnspod-record-value">
          <Input.TextArea autoSize={{ minRows: 2, maxRows: 5 }} placeholder="请输入记录值" />
        </Form.Item>
        {isMX ? <Form.Item name="mx" label="MX 优先级" rules={[{ required: true, message: '请输入 MX 优先级' }]}>
          <InputNumber min={0} max={50} style={{ width: '100%' }} />
        </Form.Item> : null}
        <Form.Item name="weight" label="权重（选填）">
          <InputNumber min={0} max={100} style={{ width: '100%' }} placeholder="支持权重的线路可填写" />
        </Form.Item>
      </Form>
    </Modal>
  </div>;
}
