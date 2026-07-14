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
  source?: string;
};

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
    const label = prefix ? `${prefix} / ${lineName}` : lineName;
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

  const loadRecordLines = async (recordType: string, currentLineId = '') => {
    if (!selectedDomain || !recordType) return;
    setLinesLoading(true);
    try {
      const response = await apiGet('/dns/record/lines', {
        domain: selectedDomain.name,
        domain_grade: selectedDomain.grade,
        record_type: recordType,
      });
      const options = flattenLines(response.data || []);
      setLineOptions(options);
      const selected = currentLineId || recordForm.getFieldValue('record_line_id');
      if (!selected && options.length) recordForm.setFieldValue('record_line_id', options[0].value);
    } catch (error: any) {
      setLineOptions([]);
      message.error(error.message || '加载解析线路失败');
    } finally {
      setLinesLoading(false);
    }
  };

  useEffect(() => { loadConfig(); }, []);
  useEffect(() => { if (config.configured) loadDomains(); }, [config.configured, domainPage, domainPageSize, domainQuery]);
  useEffect(() => { if (selectedDomain) loadRecords(); }, [selectedDomain, recordPage, recordPageSize, recordQuery, recordTypeFilter]);
  useEffect(() => {
    if (editingRecord !== undefined && watchedRecordType) loadRecordLines(watchedRecordType, editingRecord?.lineId || '');
  }, [watchedRecordType, editingRecord, selectedDomain]);

  const openSettings = () => {
    settingsForm.resetFields();
    settingsForm.setFieldsValue({ secret_id: '', secret_key: '', verify: true });
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
      await apiPost('/dns/record/delete', { domain: selectedDomain.name, record_id: record.id });
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
    { title: '线路', dataIndex: 'line', width: 160 },
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
        description="配置 DNSPod 3.0 SecretId 和 SecretKey 后，即可在这里管理账号下的域名与解析记录。"
        action={<Button icon={<SettingOutlined />} onClick={openSettings}>立即配置</Button>}
      /> : null}

      <div className="block block-rounded">
        <div className="dnspod-account-bar">
          <div className="dnspod-account-copy">
            <CloudServerOutlined />
            <div>
              <div className="dnspod-account-title">DNSPod 3.0</div>
              <div className="dnspod-account-subtitle">
                {config.configured ? <>已连接 · {config.secret_id_masked || '凭证已配置'}{config.source === 'env' ? ' · 环境变量' : ''}</> : '等待配置账号凭证'}
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
      {config.source === 'env' ? <Alert className="dnspod-modal-alert" type="info" showIcon message="当前使用环境变量中的 DNSPod 凭证" description="修改后台配置不会覆盖 DNSPOD_SECRET_ID 和 DNSPOD_SECRET_KEY。" /> : null}
      <Form form={settingsForm} layout="vertical">
        <Form.Item name="secret_id" label="SecretId" rules={[{ required: !config.configured, message: '请输入 SecretId' }]} extra={config.configured ? `当前：${config.secret_id_masked || '已配置'}，留空表示不修改` : '在腾讯云访问管理的 API 密钥管理中创建'}>
          <Input autoComplete="off" placeholder={config.configured ? '留空表示不修改' : '请输入 DNSPod SecretId'} />
        </Form.Item>
        <Form.Item name="secret_key" label="SecretKey" rules={[{ required: !config.configured, message: '请输入 SecretKey' }]} extra={config.configured ? '出于安全原因不会回显，留空表示不修改' : undefined}>
          <Input.Password autoComplete="new-password" placeholder={config.configured ? '留空表示不修改' : '请输入 DNSPod SecretKey'} />
        </Form.Item>
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
