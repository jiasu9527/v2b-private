import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Checkbox, Descriptions, Form, Input, Modal, Space, Tag, Typography, message } from 'antd';
import { MailOutlined, ReloadOutlined } from '@ant-design/icons';
import { getXboardMailInfo, MigrationRequestError, sendXboardMail } from '../lib/xboardMigration';
import type { MigrationMailInfo, MigrationMailRequest, MigrationMailTask, MigrationReceipt } from '../lib/xboardMigration';

const { Paragraph, Text } = Typography;
const active = (task?: MigrationMailTask | null) => task?.status === 'queued' || task?.status === 'running';
const statusLabels: Record<MigrationMailTask['status'], string> = {
  queued: '已进入发送队列', running: '发送中', completed: '发送任务已结束',
  partial_failed: '发送结束，部分失败', interrupted: '任务已中断', queue_failed: '入队失败，未开始发送',
};
const statusColors: Record<MigrationMailTask['status'], string> = {
  queued: 'blue', running: 'processing', completed: 'success',
  partial_failed: 'warning', interrupted: 'error', queue_failed: 'error',
};
const formatTime = (value: number) => new Date(value * 1000).toLocaleString('zh-CN', { hour12: false });
const intervalLabel = (seconds: number) => seconds > 0 ? `每 ${seconds} 秒发送一封` : '不限速（0 秒）';
const newRequestID = () => Array.from(crypto.getRandomValues(new Uint8Array(32)), (value) => value.toString(16).padStart(2, '0')).join('');
const contentBytes = (content: string) => new TextEncoder().encode(content.trim()).length;
const infoAlertStyle = { background: '#f7f8fa', borderColor: '#dfe3e8' };
const pendingStorageKey = (previewID: string) => `forest-migration-mail-pending:${previewID}`;
const validSubject = (subject: string) => !!subject.trim() && Array.from(subject.trim()).length <= 200 && !/[\r\n\x00]/.test(subject);

function readPending(previewID: string): { request: MigrationMailRequest | null; error: string } {
  try {
    const raw = sessionStorage.getItem(pendingStorageKey(previewID));
    if (raw === null) return { request: null, error: '' };
    const saved: unknown = JSON.parse(raw);
    if (!saved || typeof saved !== 'object') throw new Error('invalid pending mail');
    const request = saved as Partial<MigrationMailRequest>;
    if (request.preview_id !== previewID || typeof request.request_id !== 'string' || !/^[a-f0-9]{64}$/.test(request.request_id) || typeof request.subject !== 'string' || !validSubject(request.subject) || typeof request.content !== 'string' || !request.content.trim() || contentBytes(request.content) > 100000 || request.confirm !== true) throw new Error('invalid pending mail');
    return { request: { preview_id: previewID, request_id: request.request_id, subject: request.subject, content: request.content, confirm: true }, error: '' };
  } catch {
    return { request: null, error: '无法读取本标签页的待确认邮件记录，已禁止发送。请恢复浏览器会话存储后刷新页面，并先核查本批邮件任务；不要直接清除记录后重新群发。' };
  }
}

function savePending(request: MigrationMailRequest): void {
  const serialized = JSON.stringify(request);
  sessionStorage.setItem(pendingStorageKey(request.preview_id), serialized);
  if (sessionStorage.getItem(pendingStorageKey(request.preview_id)) !== serialized) throw new Error('pending mail was not saved');
}

function removePending(previewID: string, requestID: string): void {
  const saved = readPending(previewID);
  if (saved.error) throw new Error('pending mail unreadable');
  // An older response must not remove a newer confirmed submission.
  if (saved.request?.request_id === requestID) {
    sessionStorage.removeItem(pendingStorageKey(previewID));
    if (sessionStorage.getItem(pendingStorageKey(previewID)) !== null) throw new Error('pending mail was not removed');
  }
}

