import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Alert, Button, Checkbox, Form, Input, Modal, Segmented, Space, Table, Typography, message } from 'antd';
import { apiJsonPost } from '../lib/api';
import { mergeEntryCollectionKeys } from './clientEntryLayout';
import { availableEntryCollectionOptions, collectionResolveDefaults, type EntryCollection, type EntryCollectionOption } from './clientEntryCollections';

function CollectionMemberPicker({ value = [], onChange, options, disabled }: {
  value?: string[];
  onChange?: (keys: string[]) => void;
  options: EntryCollectionOption[];
  disabled?: boolean;
}) {
  const [query, setQuery] = useState('');
  const [view, setView] = useState<'all' | 'selected'>('all');
  const [page, setPage] = useState(1);
  const selected = new Set(value);
  const filtered = options.filter((option) => (view === 'all' || selected.has(option.value)) && option.label.toLowerCase().includes(query.trim().toLowerCase()));
  useEffect(() => { setPage(1); }, [query, view]);
  useEffect(() => { setPage((current) => Math.min(current, Math.max(1, Math.ceil(filtered.length / 8)))); }, [filtered.length]);
  const visibleKeys = filtered.map((option) => option.value);
  return <div style={{ border: '1px solid #e5e5e5', borderRadius: 6, overflow: 'hidden' }}>
    <Space wrap style={{ padding: 12, width: '100%', justifyContent: 'space-between', background: '#fafafa' }}>
      <Input.Search value={query} onChange={(event) => setQuery(event.target.value)} allowClear placeholder="搜索规则名称、编号或入口" style={{ width: 260, maxWidth: '100%' }} />
      <Segmented value={view} options={[{ label: `全部可用 ${options.length}`, value: 'all' }, { label: `已选 ${value.length}`, value: 'selected' }]} onChange={(next) => setView(next as 'all' | 'selected')} />
    </Space>
    <Table
      size="small"
      rowKey="value"
      tableLayout="fixed"
      dataSource={filtered}
      columns={[
        { title: '规则', key: 'name', ellipsis: true, render: (_, option) => <span title={option.label}>{option.label.split(' · ').slice(0, -1).join(' · ') || option.label}</span> },
        { title: '当前入口', dataIndex: 'entry_host', width: '36%', ellipsis: true },
      ]}
      rowSelection={{
        selectedRowKeys: value,
        preserveSelectedRowKeys: true,
        onChange: (keys) => onChange?.(keys.map(String)),
        getCheckboxProps: () => ({ disabled }),
        columnWidth: 42,
      }}
      pagination={{ current: page, pageSize: 8, onChange: setPage, showSizeChanger: false, size: 'small', hideOnSinglePage: true }}
      locale={{ emptyText: view === 'selected' ? '尚未选择匹配的规则' : '没有匹配的可用规则' }}
    />
    <Space wrap style={{ padding: '8px 12px', width: '100%', borderTop: '1px solid #eee', justifyContent: 'space-between' }}>
      <Typography.Text type="secondary">已选 {value.length} 条 · 当前筛选 {filtered.length} 条</Typography.Text>
      <Space size={8}>
        <Button type="link" size="small" disabled={disabled || !visibleKeys.length} onClick={() => onChange?.([...new Set([...value, ...visibleKeys])])}>全选筛选结果（{filtered.length}）</Button>
        <Button type="link" size="small" disabled={disabled || !visibleKeys.some((key) => selected.has(key))} onClick={() => onChange?.(value.filter((key) => !visibleKeys.includes(key)))}>取消筛选内选择</Button>
      </Space>
    </Space>
  </div>;
}

