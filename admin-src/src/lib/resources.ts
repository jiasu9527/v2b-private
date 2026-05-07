import type { ResourceConfig } from './crud';
import { boolNumber, safeJsonParse } from './api';

const resetOptions = [
  { label: '每月1号', value: 0 },
  { label: '按月重置', value: 1 },
  { label: '不重置', value: 2 },
  { label: '每年1月1日', value: 3 },
  { label: '按年重置', value: 4 },
];

function jsonList(value: any) {
  const parsed = safeJsonParse(value, value);
  if (Array.isArray(parsed)) return parsed;
  if (typeof parsed === 'string') return parsed.split(/[\n,]+/).map((x) => x.trim()).filter(Boolean);
  return [];
}

export const resources: Record<string, ResourceConfig> = {
  plans: {
    key: 'plans', title: '套餐管理', fetch: '/plan/fetch', save: '/plan/save', drop: '/plan/drop', sort: '/plan/sort', searchKey: 'name',
    columns: [
      { key: 'id', title: 'ID', width: 70 }, { key: 'name', title: '名称', width: 180 }, { key: 'group_id', title: '权限组', width: 90 },
      { key: 'transfer_enable', title: '流量(GB)', width: 130 }, { key: 'month_price', title: '月付', width: 100 },
      { key: 'reset_traffic_method', title: '重置方式', width: 100 }, { key: 'show', title: '显示', type: 'bool', width: 80 }, { key: 'capacity_limit', title: '容量', width: 80 }
    ],
    fields: [
      { name: 'name', label: '套餐名称', required: true }, { name: 'group_id', label: '权限组ID', type: 'number', required: true },
      { name: 'transfer_enable', label: '流量(GB)', type: 'number', required: true }, { name: 'device_limit', label: '设备限制', type: 'number' },
      { name: 'speed_limit', label: '限速 Mbps', type: 'number' }, { name: 'capacity_limit', label: '最大容纳用户', type: 'number' },
      { name: 'month_price', label: '月付(元)', type: 'number' }, { name: 'quarter_price', label: '季付(元)', type: 'number' },
      { name: 'half_year_price', label: '半年付(元)', type: 'number' }, { name: 'year_price', label: '年付(元)', type: 'number' },
      { name: 'two_year_price', label: '两年付(元)', type: 'number' }, { name: 'three_year_price', label: '三年付(元)', type: 'number' },
      { name: 'onetime_price', label: '一次性(元)', type: 'number' }, { name: 'reset_price', label: '重置流量(元)', type: 'number' },
      { name: 'reset_traffic_method', label: '流量重置方式', type: 'select', options: resetOptions },
      { name: 'content', label: '套餐说明', type: 'textarea', span: 24 }, { name: 'force_update', label: '强制更新用户', type: 'switch' }
    ],
    beforeSave(values) {
      return { ...values, force_update: !!values.force_update };
    }
  },
  notices: {
    key: 'notices', title: '公告管理', fetch: '/notice/fetch', save: '/notice/save', drop: '/notice/drop', show: '/notice/show', searchKey: 'title',
    columns: [{ key: 'id', title: 'ID', width: 70 }, { key: 'title', title: '标题' }, { key: 'show', title: '显示', type: 'bool', width: 80 }, { key: 'updated_at', title: '更新', type: 'time', width: 170 }],
    fields: [{ name: 'title', label: '标题', required: true }, { name: 'content', label: '内容', type: 'textarea', span: 24 }, { name: 'img_url', label: '图片 URL' }, { name: 'tags', label: '标签 JSON/逗号分隔', type: 'textarea', span: 24 }],
    beforeSave(values) { return { ...values, tags: jsonList(values.tags) }; }
  },
  coupons: {
    key: 'coupons', title: '优惠券', fetch: '/coupon/fetch', save: '/coupon/generate', drop: '/coupon/drop', show: '/coupon/show', saveAsForm: true,
    columns: [{ key: 'id', title: 'ID', width: 70 }, { key: 'code', title: '券码', width: 160 }, { key: 'name', title: '名称' }, { key: 'type', title: '类型', width: 80 }, { key: 'value', title: '值', width: 80 }, { key: 'show', title: '显示', type: 'bool', width: 80 }],
    fields: [{ name: 'name', label: '名称', required: true }, { name: 'code', label: '指定券码' }, { name: 'generate_count', label: '生成数量', type: 'number' }, { name: 'type', label: '类型(1金额/2比例)', type: 'number', required: true }, { name: 'value', label: '金额或比例', type: 'number', required: true }, { name: 'limit_use', label: '总使用次数', type: 'number' }, { name: 'limit_use_with_user', label: '每用户次数', type: 'number' }, { name: 'started_at', label: '开始时间戳', type: 'number', required: true }, { name: 'ended_at', label: '结束时间戳', type: 'number', required: true }, { name: 'limit_plan_ids', label: '限制套餐ID JSON/逗号分隔', type: 'textarea', span: 24 }, { name: 'limit_period', label: '限制周期 JSON/逗号分隔', type: 'textarea', span: 24 }],
    beforeSave(values) { return { ...values, limit_plan_ids: jsonList(values.limit_plan_ids), limit_period: jsonList(values.limit_period) }; }
  },
  giftcards: {
    key: 'giftcards', title: '礼品卡', fetch: '/giftcard/fetch', save: '/giftcard/generate', drop: '/giftcard/drop', saveAsForm: true,
    columns: [{ key: 'id', title: 'ID', width: 70 }, { key: 'code', title: '卡号', width: 180 }, { key: 'name', title: '名称' }, { key: 'type', title: '类型', width: 80 }, { key: 'value', title: '值', width: 80 }, { key: 'limit_use', title: '次数', width: 80 }, { key: 'updated_at', title: '更新', type: 'time', width: 170 }],
    fields: [{ name: 'name', label: '名称', required: true }, { name: 'code', label: '指定卡号' }, { name: 'generate_count', label: '生成数量', type: 'number' }, { name: 'type', label: '类型', type: 'number', required: true }, { name: 'value', label: '数值', type: 'number' }, { name: 'plan_id', label: '套餐ID', type: 'number' }, { name: 'started_at', label: '开始时间戳', type: 'number', required: true }, { name: 'ended_at', label: '结束时间戳', type: 'number', required: true }, { name: 'limit_use', label: '使用次数', type: 'number' }]
  },
  knowledge: {
    key: 'knowledge', title: '知识库', fetch: '/knowledge/fetch', save: '/knowledge/save', drop: '/knowledge/drop', show: '/knowledge/show', sort: '/knowledge/sort', searchKey: 'title',
    columns: [{ key: 'id', title: 'ID', width: 70 }, { key: 'title', title: '标题' }, { key: 'category', title: '分类', width: 130 }, { key: 'show', title: '显示', type: 'bool', width: 80 }, { key: 'updated_at', title: '更新', type: 'time', width: 170 }],
    fields: [{ name: 'title', label: '标题', required: true }, { name: 'category', label: '分类' }, { name: 'language', label: '语言' }, { name: 'body', label: '内容', type: 'textarea', span: 24 }]
  },
  payments: {
    key: 'payments', title: '支付接口', fetch: '/payment/fetch', save: '/payment/save', drop: '/payment/drop', show: '/payment/show', sort: '/payment/sort', searchKey: 'name',
    columns: [{ key: 'id', title: 'ID', width: 70 }, { key: 'name', title: '名称' }, { key: 'payment', title: '网关', width: 120 }, { key: 'enable', title: '启用', type: 'bool', width: 80 }, { key: 'sort', title: '排序', width: 80 }],
    fields: [{ name: 'name', label: '名称', required: true }, { name: 'payment', label: '支付网关', required: true }, { name: 'icon', label: '图标' }, { name: 'notify_domain', label: '回调域名' }, { name: 'handling_fee_fixed', label: '固定手续费', type: 'number' }, { name: 'handling_fee_percent', label: '比例手续费', type: 'number' }, { name: 'config', label: '配置 JSON', type: 'json', span: 24 }],
    beforeSave(values) { return { ...values, config: safeJsonParse(values.config, {}) }; }
  },
  serverGroups: {
    key: 'serverGroups', title: '权限组', fetch: '/server/group/fetch', save: '/server/group/save', drop: '/server/group/drop', searchKey: 'name',
    columns: [{ key: 'id', title: 'ID', width: 70 }, { key: 'name', title: '名称' }, { key: 'user_count', title: '用户数', width: 90 }, { key: 'server_count', title: '节点数', width: 90 }, { key: 'updated_at', title: '更新', type: 'time', width: 170 }],
    fields: [{ name: 'name', label: '名称', required: true }]
  },
  serverRoutes: {
    key: 'serverRoutes', title: '路由规则', fetch: '/server/route/fetch', save: '/server/route/save', drop: '/server/route/drop', searchKey: 'remarks',
    columns: [{ key: 'id', title: 'ID', width: 70 }, { key: 'remarks', title: '备注' }, { key: 'match', title: '匹配', type: 'array' }, { key: 'action', title: '动作', width: 100 }, { key: 'updated_at', title: '更新', type: 'time', width: 170 }],
    fields: [{ name: 'remarks', label: '备注' }, { name: 'match', label: '匹配规则 JSON/换行分隔', type: 'textarea', span: 24 }, { name: 'action', label: '动作配置' }, { name: 'action_value', label: '动作值' }],
    beforeSave(values) { return { ...values, match: jsonList(values.match) }; }
  },
  clientEntry: {
    key: 'clientEntry', title: '客户端入口', fetch: '/server/client-entry/fetch', save: '/server/client-entry/save', drop: '/server/client-entry/drop', searchKey: 'name',
    columns: [{ key: 'id', title: 'ID', width: 70 }, { key: 'code', title: '代码', width: 110 }, { key: 'display_name', title: '显示名' }, { key: 'remote_host', title: '远程主机' }, { key: 'show', title: '显示', type: 'bool', width: 80 }, { key: 'updated_at', title: '更新', type: 'time', width: 170 }],
    fields: [
      { name: 'code', label: '代码', required: true }, { name: 'name', label: '名称', required: true }, { name: 'display_name', label: '显示名称' },
      { name: 'strategy', label: '策略', type: 'select', options: [{ label: '顺序回退', value: 'ordered-fallback' }, { label: '优先', value: 'preferred' }] },
      { name: 'show', label: '显示', type: 'switch' }, { name: 'hide_member_nodes', label: '隐藏成员节点', type: 'switch' }, { name: 'remote_enabled', label: '启用远程源', type: 'switch' },
      { name: 'remote_host', label: '远程主机' }, { name: 'remote_ssh_port', label: 'SSH端口', type: 'number' }, { name: 'remote_ssh_user', label: 'SSH用户' }, { name: 'remote_ssh_password', label: 'SSH密码' },
      { name: 'remote_group_ref', label: '远程分组' }, { name: 'remote_refresh_sec', label: '刷新秒数', type: 'number' },
      { name: 'remote_exclude_names', label: '排除节点名 JSON/换行', type: 'textarea', span: 24 }, { name: 'match', label: '入口IP JSON/换行', type: 'textarea', span: 24 }
    ],
    beforeSave(values) {
      return { ...values, show: boolNumber(values.show), remote_exclude_names: jsonList(values.remote_exclude_names), match: jsonList(values.match) };
    }
  },
};
