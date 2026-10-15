import React, { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Checkbox, Collapse, DatePicker, Descriptions, Empty, Form, Input, InputNumber, Modal, Select, Space, Tag, Typography, message } from 'antd';
import { SearchOutlined, UndoOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { bytes, supportGet, supportPost } from '../lib/api';
import { type SupportEntitlementPlan, type SupportEntitlementSnapshot, type SupportEntryMembership, type SupportUserResult, supportDate } from './supportTypes';

const GB = 1024 ** 3;
function uuid() {
  if (typeof crypto.randomUUID === 'function') return crypto.randomUUID();
  const value = crypto.getRandomValues(new Uint8Array(16));
  value[6] = (value[6] & 15) | 64; value[8] = (value[8] & 63) | 128;
  const hex = Array.from(value, (v) => v.toString(16).padStart(2, '0')).join('');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
function expiryText(value?: number | null) {
  return Number(value) > 0 ? supportDate(value) : '长期有效';
}
type SaveOptions = { path: string; body: Record<string, any>; title: string; success: string; details: React.ReactNode; okText: string };
type Save = (options: SaveOptions) => void;

function EntitlementEditor({ snapshot, plans, plansLoading, plansError, busy, save }: {
  snapshot: SupportEntitlementSnapshot; plans: SupportEntitlementPlan[]; plansLoading: boolean; plansError: string; busy: boolean; save: Save;
}) {
  const [changePlan, setChangePlan] = useState(false);
  const [plan, setPlan] = useState<number | undefined>(snapshot.plan_id || undefined);
  const [changeQuota, setChangeQuota] = useState(false);
  const [quotaGB, setQuotaGB] = useState<number | null>(Number((snapshot.transfer_enable / GB).toFixed(6)));
  const [quotaEdited, setQuotaEdited] = useState(false);
  const [changeExpiry, setChangeExpiry] = useState(false);
  const [permanent, setPermanent] = useState(!Number(snapshot.expired_at));
  const [expiry, setExpiry] = useState(snapshot.expired_at ? dayjs(snapshot.expired_at * 1000) : null);
  const [resetUsage, setResetUsage] = useState(false);
  const [reason, setReason] = useState('');
  const selectedPlan = plans.find((item) => item.id === plan);
  const changesPlan = changePlan && plan !== snapshot.plan_id;
  const options = plans.map((item) => ({ value: item.id, label: `${item.name} · ${bytes(item.transfer_enable)}` }));
  if (snapshot.plan_id && !plans.some((item) => item.id === snapshot.plan_id)) {
    options.push({ value: snapshot.plan_id, label: `${snapshot.plan_name || `套餐 #${snapshot.plan_id}`}（当前套餐）` });
  }
  const submit = () => {
    const body: Record<string, any> = { user_id: snapshot.user_id, expected: snapshot.version, reset_usage: resetUsage, reason: reason.trim() };
    const details: React.ReactNode[] = [];
    if (changesPlan) {
      if (!selectedPlan) return message.warning('请选择当前可用的套餐。');
      body.plan_id = selectedPlan.id;
      details.push(<li key="plan">套餐：{snapshot.plan_name || '无套餐'} → {selectedPlan.name}（同步套餐用户组、设备数和限速）</li>);
    }
    if (changeQuota) {
      const quota = Number(quotaGB) * GB;
      if (quotaGB === null || !Number.isFinite(quota) || quota < 0 || !Number.isSafeInteger(Math.round(quota))) return message.warning('请输入有效的总流量配额。');
      body.transfer_enable = quotaEdited ? Math.round(quota) : snapshot.transfer_enable;
      if (body.transfer_enable !== snapshot.transfer_enable || changesPlan) details.push(<li key="quota">总流量配额：{bytes(snapshot.transfer_enable)} → {bytes(body.transfer_enable)}</li>);
      else delete body.transfer_enable;
    } else if (changesPlan && selectedPlan) {
      details.push(<li key="quota">总流量配额：{bytes(snapshot.transfer_enable)} → {bytes(selectedPlan.transfer_enable)}（新套餐默认配额）</li>);
    }
    if (changeExpiry) {
      if (!permanent && (!expiry || !expiry.isValid())) return message.warning('请选择有效的到期时间，或勾选长期有效。');
      const nextExpiry = permanent ? null : expiry!.unix();
      if (nextExpiry !== null && (!Number.isSafeInteger(nextExpiry) || nextExpiry <= 0 || nextExpiry > 253402300799)) return message.warning('请选择 1970 年之后的有效到期时间。');
      if ((nextExpiry || 0) !== (snapshot.expired_at || 0)) {
        body.expired_at = nextExpiry;
        details.push(<li key="expiry">有效期：{expiryText(snapshot.expired_at)} → {expiryText(nextExpiry)}</li>);
      }
    }
    if (resetUsage) details.push(<li key="usage">重置已用流量：{bytes(Number(snapshot.u) + Number(snapshot.d))} → 0 B</li>);
    if (!details.length) return message.warning('尚未选择实际需要修改的内容。');
    if (!reason.trim()) return message.warning('请填写操作原因。');
    save({ path: '/entitlement', body, title: '确认补单 / 调整用户权益？', okText: '确认保存', success: '用户权益已保存', details: <><ul className="support-change-list">{details}</ul><Typography.Paragraph>仅人工调整用户权益，不创建支付订单或财务流水，不会自动延长其他项目；请先核对付款或售后依据。</Typography.Paragraph></> });
  };
  return <Card className="support-scope" title="套餐、流量与有效期">
    <Descriptions size="small" column={{ xs: 1, sm: 2 }} items={[
      { key: 'plan', label: '当前套餐', children: snapshot.plan_name || '无套餐' },
      { key: 'expiry', label: '当前有效期', children: expiryText(snapshot.expired_at) },
      { key: 'quota', label: '总流量配额', children: bytes(snapshot.transfer_enable) },
      { key: 'usage', label: '已用流量', children: bytes(Number(snapshot.u) + Number(snapshot.d)) },
    ]} />
    <Typography.Paragraph type="secondary" style={{ marginTop: 12 }}>只修改勾选的项目；仅修改套餐时采用新套餐默认配额，已用流量和有效期保持不变。需要补发流量时请明确设置配额或重置已用。</Typography.Paragraph>
    {plansError && <Alert type="warning" showIcon message={`套餐选项加载失败：${plansError}。可重新精准查询后再试，其他权益仍可调整。`} className="support-alert" />}
    <Form layout="vertical" className="support-entitlement-form">
      <Form.Item label={<Checkbox checked={changePlan} disabled={busy || plansLoading || !!plansError} onChange={(event) => setChangePlan(event.target.checked)}>修改套餐</Checkbox>}>
        <Select aria-label="目标套餐" value={plan} disabled={busy || !changePlan || plansLoading || !!plansError} loading={plansLoading} placeholder="选择套餐" showSearch optionFilterProp="label" onChange={setPlan} options={options} />
      </Form.Item>
      <Form.Item label={<Checkbox checked={changeQuota} disabled={busy} onChange={(event) => setChangeQuota(event.target.checked)}>设置总流量配额</Checkbox>}>
        <InputNumber<number> aria-label="总流量配额GB" value={quotaGB} min={0} max={Number.MAX_SAFE_INTEGER / GB} precision={6} addonAfter="GB" disabled={busy || !changeQuota} onChange={(value) => { setQuotaGB(value); setQuotaEdited(true); }} />
      </Form.Item>
      <Form.Item className="support-full-width" label={<Checkbox checked={changeExpiry} disabled={busy} onChange={(event) => setChangeExpiry(event.target.checked)}>修改有效期</Checkbox>}>
        <Space wrap className="support-expiry-controls"><Checkbox checked={permanent} disabled={busy || !changeExpiry} onChange={(event) => setPermanent(event.target.checked)}>长期有效</Checkbox><DatePicker aria-label="用户到期时间" showTime value={expiry} disabled={busy || !changeExpiry || permanent} onChange={setExpiry} placeholder="请选择到期时间" /></Space>
      </Form.Item>
      <Form.Item className="support-full-width"><Checkbox checked={resetUsage} disabled={busy} onChange={(event) => setResetUsage(event.target.checked)}>将已用上行、下行流量重置为零</Checkbox></Form.Item>
      <Form.Item className="support-full-width" label="操作原因" required><Input.TextArea aria-label="权益调整原因" placeholder="填写补单依据或调整原因（必填）" maxLength={500} showCount autoSize={{ minRows: 2, maxRows: 4 }} disabled={busy} value={reason} onChange={(event) => setReason(event.target.value)} /></Form.Item>
      <Space wrap className="support-full-width"><Button type="primary" disabled={busy || !reason.trim() || (!changePlan && !changeQuota && !changeExpiry && !resetUsage)} onClick={submit}>确认调整权益</Button><Typography.Text type="secondary">保存前会再次核对变更，不会修改用户登录信息或管理员权限。</Typography.Text></Space>
    </Form>
  </Card>;
}

function MembershipEditor({ membership, scopes, busy, save, userID, ua }: {
  membership: SupportEntryMembership; scopes: SupportUserResult['scopes']; busy: boolean; save: Save; userID: number; ua: string;
}) {
  const [target, setTarget] = useState('');
  const [reason, setReason] = useState('');
  const groups = membership.current_groups || [];
  const targets = membership.targets || [];
  const selected = targets.find((item) => item.key === target);
  const change = (restore: boolean) => {
    if ((!restore && !selected) || !reason.trim()) return;
    save({
      path: '/entry', body: { user_id: userID, target_key: restore ? '' : target, restore, version: membership.version, reason: reason.trim(), ua },
      title: restore ? '退出指定分组，恢复默认逻辑？' : '确认加入该入口组？', okText: restore ? '退出并恢复默认' : '确认加入',
      success: restore ? '已退出指定分组，恢复默认规则，请让用户更新订阅' : '已更新用户的邮箱指定分组，请让用户更新订阅',
      details: <><Typography.Paragraph>{restore ? '将该用户从全部可操作的邮箱指定入口组中移出，之后按原有规则匹配。' : `目标：${selected?.name} · ${selected?.entry_host}。会将该用户从其他可操作的邮箱指定入口组中移出，再加入此组。`}</Typography.Paragraph>{membership.has_legacy_override && <Typography.Paragraph>同时清除该用户旧版的人工入口指定。</Typography.Paragraph>}<Typography.Paragraph>不修改其他规则条件、套餐、流量或有效期。若更早的规则已命中，或实际客户端 UA 不同，目标组可能不生效；保存后请检查显示的命中状态，并让用户更新订阅。</Typography.Paragraph></>,
    });
  };
  return <Card className="support-scope" title="入口分组">
    <Alert type="info" showIcon className="support-alert" message="直接维护指定用户的入口组邮箱名单，不再要求节点集合相同。" description="这里只提供支持按邮箱指定、已启用且当前用户满足其他条件的入口组；其他分组不能加入。加入后仍遵循入口规则的匹配顺序和用户原有节点权限。" />
    <div className="support-membership-current"><Typography.Text strong>当前加入的邮箱指定组</Typography.Text>{groups.length ? groups.map((group) => <div className="support-membership-row" key={group.policy_id}><Space wrap><span>{group.name || `规则 #${group.policy_id}`}</span><Typography.Text code>{group.entry_host || '入口未配置'}</Typography.Text><Tag color={group.effective ? 'green' : group.enabled ? 'orange' : undefined}>{group.effective ? '当前预览已命中' : group.enabled ? '已加入，当前预览未命中' : '已停用'}</Tag></Space></div>) : <div className="support-muted">未加入可操作的邮箱指定组，按现有规则分配。</div>}</div>
    {groups.some((group) => group.enabled && !group.effective) && <Alert type="warning" showIcon className="support-alert" message="存在已加入但当前未生效的组，请检查更早的规则、用户权益及实际客户端 UA，加入名单不等于已切换成功。" />}
    {membership.has_legacy_override && <Alert type="warning" showIcon className="support-alert" message="该用户存在旧版人工入口指定；本次加入或退出操作会将其清除。" />}
    {(scopes || []).length > 0 && <Collapse ghost size="small" className="support-alert" items={[{ key: 'effective', label: '查看当前预览命中的入口规则', children: scopes.map((scope) => {
      const legacyActive = scope.override?.active && scope.override.status === 'active' && scope.applicable !== false;
      const host = legacyActive ? scope.override?.entry_host : scope.automatic_entry_host;
      return <div className="support-membership-row" key={scope.source_policy_id}><Space wrap><span>{scope.name || `规则 #${scope.source_policy_id}`}</span><Typography.Text code>{host || '原节点入口'}</Typography.Text>{legacyActive && <Tag color="orange">旧版指定</Tag>}{scope.nodes?.map((node) => <Tag key={`${node.type}:${node.id}`}>{node.name || `${node.type} #${node.id}`}</Tag>)}</Space></div>;
    }) }]} />}
    <Form layout="vertical">
      <Form.Item label="加入入口组"><Select aria-label="加入入口组" value={target || undefined} placeholder={targets.length ? '选择按邮箱指定用户的入口组' : '暂无可加入的邮箱指定组'} disabled={busy || !targets.length} showSearch optionFilterProp="label" onChange={setTarget} options={targets.map((item) => ({ value: item.key, label: `${item.name} · ${item.entry_host}` }))} /></Form.Item>
      <Form.Item label="操作原因" required><Input.TextArea aria-label="入口分组调整原因" placeholder="填写用户反馈或调整原因（必填）" maxLength={500} showCount autoSize={{ minRows: 2, maxRows: 4 }} disabled={busy} value={reason} onChange={(event) => setReason(event.target.value)} /></Form.Item>
      <Space wrap><Button type="primary" disabled={busy || !selected || !reason.trim()} onClick={() => change(false)}>加入所选入口组</Button><Button icon={<UndoOutlined aria-hidden />} disabled={busy || (!groups.length && !membership.has_legacy_override) || !reason.trim()} onClick={() => change(true)}>退出指定分组，恢复默认</Button></Space>
    </Form>
  </Card>;
}

export default function SupportUserSearch() {
  const [query, setQuery] = useState('');
  const [ua, setUA] = useState('');
  const [result, setResult] = useState<SupportUserResult | null>(null);
  const [resultGeneration, setResultGeneration] = useState(0);
  const [resolvedQuery, setResolvedQuery] = useState('');
  const [resolvedUA, setResolvedUA] = useState('');
  const [plans, setPlans] = useState<SupportEntitlementPlan[]>([]);
  const [plansLoading, setPlansLoading] = useState(false);
  const [plansError, setPlansError] = useState('');
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [error, setError] = useState('');
  const sequence = useRef(0);
  const savingRef = useRef(false);
  const modal = useRef<ReturnType<typeof Modal.confirm> | null>(null);
  useEffect(() => () => { sequence.current += 1; modal.current?.destroy(); }, []);
  const clearResult = () => { sequence.current += 1; setResult(null); setError(''); setPlans([]); setPlansError(''); };
  const search = async () => {
    if (savingRef.current || modal.current || loading) return;
    const value = query.trim();
    if (!/^[1-9]\d*$/.test(value) && !/^[^\s@]+@[^\s@]+$/.test(value)) return message.warning('请输入完整邮箱或完整的十进制用户 ID，不支持模糊搜索。');
    const ticket = ++sequence.current;
    setResult(null); setError(''); setLoading(true); setPlans([]); setPlansError('');
    try {
      const res = await supportGet('/user', { query: value, ua: ua.trim() });
      if (ticket !== sequence.current) return;
      const data = res.data || { found: false, scopes: [] };
      setResult(data); setResultGeneration((current) => current + 1); setResolvedQuery(value); setResolvedUA(ua.trim());
      if (data.found && data.entitlement) {
        setPlansLoading(true);
        try {
          const choices = await supportGet('/entitlement/plans');
          if (ticket === sequence.current) setPlans(Array.isArray(choices.data) ? choices.data : []);
        } catch (e: any) { if (ticket === sequence.current) setPlansError(e.message || '请求失败'); }
        finally { if (ticket === sequence.current) setPlansLoading(false); }
      }
    } catch (e: any) { if (ticket === sequence.current) { setResult(null); setError(e.message || '查询失败'); } }
    finally { if (ticket === sequence.current) setLoading(false); }
  };
  const save: Save = ({ path, body, title, success, details, okText }) => {
    if (!result?.user || savingRef.current || modal.current) return;
    const user = result.user;
    const ticket = sequence.current;
    // Keep this immutable request for retries from the same confirmation dialog.
    // A lost response must not issue another reset or membership change with a new ID.
    const request = { ...body, request_id: uuid() };
    setConfirming(true);
    modal.current = Modal.confirm({
      title, width: 600, content: <><Typography.Paragraph>用户：{user.email}（ID {user.id}）</Typography.Paragraph>{details}<Typography.Paragraph>原因：{body.reason}</Typography.Paragraph></>,
      okText, cancelText: '取消', maskClosable: false, keyboard: false,
      afterClose: () => { modal.current = null; setConfirming(false); },
      onOk: async () => {
        if (savingRef.current) throw new Error('正在处理另一笔操作');
        savingRef.current = true; setSaving(true); setError('');
        modal.current?.update({ cancelButtonProps: { disabled: true } });
        try {
          await supportPost(path, request);
        } catch (e: any) {
          if (ticket === sequence.current) { setResult(null); setError(`${e.message || '操作失败'}。旧状态已清除；可在当前确认框使用同一请求重试，或取消后重新精准查询核对。`); }
          message.error(e.message || '操作失败');
          savingRef.current = false; setSaving(false);
          modal.current?.update({ cancelButtonProps: { disabled: false } });
          throw e;
        }
        try {
          // A committed write must never be retried merely because its refresh failed.
          const fresh = await supportGet('/user', { query: resolvedQuery, ua: resolvedUA });
          if (!fresh.data || typeof fresh.data.found !== 'boolean') throw new Error('刷新响应无效');
          if (ticket === sequence.current) { setResult(fresh.data); setResultGeneration((current) => current + 1); message.success(success); }
        } catch (e: any) {
          if (ticket === sequence.current) { setResult(null); setError(`${success}，但刷新用户状态失败，请重新精准查询确认。不要重复提交本次调整。`); message.warning('已保存，但刷新失败，请重新查询'); }
        } finally { savingRef.current = false; setSaving(false); modal.current?.update({ cancelButtonProps: { disabled: false } }); }
      },
    });
  };
  const busy = saving || loading || confirming;
  return <div className="support-panel">
    <Alert type="info" showIcon className="support-alert" message="只支持完整邮箱或用户 ID 精准查询，不提供用户列表、模糊搜索和导出。" description="员工可以人工补单调整套餐、流量、有效期，以及加入或退出邮箱指定入口组。每笔实际变更需填写原因并确认，保留操作记录。两类操作分别保存；保存成功会刷新状态，并清空其他未保存的编辑。" />
    <Card><Form layout="vertical" onFinish={search}><Form.Item label="精准查询用户" required><Space.Compact style={{ width: '100%' }}><Input aria-label="完整邮箱或用户ID" placeholder="完整邮箱或完整用户 ID" maxLength={254} autoComplete="off" value={query} disabled={busy} onChange={(event) => { setQuery(event.target.value); clearResult(); }} /><Button type="primary" htmlType="submit" icon={<SearchOutlined aria-hidden />} loading={loading} disabled={saving || confirming || !query.trim()}>精准查询</Button></Space.Compact></Form.Item><Collapse size="small" ghost items={[{ key: 'ua', label: '高级：按客户端 UA 预览入口规则', children: <><Input aria-label="客户端UA" placeholder="可选，输入用户订阅客户端的完整 User-Agent" maxLength={1024} autoComplete="off" value={ua} disabled={busy} onChange={(event) => { setUA(event.target.value); clearResult(); }} /><Typography.Paragraph type="secondary" style={{ marginTop: 8, marginBottom: 0 }}>留空使用默认预览；实际客户端 UA 不同，可能命中其他入口规则。不修改用户客户端或规则条件。</Typography.Paragraph></> }]} /></Form></Card>
    {error && <Alert type="error" showIcon className="support-alert" style={{ marginTop: 16 }} message={error} />}
    {result && !result.found && <div className="support-empty" style={{ marginTop: 16 }}><Empty description="未找到可操作用户，请检查完整邮箱或用户 ID。" /></div>}
    {result?.found && result.user && <>
      <Card className="support-user-summary"><Descriptions title="用户信息" column={{ xs: 1, sm: 2, lg: 4 }} items={[
        { key: 'id', label: '用户 ID', children: result.user.id }, { key: 'email', label: '邮箱', children: result.user.email },
        { key: 'plan', label: '套餐', children: result.entitlement?.plan_name || result.user.plan_name || '无套餐' }, { key: 'expire', label: '有效期至', children: expiryText(result.entitlement ? result.entitlement.expired_at : result.user.expired_at) },
        { key: 'remain', label: '剩余流量', children: bytes(result.user.remaining_bytes) },
      ]} /><Typography.Text type="secondary">本次查询：{resolvedQuery}{resolvedUA ? ` · UA：${resolvedUA}` : ' · 默认 UA 预览'}</Typography.Text></Card>
      {result.entitlement ? <EntitlementEditor key={`entitlement:${result.user.id}:${resultGeneration}`} snapshot={result.entitlement} plans={plans} plansLoading={plansLoading} plansError={plansError} busy={busy} save={save} /> : <Alert type="warning" showIcon className="support-alert" message="未取得当前用户的权益版本，暂不能调整套餐、流量或有效期，请重新查询。" />}
      {result.entry_membership ? <MembershipEditor key={`membership:${result.user.id}:${resultGeneration}`} membership={result.entry_membership} scopes={result.scopes || []} busy={busy} save={save} userID={result.user.id} ua={resolvedUA} /> : <Alert type="warning" showIcon className="support-alert" message="未取得邮箱指定入口组状态，暂不能调整入口，请重新查询。" />}
    </>}
  </div>;
}
