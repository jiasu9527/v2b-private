import { Alert, Descriptions, Table, Typography } from 'antd';
import type { AIAction } from './aiOpsModel';

export const aiActionNames: Record<string, string> = {
  ssh_command: '服务器命令', entry_host: '修改入口地址', node_change: '修改节点',
  monitor_run: '按入口规则发起检测', entry_probe_run: '指定入口与探针检测',
  user_update: '修改用户', ticket_update: '处理工单', ticket_settings: '全站工单设置',
};
const fieldNames: Record<string, string> = {
  plan_id: '套餐 ID', group_id: '节点权限组 ID', transfer_enable: '总流量配额', expired_at: '有效期',
  device_limit: '设备数上限', speed_limit: '限速', banned: '账号状态', balance: '账户余额',
  commission_balance: '佣金余额', discount: '专属折扣', commission_rate: '返佣比例',
  commission_type: '返佣类型', remarks: '管理员备注',
};
const modeNames: Record<string, string> = { all: '所有用户可提交', paid_only: '仅付费用户可提交', disabled: '关闭用户提交工单' };
function valueText(key: string, value: unknown): string {
  if (value === null) return key === 'expired_at' ? '永久有效' : '未设置 / 使用默认';
  if (value === undefined) return '—';
  if (key === 'banned') return Number(value) === 1 ? '封禁' : '正常';
  if (key === 'expired_at') return Number(value) <= 0 ? '永久有效' : new Date(Number(value) * 1000).toLocaleString('zh-CN', { hour12: false });
  if (key === 'transfer_enable') return `${Number(value) / 1073741824} GiB（${String(value)} 字节）`;
  if (key === 'balance' || key === 'commission_balance') return `${(Number(value) / 100).toFixed(2)}（${String(value)} 分）`;
  if (key === 'discount' || key === 'commission_rate') return `${String(value)}%`;
  if (key === 'speed_limit') return Number(value) === 0 ? '不限速' : `${String(value)} Mbps`;
  if (key === 'device_limit') return Number(value) === 0 ? '不限' : `${String(value)} 台`;
  return typeof value === 'object' ? JSON.stringify(value) : String(value);
}

export default function AIOpsActionSummary({ action }: { action: AIAction }) {
  const p = action.payload || {};
  if (action.kind === 'user_update') {
    const before = (p.before || {}) as Record<string, unknown>;
    const changes = (p.effective_changes || p.changes || {}) as Record<string, unknown>;
    return <>
      <Typography.Text strong>用户 #{String(p.user_id)} · {String(before.email || action.server_label || '')}</Typography.Text>
      <Table size="small" rowKey="key" pagination={false} scroll={{ x: 460 }} dataSource={Object.entries(changes).map(([key, value]) => ({ key, name: fieldNames[key] || key, before: valueText(key, before[key]), after: valueText(key, value) }))} columns={[
        { title: '字段', dataIndex: 'name', width: 120 }, { title: '修改前', dataIndex: 'before' }, { title: '修改后', dataIndex: 'after' },
      ]} />
      {p.before_remarks_truncated === true && <Typography.Text type="secondary">原备注较长，此处仅显示摘要；未修改备注时完整原文会保留。</Typography.Text>}
      {p.reset_usage === true && <Alert type="warning" showIcon message="同时将该用户已用上传和下载流量清零" />}
    </>;
  }
  if (action.kind === 'ticket_settings') return <Descriptions size="small" column={1}>
    <Descriptions.Item label="作用范围">全站用户提交工单</Descriptions.Item>
    <Descriptions.Item label="修改前">{modeNames[String(p.before_mode)] || String(p.before_mode)}</Descriptions.Item>
    <Descriptions.Item label="修改后">{modeNames[String(p.mode)] || String(p.mode)}</Descriptions.Item>
  </Descriptions>;
  if (action.kind === 'ticket_update') {
    const before = (p.before || {}) as Record<string, unknown>;
    return <>
      <Descriptions size="small" column={1}>
        <Descriptions.Item label="工单">#{String(p.ticket_id)} · {String(before.subject || '')}</Descriptions.Item>
        <Descriptions.Item label="动作">{{ close: '关闭工单', reopen: '重新开启工单', reply: '回复工单并标记已回复' }[String(p.operation)] || String(p.operation)}</Descriptions.Item>
      </Descriptions>
      {p.operation === 'reply' && <><Typography.Text strong>将发送给用户的完整回复</Typography.Text><pre className="ai-ops-pre ai-ops-command">{String(p.message || '')}</pre></>}
    </>;
  }
  if (action.kind === 'entry_probe_run' || action.kind === 'monitor_run') return <Alert type="info" showIcon message="通过探针发起新的入口检测" description="请核对下方入口、端口及探针范围。任务受理后可让 AI 查询检测结果；本次上报仍可能触发现有的自动切换或二分配置。" />;
  return null;
}
