import React, { useEffect, useMemo, useRef, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Select,
  Space,
  Spin,
  Switch,
  Table,
  Tag,
  Typography,
  message,
} from 'antd';
import {
  CopyOutlined,
  DeleteOutlined,
  LoadingOutlined,
  MenuOutlined,
  PlayCircleOutlined,
  PlusOutlined,
} from '@ant-design/icons';
import { apiGet, apiPost } from '../lib/api';
import { moveItem } from '../lib/drag';
import { buildVisibleServerOptions, memberKey, splitMemberKey, type ClientEntryServerOption } from './clientEntryHelpers';

type ConditionField = 'user_id' | 'email' | 'registration_days' | 'ua' | 'plan_id';

type EntryCondition = {
  field: ConditionField;
  operator: string;
  values?: Array<number | string>;
  min?: number;
  max?: number;
  value?: number;
};

type UserOption = { value: number; label: string };
type EmailOption = { value: string; label: string };
type PolicyAction = 'override' | 'original' | 'hide';

const fieldOptions = [
  { label: '用户 ID', value: 'user_id' },
  { label: '用户邮箱', value: 'email' },
  { label: '注册天数', value: 'registration_days' },
  { label: 'User-Agent', value: 'ua' },
  { label: '套餐 ID', value: 'plan_id' },
];

const operatorOptions: Record<ConditionField, Array<{ label: string; value: string }>> = {
  user_id: [
    { label: '指定用户', value: 'in' },
    { label: 'ID 范围（含边界）', value: 'between' },
  ],
  email: [{ label: '指定邮箱', value: 'in' }],
  registration_days: [
    { label: '天数范围（含边界）', value: 'between' },
    { label: '大于', value: 'gt' },
    { label: '小于等于', value: 'lte' },
  ],
  ua: [
    { label: '包含任一关键词', value: 'contains_any' },
    { label: '排除全部关键词', value: 'excludes_any' },
    { label: '为空', value: 'empty' },
    { label: '不为空', value: 'not_empty' },
  ],
  plan_id: [{ label: '套餐 ID 范围（含边界）', value: 'between' }],
};

const defaultOperator: Record<ConditionField, string> = {
  user_id: 'in',
  email: 'in',
  registration_days: 'between',
  ua: 'contains_any',
  plan_id: 'between',
};

function parseConditions(value: any): EntryCondition[] {
  let current = value;
  for (let index = 0; index < 3 && typeof current === 'string'; index += 1) {
    const text = current.trim();
    if (!text) return [];
    try {
      current = JSON.parse(text);
    } catch {
      return [];
    }
  }
  if (!Array.isArray(current)) return [];
  return current
    .filter((item) => item && typeof item === 'object')
    .map((item) => ({
      field: item.field,
      operator: item.operator,
      values: Array.isArray(item.values) ? item.values : undefined,
      min: item.min === '' || item.min === null || item.min === undefined ? undefined : Number(item.min),
      max: item.max === '' || item.max === null || item.max === undefined ? undefined : Number(item.max),
      value: item.value === '' || item.value === null || item.value === undefined ? undefined : Number(item.value),
    }))
    .filter((item) => fieldOptions.some((option) => option.value === item.field) && item.operator) as EntryCondition[];
}

function finiteNumber(value: any) {
  if (value === '' || value === null || value === undefined) return undefined;
  const number = Number(value);
  return Number.isFinite(number) ? number : undefined;
}

function cleanConditions(value: any): EntryCondition[] {
  return parseConditions(Array.isArray(value) ? value : []).map((condition) => {
    if (condition.field === 'user_id' && condition.operator === 'in') {
      return {
        field: condition.field,
        operator: condition.operator,
        values: (condition.values || []).map(Number).filter((item) => Number.isFinite(item) && item > 0),
      };
    }
    if (condition.field === 'email' && condition.operator === 'in') {
      return {
        field: condition.field,
        operator: condition.operator,
        values: (condition.values || []).map((item) => String(item).trim().toLowerCase()).filter(Boolean),
      };
    }
    if (condition.operator === 'between') {
      return {
        field: condition.field,
        operator: condition.operator,
        min: finiteNumber(condition.min),
        max: finiteNumber(condition.max),
      };
    }
    if (['contains_any', 'excludes_any'].includes(condition.operator)) {
      return {
        field: condition.field,
        operator: condition.operator,
        values: (condition.values || []).map((item) => String(item).trim()).filter(Boolean),
      };
    }
    if (['empty', 'not_empty', 'is_empty', 'is_not_empty'].includes(condition.operator)) {
      return { field: condition.field, operator: condition.operator };
    }
    return {
      field: condition.field,
      operator: condition.operator,
      value: finiteNumber(condition.value),
    };
  });
}

