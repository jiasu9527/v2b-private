import React, { useEffect, useMemo, useState } from 'react';
import { Button, Input, InputNumber, Select, Space, Spin, Switch, Table, message } from 'antd';
import { apiGet, apiPost, money } from '../lib/api';

const statusMeta: Record<number, { text: string; className: string }> = {
  0: { text: '进行中', className: 'status-ongoing' },
  1: { text: '已达标', className: 'status-completed' },
  2: { text: '已过期', className: 'status-expired' },
  3: { text: '已放弃', className: 'status-abandoned' },
  4: { text: '已使用', className: 'status-used' },
};
const periodLabels: Record<string, string> = { month_price: '月付', quarter_price: '季付', half_year_price: '半年付', year_price: '年付', two_year_price: '两年付', three_year_price: '三年付', onetime_price: '一次性', reset_price: '重置流量', deposit: '余额充值' };

function statusOf(value: any) { return statusMeta[Number(value)] || { text: '未知', className: 'status-unknown' }; }
function date(value: any) {
  if (!value) return '--';
  const d = new Date(Number(value) * 1000);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}
function countdown(value: any) {
  if (!value) return '--';
  const rest = Math.max(0, Number(value) - Math.floor(Date.now() / 1000));
  const d = Math.floor(rest / 86400);
  const h = Math.floor((rest % 86400) / 3600);
  const m = Math.floor((rest % 3600) / 60);
  const s = rest % 60;
  return [d, h, m, s].map((n) => String(n).padStart(2, '0')).join(':');
}
function yuanToCents(value: any) { return Math.round(Number(value || 0) * 100); }