export default function EntryCollectionEditor({ open, collection, options, initialKeys, additionalKeys = [], onClose, onDone }: {
  open: boolean;
  collection?: EntryCollection;
  options: EntryCollectionOption[];
  initialKeys: string[];
  additionalKeys?: string[];
  onClose: () => void;
  onDone: () => void | Promise<void>;
}) {
  const [form] = Form.useForm();
  const [saving, setSaving] = useState(false);
  const available = useMemo(() => availableEntryCollectionOptions(options, collection?.id), [options, collection?.id]);
  const memberKeys = Form.useWatch('member_keys', form) || [];
  const resolveTouched = useRef(false);
  const resolveDefaults = collectionResolveDefaults(available, memberKeys, collection);

  useEffect(() => {
    if (!open) return;
    const keys = collection ? mergeEntryCollectionKeys(collection.items, additionalKeys) : initialKeys;
    const hosts = [...new Set(available.filter((item) => keys.includes(item.value)).map((item) => item.entry_host).filter(Boolean))];
    resolveTouched.current = false;
    form.resetFields();
    form.setFieldsValue({ resolve_entry_host: collectionResolveDefaults(available, keys, collection).enabled, name: collection?.name || '', entry_host: collection?.entry_host || (hosts.length === 1 ? hosts[0] : ''), member_keys: keys });
  }, [open, collection]);

  const save = async () => {
    try {
      const values = await form.validateFields();
      const lookup = new Map(available.map((option) => [option.value, option.item]));
      const items = values.member_keys.map((key: string) => lookup.get(key));
      if (items.some((item: any) => !item)) {
        message.error('部分规则状态已变化，请关闭窗口并刷新后重试');
        return;
      }
      setSaving(true);
      await apiJsonPost('/server/client-entry-user-policy/collection/save', {
        ...(collection ? { id: collection.id, version: collection.version } : {}),
        name: String(values.name || '').trim(),
        entry_host: String(values.entry_host || '').trim(),
        resolve_entry_host: values.resolve_entry_host ? 1 : 0,
        items,
      });
      message.success(collection ? '入口合集已更新' : '入口合集已创建，所选规则统一使用入口和解析设置');
      onClose();
      await onDone();
    } catch (error: any) {
      if (!error?.errorFields) message.error(error?.message || '保存入口合集失败');
    } finally {
      setSaving(false);
    }
  };

  return <Modal className="entry-collection-editor" title={collection ? '编辑入口合集' : '合并到入口合集'} open={open} onCancel={onClose} onOk={save} confirmLoading={saving} okText="保存合集" cancelText="取消" width={860} destroyOnHidden styles={{ body: { maxHeight: '72vh', overflowY: 'auto' } }}>
    <Alert showIcon type="info" message="只填一个入口，成员规则统一使用" description="统一管理入口地址和域名解析设置；原规则名称、用户条件、固定名单、生效节点及全局匹配优先级不变。移出成员或解散合集时保留当前入口和解析设置，不删除规则。" style={{ marginBottom: 20 }} />
    <Form form={form} layout="vertical" onValuesChange={(changed) => {
      if ('resolve_entry_host' in changed) resolveTouched.current = true;
      if ('member_keys' in changed && !resolveTouched.current && collection?.resolve_entry_host == null) {
        form.setFieldValue('resolve_entry_host', collectionResolveDefaults(available, changed.member_keys || [], collection).enabled);
      }
    }}>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(240px, 1fr))', gap: '0 20px' }}>
      <Form.Item name="name" label="合集名称" rules={[{ required: true, whitespace: true, message: '请输入合集名称' }]}>
        <Input maxLength={255} showCount placeholder="例如：常用入口" />
      </Form.Item>
      <Form.Item name="entry_host" label="统一入口域名 / IP" rules={[
        { required: true, whitespace: true, message: '请输入统一入口地址' },
        { validator: (_, value) => /[,，()\s/?#]/.test(String(value || '').trim()) || String(value || '').includes('://') ? Promise.reject(new Error('仅填写一个普通域名或 IP，不要填写协议或路径')) : Promise.resolve() },
      ]} extra="保存后，合集内全部规则统一使用这个入口地址和下方的域名解析设置。">
        <Input placeholder="entry.example.com 或 1.2.3.4" />
      </Form.Item>
      </div>
      <Form.Item name="resolve_entry_host" valuePropName="checked" extra="统一应用于合集内全部规则。勾选后由后端解析域名并下发 IP；DNS 成功结果缓存 1 分钟，解析失败回退原域名，填写 IP 时原样下发。">
        <Checkbox>解析域名下发 IP</Checkbox>
      </Form.Item>
      {(collection?.resolve_entry_host === null || resolveDefaults.mixed) && <Alert
        showIcon
        type="info"
        message={collection?.resolve_entry_host === null ? '此合集尚未统一解析设置' : '所选规则的解析设置不一致'}
        description="保存后全部成员将统一为上方勾选状态，不再需要逐条设置。"
        style={{ marginBottom: 20 }}
      />}
      <Form.Item name="member_keys" label="合集成员" rules={[{ required: true, type: 'array', min: collection ? 1 : 2, message: collection ? '请至少选择一条规则；如不再需要合集请使用解散' : '新建合集请至少选择两条规则' }]} extra="可选择尚未加入合集的覆盖入口规则、二分叶子组，以及本合集现有成员；其他合集成员须先移出。">
        <CollectionMemberPicker options={available} disabled={saving} />
      </Form.Item>
    </Form>
  </Modal>;
}