function validateConditionValues(conditions: EntryCondition[]) {
  for (let index = 0; index < conditions.length; index += 1) {
    const condition = conditions[index];
    const prefix = `第 ${index + 1} 个条件`;
    if (!condition.field || !condition.operator) return `${prefix}不完整`;
    if (condition.operator === 'in' && !(condition.values || []).length) return condition.field === 'email' ? `${prefix}至少填写一个邮箱` : `${prefix}至少选择一个用户`;
    if (condition.operator === 'between') {
      if (condition.min === undefined || condition.max === undefined) return `${prefix}需要填写起始值和结束值`;
      if (Number(condition.min) > Number(condition.max)) return `${prefix}的起始值不能大于结束值`;
    }
    if (['contains_any', 'excludes_any'].includes(condition.operator) && !(condition.values || []).length) {
      return `${prefix}至少填写一个 UA 关键词`;
    }
    if (['gt', 'gte', 'lt', 'lte', 'eq'].includes(condition.operator) && condition.value === undefined) {
      return `${prefix}需要填写匹配值`;
    }
  }
  return '';
}

function normalizedMembers(row: any) {
  return (Array.isArray(row?.members) ? row.members : []).map(memberKey).filter(Boolean);
}

function normalizePolicyAction(value: any): PolicyAction {
  if (value === 'original' || value === 'hide') return value;
  return 'override';
}

function policyPayload(row: any, overrides: Record<string, any> = {}) {
  const source = { ...row, ...overrides };
  const action = normalizePolicyAction(source.action);
  return {
    id: source.id,
    name: String(source.name || '').trim(),
    entry_host: action === 'override' ? String(source.entry_host || '').trim() : '',
    action,
    conditions: cleanConditions(source.conditions),
    members: (Array.isArray(source.members) ? source.members : [])
      .map((member: any) => typeof member === 'string' ? splitMemberKey(member) : member)
      .filter(Boolean),
    enabled: source.enabled === true || Number(source.enabled) !== 0 ? 1 : 0,
    remarks: String(source.remarks || '').trim(),
  };
}

function mergeUserOptions(current: UserOption[], next: UserOption[]) {
  const map = new Map<number, UserOption>();
  [...current, ...next].forEach((item) => {
    if (Number.isFinite(Number(item.value))) map.set(Number(item.value), { ...item, value: Number(item.value) });
  });
  return Array.from(map.values());
}

