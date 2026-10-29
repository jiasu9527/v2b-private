import { Alert, Descriptions, Table, Typography } from 'antd';
import type { AIAction } from './aiOpsModel';

export const aiActionNames: Record<string, string> = {
  ssh_command: '服务器命令', server_register: '登记 SSH 服务器', entry_host: '修改入口地址', node_change: '修改节点',
  monitor_run: '按入口规则发起检测', entry_probe_run: '指定入口与探针检测',
  user_update: '修改用户', ticket_update: '处理工单', ticket_settings: '全站工单设置',
  panel_operation: '后台业务操作',
  entry_rule_update: '修改入口规则', node_update: '修改节点连接配置',
  entry_manual_split: '手动触发入口二分', entry_auto_split: '设置入口自动二分', entry_split: '直接二分入口用户组',
};
const fieldNames: Record<string, string> = {
  plan_id: '套餐 ID', group_id: '节点权限组 ID', transfer_enable: '总流量配额', expired_at: '有效期',
  device_limit: '设备数上限', speed_limit: '限速', banned: '账号状态', balance: '账户余额',
  commission_balance: '佣金余额', discount: '专属折扣', commission_rate: '返佣比例',
  commission_type: '返佣类型', remarks: '管理员备注',
  name: '名称', enabled: '启用状态', sort: '排序', action: '分配动作', conditions: '匹配条件',
  members: '生效节点', entry_host: '入口地址', resolve_entry_host: '解析入口域名', host: '连接地址',
  port: '订阅连接端口', server_port: '服务监听端口', show: '显示状态', rate: '流量倍率',
  client_entry_only: '仅客户端入口', tags: '标签', tls: 'TLS 设置', server_name: 'TLS / SNI 主机名',
  tls_settings: 'TLS 配置', tlsSettings: 'TLS 配置', network: '传输方式', network_settings: '传输配置', networkSettings: '传输配置',
  allow_insecure: '允许不验证证书', flow: '流控模式', alpn: 'ALPN',
};
const modeNames: Record<string, string> = { all: '所有用户可提交', paid_only: '仅付费用户可提交', disabled: '关闭用户提交工单' };
function valueText(key: string, value: unknown): string {
  if (value === null) return key === 'expired_at' ? '永久有效' : '未设置 / 使用默认';
  if (value === undefined) return '—';
  if (['enabled', 'show', 'resolve_entry_host', 'client_entry_only'].includes(key)) return Number(value) === 1 ? '开启' : '关闭';
  if (key === 'action') return ({ override: '替换入口', original: '使用原节点', hide: '隐藏节点' } as Record<string, string>)[String(value)] || String(value);
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
  if (action.kind === 'panel_operation') return <Descriptions size="small" column={1}>
    <Descriptions.Item label="后台操作">{action.server_label || String(p.operation || '—')}</Descriptions.Item>
    <Descriptions.Item label="操作名称"><Typography.Text code>{String(p.operation || '—')}</Typography.Text></Descriptions.Item>
  </Descriptions>;
  if (['entry_manual_split', 'entry_auto_split', 'entry_split'].includes(action.kind)) {
    const target = (p.target || {}) as Record<string, unknown>;
    const direct = (p.direct || {}) as Record<string, unknown>;
    const selection = (p.selection || direct.selection || {}) as Record<string, unknown>;
    let result: Record<string, unknown> = {};
    try { const value = JSON.parse(action.output || '{}'); if (value && typeof value === 'object' && !Array.isArray(value)) result = value; } catch { /* Raw command output stays visible below. */ }
    return <>
      <Descriptions size="small" column={1}>
        <Descriptions.Item label="入口">{String(target.name || direct.name || action.server_label || '—')} · #{String(target.target_id || selection.target_id || p.target_id || action.target_id)}</Descriptions.Item>
        <Descriptions.Item label="当前地址">{String(target.host || direct.source_host || '—')}{target.port ? `:${String(target.port)}` : ''}</Descriptions.Item>
        {(target.user_count != null || direct.user_count != null) && <Descriptions.Item label="涉及用户">{String(target.user_count ?? direct.user_count)} 人</Descriptions.Item>}
        {action.kind === 'entry_auto_split' && <Descriptions.Item label="自动二分">{target.auto_split_enabled ? '开启' : '关闭'} → {p.enabled ? '开启' : '关闭'}</Descriptions.Item>}
        {action.kind === 'entry_manual_split' && <Descriptions.Item label="执行方式">本次触发二分，不依赖也不自动开启永久自动二分开关；任务受理后需查询执行结果。</Descriptions.Item>}
        {action.kind === 'entry_split' && <>
          <Descriptions.Item label="分组对象">{selection.kind === 'split_group' ? '现有二分子组' : '标准入口规则'}；保留 UA 条件并建立两个子组。</Descriptions.Item>
          <Descriptions.Item label="新入口">{selection.use_backup_ip_pool ? '从 IP 池分配两个入口' : `${String(selection.entry_host_a || '—')} / ${String(selection.entry_host_b || '—')}`}</Descriptions.Item>
        </>}
        {result.operation_id != null && <Descriptions.Item label="任务">#{String(result.operation_id)}{result.operation_status ? ` · ${String(result.operation_status)}` : ''}</Descriptions.Item>}
        {result.split_completed === false && <Descriptions.Item label="二分结果">任务已受理，二分尚未完成。</Descriptions.Item>}
        {result.split_completed === true && <Descriptions.Item label="二分结果">已完成</Descriptions.Item>}
      </Descriptions>
      {Array.isArray(p.effects) && p.effects.length > 0 && <Typography.Paragraph type="secondary">{p.effects.map(String).join('；')}</Typography.Paragraph>}
      {typeof p.effects === 'string' && <Typography.Paragraph type="secondary">{p.effects}</Typography.Paragraph>}
      {Array.isArray(result.groups) && result.groups.length > 0 && <Descriptions size="small" column={1}>{result.groups.filter((value) => value && typeof value === 'object' && !Array.isArray(value)).map((value, index) => { const group = value as Record<string, unknown>; return <Descriptions.Item key={`${String(group.group_id)}:${index}`} label={String(group.name || `子组 ${index + 1}`)}>#{String(group.group_id)} · {String(group.entry_host || '—')}</Descriptions.Item>; })}</Descriptions>}
    </>;
  }
  if (action.kind === 'server_register') return <Descriptions size="small" column={1}>
    <Descriptions.Item label="服务器名称">{String(p.name || '—')}</Descriptions.Item>
    <Descriptions.Item label="连接身份"><Typography.Text code>{String(p.username || '')}@{String(p.host || '')}:{String(p.port || 22)}</Typography.Text></Descriptions.Item>
    <Descriptions.Item label="凭据">{[p.has_password && `密码${action.status === 'succeeded' ? '已加密保存' : '已安全暂存'}`, p.has_private_key && `私钥${action.status === 'succeeded' ? '已加密保存' : '已安全暂存'}`].filter(Boolean).join('；') || '未提供'}</Descriptions.Item>
    <Descriptions.Item label="主机指纹"><Typography.Text code>{String(p.host_key_sha256 || '—')}</Typography.Text></Descriptions.Item>
    <Descriptions.Item label="指纹来源">{p.trust_source === 'first_use' ? '首次连接记录的指纹，未经独立验证；后续连接必须匹配。' : '使用提供的固定指纹；后续连接必须匹配。'}</Descriptions.Item>
  </Descriptions>;
  if (action.kind === 'ssh_command') return <Descriptions size="small" column={1}>
    <Descriptions.Item label="最长执行时间">{Number(p.timeout_seconds || 120)} 秒</Descriptions.Item>
    <Descriptions.Item label="执行方式">非交互 SSH；运行中可展开操作详情查看实时输出。</Descriptions.Item>
  </Descriptions>;
  if (action.kind === 'node_update' || action.kind === 'entry_rule_update' || action.kind === 'node_change') {
    const before = (p.before || {}) as Record<string, unknown>;
    const after = (p.after || p.effective_changes || p.changes || {}) as Record<string, unknown>;
    return <>
      {Array.isArray(p.shared_fields) && p.shared_fields.length > 0 && <Alert type="warning" showIcon message={`以下字段影响父规则 #${String(p.policy_id)} 的全部二分组：${p.shared_fields.map((field) => fieldNames[String(field)] || String(field)).join('、')}`} />}
      {action.kind === 'entry_rule_update' && Array.isArray(after.conditions) && after.conditions.length === 0 && <Alert type="warning" showIcon message="清空全部匹配条件后，此规则将匹配所有用户。" />}
      <Table size="small" rowKey="key" pagination={false} scroll={{ x: 460 }} dataSource={Object.entries(after).filter(([key, value]) => JSON.stringify(before[key]) !== JSON.stringify(value)).map(([key, value]) => ({ key, name: fieldNames[key] || key, before: valueText(key, before[key]), after: valueText(key, value) }))} columns={[
      { title: '字段', dataIndex: 'name', width: 120 },
      { title: '修改前', dataIndex: 'before', render: (value) => <span style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{value}</span> },
      { title: '修改后', dataIndex: 'after', render: (value) => <span style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{value}</span> },
      ]} />
    </>;
  }
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
