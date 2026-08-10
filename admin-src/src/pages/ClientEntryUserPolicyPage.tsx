import React, { useEffect, useMemo, useRef, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Checkbox,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Segmented,
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
  BranchesOutlined,
  CopyOutlined,
  DeleteOutlined,
  EditOutlined,
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
type ExtraNodePosition = 'before' | 'after';

type SplitGroup = {
  id: number;
  policy_id: number;
  parent_id?: number;
  name: string;
  path: string;
  entry_host: string;
  sort: number;
  global_sort?: number;
  user_count: number;
  is_leaf: boolean;
};

function isSplitPolicy(row: any) {
  return String(row?.mode || '').toLowerCase() === 'split';
}

function formatSnapshotTime(value: any) {
  const timestamp = Number(value);
  if (!Number.isFinite(timestamp) || timestamp <= 0) return '-';
  return new Date(timestamp * 1000).toLocaleString();
}

function sortSplitLeaves(groups: SplitGroup[]) {
  return groups
    .filter((group) => group.is_leaf)
    .slice()
    .sort((left, right) => {
      const sortDifference = Number(left.sort || 0) - Number(right.sort || 0);
      if (sortDifference !== 0) return sortDifference;
      return String(left.path).localeCompare(String(right.path), undefined, { numeric: true });
    });
}

function isSplitGroupDisplayRow(row: any) {
  return row?.__row_kind === 'split_group' && row?.__split_group;
}

function displayRowKey(row: any) {
  if (isSplitGroupDisplayRow(row)) return `split_group:${Number(row.__split_group.id)}`;
  return `policy:${Number(row?.id)}`;
}

function buildPolicyDisplayRows(policies: any[]) {
  const visible: any[] = [];
  policies.forEach((policy) => {
    if (!isSplitPolicy(policy)) {
      visible.push({ ...policy, __row_kind: 'policy', __visible_sort: Number(policy.sort || 0) });
      return;
    }
    sortSplitLeaves(Array.isArray(policy?.split_groups) ? policy.split_groups : []).forEach((group) => {
      visible.push({
        ...policy,
        __row_kind: 'split_group',
        __split_group: group,
        __visible_sort: Number(group.global_sort ?? policy.sort ?? 0),
      });
    });
  });
  return visible.sort((left, right) => {
    const difference = Number(left.__visible_sort || 0) - Number(right.__visible_sort || 0);
    if (difference !== 0) return difference;
    return displayRowKey(left).localeCompare(displayRowKey(right), undefined, { numeric: true });
  });
}

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

function singleUserIDRange(value: any): { min: number; max: number } | undefined {
  const conditions = parseConditions(value);
  if (conditions.length !== 1) return undefined;
  const condition = conditions[0];
  if (condition.field !== 'user_id' || condition.operator !== 'between') return undefined;
  const min = finiteNumber(condition.min);
  const max = finiteNumber(condition.max);
  if (min === undefined || max === undefined || min > max) return undefined;
  return { min, max };
}

function canConvertRangeToSplit(row: any) {
  if (String(row?.mode || 'standard').toLowerCase() !== 'standard') return false;
  if (normalizePolicyAction(row?.action) !== 'override') return false;
  if (parseExtraNodes(row?.extra_nodes).length !== 0) return false;
  return Boolean(singleUserIDRange(row?.conditions));
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

function parseExtraNodes(value: any): string[] {
  let current = value;
  for (let index = 0; index < 3 && typeof current === 'string'; index += 1) {
    const text = current.trim();
    if (!text) return [];
    if (!text.startsWith('[')) {
      return current.split(/\r?\n/).map((item: string) => item.trim()).filter(Boolean);
    }
    try {
      current = JSON.parse(text);
    } catch {
      return current.split(/\r?\n/).map((item: string) => item.trim()).filter(Boolean);
    }
  }
  if (!Array.isArray(current)) return [];
  return current.map((item) => String(item || '').trim()).filter(Boolean);
}

function normalizePolicyAction(value: any): PolicyAction {
  if (value === 'original' || value === 'hide') return value;
  return 'override';
}

function normalizeExtraNodePosition(value: any): ExtraNodePosition {
  return value === 'before' ? 'before' : 'after';
}

function normalizeResolveEntryHost(value: any) {
  if (value === true || value === 'true') return true;
  const number = Number(value);
  return Number.isFinite(number) && number !== 0;
}

function policyPayload(row: any, overrides: Record<string, any> = {}) {
  const source = { ...row, ...overrides };
  const action = normalizePolicyAction(source.action);
  return {
    id: source.id,
    name: String(source.name || '').trim(),
    entry_host: action === 'override' ? String(source.entry_host || '').trim() : '',
    resolve_entry_host: action === 'override' && normalizeResolveEntryHost(source.resolve_entry_host) ? 1 : 0,
    action,
    conditions: cleanConditions(source.conditions),
    members: (Array.isArray(source.members) ? source.members : [])
      .map((member: any) => typeof member === 'string' ? splitMemberKey(member) : member)
      .filter(Boolean),
    extra_nodes: parseExtraNodes(source.extra_nodes),
    extra_nodes_position: normalizeExtraNodePosition(source.extra_nodes_position),
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
  const extraNodeCount = parseExtraNodes(Form.useWatch('extra_nodes', form)).length;

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
      resolve_entry_host: normalizeResolveEntryHost(row?.resolve_entry_host),
      action: normalizePolicyAction(row?.action),
      conditions,
      members: normalizedMembers(row),
      extra_nodes: parseExtraNodes(row?.extra_nodes).join('\n'),
      extra_nodes_position: normalizeExtraNodePosition(row?.extra_nodes_position),
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
        {action === 'override' && <>
          <Form.Item
            name="entry_host"
            label="独立入口地址"
            rules={[
              { required: true, whitespace: true, message: '请输入独立入口地址' },
              { validator: (_, value) => /[,，()]/.test(String(value || '')) ? Promise.reject(new Error('这里只能填写单个普通域名或 IP，条件请在下方配置')) : Promise.resolve() },
            ]}
            tooltip="规则命中时，所选节点会下发这个地址。这里只填写普通域名或 IP。"
          >
            <Input placeholder="例如 vip.example.com 或 1.2.3.4" />
          </Form.Item>
          <Form.Item
            name="resolve_entry_host"
            valuePropName="checked"
            extra="勾选后，用户拉取订阅时由后端解析上方域名并下发 IP；上方填写 IP 时会原样下发。解析暂时失败时仍下发原域名，避免节点缺失。"
          >
            <Checkbox>解析域名下发 IP</Checkbox>
          </Form.Item>
        </>}
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
        <Form.Item
          name="extra_nodes"
          label="额外下发节点"
          tooltip="这里的节点使用 URI 自带的地址和认证信息，不会被上方的独立入口地址覆盖。"
          extra={`一行一个完整节点 URI，行顺序就是下发顺序；空行会在保存时自动忽略。当前 ${extraNodeCount} 个。`}
        >
          <Input.TextArea
            autoSize={{ minRows: 5, maxRows: 12 }}
            placeholder={'例如：\ntrojan://password@example.com:443?allowInsecure=1&peer=example.com#Hong%20Kong%20%7C%2001'}
          />
        </Form.Item>
        {extraNodeCount > 0 && <Form.Item
          name="extra_nodes_position"
          label="额外节点位置"
          tooltip="选择置顶后，额外节点会按上方行顺序排在站内节点前面。"
        >
          <Segmented block options={[
            { label: '现有节点前面（置顶）', value: 'before' },
            { label: '现有节点后面（置底）', value: 'after' },
          ]} />
        </Form.Item>}
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
  if (isSplitPolicy(row)) return false;
  return parseConditions(row.conditions).every((condition) => conditionMatches(condition, input));
}

function policyResultDescription(row: any) {
  if (isSplitPolicy(row)) return `结果：按固定二分组下发不同入口，共 ${Number(row?.snapshot_user_count || 0)} 人`;
  const action = normalizePolicyAction(row?.action);
  const extraNodeCount = parseExtraNodes(row?.extra_nodes).length;
  const position = normalizeExtraNodePosition(row?.extra_nodes_position) === 'before' ? '置顶' : '置底';
  const extraDescription = extraNodeCount ? `；另下发 ${extraNodeCount} 个额外节点（${position}，保留 URI 原地址）` : '';
  if (action === 'hide') return `结果：不下发所选节点${extraDescription}`;
  if (action === 'original') return `结果：下发所选节点各自的原入口地址${extraDescription}`;
  const resolveDescription = normalizeResolveEntryHost(row?.resolve_entry_host) ? '（后端解析域名后下发 IP）' : '';
  return `结果：下发独立入口 ${row?.entry_host || '-'}${resolveDescription}${extraDescription}`;
}

function ExtraNodesSummary({ value, position }: { value: any; position: any }) {
  const nodes = parseExtraNodes(value);
  if (!nodes.length) return <>-</>;
  const positionLabel = normalizeExtraNodePosition(position) === 'before' ? '置顶' : '置底';
  return <details className="client-entry-members">
    <summary>额外下发 {nodes.length} 个节点（{positionLabel}）</summary>
    <div style={{ marginTop: 8, maxHeight: 180, maxWidth: 360, overflow: 'auto' }}>
      {nodes.map((node, index) => <Typography.Text
        key={`${index}-${node}`}
        code
        copyable={{ text: node }}
        ellipsis={{ tooltip: node }}
        style={{ display: 'block', maxWidth: 340, marginBottom: 6 }}
      >
        {index + 1}. {node}
      </Typography.Text>)}
    </div>
  </details>;
}

function SplitPolicyCreator({
  children,
  serverOptions,
  onDone,
}: {
  children: React.ReactElement;
  serverOptions: ClientEntryServerOption[];
  onDone: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [preview, setPreview] = useState<any>();
  const previewSequence = useRef(0);
  const [form] = Form.useForm();
  const minutes = Form.useWatch('minutes', form) || 60;

  const refreshPreview = async (value: number) => {
    const sequence = ++previewSequence.current;
    setPreviewLoading(true);
    try {
      const response = await apiGet('/server/client-entry-user-policy/split-preview', { minutes: value });
      if (sequence === previewSequence.current) setPreview(response.data || {});
    } catch (error: any) {
      if (sequence === previewSequence.current) {
        setPreview(undefined);
        message.error(error?.message || '读取近期订阅用户失败');
      }
    } finally {
      if (sequence === previewSequence.current) setPreviewLoading(false);
    }
  };

  useEffect(() => {
    if (!open) return;
    const value = Math.max(1, Math.floor(Number(minutes) || 60));
    const timer = window.setTimeout(() => refreshPreview(value), 300);
    return () => window.clearTimeout(timer);
  }, [open, minutes]);

  const show = () => {
    form.resetFields();
    form.setFieldsValue({ minutes: 60, enabled: true, resolve_entry_host: false, members: [] });
    setPreview(undefined);
    setOpen(true);
  };

  const save = async () => {
    setSaving(true);
    try {
      const values = await form.validateFields();
      await apiPost('/server/client-entry-user-policy/split-create', {
        name: String(values.name || '').trim(),
        minutes: Number(values.minutes),
        members: (values.members || []).map(splitMemberKey).filter(Boolean),
        entry_host_a: String(values.entry_host_a || '').trim(),
        entry_host_b: String(values.entry_host_b || '').trim(),
        resolve_entry_host: values.resolve_entry_host ? 1 : 0,
        enabled: values.enabled ? 1 : 0,
        remarks: String(values.remarks || '').trim(),
      });
      message.success('已创建固定二分规则');
      setOpen(false);
      onDone();
    } catch (error: any) {
      if (!error?.errorFields) message.error(error?.message || '创建二分规则失败');
    } finally {
      setSaving(false);
    }
  };

  const userCount = finiteNumber(preview?.user_count) || 0;
  return <>
    {React.cloneElement(children, { onClick: show })}
    <Modal
      title="从近期订阅用户创建固定二分"
      open={open}
      onCancel={() => setOpen(false)}
      onOk={save}
      okText="创建并固定分组"
      confirmLoading={saving}
      width={820}
      destroyOnHidden
    >
      <Alert
        type="info"
        showIcon
        message="创建时固定用户名单，后续不会自动换组"
        description="系统会把时间窗口内成功拉取过节点列表的用户按 ID 顺序平均分成 A、B 两组。两组使用完全相同的生效节点，只需要分别填写入口；之后可继续二分任意叶子组。"
        style={{ marginBottom: 18 }}
      />
      <Form form={form} layout="vertical">
        <Form.Item name="name" label="规则名称" rules={[{ required: true, whitespace: true, message: '请输入规则名称' }]}>
          <Input placeholder="例如：近期活跃用户排查" maxLength={100} showCount />
        </Form.Item>
        <Space align="start" wrap style={{ width: '100%' }}>
          <Form.Item name="minutes" label="统计最近多少分钟" rules={[{ required: true }]}>
            <InputNumber min={1} max={43200} precision={0} addonAfter="分钟" style={{ width: 220 }} />
          </Form.Item>
          <div style={{ paddingTop: 30 }}>
            {previewLoading ? <Spin size="small" /> : <Tag color={userCount >= 2 ? 'cyan' : 'orange'}>当前可固定 {userCount} 人</Tag>}
          </div>
        </Space>
        <Alert
          type={userCount >= 2 ? 'success' : 'warning'}
          showIcon
          message={userCount >= 2 ? `预计 A 组 ${Math.ceil(userCount / 2)} 人，B 组 ${Math.floor(userCount / 2)} 人` : '至少需要 2 个近期订阅用户'}
          description="活跃记录从安装此功能并更新服务后开始累计；创建瞬间会重新统计，人数可能与预览略有变化。"
          style={{ marginBottom: 18 }}
        />
        <Form.Item name="members" label="两组共同生效节点" rules={[{ required: true, message: '请选择生效节点' }]}>
          <Select mode="multiple" showSearch allowClear options={serverOptions} optionFilterProp="label" placeholder="选择节点；以后继续二分会自动继承，无需重复选择" />
        </Form.Item>
        <Space align="start" wrap style={{ width: '100%' }}>
          <Form.Item name="entry_host_a" label="A 组入口" rules={[{ required: true, whitespace: true, message: '请输入 A 组入口' }]} style={{ minWidth: 320, flex: 1 }}>
            <Input placeholder="a.example.com 或 IP" />
          </Form.Item>
          <Form.Item name="entry_host_b" label="B 组入口" rules={[{ required: true, whitespace: true, message: '请输入 B 组入口' }]} style={{ minWidth: 320, flex: 1 }}>
            <Input placeholder="b.example.com 或 IP" />
          </Form.Item>
        </Space>
        <Form.Item name="resolve_entry_host" valuePropName="checked" extra="两组及后续子组共同使用此设置；DNS 成功结果缓存 1 分钟，解析失败回退原域名。">
          <Checkbox>解析域名后下发 IP</Checkbox>
        </Form.Item>
        <Form.Item name="enabled" label="状态" valuePropName="checked">
          <Switch checkedChildren="启用" unCheckedChildren="禁用" />
        </Form.Item>
        <Form.Item name="remarks" label="备注">
          <Input.TextArea rows={2} maxLength={255} showCount />
        </Form.Item>
      </Form>
    </Modal>
  </>;
}

function RangePolicySplitConverter({ row, onDone }: { row: any; onDone: () => void }) {
  const [open, setOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [form] = Form.useForm();
  const range = singleUserIDRange(row?.conditions);
  if (!range) return null;

  const show = () => {
    form.resetFields();
    setOpen(true);
  };

  const convert = async () => {
    try {
      const values = await form.validateFields();
      setSaving(true);
      await apiPost('/server/client-entry-user-policy/split-convert', {
        policy_id: row.id,
        entry_host_a: String(values.entry_host_a || '').trim(),
        entry_host_b: String(values.entry_host_b || '').trim(),
      });
      message.success('已将 ID 范围规则转换为固定二分');
      setOpen(false);
      onDone();
    } catch (error: any) {
      if (!error?.errorFields) message.error(error?.message || '转换固定二分失败');
    } finally {
      setSaving(false);
    }
  };

  return <>
    <a onClick={show}><BranchesOutlined /> 固定二分</a>
    <Modal
      title={`将“${String(row?.name || `规则 #${row?.id}`)}”转换为固定二分`}
      open={open}
      onCancel={() => setOpen(false)}
      onOk={convert}
      okText="确认转换并固定"
      confirmLoading={saving}
      destroyOnHidden
      width={680}
    >
      <Alert
        type="info"
        showIcon
        message={`固定用户 ID #${range.min} ～ #${range.max} 范围内当前实际存在的用户`}
        description={`转换后仍沿用原规则的名称、${Array.isArray(row?.members) ? row.members.length : 0} 个生效节点、规则顺序、启停状态和${normalizeResolveEntryHost(row?.resolve_entry_host) ? '已开启的' : '未开启的'}域名解析设置。用户名单会固定为静态快照；只要当前叶子组仍有至少 2 人，就可以继续逐层二分。这里只需填写 A、B 两组入口。`}
        style={{ marginBottom: 18 }}
      />
      <Form form={form} layout="vertical">
        <Space align="start" wrap style={{ width: '100%' }}>
          <Form.Item name="entry_host_a" label="A 组入口" rules={[{ required: true, whitespace: true, message: '请输入 A 组入口' }]} style={{ minWidth: 290, flex: 1 }}>
            <Input placeholder="a.example.com 或 IP" />
          </Form.Item>
          <Form.Item name="entry_host_b" label="B 组入口" rules={[{ required: true, whitespace: true, message: '请输入 B 组入口' }]} style={{ minWidth: 290, flex: 1 }}>
            <Input placeholder="b.example.com 或 IP" />
          </Form.Item>
        </Space>
      </Form>
    </Modal>
  </>;
}

function SplitGroupRowActions({ row, group, onDone }: { row: any; group: SplitGroup; onDone: () => void | Promise<void> }) {
  const [splitOpen, setSplitOpen] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [splitForm] = Form.useForm();
  const [editForm] = Form.useForm();

  const openSplit = () => {
    splitForm.resetFields();
    setSplitOpen(true);
  };
  const openEdit = () => {
    editForm.resetFields();
    editForm.setFieldsValue({ entry_host: group.entry_host });
    setEditOpen(true);
  };
  const submitSplit = async () => {
    setSaving(true);
    try {
      const values = await splitForm.validateFields();
      await apiPost('/server/client-entry-user-policy/split-group', {
        policy_id: row.id,
        group_id: group.id,
        entry_host_a: String(values.entry_host_a || '').trim(),
        entry_host_b: String(values.entry_host_b || '').trim(),
      });
      message.success(`${group.path} 组已继续二分，新分组已在原位置展开`);
      setSplitOpen(false);
      await onDone();
    } catch (error: any) {
      if (!error?.errorFields) message.error(error?.message || '继续二分失败');
    } finally {
      setSaving(false);
    }
  };
  const submitEdit = async () => {
    setSaving(true);
    try {
      const values = await editForm.validateFields();
      await apiPost('/server/client-entry-user-policy/split-group-host', {
        policy_id: row.id,
        group_id: group.id,
        entry_host: String(values.entry_host || '').trim(),
      });
      message.success('分组入口已更新');
      setEditOpen(false);
      await onDone();
    } catch (error: any) {
      if (!error?.errorFields) message.error(error?.message || '更新入口失败');
    } finally {
      setSaving(false);
    }
  };

  return <>
    <Space className="client-entry-actions" size={10}>
      <a onClick={openEdit}><EditOutlined /> 修改入口</a>
      {Number(group.user_count) >= 2
        ? <a onClick={openSplit}><BranchesOutlined /> 继续二分</a>
        : <Typography.Text type="secondary">不足 2 人</Typography.Text>}
    </Space>
    <Modal
      title={`继续二分 ${group.path} 组`}
      open={splitOpen}
      onCancel={() => setSplitOpen(false)}
      onOk={submitSplit}
      confirmLoading={saving}
      okText="确认二分"
      destroyOnHidden
    >
      <Alert
        type="info"
        showIcon
        message={`${group.path} 组当前 ${Number(group.user_count || 0)} 人`}
        description="当前行会被两个新叶子组原地替换；节点、解析设置和全局优先级都会继承。"
        style={{ marginBottom: 16 }}
      />
      <Form form={splitForm} layout="vertical">
        <Form.Item name="entry_host_a" label={`${group.path}.1 组入口`} rules={[{ required: true, whitespace: true, message: '请输入第一个子组入口' }]}>
          <Input placeholder="域名或 IP" />
        </Form.Item>
        <Form.Item name="entry_host_b" label={`${group.path}.2 组入口`} rules={[{ required: true, whitespace: true, message: '请输入第二个子组入口' }]}>
          <Input placeholder="域名或 IP" />
        </Form.Item>
      </Form>
    </Modal>
    <Modal
      title={`修改 ${group.path} 组入口`}
      open={editOpen}
      onCancel={() => setEditOpen(false)}
      onOk={submitEdit}
      confirmLoading={saving}
      okText="保存入口"
      destroyOnHidden
    >
      <Form form={editForm} layout="vertical">
        <Form.Item name="entry_host" label="入口地址" rules={[{ required: true, whitespace: true, message: '请输入入口地址' }]}>
          <Input placeholder="域名或 IP" />
        </Form.Item>
      </Form>
    </Modal>
  </>;
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
      const hasRelevantSplitRule = rows.some((row) => Number(row.enabled) !== 0 && isSplitPolicy(row) && (!selectedMember || normalizedMembers(row).includes(selectedMember)));
      if (hasRelevantSplitRule) {
        setResult(undefined);
        setSimulationError('当前节点存在固定二分规则，用户所属分组只保存在服务端，请以实际订阅结果为准');
        return;
      }
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
      description="固定二分规则按服务端保存的用户快照分组，不在这里按普通条件模拟。"
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
  const [policyRows, setPolicyRows] = useState<any[]>([]);
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
      const normalizedPolicies = policies.map((row: any) => ({
        ...row,
        mode: isSplitPolicy(row) ? 'split' : 'standard',
        resolve_entry_host: normalizeResolveEntryHost(row.resolve_entry_host),
        split_groups: Array.isArray(row.split_groups) ? row.split_groups.map((group: any) => ({
          ...group,
          id: Number(group.id),
          policy_id: Number(group.policy_id),
          parent_id: group.parent_id === undefined || group.parent_id === null ? undefined : Number(group.parent_id),
          user_count: Number(group.user_count || 0),
          sort: Number(group.sort || 0),
          global_sort: group.global_sort === undefined || group.global_sort === null ? undefined : Number(group.global_sort),
          is_leaf: group.is_leaf === true || Number(group.is_leaf) !== 0,
        })) : [],
        conditions: parseConditions(row.conditions),
        extra_nodes: parseExtraNodes(row.extra_nodes),
        extra_nodes_position: normalizeExtraNodePosition(row.extra_nodes_position),
      }));
      setPolicyRows(normalizedPolicies);
      setRows(buildPolicyDisplayRows(normalizedPolicies));
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
      if (isSplitPolicy(row)) {
        await apiPost('/server/client-entry-user-policy/enabled', { id: row.id, enabled: enabled ? 1 : 0 });
      } else {
        await apiPost('/server/client-entry-user-policy/save', policyPayload(row, { enabled }));
      }
      message.success(enabled ? '规则已启用' : '规则已禁用');
      load();
    } catch (error: any) {
      message.error(error?.message || '状态更新失败');
    }
  };

  const saveSort = async (nextRows: any[]) => {
    await apiPost('/server/client-entry-user-policy/sort', {
      items: nextRows.map((row) => isSplitGroupDisplayRow(row)
        ? { kind: 'split_group', id: Number(row.__split_group.id) }
        : { kind: 'policy', id: Number(row.id) }),
    });
  };

  const handleDrop = async (targetRow: any) => {
    if (!draggingKey) return;
    const targetKey = displayRowKey(targetRow);
    if (draggingKey === targetKey) {
      setDraggingKey(null);
      return;
    }
    const fromIndex = rows.findIndex((row) => displayRowKey(row) === draggingKey);
    const toIndex = rows.findIndex((row) => displayRowKey(row) === targetKey);
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
        if (isSplitGroupDisplayRow(row)) return <Space size={6} wrap>
          <Tag color="purple">{row.__split_group.path} 组</Tag>
          <span className="client-entry-rule-name" title={text}>{text}</span>
        </Space>;
        return <span className="client-entry-rule-name" title={text}>{text}</span>;
      },
    },
    {
      title: '匹配条件（全部 AND）',
      dataIndex: 'conditions',
      width: 620,
      render: (value: any, row: any) => {
        if (isSplitPolicy(row)) {
          const group = isSplitGroupDisplayRow(row) ? row.__split_group as SplitGroup : undefined;
          const range = singleUserIDRange(value);
          const from = Number(row.snapshot_from || 0);
          const to = Number(row.snapshot_to || 0);
          const minutes = from > 0 && to >= from ? Math.max(1, Math.round((to - from) / 60)) : 0;
          if (range) return <Space direction="vertical" size={4}>
            <Space wrap>
              <Tag color="purple" icon={<BranchesOutlined />}>{group?.path || '-'} 组固定名单</Tag>
              <Tag color="cyan">{Number(group?.user_count || 0)} 人</Tag>
              <Tag>ID 范围固定快照 #{range.min} ～ #{range.max}</Tag>
            </Space>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              来源：用户 ID {range.min}～{range.max} 固定快照
              {to > 0 && <> · 固定于 {formatSnapshotTime(to)}</>}
            </Typography.Text>
          </Space>;
          return <Space direction="vertical" size={4}>
            <Space wrap>
              <Tag color="purple" icon={<BranchesOutlined />}>{group?.path || '-'} 组固定名单</Tag>
              <Tag color="cyan">{Number(group?.user_count || 0)} 人</Tag>
              {minutes > 0 && <Tag>最近 {minutes} 分钟</Tag>}
            </Space>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              固定于 {formatSnapshotTime(row.snapshot_to)}，后续用户不会自动加入或换组
            </Typography.Text>
          </Space>;
        }
        const conditions = parseConditions(value);
        if (!conditions.length) return <Tag>全部用户</Tag>;
        const hasEmailList = conditions.some((condition) => condition.field === 'email' && condition.operator === 'in' && (condition.values || []).length > 1);
        const hasIDRange = conditions.some((condition) => condition.field === 'user_id' && condition.operator === 'between');
        const idRangeUserCount = finiteNumber(row?.id_range_user_count);
        return <div className={`client-entry-condition-list${hasEmailList ? ' client-entry-condition-list--expandable' : ''}`}>
          {conditions.map((condition, index) => {
            const text = conditionSummary(condition);
            if (condition.field === 'email' && condition.operator === 'in') return <EmailConditionSummary condition={condition} key={`${condition.field}-${condition.operator}-${index}`} />;
            return <Tag className="client-entry-condition-tag" title={text} key={`${condition.field}-${condition.operator}-${index}`}>{text}</Tag>;
          })}
          {hasIDRange && idRangeUserCount !== undefined && <Tag
            color="cyan"
            title="按当前数据库中实际存在的用户统计；已删除的用户 ID 不计入"
          >ID 范围内实际 {idRangeUserCount} 人</Tag>}
        </div>;
      },
    },
    {
      title: '命中结果',
      key: 'result',
      width: 410,
      render: (_: any, row: any) => {
        if (isSplitGroupDisplayRow(row)) return <Space direction="vertical" size={4}>
          <Typography.Text code copyable={{ text: String(row.__split_group.entry_host || '') }}>{row.__split_group.entry_host || '-'}</Typography.Text>
          {normalizeResolveEntryHost(row.resolve_entry_host) && <Tag color="cyan" style={{ width: 'fit-content', marginInlineEnd: 0 }}>解析为 IP</Tag>}
        </Space>;
        const action = normalizePolicyAction(row.action);
        if (action === 'hide') return <Tag color="red">不下发节点</Tag>;
        if (action === 'original') return <Tag color="blue">下发原入口地址</Tag>;
        return <Space direction="vertical" size={4}>
          <Typography.Text code copyable={{ text: String(row.entry_host || '') }}>{row.entry_host || '-'}</Typography.Text>
          {normalizeResolveEntryHost(row.resolve_entry_host) && <Tag color="cyan" style={{ width: 'fit-content', marginInlineEnd: 0 }}>解析为 IP</Tag>}
        </Space>;
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
      title: '额外节点',
      dataIndex: 'extra_nodes',
      width: 190,
      render: (value: any, row: any) => isSplitPolicy(row) ? <Typography.Text type="secondary">二分组不使用</Typography.Text> : <ExtraNodesSummary value={value} position={row.extra_nodes_position} />,
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
      width: 310,
      render: (_: any, row: any) => {
        if (isSplitGroupDisplayRow(row)) return <Space className="client-entry-actions" size={10}>
          <SplitGroupRowActions row={row} group={row.__split_group} onDone={load} />
          <Popconfirm title={`确认删除“${row.name || `规则 #${row.id}`}”整条二分规则及全部固定名单？`} onConfirm={() => drop(row)}><a>删除整组</a></Popconfirm>
        </Space>;
        const copyRow = { ...row, id: undefined, name: `${row.name || `规则 #${row.id}`} - 副本` };
        return <Space className="client-entry-actions" size={10}>
          <PolicyEditor row={row} onDone={load} serverOptions={serverOptions}><a>编辑</a></PolicyEditor>
          {canConvertRangeToSplit(row) && <RangePolicySplitConverter row={row} onDone={load} />}
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
        description="节点编辑中标记为“仅入口分配”的节点，请先保持节点“显示”为“显示”。固定二分的每个当前叶子组都会直接显示为主表独立行，不再显示父容器；这些行可与普通规则一起拖动，页面顺序就是实际订阅匹配优先级。名单固定后可继续二分任意叶子组。"
      />
      <Card className="block-card" styles={{ body: { padding: 0 } }}>
        <div className="forest-table-action">
          <Space wrap>
            <PolicyEditor onDone={load} serverOptions={serverOptions}><Button type="primary" icon={<PlusOutlined />}>新增入口规则</Button></PolicyEditor>
            <SplitPolicyCreator onDone={load} serverOptions={serverOptions}><Button icon={<BranchesOutlined />}>近期用户固定二分</Button></SplitPolicyCreator>
            <Button icon={<PlayCircleOutlined />} onClick={() => setSimulatorOpen(true)}>模拟匹配</Button>
            <Typography.Text type="secondary">普通规则和二分叶子组统一从上到下匹配；拖动任意行即可调整全局优先级。</Typography.Text>
          </Space>
        </div>
        <Table
          className="forest-table"
          rowKey={displayRowKey}
          tableLayout="fixed"
          columns={columns}
          dataSource={rows}
          pagination={false}
          scroll={{ x: 2405 }}
          rowClassName={(row) => `sortable-row ${draggingKey === displayRowKey(row) ? 'dragging-row' : ''} ${isSplitGroupDisplayRow(row) ? 'split-group-main-row' : ''}`}
          onRow={(row) => ({
            draggable: true,
            onDragStart: () => setDraggingKey(displayRowKey(row)),
            onDragOver: (event) => event.preventDefault(),
            onDrop: () => handleDrop(row),
            onDragEnd: () => setDraggingKey(null),
          })}
        />
      </Card>
    </Spin>
    <SimulationModal open={simulatorOpen} onClose={() => setSimulatorOpen(false)} rows={policyRows} serverOptions={serverOptions} />
  </div>;
}