function ConditionEditorRow({
  name,
  remove,
  form,
  userOptions,
  userSearching,
  onSearchUsers,
  emailOptions,
  emailSearching,
  onSearchEmails,
}: {
  name: number;
  remove: (index: number) => void;
  form: any;
  userOptions: UserOption[];
  userSearching: boolean;
  onSearchUsers: (keyword: string) => void;
  emailOptions: EmailOption[];
  emailSearching: boolean;
  onSearchEmails: (keyword: string) => void;
}) {
  const field = (Form.useWatch(['conditions', name, 'field'], form) || 'user_id') as ConditionField;
  const operator = Form.useWatch(['conditions', name, 'operator'], form) || defaultOperator[field];

  const changeField = (nextField: ConditionField) => {
    form.setFieldValue(['conditions', name], { field: nextField, operator: defaultOperator[nextField] });
  };

  const changeOperator = (nextOperator: string) => {
    form.setFieldValue(['conditions', name], { field, operator: nextOperator });
  };

  let valueEditor: React.ReactNode = null;
  if (field === 'user_id' && operator === 'in') {
    valueEditor = <Form.Item name={[name, 'values']} label="用户" rules={[{ required: true, message: '请选择用户' }]} style={{ minWidth: 300, flex: 1 }}>
      <Select
        mode="multiple"
        showSearch
        allowClear
        filterOption={false}
        loading={userSearching}
        options={userOptions}
        placeholder="输入用户 ID 或邮箱检索"
        onSearch={onSearchUsers}
        optionFilterProp="label"
      />
    </Form.Item>;
  } else if (field === 'email' && operator === 'in') {
    valueEditor = <Form.Item name={[name, 'values']} label="邮箱" rules={[{ required: true, message: '请填写至少一个邮箱' }]} style={{ minWidth: 300, flex: 1 }}>
      <Select
        mode="tags"
        showSearch
        allowClear
        filterOption={false}
        loading={emailSearching}
        options={emailOptions}
        maxTagCount={2}
        maxTagPlaceholder={(omittedValues) => `+${omittedValues.length} 个邮箱`}
        maxTagTextLength={28}
        tokenSeparators={[',', '，', ';', '；']}
        placeholder="输入邮箱检索用户，也可直接填写"
        onSearch={onSearchEmails}
      />
    </Form.Item>;
  } else if (operator === 'between') {
    const label = field === 'registration_days' ? '天数范围' : field === 'plan_id' ? '套餐 ID 范围' : 'ID 范围';
    valueEditor = <Space align="start" wrap style={{ flex: 1 }}>
      <Form.Item name={[name, 'min']} label={`${label}起始`} rules={[{ required: true, message: '请输入起始值' }]}>
        <InputNumber min={0} precision={0} placeholder="起始值" style={{ width: 150 }} />
      </Form.Item>
      <Form.Item name={[name, 'max']} label={`${label}结束`} rules={[{ required: true, message: '请输入结束值' }]}>
        <InputNumber min={0} precision={0} placeholder="结束值" style={{ width: 150 }} />
      </Form.Item>
    </Space>;
  } else if (field === 'ua' && ['contains_any', 'excludes_any'].includes(operator)) {
    valueEditor = <Form.Item name={[name, 'values']} label="UA 关键词" rules={[{ required: true, message: '请填写至少一个关键词' }]} style={{ minWidth: 300, flex: 1 }}>
      <Select mode="tags" tokenSeparators={[',', '，']} placeholder="输入关键词后回车，可填写多个" />
    </Form.Item>;
  } else if (!['empty', 'not_empty', 'is_empty', 'is_not_empty'].includes(operator)) {
    valueEditor = <Form.Item name={[name, 'value']} label={field === 'registration_days' ? '注册天数' : '匹配值'} rules={[{ required: true, message: '请输入匹配值' }]} style={{ minWidth: 180, flex: 1 }}>
      <InputNumber min={0} precision={0} placeholder="请输入" style={{ width: '100%' }} />
    </Form.Item>;
  }

  return <Card size="small" styles={{ body: { padding: '12px 12px 0' } }}>
    <Space align="start" wrap style={{ width: '100%' }}>
      <Form.Item name={[name, 'field']} label="条件类型" rules={[{ required: true }]} style={{ width: 150 }}>
        <Select options={fieldOptions} onChange={changeField} />
      </Form.Item>
      <Form.Item name={[name, 'operator']} label="匹配方式" rules={[{ required: true }]} style={{ width: 190 }}>
        <Select options={operatorOptions[field]} onChange={changeOperator} />
      </Form.Item>
      {valueEditor}
      <Form.Item label=" ">
        <Button danger type="text" icon={<DeleteOutlined />} onClick={() => remove(name)}>删除</Button>
      </Form.Item>
    </Space>
  </Card>;
}

