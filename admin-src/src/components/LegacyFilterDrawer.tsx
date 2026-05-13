import React, { useEffect, useState } from 'react';
import { Button, DatePicker, Divider, Drawer, Input, Select, Space } from 'antd';
import { DeleteOutlined, FilterOutlined, PlusOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';

export type LegacyFilterOption = { label?: React.ReactNode; key?: React.ReactNode; value: any };
export type LegacyFilterDefinition = {
  key: string;
  title: string;
  condition: string[];
  type?: 'select' | 'date';
  options?: LegacyFilterOption[];
};
export type LegacyFilterValue = { key: string; condition: string; value: any };

type Props = {
  value: LegacyFilterValue[];
  keys: LegacyFilterDefinition[];
  onOk: (filters: LegacyFilterValue[]) => void;
  children?: React.ReactElement;
};

function cloneFilters(filters: LegacyFilterValue[] = []) {
  return filters.map((item) => ({ ...item }));
}

function defaultFilter(keys: LegacyFilterDefinition[]): LegacyFilterValue {
  const first = keys[0];
  return { key: first?.key || '', condition: first?.condition?.[0] || '=', value: '' };
}

export default function LegacyFilterDrawer({ value, keys, onOk, children }: Props) {
  const [open, setOpen] = useState(false);
  const [filters, setFilters] = useState<LegacyFilterValue[]>(cloneFilters(value));

  useEffect(() => {
    if (!open) setFilters(cloneFilters(value));
  }, [value, open]);

  const show = () => {
    setFilters(cloneFilters(value));
    setOpen(true);
  };
  const hide = () => setOpen(false);
  const update = (index: number, patch: Partial<LegacyFilterValue>) => setFilters((list) => list.map((item, idx) => idx === index ? { ...item, ...patch } : item));
  const remove = (index: number) => setFilters((list) => list.filter((_, idx) => idx !== index));
  const add = () => setFilters((list) => [...list, defaultFilter(keys)]);
  const submit = (nextFilters = filters) => {
    onOk(cloneFilters(nextFilters));
    setOpen(false);
  };
  const reset = () => {
    setFilters([]);
    submit([]);
  };

  const trigger = children ? React.cloneElement(children, { onClick: show } as any) : <Button onClick={show}>过滤器</Button>;

  return <>
    {trigger}
    <Drawer title="过滤器" open={open} onClose={hide} className="fixed-action-drawer forest-filter-drawer" footer={null} width={378} destroyOnClose>
      <div className="fixed-action-drawer-body">
        {filters.map((filter, index) => {
          const def = keys.find((item) => item.key === filter.key) || keys[0];
          const conditionOptions = (def?.condition || []).map((condition) => ({ label: condition, value: condition }));
          return <React.Fragment key={index}>
            <Divider orientation="left">条件{index + 1} <DeleteOutlined className="forest-filter-delete" onClick={() => remove(index)} /></Divider>
            <div className="form-group">
              <label>字段名</label>
              <Select value={filter.key} style={{ width: '100%' }} options={keys.map((item) => ({ label: item.title, value: item.key }))} onChange={(key) => {
                const next = keys.find((item) => item.key === key) || keys[0];
                update(index, { key, condition: next?.condition?.[0] || '=', value: '' });
              }} />
            </div>
            <div className="form-group">
              <label>条件</label>
              <Select value={filter.condition} style={{ width: '100%' }} options={conditionOptions} onChange={(condition) => update(index, { condition })} />
            </div>
            <div className="form-group">
              <label>欲检索内容</label>
              {def?.type === 'select' || def?.options ? <Select value={filter.value === '' ? undefined : filter.value} style={{ width: '100%' }} placeholder="请选择值" options={(def.options || []).map((item) => ({ label: item.label ?? item.key, value: item.value }))} onChange={(nextValue) => update(index, { value: nextValue })} /> : def?.type === 'date' ? <DatePicker showTime style={{ width: '100%' }} value={filter.value ? dayjs(Number(filter.value) * 1000) : null} onChange={(date) => update(index, { value: date ? date.unix() : '' })} /> : <Input value={filter.value} placeholder="值" onChange={(event) => update(index, { value: event.target.value })} onPressEnter={() => submit()} />}
            </div>
          </React.Fragment>;
        })}
        <Button type="primary" block icon={<PlusOutlined />} onClick={add}>添加条件</Button>
      </div>
      <div className="forest-drawer-action">
        <Button danger disabled={!filters.length} onClick={reset} style={{ float: 'left' }}>重置</Button>
        <Space>
          <Button onClick={hide}>取消</Button>
          <Button type="primary" disabled={!filters.length} onClick={() => submit()}>检索</Button>
        </Space>
      </div>
    </Drawer>
  </>;
}

export function FilterButton({ active, ...props }: { active: boolean } & React.ComponentProps<typeof Button>) {
  return <Button {...props} type={active ? 'primary' : 'default'} icon={<FilterOutlined />}>过滤器</Button>;
}