export default function InviteCampaignPage() {
  const [rows, setRows] = useState<any[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState({ current: 1, pageSize: 20 });
  const [keywordType, setKeywordType] = useState('email');
  const [keyword, setKeyword] = useState('');
  const [status, setStatus] = useState<any>('');
  const [detail, setDetail] = useState<any>(null);
  const [records, setRecords] = useState<any[]>([]);
  const [recordsTotal, setRecordsTotal] = useState(0);
  const [recordsPage, setRecordsPage] = useState(1);
  const [loading, setLoading] = useState(false);
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [savingSettings, setSavingSettings] = useState(false);
  const [savingEnabled, setSavingEnabled] = useState(false);
  const [plans, setPlans] = useState<any[]>([]);
  const [settings, setSettings] = useState<any>({ rewardAmountYuan: '', expireHours: '', inviteeTryOutPlanId: '0', inviteeTryOutTransferGb: '0', inviteeTryOutHours: '0' });

  const loadConfig = async () => {
    try {
      const [cfgRes, planRes] = await Promise.all([
        apiGet('/config/fetch', { key: 'invite' }),
        apiGet('/plan/fetch').catch(() => ({ data: [] })),
      ]);
      const invite = cfgRes.data?.invite || {};
      setEnabled(Number(invite.invite_campaign_enable ?? 1) === 1);
      setSettings({
        rewardAmountYuan: (Number(invite.invite_campaign_reward_amount ?? 1000) / 100).toString(),
        expireHours: String(invite.invite_campaign_expire_hours ?? 48),
        inviteeTryOutPlanId: String(invite.invite_campaign_try_out_plan_id ?? 0),
        inviteeTryOutTransferGb: String(invite.invite_campaign_try_out_transfer_gb ?? 0),
        inviteeTryOutHours: String(invite.invite_campaign_try_out_hours ?? 0),
      });
      setPlans(planRes.data || []);
    } catch (e: any) {
      message.error(e.message || '加载活动配置失败');
    }
  };

  const buildFilters = () => {
    const filters: any[] = [];
    if (keyword.trim()) filters.push({ key: keywordType, condition: keywordType === 'email' ? '=' : '模糊', value: keyword.trim() });
    if (status !== '') filters.push({ key: 'status', condition: '=', value: status });
    return filters;
  };

  const loadList = async (override: any = {}) => {
    setLoading(true);
    try {
      const next = { ...page, ...override };
      const params: any = { current: next.current, pageSize: next.pageSize };
      const filters = buildFilters();
      if (filters.length) params.filter = filters;
      const res = await apiGet('/invite/campaign/fetch', params);
      setRows(res.data || []);
      setTotal(res.total || 0);
      setPage(next);
    } catch (e: any) {
      message.error(e.message || '加载任务列表失败');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { loadConfig(); loadList(); }, []);

  const saveEnabled = async (value: boolean) => {
    if (savingEnabled || enabled === null) return;
    const old = enabled;
    setEnabled(value);
    setSavingEnabled(true);
    try {
      await apiPost('/config/save', { invite_campaign_enable: value ? 1 : 0 });
      message.success(value ? '活动任务已开启' : '活动任务已关闭');
    } catch (e: any) {
      setEnabled(old);
      message.error(e.message || '保存活动开关失败');
    } finally {
      setSavingEnabled(false);
    }
  };

  const saveSettings = async () => {
    const amount = Number(settings.rewardAmountYuan || 0);
    const hours = Number(settings.expireHours || 0);
    const transfer = Number(settings.inviteeTryOutTransferGb || 0);
    const tryHours = Number(settings.inviteeTryOutHours || 0);
    if (amount < 0) return message.error('每邀请减免金额不能小于 0');
    if (hours <= 0) return message.error('任务有效期必须大于 0 小时');
    if (transfer < 0 || tryHours < 0) return message.error('活动试用流量和时长不能小于 0');
    setSavingSettings(true);
    try {
      await apiPost('/config/save', {
        invite_campaign_reward_amount: yuanToCents(amount),
        invite_campaign_expire_hours: Math.round(hours),
        invite_campaign_try_out_plan_id: Number(settings.inviteeTryOutPlanId || 0),
        invite_campaign_try_out_transfer_gb: transfer,
        invite_campaign_try_out_hours: tryHours,
      });
      message.success('活动参数已保存');
    } catch (e: any) {
      message.error(e.message || '保存活动参数失败');
    } finally {
      setSavingSettings(false);
    }
  };

  const loadRecords = async (campaign: any, current = 1) => {
    const res = await apiGet('/invite/campaign/records', { campaign_id: campaign.id, current, page_size: 10 });
    setRecords(res.data || []);
    setRecordsTotal(res.total || 0);
    setRecordsPage(current);
  };

  const openDetail = async (row: any) => {
    try {
      const res = await apiPost('/invite/campaign/detail', { id: row.id }, { form: true });
      const data = res.data || row;
      setDetail(data);
      await loadRecords(data, 1);
    } catch (e: any) {
      message.error(e.message || '加载详情失败');
    }
  };

  const activeCount = rows.filter((item) => Number(item.status) === 0).length;
  const completedCount = rows.filter((item) => Number(item.status) === 1).length;
  const usedExpiredCount = rows.filter((item) => [2, 4].includes(Number(item.status))).length;
  const recordTotalPages = Math.max(1, Math.ceil(recordsTotal / 10));

  const columns: any[] = [
    { title: 'ID', dataIndex: 'id', width: 80, render: (id: any) => `#${id}` },
    { title: '邀请人', dataIndex: 'user_email', width: 190, render: (value: any) => value || '--' },
    { title: '目标套餐', dataIndex: 'plan_name', width: 170, render: (value: any, row: any) => <>{value || '--'}<div className="campaign-subvalue">{periodLabels[row.period] || row.period || '--'}</div></> },
    { title: '邀请码', dataIndex: 'invite_code', width: 130, render: (value: any) => value || '--' },
    { title: '状态', dataIndex: 'status', width: 110, render: (value: any) => { const meta = statusOf(value); return <span className={`status-badge ${meta.className}`}>{meta.text}</span>; } },
    { title: '进度', dataIndex: 'current_amount', width: 230, render: (_: any, row: any) => { const progress = Number(row.target_amount) > 0 ? Math.min(100, Math.round(Number(row.current_amount) / Number(row.target_amount) * 100)) : 0; return <div style={{ minWidth: 180 }}><div className="campaign-progress-bar"><div className="campaign-progress-fill" style={{ width: `${progress}%` }} /></div><div className="campaign-progress-text">{money(row.current_amount)} / {money(row.target_amount)}</div></div>; } },
    { title: '倒计时', dataIndex: 'expired_at', width: 120, render: (value: any, row: any) => Number(row.status) === 0 ? countdown(value) : '--' },
    { title: '抵扣订单', dataIndex: 'used_order_trade_no', width: 180, render: (value: any) => value || '--' },
    { title: '创建时间', dataIndex: 'created_at', width: 180, render: date },
    { title: '操作', dataIndex: 'action', align: 'right', fixed: 'right', width: 100, render: (_: any, row: any) => <Button size="small" onClick={() => openDetail(row)}>详情</Button> },
  ];

  const recordColumns: any[] = [
    { title: '注册时间', dataIndex: 'created_at', render: date },
    { title: '被邀请用户', dataIndex: 'invitee_email', render: (value: any, row: any) => value || `#${row.invitee_user_id}` },
    { title: '邀请码', dataIndex: 'invite_code', render: (value: any) => value || '--' },
    { title: '奖励', dataIndex: 'reward_amount', render: money },
  ];

  const configStatus = useMemo(() => enabled === null ? { text: '读取中', cls: 'status-unknown' } : enabled ? { text: savingEnabled ? '保存中...' : '已开启', cls: 'status-ongoing' } : { text: savingEnabled ? '保存中...' : '已关闭', cls: 'status-abandoned' }, [enabled, savingEnabled]);

  return <div className="legacy-page invite-campaign-page">
    <div className="content-heading">活动任务</div>
    <div className="campaign-shell campaign-shell--admin">
      <Spin spinning={loading}>
        <div className="block block-rounded campaign-admin-panel">
          <div className="block-header block-header-default"><h3 className="block-title">活动任务</h3><div className="block-options"><Button size="small" onClick={() => loadList()}>刷新列表</Button></div></div>
          <div className="block-content"><p className="campaign-admin-intro">查看邀请减免活动任务、绑定邀请码、完成进度和触发的实际抵扣订单。</p>
            <div className="campaign-admin-config">
              <div className="campaign-admin-config-top"><div className="campaign-admin-config-copy"><div className="campaign-admin-config-title">活动开关与参数</div><div className="campaign-admin-config-desc">关闭后禁止用户新建活动任务，普通邀请返佣不受影响，已有任务仍可查看和使用。被邀请用户使用活动专属体验套餐，不依赖全站注册试用；流量和体验时长可在下方单独配置。</div></div><div className="campaign-admin-config-actions"><span className={`status-badge ${configStatus.cls} campaign-admin-config-badge`}>{configStatus.text}</span><Switch checked={!!enabled} disabled={enabled === null || savingEnabled} onChange={saveEnabled} /></div></div>
              <div className="campaign-admin-config-grid">
                <div className="campaign-field"><label>每邀请 1 人减免金额（元）</label><InputNumber min={0} step={0.01} value={settings.rewardAmountYuan} onChange={(v) => setSettings((old: any) => ({ ...old, rewardAmountYuan: String(v ?? '') }))} /></div>
                <div className="campaign-field"><label>任务有效期（小时）</label><InputNumber min={1} step={1} value={settings.expireHours} onChange={(v) => setSettings((old: any) => ({ ...old, expireHours: String(v ?? '') }))} /></div>
                <div className="campaign-field"><label>活动体验套餐</label><Select value={settings.inviteeTryOutPlanId} onChange={(v) => setSettings((old: any) => ({ ...old, inviteeTryOutPlanId: String(v) }))} options={[{ label: '请选择活动体验套餐', value: '0' }, ...plans.map((plan) => ({ label: plan.name || `套餐#${plan.id}`, value: String(plan.id) }))]} /></div>
                <div className="campaign-field"><label>被邀请用户体验流量（GB）</label><InputNumber min={0} step={1} value={settings.inviteeTryOutTransferGb} onChange={(v) => setSettings((old: any) => ({ ...old, inviteeTryOutTransferGb: String(v ?? '') }))} /></div>
                <div className="campaign-field"><label>被邀请用户体验时长（小时）</label><InputNumber min={0} step={1} value={settings.inviteeTryOutHours} onChange={(v) => setSettings((old: any) => ({ ...old, inviteeTryOutHours: String(v ?? '') }))} /></div>
              </div>
              <div className="campaign-admin-config-footer"><div className="campaign-admin-config-hint">当前预览：每邀请 1 人减免 {Number(settings.rewardAmountYuan || 0).toFixed(2)} 元，任务有效期 {settings.expireHours || '--'} 小时，活动体验套餐 {settings.inviteeTryOutPlanId || '0'}，被邀请用户额外获得 {settings.inviteeTryOutTransferGb || '0'} GB / {settings.inviteeTryOutHours || '0'} 小时。</div><Button type="primary" size="small" loading={savingSettings} disabled={enabled === null} onClick={saveSettings}>{savingSettings ? '保存中...' : '保存参数'}</Button></div>
            </div>
          </div>
        </div>
        <div className="row gutters-tiny campaign-admin-stats">
          {[['总任务数', total], ['当前页进行中', activeCount], ['当前页已达标', completedCount], ['当前页已使用/过期', usedExpiredCount]].map(([label, value]) => <div className="col-6 col-xl-3" key={label as string}><div className="block block-rounded block-link-shadow text-center h-100 mb-0 campaign-admin-stat-card"><div className="block-content block-content-full"><div className="campaign-admin-stat-label">{label}</div><div className="campaign-admin-stat-value">{value}</div></div></div></div>)}
        </div>
        <div className="block block-rounded campaign-admin-panel">
          <div className="block-header block-header-default"><h3 className="block-title">任务列表</h3></div>
          <div className="block-content"><div className="campaign-toolbar campaign-toolbar--admin">
            <div className="campaign-field"><label>搜索字段</label><Select value={keywordType} onChange={setKeywordType} options={[{ label: '邀请人邮箱', value: 'email' }, { label: '邀请码', value: 'invite_code' }]} /></div>
            <div className="campaign-field"><label>关键词</label><Input value={keyword} onChange={(e) => setKeyword(e.target.value)} onPressEnter={() => loadList({ current: 1 })} placeholder="邮箱或邀请码" /></div>
            <div className="campaign-field"><label>状态</label><Select value={status} onChange={setStatus} options={[{ label: '全部状态', value: '' }, { label: '进行中', value: 0 }, { label: '已达标', value: 1 }, { label: '已过期', value: 2 }, { label: '已放弃', value: 3 }, { label: '已使用', value: 4 }]} /></div>
            <Button type="primary" onClick={() => loadList({ current: 1 })}>搜索</Button>
          </div></div>
          <div className="block-content block-content-full pt-0"><div className="table-responsive"><Table className="campaign-table--admin" rowKey="id" dataSource={rows} columns={columns} pagination={{ total, current: page.current, pageSize: page.pageSize, size: 'small' }} scroll={{ x: 1300 }} onChange={(pagination: any) => loadList({ current: pagination.current, pageSize: pagination.pageSize })} /></div></div>
        </div>
        {detail && <div id="admin-campaign-detail-wrap" className="campaign-admin-detail-wrap">
          <div className="row"><div className="col-xl-7"><div className="block block-rounded campaign-admin-panel"><div className="block-header block-header-default"><h3 className="block-title">任务详情 #{detail.id}</h3><div className="block-options"><span className={`status-badge ${statusOf(detail.status).className}`}>{statusOf(detail.status).text}</span></div></div><div className="block-content"><p className="campaign-admin-subtitle">绑定邀请码：{detail.invite_code || '--'}</p><div className="campaign-progress"><div className="campaign-progress-bar"><div className="campaign-progress-fill" style={{ width: `${Number(detail.target_amount) > 0 ? Math.min(100, Math.round(Number(detail.current_amount) / Number(detail.target_amount) * 100)) : 0}%` }} /></div><div className="campaign-progress-text">{money(detail.current_amount)} / {money(detail.target_amount)}</div></div><div className="campaign-admin-kv-list">
            {[
              ['邀请人邮箱', detail.user?.email || detail.user_email || '--'], ['目标套餐', `${detail.plan?.name || '--'} / ${periodLabels[detail.period] || detail.period || '--'}`], ['单次奖励', money(detail.reward_amount)], ['邀请人数', detail.invite_count], ['任务时效', `${date(detail.started_at)} 至 ${date(detail.expired_at)}`], ['绑定邀请码 ID', detail.invite_code_id || '--'], ['使用订单', detail.used_order?.trade_no || '--'], ['订单抵扣', detail.used_order ? money(detail.used_order.invite_campaign_discount_amount || 0) : '--'],
            ].map(([k, v]) => <div className="campaign-kv" key={k}><div className="campaign-kv-key">{k}</div><div className="campaign-kv-value">{v}</div></div>)}
          </div></div></div></div><div className="col-xl-5"><div className="block block-rounded campaign-admin-panel"><div className="block-header block-header-default"><h3 className="block-title">注册记录</h3></div><div className="block-content block-content-full"><Table rowKey="id" dataSource={records} columns={recordColumns} pagination={false} size="small" /><div className="campaign-pagination"><Button size="small" disabled={recordsPage <= 1} onClick={() => loadRecords(detail, recordsPage - 1)}>上一页</Button><span>第 {recordsPage} / {recordTotalPages} 页</span><Button size="small" disabled={recordsPage >= recordTotalPages} onClick={() => loadRecords(detail, recordsPage + 1)}>下一页</Button></div></div></div></div></div>
        </div>}
      </Spin>
    </div>
  </div>;
}
