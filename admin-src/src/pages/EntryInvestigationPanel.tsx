import { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Checkbox, Col, DatePicker, Descriptions, Form, Input, InputNumber, Modal, Row, Select, Space, Spin, Tag, Typography, message } from 'antd';
import { BranchesOutlined, ReloadOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { apiGet, apiJsonPost, unwrapData } from '../lib/api';
import { ENTRY_EVIDENCE_PATH, evidenceDuration, evidenceTime, type EvidenceCase } from './entryEvidence';
import {
  INVESTIGATION_CONTROL, INVESTIGATION_HEALTH, INVESTIGATION_NOT_FOUND, INVESTIGATION_STATES,
  investigationCanPrepareNext, investigationConfirmPayload, investigationStartPayload, parseEntryInvestigation,
  type EntryInvestigation, type InvestigationBranchName, type InvestigationControlStatus,
} from './entryInvestigation';
import './EntryInvestigationPanel.css';

const errorText = (error: unknown) => error instanceof Error ? error.message : '操作未完成，请先重新读取任务状态，不要重复提交';
const nowSeconds = () => Math.floor(Date.now() / 1000);

export default function EntryInvestigationPanel({ record, onOpenCase, disabled = false, onBusyChange }: {
  record: EvidenceCase; onOpenCase: (id: number) => void | Promise<void>;
  disabled?: boolean; onBusyChange?: (busy: boolean) => void;
}) {
  const [task, setTask] = useState<EntryInvestigation | null>(null);
  const [loaded, setLoaded] = useState(false), [loading, setLoading] = useState(false);
  const [error, setError] = useState(''), [busy, setBusy] = useState('');
  const [writeBlocked, setWriteBlocked] = useState(false);
  const [blockedAt, setBlockedAt] = useState<number | null>(record.query.to);
  const [note, setNote] = useState(''), [confirmed, setConfirmed] = useState(false);
  const [hours, setHours] = useState<number | null>(24), [maxRounds, setMaxRounds] = useState<number | null>(10), [stopUsers, setStopUsers] = useState<number | null>(8);
  const [failure, setFailure] = useState<{ task: EntryInvestigation; branch: InvestigationBranchName } | null>(null);
  const [failureAt, setFailureAt] = useState<number | null>(null), [failureNote, setFailureNote] = useState('');
  const [failureConfirmed, setFailureConfirmed] = useState(false);
  const [controlStatus, setControlStatus] = useState<InvestigationControlStatus>('unknown');
  const [nextTask, setNextTask] = useState<EntryInvestigation | null>(null);
  const alive = useRef(false), sequence = useRef(0), busyRef = useRef(false);
  const readRequest = useRef<AbortController | null>(null);
  const currentCase = useRef(record.id); currentCase.current = record.id;
  const busyCallback = useRef(onBusyChange); busyCallback.current = onBusyChange;

  const load = useCallback(async (quiet = false) => {
    if (busyRef.current) return;
    const token = ++sequence.current;
    readRequest.current?.abort(); const controller = new AbortController(); readRequest.current = controller;
    if (!quiet) setLoading(true);
    try {
      const result = unwrapData(await apiGet(`${ENTRY_EVIDENCE_PATH}/investigation`, { case_id: record.id }, { cache: 'no-store', signal: controller.signal }));
      if (!alive.current || token !== sequence.current || currentCase.current !== record.id) return;
      setTask(parseEntryInvestigation(result, record.id)); setLoaded(true); setError(''); setWriteBlocked(false);
    } catch (error) {
      if (!alive.current || token !== sequence.current || controller.signal.aborted || currentCase.current !== record.id) return;
      // Do not treat generic HTTP 404/HTML, auth errors or an old backend as an
      // absent task. Only the established evidence API's exact message does so.
      if (errorText(error) === INVESTIGATION_NOT_FOUND) { setTask(null); setLoaded(true); setError(''); setWriteBlocked(false); }
      else { setError(errorText(error)); setWriteBlocked(true); }
    } finally { if (alive.current && token === sequence.current) setLoading(false); }
  }, [record.id]);

  useEffect(() => {
    alive.current = true; setTask(null); setLoaded(false); setError(''); setWriteBlocked(false); setFailure(null); setNextTask(null);
    setBlockedAt(record.query.to); setNote(''); setConfirmed(false); setHours(24); setMaxRounds(10); setStopUsers(8);
    void load();
    const timer = window.setInterval(() => { if (!document.hidden) void load(true); }, 60000);
    return () => { alive.current = false; sequence.current++; readRequest.current?.abort(); window.clearInterval(timer); busyCallback.current?.(false); };
  }, [load]);
  useEffect(() => { if (alive.current) void load(true); }, [record.revision, load]);

  const mutate = async (name: string, path: string, payload: Record<string, unknown>, after?: (value: EntryInvestigation) => void | Promise<void>) => {
    if (busyRef.current || disabled || (writeBlocked && name !== 'refresh')) return;
    const caseID = record.id;
    busyRef.current = true; sequence.current++; readRequest.current?.abort(); setLoading(false); setBusy(name); setError(''); busyCallback.current?.(true);
    try {
      const result = parseEntryInvestigation(unwrapData(await apiJsonPost(`${ENTRY_EVIDENCE_PATH}/${path}`, payload)), name === 'next' ? undefined : caseID);
      if (!alive.current || currentCase.current !== caseID) return;
      if (name !== 'next') { setTask(result); setLoaded(true); setWriteBlocked(false); }
      await after?.(result);
    } catch (error) {
      if (alive.current && currentCase.current === caseID) { setError(`${errorText(error)}。请先“重新读取状态”核对结果，不要直接重试写入。`); setWriteBlocked(true); }
    } finally {
      busyRef.current = false; busyCallback.current?.(false);
      if (alive.current && currentCase.current === caseID) setBusy('');
    }
  };
  const start = () => {
    try {
      const payload = investigationStartPayload(record, { blockedAt, note, hours, maxRounds, stopUsers, confirmed });
      void mutate('start', 'investigation-start', payload, () => { message.success('半自动排查已保存；请核对本轮二分预览后确认执行'); });
    } catch (error) { setError(errorText(error)); }
  };
  const openFailure = (branch: InvestigationBranchName) => {
    if (!task) return;
    setFailure({ task, branch }); setFailureAt(nowSeconds()); setFailureNote(''); setFailureConfirmed(false); setControlStatus('unknown');
  };
  const confirmFailure = () => {
    if (!failure) return;
    try {
      // Bind to the version the operator reviewed, not a newer polling result.
      const payload = investigationConfirmPayload(failure.task, { branch: failure.branch, blockedAt: failureAt, note: failureNote, controlStatus, confirmed: failureConfirmed });
      void mutate('confirm', 'investigation-confirm', payload, () => { setFailure(null); message.success('人工核实结果已记录；系统不会据此直接封禁用户'); });
    } catch (error) { setError(errorText(error)); }
  };
  const prepareNext = () => {
    if (!nextTask || !investigationCanPrepareNext(nextTask)) return;
    void mutate('next', 'investigation-next', { case_id: nextTask.case_id, revision: nextTask.revision }, async (value) => {
      setNextTask(null); message.success('下一轮名单已冻结，尚未修改入口'); await onOpenCase(value.case_id);
    });
  };
  const locked = !!busy || disabled;
  const state = task && (INVESTIGATION_STATES[task.report.state] || { label: '待核对任务状态', color: 'default' });

  return <Card size="small" className="entry-investigation-panel" title={<Space wrap><BranchesOutlined /><span>半自动排查</span>{state && <Tag color={state.color}>{state.label}</Tag>}</Space>}>
    <Space direction="vertical" size="middle" style={{ width: '100%' }}>
      <Typography.Text type="secondary">直接在 AI 运维说“这个组确定被墙了，帮我排查”即可，由 AI 查询、冻结名单并按当前权限执行二分；本页用于查看进度，也可手动办理。任务保存在后台，不随聊天关闭而丢失；不会自动认定或封禁用户。</Typography.Text>
      {error && <Alert type="error" showIcon message={error} action={<Button size="small" disabled={locked} onClick={() => void load()}>重新读取状态</Button>} />}
      {!loaded ? <Spin spinning={loading}><Typography.Text type="secondary">正在读取排查任务…</Typography.Text></Spin> : !task ? <>
        {record.status !== 'frozen' || record.parent_case_id > 0 ? <Alert type="info" showIcon message="此档案尚未开启半自动任务" description="请从独立、已冻结且尚未执行的档案开启；子档案请由上一轮任务预备，避免重置轮次和观察条件。现有记录仍可查看。" /> : <>
          <Alert type="info" showIcon message="从本档案的冻结候选开始" description="首次异常时间必须等于冻结名单的窗口结束时间；不一致时请重新查询并冻结。仅支持实际 IP 入口，不按域名推断。单次 TCP 失败不能证明被墙。" />
          <Form layout="vertical" className="entry-investigation-form">
            <Row gutter={16}><Col xs={24} md={12}><Form.Item label="首次异常时间（固定为证据窗口结束）" required><DatePicker showTime format="YYYY-MM-DD HH:mm:ss" value={blockedAt ? dayjs.unix(blockedAt) : null} disabled /></Form.Item></Col><Col xs={24} md={12}><Form.Item label="每轮最短观察时间（小时）"><InputNumber<number> min={1} max={168} precision={0} value={hours} disabled={locked} onChange={setHours} /></Form.Item></Col></Row>
            <Row gutter={16}><Col xs={12}><Form.Item label="最大观察轮数"><InputNumber<number> min={1} max={30} precision={0} value={maxRounds} disabled={locked} onChange={setMaxRounds} /></Form.Item></Col><Col xs={12}><Form.Item label="剩余多少人时转人工复核"><InputNumber<number> min={2} max={8} precision={0} value={stopUsers} disabled={locked} onChange={setStopUsers} /></Form.Item></Col></Row>
            <Form.Item label="确认依据" required><Input.TextArea maxLength={2000} showCount autoSize={{ minRows: 2, maxRows: 5 }} value={note} disabled={locked} onChange={(event) => setNote(event.target.value)} placeholder="例如：多网络持续失败，源站和境外连接正常，已排除监听与机房故障" /></Form.Item>
            <Form.Item><Checkbox checked={confirmed} disabled={locked} onChange={(event) => setConfirmed(event.target.checked)}>我已核实该入口被封锁，不是仅凭单次探针失败</Checkbox></Form.Item>
            <Button type="primary" disabled={locked || !confirmed || writeBlocked} loading={busy === 'start'} onClick={start}>开启半自动排查</Button>
          </Form>
        </>}
      </> : <>
        <Descriptions size="small" column={{ xs: 1, sm: 2, lg: 3 }} items={[
          { key: 'round', label: '当前轮次', children: `第 ${task.round} / ${task.settings.max_rounds} 轮` },
          { key: 'root', label: '起始档案', children: <Button size="small" type="link" disabled={locked} onClick={() => void onOpenCase(task.root_case_id)}>#{task.root_case_id}</Button> },
          { key: 'duration', label: '已观察', children: evidenceDuration(task.report.observed_seconds) },
          { key: 'minimum', label: '最短观察', children: evidenceDuration(task.settings.min_observe_seconds) },
          { key: 'checked', label: '后台整理时间', children: evidenceTime(task.report.checked_at) },
          { key: 'stop', label: '转人工复核', children: `候选不超过 ${task.settings.stop_users} 人` },
        ]} />
        <Alert type={task.report.blockers.length ? 'warning' : 'info'} showIcon message={task.report.reason || '请结合冻结名单、实际下发和人工确认继续核对。'} />
        {task.report.blockers.map((text, index) => <Alert key={`blocker-${index}`} type="warning" showIcon message={text} />)}
        {!!task.report.warnings.length && <details className="entry-investigation-notes"><summary>证据范围与注意事项（{task.report.warnings.length}）</summary><ul>{task.report.warnings.map((text, index) => <li key={index}>{text}</li>)}</ul></details>}
        <Row gutter={[12, 12]}>{task.report.branches.map((branch) => <Col xs={24} sm={12} key={branch.branch}>
          <Card size="small" className="entry-investigation-branch" title={<Space wrap><strong>{branch.branch} 组</strong><Tag>{INVESTIGATION_HEALTH[branch.health] || '检测状态待核对'}</Tag></Space>}>
            <Typography.Paragraph className="entry-investigation-host">{branch.host}</Typography.Paragraph>
            <div className="entry-investigation-counts">{[
              ['分配人数', branch.assigned_users], ['实际下发人数', branch.acquired_users], ['未记录下发', branch.unrecorded_users], ['异常前已下发', branch.blocked_at ? branch.pre_failure_users : '—'],
            ].map(([label, value]) => <div key={label}><Typography.Text type="secondary">{label}</Typography.Text><strong>{value}</strong></div>)}</div>
            <Typography.Paragraph type="secondary" className="entry-investigation-timestamp">最近检测：{evidenceTime(branch.last_reported_at)}{branch.blocked_at ? <><br />人工确认异常：{evidenceTime(branch.blocked_at)}</> : null}</Typography.Paragraph>
            {!!task.report.executed_at && <Button disabled={locked || writeBlocked} onClick={() => openFailure(branch.branch)}>{branch.blocked_at ? '补充异常核实记录' : '记录已核实的封锁'}</Button>}
          </Card>
        </Col>)}</Row>
        {!!task.failures.length && <details className="entry-investigation-notes"><summary>人工核实记录（{task.failures.length}）</summary>{task.failures.map((item, index) => <div className="entry-investigation-failure" key={`${item.branch}-${item.confirmed_at}-${index}`}><Typography.Text strong>{item.branch} 组 · {evidenceTime(item.blocked_at)}</Typography.Text><p>{item.note}</p><Typography.Text type="secondary">{INVESTIGATION_CONTROL[item.control_status] || '对照状态待核对'} · 操作人 #{item.confirmed_by} · {evidenceTime(item.confirmed_at)}</Typography.Text></div>)}</details>}
        <Space wrap className="entry-investigation-actions">
          <Button icon={<ReloadOutlined />} disabled={locked} loading={busy === 'refresh'} onClick={() => void mutate('refresh', 'investigation-refresh', { case_id: record.id })}>重新分析进度</Button>
          {task.next_case_id > 0 ? <Button type="primary" disabled={locked} onClick={() => void onOpenCase(task.next_case_id)}>查看下一轮 #{task.next_case_id}</Button> : <Button type="primary" icon={<BranchesOutlined />} disabled={locked || writeBlocked || !investigationCanPrepareNext(task)} onClick={() => setNextTask(task)}>预备下一轮</Button>}
        </Space>
        <Typography.Text type="secondary">实际下发指服务端完整返回，不证明客户端已导入或使用。检测正常的一组仍保留观察；这里不会认定账号本人有责任。</Typography.Text>
      </>}
    </Space>
    <Modal title={failure ? `${failure.branch} 组 · 记录人工核实的封锁` : '记录封锁'} open={!!failure} onCancel={() => { if (!busy) setFailure(null); }} onOk={confirmFailure} okText="保存核实记录" confirmLoading={busy === 'confirm'} okButtonProps={{ disabled: locked || writeBlocked || !failureConfirmed }} destroyOnHidden>
      {error && <Alert type="error" showIcon message={error} />}
      <Alert type="warning" showIcon message="这是人工核实记录，不是自动归因" description="请填写首次异常时间，而不是现在点击确认的时间。只有此前实际下发的用户会成为下一轮优先候选；缺少对照或证据时可能需要继续复核。" />
      <Form layout="vertical" className="entry-investigation-form">
        <Form.Item label="首次异常时间（本地时区）" required><DatePicker showTime format="YYYY-MM-DD HH:mm:ss" value={failureAt ? dayjs.unix(failureAt) : null} disabled={locked} onChange={(value) => setFailureAt(value?.unix() ?? null)} /></Form.Item>
        <Form.Item label="未向用户下发的对照入口"><Select value={controlStatus} disabled={locked} onChange={setControlStatus} options={Object.entries(INVESTIGATION_CONTROL).map(([value, label]) => ({ value, label }))} /></Form.Item>
        <Form.Item label="核实依据" required><Input.TextArea maxLength={2000} showCount autoSize={{ minRows: 3, maxRows: 6 }} value={failureNote} disabled={locked} onChange={(event) => setFailureNote(event.target.value)} /></Form.Item>
        <Checkbox checked={failureConfirmed} disabled={locked} onChange={(event) => setFailureConfirmed(event.target.checked)}>我已人工核实该分支入口被封锁，并按实际情况填写对照状态</Checkbox>
      </Form>
    </Modal>
    <Modal title="仅冻结下一轮，不立即修改入口" open={!!nextTask} onCancel={() => { if (!busy) setNextTask(null); }} onOk={prepareNext} okText="冻结并查看下一轮" confirmLoading={busy === 'next'} okButtonProps={{ disabled: locked || writeBlocked || !investigationCanPrepareNext(nextTask) }} destroyOnHidden>
      <Typography.Paragraph>后台将按本轮已核实的异常时间，冻结对应分支实际下发过入口的候选名单。另一分支和其他用户不会因此被删除或封禁。</Typography.Paragraph>
      {nextTask?.report.next_query && <Descriptions column={1} size="small" items={[
        { key: 'host', label: '目标入口', children: `${nextTask.report.next_query.host}:${nextTask.report.next_query.port}` },
        { key: 'window', label: '证据窗口', children: `${evidenceTime(nextTask.report.next_query.from)} ～ ${evidenceTime(nextTask.report.next_query.to)}（结束不含）` },
      ]} />}
      <Alert type="info" showIcon message="冻结后仍需核对二分预览，点击“确认执行二分”才会改变真实入口。" />
    </Modal>
  </Card>;
}