function PolicyEditor({
  row,
  children,
  onDone,
  serverOptions,
}: {
  row?: any;
  children: React.ReactElement;
  onDone: () => void;
  serverOptions: ClientEntryServerOption[];
}) {
  const [open, setOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [userSearching, setUserSearching] = useState(false);
  const [userOptions, setUserOptions] = useState<UserOption[]>([]);
  const [emailSearching, setEmailSearching] = useState(false);
  const [emailOptions, setEmailOptions] = useState<EmailOption[]>([]);
  const emailSearchSequence = useRef(0);
  const [form] = Form.useForm();
  const action = Form.useWatch('action', form) || 'override';

  const show = () => {
    const conditions = parseConditions(row?.conditions);
    const selectedUserIDs = conditions
      .filter((condition) => condition.field === 'user_id' && condition.operator === 'in')
      .flatMap((condition) => condition.values || [])
      .map(Number)
      .filter((value) => Number.isFinite(value) && value > 0);
    const selectedEmails = conditions
      .filter((condition) => condition.field === 'email' && condition.operator === 'in')
      .flatMap((condition) => condition.values || [])
      .map((value) => String(value).trim().toLowerCase())
      .filter(Boolean);
    setUserOptions(selectedUserIDs.map((value) => ({ value, label: `#${value}` })));
    setEmailOptions(selectedEmails.map((value) => ({ value, label: value })));
    form.resetFields();
    form.setFieldsValue({
      id: row?.id,
      name: row?.name || '',
      entry_host: row?.entry_host || '',
      action: normalizePolicyAction(row?.action),
      conditions,
      members: normalizedMembers(row),
      enabled: row?.enabled === undefined ? true : Number(row.enabled) !== 0,
      remarks: row?.remarks || '',
    });
    setOpen(true);
  };

  const searchUsers = async (keyword: string) => {
    const value = String(keyword || '').trim();
    if (!value) return;
    setUserSearching(true);
    try {
      const numeric = /^\d+$/.test(value);
      const res = await apiGet('/user/fetch', {
        current: 1,
        pageSize: 20,
        filter: [{ key: numeric ? 'id' : 'email', condition: numeric ? '=' : '模糊', value }],
      });
      const list = Array.isArray(res.data) ? res.data : [];
      const next = list.map((item: any) => ({
        value: Number(item.id),
        label: `#${item.id} ${String(item.email || '').trim()}`.trim(),
      })).filter((item: UserOption) => Number.isFinite(item.value) && item.value > 0);
      setUserOptions((current) => mergeUserOptions(current, next));
    } catch (error: any) {
      message.error(error?.message || '用户检索失败');
    } finally {
      setUserSearching(false);
    }
  };

  const searchEmails = async (keyword: string) => {
    const value = String(keyword || '').trim();
    const sequence = ++emailSearchSequence.current;
    if (!value) {
      setEmailOptions([]);
      return;
    }
    setEmailSearching(true);
    try {
      const res = await apiGet('/user/fetch', {
        current: 1,
        pageSize: 20,
        filter: [{ key: 'email', condition: '模糊', value }],
      });
      const list = Array.isArray(res.data) ? res.data : [];
      const next = list.map((item: any) => {
        const email = String(item.email || '').trim().toLowerCase();
        return { value: email, label: email };
      }).filter((item: EmailOption) => Boolean(item.value));
      const normalizedValue = value.toLowerCase();
      next.sort((left, right) => {
        const rank = (email: string) => email === normalizedValue ? 0 : email.startsWith(normalizedValue) ? 1 : 2;
        return rank(left.value) - rank(right.value) || left.value.localeCompare(right.value);
      });
      // Search results must reflect the current text only. Selected tag values remain in the form.
      if (sequence === emailSearchSequence.current) setEmailOptions(next);
    } catch (error: any) {
      if (sequence === emailSearchSequence.current) message.error(error?.message || '用户检索失败');
    } finally {
      if (sequence === emailSearchSequence.current) setEmailSearching(false);
    }
  };

  const save = async () => {
    setSaving(true);
    try {
      const values = await form.validateFields();
      const conditions = cleanConditions(values.conditions || []);
      const conditionError = validateConditionValues(conditions);
      if (conditionError) {
        message.error(conditionError);
        return;
      }
      await apiPost('/server/client-entry-user-policy/save', policyPayload({ ...values, conditions }));
      message.success('保存成功');
      setOpen(false);
      onDone();
    } catch (error: any) {
      if (!error?.errorFields) message.error(error?.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };

  return <>
    {React.cloneElement(children, { onClick: show })}
    <Modal
      title={row?.id ? '编辑用户入口规则' : '新增用户入口规则'}
      open={open}
      onCancel={() => setOpen(false)}
      onOk={save}
      okText={saving ? <LoadingOutlined /> : '保存'}
      cancelText="取消"
      confirmLoading={saving}
      width={920}
      destroyOnHidden
    >
      <Form form={form} layout="vertical">
        <Form.Item name="id" hidden><Input /></Form.Item>
        <Form.Item name="name" label="规则名称" rules={[{ required: true, whitespace: true, message: '请输入规则名称' }]}>
          <Input placeholder="例如：新用户 Clash 独立入口" maxLength={100} showCount />
        </Form.Item>
        <Form.Item name="action" label="命中后动作" rules={[{ required: true }]}>
          <Select options={[
            { label: '覆盖入口地址', value: 'override' },
            { label: '下发原入口地址', value: 'original' },
            { label: '不下发所选节点', value: 'hide' },
          ]} />
        </Form.Item>
        {action === 'original' && <Alert
          type="info"
          showIcon
          message="保留每个节点自己的原入口地址"
          description="可以在下方一次选择多个节点。节点需在编辑页开启“仅入口分配用户可见”，这样未命中本规则的用户不会收到这些节点。"
          style={{ marginBottom: 24 }}
        />}
        {action === 'override' && <Form.Item
          name="entry_host"
          label="独立入口地址"
          rules={[
            { required: true, whitespace: true, message: '请输入独立入口地址' },
            { validator: (_, value) => /[,，()]/.test(String(value || '')) ? Promise.reject(new Error('这里只能填写单个普通域名或 IP，条件请在下方配置')) : Promise.resolve() },
          ]}
          tooltip="规则命中时，所选节点会下发这个地址。这里只填写普通域名或 IP。"
        >
          <Input placeholder="例如 vip.example.com 或 1.2.3.4" />
        </Form.Item>}
        <Form.Item label="匹配条件" extra="同一条规则中的所有条件必须同时满足（AND）。不添加条件表示匹配全部用户。">
          <Form.List name="conditions">
            {(fields, { add, remove }) => <Space direction="vertical" size={10} style={{ width: '100%' }}>
              {fields.map((item) => <ConditionEditorRow
                key={item.key}
                name={item.name}
                remove={remove}
                form={form}
                userOptions={userOptions}
                userSearching={userSearching}
                onSearchUsers={searchUsers}
                emailOptions={emailOptions}
                emailSearching={emailSearching}
                onSearchEmails={searchEmails}
              />)}
              <Button type="dashed" block icon={<PlusOutlined />} onClick={() => add({ field: 'user_id', operator: 'in', values: [] })}>
                添加匹配条件
              </Button>
            </Space>}
          </Form.List>
        </Form.Item>
        <Form.Item name="members" label="生效节点" rules={[{ required: true, message: '请选择生效节点' }]} tooltip="规则只作用于选中的节点，可以选择多个。">
          <Select mode="multiple" showSearch allowClear placeholder="选择多个生效节点" options={serverOptions} optionFilterProp="label" />
        </Form.Item>
        <Form.Item name="enabled" label="状态" valuePropName="checked">
          <Switch checkedChildren="启用" unCheckedChildren="禁用" />
        </Form.Item>
        <Form.Item name="remarks" label="备注">
          <Input.TextArea rows={3} placeholder="可选" maxLength={500} showCount />
        </Form.Item>
      </Form>
    </Modal>
  </>;
}

function conditionSummary(condition: EntryCondition) {
  const values = condition.values || [];
  if (condition.field === 'user_id' && condition.operator === 'in') return `指定用户：${values.map((item) => `#${item}`).join('、') || '未选择'}`;
  if (condition.field === 'email' && condition.operator === 'in') return `指定邮箱：${values.length} 个`;
  if (condition.field === 'user_id' && condition.operator === 'between') return `用户 ID：${condition.min} ～ ${condition.max}`;
  if (condition.field === 'registration_days') {
    if (condition.operator === 'between') return `注册天数：${condition.min} ～ ${condition.max} 天`;
    if (condition.operator === 'gt') return `注册天数 > ${condition.value} 天`;
    if (condition.operator === 'gte') return `注册天数 ≥ ${condition.value} 天`;
    if (condition.operator === 'lte') return `注册天数 ≤ ${condition.value} 天`;
    if (condition.operator === 'lt') return `注册天数 < ${condition.value} 天`;
  }
  if (condition.field === 'ua') {
    if (condition.operator === 'contains_any') return `UA 包含：${values.join(' / ')}`;
    if (condition.operator === 'excludes_any') return `UA 排除：${values.join(' / ')}`;
    if (['empty', 'is_empty'].includes(condition.operator)) return 'UA 为空';
    if (['not_empty', 'is_not_empty'].includes(condition.operator)) return 'UA 不为空';
  }
  if (condition.field === 'plan_id' && condition.operator === 'between') return `套餐 ID：${condition.min} ～ ${condition.max}`;
  return `${condition.field} ${condition.operator}`;
}

function EmailConditionSummary({ condition }: { condition: EntryCondition }) {
  const emails = (condition.values || []).map((item) => String(item).trim()).filter(Boolean);
  const summary = `指定邮箱：${emails.length} 个`;
  if (emails.length <= 1) return <Tag className="client-entry-condition-tag" title={emails[0] || summary}>{emails[0] ? `指定邮箱：${emails[0]}` : summary}</Tag>;
  return <details className="client-entry-email-condition">
    <summary><Tag className="client-entry-condition-tag">{summary}（点击展开）</Tag></summary>
    <div className="client-entry-condition-list">
      {emails.map((email) => <Tag className="client-entry-condition-tag" title={email} key={email}>{email}</Tag>)}
    </div>
  </details>;
}

function numericConditionMatches(condition: EntryCondition, actual: any) {
  const number = finiteNumber(actual);
  if (number === undefined) return false;
  if (condition.operator === 'in') return (condition.values || []).some((item) => Number(item) === number);
  if (condition.operator === 'between') return number >= Number(condition.min) && number <= Number(condition.max);
  if (condition.operator === 'eq') return number === Number(condition.value);
  if (condition.operator === 'gt') return number > Number(condition.value);
  if (condition.operator === 'gte') return number >= Number(condition.value);
  if (condition.operator === 'lt') return number < Number(condition.value);
  if (condition.operator === 'lte') return number <= Number(condition.value);
  return false;
}

function conditionMatches(condition: EntryCondition, input: any) {
  if (condition.field === 'user_id') return numericConditionMatches(condition, input.user_id);
  if (condition.field === 'email') return (condition.values || []).some((item) => String(item).trim().toLowerCase() === String(input.email || '').trim().toLowerCase());
  if (condition.field === 'registration_days') return numericConditionMatches(condition, input.registration_days);
  if (condition.field === 'plan_id') return numericConditionMatches(condition, input.plan_id);
  const ua = String(input.ua || '').trim();
  const needles = (condition.values || []).map((item) => String(item).trim().toLowerCase()).filter(Boolean);
  const normalizedUA = ua.toLowerCase();
  if (condition.operator === 'contains_any') return needles.some((needle) => normalizedUA.includes(needle));
  if (condition.operator === 'excludes_any') return needles.every((needle) => !normalizedUA.includes(needle));
  if (['empty', 'is_empty'].includes(condition.operator)) return !ua;
  if (['not_empty', 'is_not_empty'].includes(condition.operator)) return !!ua;
  return false;
}

function policyMatches(row: any, input: any) {
  return parseConditions(row.conditions).every((condition) => conditionMatches(condition, input));
}

function policyResultDescription(row: any) {
  const action = normalizePolicyAction(row?.action);
  if (action === 'hide') return '结果：不下发所选节点';
  if (action === 'original') return '结果：下发所选节点各自的原入口地址';
  return `结果：下发独立入口 ${row?.entry_host || '-'}`;
}

function SimulationModal({
  open,
  onClose,
  rows,
  serverOptions,
}: {
  open: boolean;
  onClose: () => void;
  rows: any[];
  serverOptions: ClientEntryServerOption[];
}) {
  const [form] = Form.useForm();
  const [result, setResult] = useState<any>(undefined);
  const [running, setRunning] = useState(false);
  const [simulationError, setSimulationError] = useState('');

  useEffect(() => {
    if (!open) return;
    setResult(undefined);
    setSimulationError('');
    form.resetFields();
  }, [open]);

  const run = async () => {
    const values = await form.validateFields();
    setRunning(true);
    setSimulationError('');
    try {
      const email = String(values.email || '').trim().toLowerCase();
      const response = await apiGet('/user/fetch', {
        current: 1,
        pageSize: 10,
        filter: [{ key: 'email', condition: '=', value: email }],
      });
      const user = (Array.isArray(response.data) ? response.data : []).find((item: any) => String(item.email || '').trim().toLowerCase() === email);
      if (!user) {
        setResult(undefined);
        setSimulationError('没有找到该邮箱对应的用户');
        return;
      }
      const createdAt = finiteNumber(user.created_at);
      const now = Math.floor(Date.now() / 1000);
      const subject = {
        ...values,
        user_id: finiteNumber(user.id),
        email: String(user.email || email).trim().toLowerCase(),
        registration_days: createdAt !== undefined && createdAt > 0 && now >= createdAt ? Math.floor((now - createdAt) / 86400) : -1,
        plan_id: finiteNumber(user.plan_id) ?? 0,
      };
      const selectedMember = values.member;
      const matched = rows.find((row) => {
        if (Number(row.enabled) === 0) return false;
        if (selectedMember && !normalizedMembers(row).includes(selectedMember)) return false;
        return policyMatches(row, subject);
      });
      setResult(matched || null);
    } catch (error: any) {
      setResult(undefined);
      setSimulationError(error?.message || '查询用户失败');
    } finally {
      setRunning(false);
    }
  };

  return <Modal title="模拟入口规则匹配" open={open} onCancel={onClose} footer={null} width={720} destroyOnHidden>
    <Alert
      type="info"
      showIcon
      message="按当前列表从上到下匹配，第一条满足全部条件的启用规则生效。模拟不会保存或修改任何数据。"
      style={{ marginBottom: 16 }}
    />
    <Form form={form} layout="vertical">
      <Space align="start" wrap style={{ width: '100%' }}>
        <Form.Item name="email" label="用户邮箱" rules={[{ required: true, type: 'email', message: '请输入有效的用户邮箱' }]}>
          <Input style={{ width: 320 }} placeholder="输入邮箱后自动读取用户 ID、注册天数和套餐" />
        </Form.Item>
      </Space>
      <Form.Item name="ua" label="User-Agent">
        <Input.TextArea rows={3} placeholder="可留空，用于测试空 UA 规则" />
      </Form.Item>
      <Form.Item name="member" label="生效节点（可选）" tooltip="选择后只模拟对该节点生效的规则；不选则按全部规则匹配。">
        <Select showSearch allowClear options={serverOptions} optionFilterProp="label" placeholder="不限制节点" />
      </Form.Item>
      <Button type="primary" icon={<PlayCircleOutlined />} loading={running} onClick={run}>开始模拟</Button>
    </Form>
    {simulationError && <Alert type="error" showIcon message={simulationError} style={{ marginTop: 16 }} />}
    {result === null && <Alert
      type="warning"
      showIcon
      message="没有命中任何启用规则"
      description="普通节点将使用默认地址；标记为“仅入口分配”的节点不会下发。"
      style={{ marginTop: 16 }}
    />}
    {result && <Alert
      type={result.action === 'hide' ? 'warning' : 'success'}
      showIcon
      style={{ marginTop: 16 }}
      message={`命中：${result.name || `规则 #${result.id}`}`}
      description={policyResultDescription(result)}
    />}
  </Modal>;
}

export default function ClientEntryUserPolicyPage() {
  const [rows, setRows] = useState<any[]>([]);
  const [serverOptions, setServerOptions] = useState<ClientEntryServerOption[]>([]);
  const [loading, setLoading] = useState(false);
  const [draggingKey, setDraggingKey] = useState<string | null>(null);
  const [simulatorOpen, setSimulatorOpen] = useState(false);

  const load = async () => {
    setLoading(true);
    try {
      const [policyRes, nodeRes] = await Promise.all([
        apiGet('/server/client-entry-user-policy/fetch'),
        apiGet('/server/manage/getNodes').catch(() => ({ data: [] })),
      ]);
      const policies = Array.isArray(policyRes.data) ? policyRes.data : [];
      setRows(policies.map((row: any) => ({ ...row, conditions: parseConditions(row.conditions) })));
      setServerOptions(buildVisibleServerOptions(Array.isArray(nodeRes.data) ? nodeRes.data : []));
    } catch (error: any) {
      message.error(error?.message || '加载失败');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { load(); }, []);

  const serverOptionMap = useMemo(() => Object.fromEntries(serverOptions.map((item) => [item.value, item.label])), [serverOptions]);

  const drop = async (row: any) => {
    try {
      await apiPost('/server/client-entry-user-policy/drop', { id: row.id }, { form: true });
      message.success('已删除');
      load();
    } catch (error: any) {
      message.error(error?.message || '删除失败');
    }
  };

  const toggle = async (row: any, enabled: boolean) => {
    try {
      await apiPost('/server/client-entry-user-policy/save', policyPayload(row, { enabled }));
      message.success(enabled ? '规则已启用' : '规则已禁用');
      load();
    } catch (error: any) {
      message.error(error?.message || '状态更新失败');
    }
  };

  const saveSort = async (nextRows: any[]) => {
    await apiPost('/server/client-entry-user-policy/sort', { ids: nextRows.map((row) => row.id) });
  };

  const handleDrop = async (targetRow: any) => {
    if (!draggingKey) return;
    const targetKey = String(targetRow.id);
    if (draggingKey === targetKey) {
      setDraggingKey(null);
      return;
    }
    const fromIndex = rows.findIndex((row) => String(row.id) === draggingKey);
    const toIndex = rows.findIndex((row) => String(row.id) === targetKey);
    if (fromIndex < 0 || toIndex < 0) {
      setDraggingKey(null);
      return;
    }
    const previousRows = rows;
    const nextRows = moveItem(rows, fromIndex, toIndex);
    setRows(nextRows);
    setDraggingKey(null);
    try {
      await saveSort(nextRows);
      message.success('规则顺序已保存');
    } catch (error: any) {
      setRows(previousRows);
      message.error(error?.message || '排序保存失败');
    }
  };

  const columns: any[] = [
    {
      title: '顺序',
      key: 'sequence',
      width: 90,
      render: (_: any, __: any, index: number) => <Space><MenuOutlined className="drag-handle" title="拖动调整匹配顺序" /><span>{index + 1}</span></Space>,
    },
    {
      title: '规则名称',
      dataIndex: 'name',
      width: 210,
      render: (value: any, row: any) => {
        const text = String(value || `规则 #${row.id}`);
        return <span className="client-entry-rule-name" title={text}>{text}</span>;
      },
    },
    {
      title: '匹配条件（全部 AND）',
      dataIndex: 'conditions',
      width: 620,
      render: (value: any) => {
        const conditions = parseConditions(value);
        if (!conditions.length) return <Tag>全部用户</Tag>;
        const hasEmailList = conditions.some((condition) => condition.field === 'email' && condition.operator === 'in' && (condition.values || []).length > 1);
        return <div className={`client-entry-condition-list${hasEmailList ? ' client-entry-condition-list--expandable' : ''}`}>{conditions.map((condition, index) => {
          const text = conditionSummary(condition);
          if (condition.field === 'email' && condition.operator === 'in') return <EmailConditionSummary condition={condition} key={`${condition.field}-${condition.operator}-${index}`} />;
          return <Tag className="client-entry-condition-tag" title={text} key={`${condition.field}-${condition.operator}-${index}`}>{text}</Tag>;
        })}</div>;
      },
    },
    {
      title: '命中结果',
      key: 'result',
      width: 250,
      render: (_: any, row: any) => {
        const action = normalizePolicyAction(row.action);
        if (action === 'hide') return <Tag color="red">不下发节点</Tag>;
        if (action === 'original') return <Tag color="blue">下发原入口地址</Tag>;
        return <Typography.Text code copyable={{ text: String(row.entry_host || '') }}>{row.entry_host || '-'}</Typography.Text>;
      },
    },
    {
      title: '生效节点',
      dataIndex: 'members',
      width: 250,
      render: (value: any) => {
        const members = Array.isArray(value) ? value : [];
        if (!members.length) return '-';
        return <details className="client-entry-members"><summary>已选择 {members.length} 个节点</summary><div className="client-entry-member-list">{members.map((member: any) => {
          const key = memberKey(member);
          return <div key={key}>{serverOptionMap[key] || key}</div>;
        })}</div></details>;
      },
    },
    {
      title: '状态',
      dataIndex: 'enabled',
      width: 100,
      render: (value: any, row: any) => <Switch size="small" checked={Number(value) !== 0} onChange={(checked) => toggle(row, checked)} />,
    },
    { title: '备注', dataIndex: 'remarks', width: 180, ellipsis: true, render: (value: any) => value || '-' },
    {
      title: '操作',
      key: 'action',
      fixed: 'right',
      align: 'right',
      width: 205,
      render: (_: any, row: any) => {
        const copyRow = { ...row, id: undefined, name: `${row.name || `规则 #${row.id}`} - 副本` };
        return <Space className="client-entry-actions" size={10}>
          <PolicyEditor row={row} onDone={load} serverOptions={serverOptions}><a>编辑</a></PolicyEditor>
          <PolicyEditor row={copyRow} onDone={load} serverOptions={serverOptions}><a><CopyOutlined /> 复制</a></PolicyEditor>
          <Popconfirm title="确认删除这条入口规则？" onConfirm={() => drop(row)}><a>删除</a></Popconfirm>
        </Space>;
      },
    },
  ];

  return <div className="legacy-page client-entry-page">
    <div className="content-heading">用户入口分配</div>
    <Spin spinning={loading}>
      <Alert
        className="client-entry-help"
        type="info"
        showIcon
        message="仅入口分配节点的下发规则"
        description="节点编辑中标记为“仅入口分配”的节点，请先保持节点“显示”为“显示”。命中“覆盖入口地址”或“下发原入口地址”规则后才会下发；后者可一次选择多个节点并分别保留各自地址。未命中或命中“不下发所选节点”规则的用户看不到这些节点，节点权限组仍然生效。"
      />
      <Card className="block-card" styles={{ body: { padding: 0 } }}>
        <div className="forest-table-action">
          <Space wrap>
            <PolicyEditor onDone={load} serverOptions={serverOptions}><Button type="primary" icon={<PlusOutlined />}>新增入口规则</Button></PolicyEditor>
            <Button icon={<PlayCircleOutlined />} onClick={() => setSimulatorOpen(true)}>模拟匹配</Button>
            <Typography.Text type="secondary">规则从上到下匹配，拖动行即可调整顺序；第一条命中的规则生效。</Typography.Text>
          </Space>
        </div>
        <Table
          className="forest-table"
          rowKey="id"
          tableLayout="fixed"
          columns={columns}
          dataSource={rows}
          pagination={false}
          scroll={{ x: 1900 }}
          rowClassName={(row) => `sortable-row ${draggingKey === String(row.id) ? 'dragging-row' : ''}`}
          onRow={(row) => ({
            draggable: true,
            onDragStart: () => setDraggingKey(String(row.id)),
            onDragOver: (event) => event.preventDefault(),
            onDrop: () => handleDrop(row),
            onDragEnd: () => setDraggingKey(null),
          })}
        />
      </Card>
    </Spin>
    <SimulationModal open={simulatorOpen} onClose={() => setSimulatorOpen(false)} rows={rows} serverOptions={serverOptions} />
  </div>;
}
