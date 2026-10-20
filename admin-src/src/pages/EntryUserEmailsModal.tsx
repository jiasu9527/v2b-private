import { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, Button, Input, Modal, Space, Typography, message } from 'antd';
import { CopyOutlined, DownloadOutlined, ReloadOutlined } from '@ant-design/icons';
import { apiGet } from '../lib/api';
import { entryUserEmailSnapshot, entryUserEmailsFilename, type EntryUserEmailSnapshot } from './entryUserEmails';

async function copyEmails(text: string) {
  if (navigator.clipboard?.writeText) {
    try { await navigator.clipboard.writeText(text); return; } catch { /* Allow HTTP panels and denied clipboard access to fall back. */ }
  }
  const input = document.createElement('textarea');
  input.value = text;
  input.style.position = 'fixed';
  input.style.opacity = '0';
  input.setAttribute('readonly', '');
  const focused = document.activeElement;
  let copied = false;
  try {
    document.body.appendChild(input);
    input.focus();
    input.select();
    copied = document.execCommand('copy');
  } finally {
    input.remove();
    if (focused instanceof HTMLElement) focused.focus();
  }
  if (!copied) throw new Error('复制失败，请在文本框中手动复制');
}

function downloadEmails(snapshot: EntryUserEmailSnapshot) {
  const url = URL.createObjectURL(new Blob([snapshot.text], { type: 'text/plain;charset=UTF-8' }));
  const link = document.createElement('a');
  try {
    link.href = url;
    link.download = entryUserEmailsFilename(snapshot.policyID, snapshot.name);
    link.style.display = 'none';
    document.body.appendChild(link);
    link.click();
  } finally {
    link.remove();
    // Leave the object URL available until the browser has started its download.
    window.setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
}

export default function EntryUserEmailsModal({ policyID, open, onClose }: { policyID: number; open: boolean; onClose: () => void }) {
  const [snapshot, setSnapshot] = useState<EntryUserEmailSnapshot | null>(null);
  const [loading, setLoading] = useState(false);
  const [copying, setCopying] = useState(false);
  const [error, setError] = useState('');
  const sequence = useRef(0);
  const copyPending = useRef(false);
  const load = useCallback(async () => {
    const ticket = ++sequence.current;
    setSnapshot(null);
    setError('');
    setLoading(true);
    try {
      const response = await apiGet('/server/client-entry-user-policy/fetch');
      if (ticket !== sequence.current) return;
      setSnapshot(entryUserEmailSnapshot(response?.data, policyID));
    } catch (e: any) {
      if (ticket === sequence.current) setError(e?.message || '读取指定邮箱失败，请刷新重试');
    } finally {
      if (ticket === sequence.current) setLoading(false);
    }
  }, [policyID]);
  useEffect(() => {
    setSnapshot(null);
    setError('');
    setCopying(false);
    copyPending.current = false;
    if (open) void load();
    return () => { sequence.current += 1; };
  }, [open, load]);
  const close = () => {
    sequence.current += 1;
    setSnapshot(null);
    setError('');
    setLoading(false);
    onClose();
  };
  const copy = async () => {
    if (loading || !snapshot?.text || copyPending.current) return;
    const ticket = sequence.current;
    copyPending.current = true;
    setCopying(true);
    try {
      await copyEmails(snapshot.text);
      if (ticket === sequence.current) message.success(`已复制 ${snapshot.emails.length} 个指定邮箱`);
    } catch (e: any) {
      if (ticket === sequence.current) message.error(e?.message || '复制失败，请在文本框中手动复制');
    } finally {
      if (ticket === sequence.current) { copyPending.current = false; setCopying(false); }
    }
  };
  const exportTXT = () => {
    if (loading || !snapshot?.text) return;
    try { downloadEmails(snapshot); } catch { message.error('导出失败，请重试或复制全部邮箱'); }
  };
  const unavailable = loading || !snapshot?.text;
  return <Modal title="规则指定邮箱" open={open} onCancel={close} width={760} destroyOnHidden footer={<Button onClick={close}>关闭</Button>}>
    <Alert type="info" showIcon message="仅查看规则配置，不计算实际命中" description="仅展示规则配置中的指定邮箱，不代表实际命中用户；不查询用户资料，不过滤未注册、过期或停用账号。" style={{ marginBottom: 16, background: '#f7f8fa', borderColor: '#dfe3e8' }} />
    {snapshot && <Typography.Paragraph style={{ overflowWrap: 'anywhere' }}><Typography.Text strong>{snapshot.name}</Typography.Text><Typography.Text type="secondary"> · 规则 #{snapshot.policyID}</Typography.Text></Typography.Paragraph>}
    {snapshot && snapshot.conditionCount > 1 && <Alert type="warning" showIcon message="多个邮箱条件的配置合集" description={`本规则有 ${snapshot.conditionCount} 个指定邮箱条件，以下合并去重展示各条件填写的邮箱；实际规则条件为 AND，不能将此合集视为实际命中名单。`} style={{ marginBottom: 16 }} />}
    <Space wrap style={{ marginBottom: 12 }}>
      <Button icon={<ReloadOutlined aria-hidden />} loading={loading} disabled={copying} onClick={() => { void load(); }}>刷新名单</Button>
      <Button icon={<CopyOutlined aria-hidden />} disabled={unavailable} loading={copying} onClick={() => { void copy(); }}>复制全部</Button>
      <Button icon={<DownloadOutlined aria-hidden />} disabled={unavailable || copying} onClick={exportTXT}>导出 TXT</Button>
      <Typography.Text type="secondary">{loading ? '正在读取最新配置…' : snapshot ? `共 ${snapshot.emails.length} 个指定邮箱` : '名单未加载'}</Typography.Text>
    </Space>
    {error && <Alert type="error" showIcon message={error} style={{ marginBottom: 12 }} />}
    <Input.TextArea readOnly aria-label="指定邮箱（每行一个）" value={snapshot?.text || ''} rows={12} spellCheck={false} placeholder={loading ? '正在读取…' : snapshot ? '此规则的指定邮箱名单为空' : '请读取有效的规则名单后查看'} style={{ overflowWrap: 'anywhere', resize: 'vertical' }} />
    <Typography.Paragraph type="secondary" style={{ marginTop: 8, marginBottom: 0 }}>去除首尾空格、转为小写后去重排序；复制和 TXT 导出均为每行一个邮箱，不包含标题或用户资料。</Typography.Paragraph>
  </Modal>;
}
