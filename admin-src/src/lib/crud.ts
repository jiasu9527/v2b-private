import { apiGet, apiPost, unwrapList, unwrapTotal } from './api';

export type ResourceConfig = {
  key: string;
  title: string;
  fetch: string;
  save?: string;
  drop?: string;
  show?: string;
  sort?: string;
  idKey?: string;
  fields: FieldConfig[];
  columns?: ColumnConfig[];
  pageSize?: number;
  searchKey?: string;
  saveAsForm?: boolean;
  beforeSave?: (values: any, editing?: any) => any;
};

export type FieldConfig = {
  name: string;
  label: string;
  type?: 'text' | 'number' | 'textarea' | 'switch' | 'select' | 'json' | 'tags';
  options?: { label: string; value: any }[];
  required?: boolean;
  span?: number;
  placeholder?: string;
};

export type ColumnConfig = {
  key: string;
  title: string;
  type?: 'text' | 'money' | 'bytes' | 'time' | 'bool' | 'tag' | 'json' | 'array';
  width?: number;
  render?: (value: any) => any;
};

export async function fetchResource(cfg: ResourceConfig, params: any = {}) {
  const payload = await apiGet(cfg.fetch, params);
  const rows = unwrapList(payload);
  return { rows, total: unwrapTotal(payload, rows) };
}

export async function saveResource(cfg: ResourceConfig, values: any) {
  if (!cfg.save) throw new Error('未配置保存接口');
  return apiPost(cfg.save, values, { form: cfg.saveAsForm });
}

export async function dropResource(cfg: ResourceConfig, id: any) {
  if (!cfg.drop) throw new Error('未配置删除接口');
  return apiPost(cfg.drop, { [cfg.idKey || 'id']: id });
}