export default function XboardMigrationMail({ receipt }: { receipt: MigrationReceipt }) {
  // The parent keys this component by preview_id. Restore before its first
  // status query/render so navigation never briefly enables a new request.
  const [recovered] = useState(() => readPending(receipt.preview_id));
  const [storageError, setStorageError] = useState(recovered.error);
  const [info, setInfo] = useState<MigrationMailInfo>();
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState('');
  const [sendError, setSendError] = useState('');
  const [open, setOpen] = useState(false);
  const [subject, setSubject] = useState(recovered.request?.subject || '');
  const [content, setContent] = useState(recovered.request?.content || '');
  const [confirmed, setConfirmed] = useState(false);
  const [sending, setSending] = useState(false);
  const [uncertain, setUncertain] = useState(!!recovered.request);
  const [submission, setSubmission] = useState<MigrationMailRequest | null>(recovered.request);
  const mounted = useRef(true);
  const reading = useRef(false);
  const submitting = useRef(false);
  const taskVersion = useRef(0);
  const submissionRef = useRef<MigrationMailRequest | null>(recovered.request);

  const resolvePending = useCallback((requestID: string) => {
    try { removePending(receipt.preview_id, requestID); }
    catch { setStorageError('任务结果已确认，但无法清理本标签页的待确认记录，已禁止新建群发。请恢复浏览器会话存储后刷新，并核查任务状态；不要盲目重复发送。'); }
    submissionRef.current = null; setSubmission(null); setUncertain(false);
  }, [receipt.preview_id]);

  const refresh = useCallback(async () => {
    if (reading.current) return;
    reading.current = true;
    const version = taskVersion.current;
    setLoading(true);
    try {
      const data = await getXboardMailInfo(receipt.preview_id);
      if (!mounted.current || version !== taskVersion.current) return;
      setInfo(data); setLoadError('');
      if (data.task && data.task.request_id === submissionRef.current?.request_id) {
        resolvePending(data.task.request_id); setOpen(false); setSendError('');
      }
    } catch (error: any) {
      if (mounted.current) setLoadError(error?.message || '读取邮件状态失败，请重试；未读取成功时不可发送');
    } finally {
      reading.current = false;
      if (mounted.current) setLoading(false);
    }
  }, [receipt.preview_id, resolvePending]);

  useEffect(() => {
    mounted.current = true;
    void refresh();
    return () => { mounted.current = false; };
  }, [refresh]);
  useEffect(() => {
    if (!active(info?.task) && !uncertain) return;
    const timer = window.setInterval(() => { void refresh(); }, 5000);
    return () => window.clearInterval(timer);
  }, [info?.task?.status, uncertain, refresh]);
  useEffect(() => {
    if (!uncertain && !sending) return;
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ''; };
    window.addEventListener('beforeunload', warn);
    return () => window.removeEventListener('beforeunload', warn);
  }, [uncertain, sending]);

  const task = info?.task;
  const canCompose = !!info && !loading && !loadError && !storageError && info.tracking_available && info.configured && info.queue_available && info.recipients > 0 && !active(task) && !uncertain && !sending;
  const invalidSubject = Array.from(subject.trim()).length > 200 || /[\r\n\x00]/.test(subject);
  const invalidContent = contentBytes(content) > 100000;
  const canSubmit = canCompose && !!subject.trim() && !!content.trim() && !invalidSubject && !invalidContent && confirmed;
  const edit = () => {
    if (!canCompose) return;
    setSubject(''); setContent(''); setConfirmed(false); setSendError('');
    submissionRef.current = null; setSubmission(null); setOpen(true);
  };
  const submit = async (retry = false) => {
    if (submitting.current || storageError || (!retry && !canSubmit) || (retry && (!uncertain || !submissionRef.current))) return;
    submitting.current = true;
    let body: MigrationMailRequest;
    try {
      body = retry ? submissionRef.current! : {
        preview_id: receipt.preview_id, request_id: newRequestID(), subject: subject.trim(), content: content.trim(), confirm: true,
      };
    } catch {
      submitting.current = false; setSendError('浏览器无法生成安全请求编号，请使用支持 Web Crypto 的浏览器'); return;
    }
    try { savePending(body); }
    catch {
      submitting.current = false;
      setStorageError('浏览器无法暂存待确认邮件，本次没有发起发送请求。请恢复本标签页的会话存储后刷新页面，再先核查已有任务。');
      return;
    }
    submissionRef.current = body; setSubmission(body); setSending(true); setUncertain(true); setSendError('');
    try {
      const result = await sendXboardMail(body);
      if (!mounted.current) return;
      // A status request started before this response may still contain an older task.
      taskVersion.current += 1;
      setInfo((previous) => previous ? { ...previous, task: result } : previous);
      resolvePending(body.request_id); setOpen(false);
      if (result.status === 'queue_failed') message.warning('任务入队失败，未开始发送，请查看任务状态');
      else message.success('已确认邮件任务，请以下方任务状态为准');
      void refresh();
    } catch (error: any) {
      if (!mounted.current) return;
      setSendError(error?.message || '提交结果尚未确认，请先查询状态，不要重新创建群发');
      // Only explicit pre-submission refusals are safe to unlock. A transport failure is not.
      if (error instanceof MigrationRequestError && ['invalid_parameter', 'mail_tracking_unavailable', 'mail_unavailable', 'mail_queue_unavailable', 'mail_no_recipients', 'mail_busy', 'receipt_not_found'].includes(error.code)) {
        resolvePending(body.request_id);
      }
      void refresh();
    } finally {
      submitting.current = false;
      if (mounted.current) setSending(false);
    }
  };

  return <Card className="migration-card" title="给本批迁入用户发送邮件" styles={{ body: { padding: 20 } }}>
    <Space direction="vertical" size={16} style={{ width: '100%', minWidth: 0 }}>
      <Paragraph type="secondary" style={{ marginBottom: 0 }}>只向本批实际新增且目前仍存在的普通用户发送，不包含跳过的重复邮箱或目标站原有用户。迁移完成不会自动发信；收件人由后端确定，不支持在这里手动添加。</Paragraph>
      {storageError && <Alert type="error" showIcon message="浏览器待确认请求保护不可用，已禁止发送" description={storageError} />}
      {loadError && <Alert type="error" showIcon message="暂时无法确认邮件状态，已禁止新建群发" description={loadError} />}
      {info && <>
        <Descriptions size="small" bordered column={{ xs: 1, sm: 2 }}>
          <Descriptions.Item label="本批新增用户">{info.imported.toLocaleString()}</Descriptions.Item>
          <Descriptions.Item label="当前可发人数">{info.recipients.toLocaleString()}</Descriptions.Item>
          <Descriptions.Item label="后台群发频率">{intervalLabel(info.interval_seconds)}</Descriptions.Item>
          <Descriptions.Item label="预计最少间隔耗时">{info.interval_seconds > 0 ? `${Math.ceil(Math.max(0, info.recipients - 1) * info.interval_seconds / 60).toLocaleString()} 分钟（不含排队与 SMTP 耗时）` : '无固定等待间隔'}</Descriptions.Item>
        </Descriptions>
        {!info.tracking_available ? <Alert type="warning" showIcon message="此批次未记录实际新增用户名单，不能精准群发" description="旧版本回执只有汇总数量，不能根据邮箱、迁移时间或连续用户 ID 推测收件人。" />
          : !info.recipients ? <Alert type="info" style={infoAlertStyle} showIcon message="本批当前没有可发送邮件的用户" /> : null}
        {!info.configured && <Alert type="warning" showIcon message="邮件服务尚未配置完成，请先检查后台邮件设置" />}
        {!info.queue_available && <Alert type="warning" showIcon message="邮件后台队列不可用，暂不能提交群发" />}
      </>}
      {sendError && <Alert type="error" showIcon message={uncertain ? '提交结果尚未确认' : '邮件任务未提交'} description={sendError} />}
      {uncertain && submission && <Alert type="warning" showIcon message="请保留当前标签页，先核对原请求，不要创建新群发" description={<Space direction="vertical" style={{ width: '100%' }}><span>原邮件内容和请求编号已锁定，并仅在本标签页的会话存储中临时保存，便于刷新或返回页面后恢复；结果确认后清除，关闭标签页会丢失。网络中断不代表任务失败，可查询状态或使用完全相同的编号与内容重试，不会自动重新发送。关闭标签页后请先恢复迁移回执并核查任务，不要盲目补发。</span><Text copyable style={{ overflowWrap: 'anywhere' }}>{submission.request_id}</Text><Button loading={sending} disabled={sending || loading || !!storageError} onClick={() => { void submit(true); }}>使用原请求重试</Button></Space>} />}
      {task && <>
        <Descriptions size="small" bordered column={{ xs: 1, sm: 2 }}>
          <Descriptions.Item label="最近邮件任务"><Tag color={statusColors[task.status]}>{statusLabels[task.status] || task.status}</Tag></Descriptions.Item>
          <Descriptions.Item label="任务收件人数">{task.recipients.toLocaleString()}</Descriptions.Item>
          <Descriptions.Item label="SMTP 已接受">{task.sent.toLocaleString()}</Descriptions.Item>
          <Descriptions.Item label="发送失败">{task.failed.toLocaleString()}</Descriptions.Item>
          <Descriptions.Item label="任务发送频率">{intervalLabel(task.interval_seconds)}</Descriptions.Item>
          <Descriptions.Item label="最后更新">{formatTime(task.updated_at)}</Descriptions.Item>
          <Descriptions.Item label="请求编号" span={2}><Text copyable style={{ overflowWrap: 'anywhere' }}>{task.request_id}</Text></Descriptions.Item>
        </Descriptions>
        {task.status === 'interrupted' && <Alert type="warning" showIcon message="任务已中断，不会自动续发" description="请先核查已发送记录；重新群发将面向整个批次，可能给已发送用户重复发信。" />}
      </>}
      <Alert type="info" style={infoAlertStyle} showIcon message="复用后台邮件模板及群发频率，按提交时的设置执行" description="“SMTP 已接受”不代表收件人最终收到。邮件使用现有进程内队列，服务重启不会自动恢复；若状态长期停留在排队或发送中，请先检查服务和邮件记录，确认前不要补发。此频率是每批内部的间隔，不是多个并行群发任务的全局限速。" />
      <Space wrap>
        <Button icon={<ReloadOutlined aria-hidden />} loading={loading} disabled={sending} onClick={() => { void refresh(); }}>刷新邮件状态</Button>
        <Button type="primary" icon={<MailOutlined aria-hidden />} disabled={!canCompose} onClick={edit}>{task ? '编辑新邮件' : '编辑群发邮件'}</Button>
        {active(task) && <Text type="secondary">每 5 秒自动刷新任务状态</Text>}
      </Space>
    </Space>

    <Modal title="确认给本批迁入用户群发邮件" open={open} onCancel={() => { if (!sending) setOpen(false); }} onOk={() => { void submit(); }} okText="确认提交群发" cancelText="暂不发送" confirmLoading={sending} okButtonProps={{ disabled: !canSubmit }} cancelButtonProps={{ disabled: sending }} closable={!sending} maskClosable={!sending} width={680}>
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <Alert type="info" style={infoAlertStyle} showIcon message={`本批当前可发 ${info?.recipients || 0} 人 · ${intervalLabel(info?.interval_seconds || 0)}`} description="使用后台通知邮件模板。请填写站点迁移说明、目标站登录方式等内容，不要在群发内容中写入数据库密码或用户的个人凭据。" />
        <Text type="secondary">确认提交时，会在本标签页的会话存储中临时保存这封邮件的主题、正文和请求编号，仅用于恢复待确认请求；结果确认后清除，关闭标签页会丢失。不保存源数据库凭据或收件人名单。</Text>
        {task && <Alert type="warning" showIcon message="这是一次新的整批群发，不是重试失败收件人" description="此前任务中的已发送用户也会再次收到邮件。请先核对历史任务与邮件记录。" />}
        {sendError && <Alert type="error" showIcon message={sendError} />}
        {uncertain && <Alert type="warning" showIcon message="原提交结果未确认，内容已锁定" description="关闭此窗口后，请在邮件卡片查询状态或使用原请求重试；不要重新创建邮件。" />}
        <Form layout="vertical" style={{ width: '100%' }}>
          <Form.Item label="邮件主题" required validateStatus={invalidSubject ? 'error' : undefined} help={invalidSubject ? '主题最多 200 个字符，不能包含换行或 NUL 字符' : undefined}><Input aria-label="迁移邮件主题" value={subject} disabled={sending || uncertain} onChange={(event) => setSubject(event.target.value)} placeholder="例如：网站迁移完成，请获取新的订阅" /></Form.Item>
          <Form.Item label="邮件正文" required validateStatus={invalidContent ? 'error' : undefined} help={invalidContent ? '正文 UTF-8 编码不能超过 100000 字节' : '支持后台通知模板允许的正文格式；不会自动附加账号密码或订阅 Token。'}><Input.TextArea aria-label="迁移邮件正文" value={content} disabled={sending || uncertain} onChange={(event) => setContent(event.target.value)} rows={7} placeholder="请填写要发送给本批迁入用户的通知内容" /></Form.Item>
        </Form>
        <Checkbox checked={confirmed} disabled={sending || uncertain} onChange={(event) => setConfirmed(event.target.checked)}>我已核对收件范围、邮件内容和后台发送频率，确认向本批用户提交群发{task ? '，并接受整批再次发送可能产生的重复邮件' : ''}。</Checkbox>
      </Space>
    </Modal>
  </Card>;
}
