import { useEffect, useRef, useState } from 'react';
import { message } from 'antd';
import { apiGet } from '../lib/api';

// Keep both split dialogs in the same pool order. This is a preview only:
// the server checks health/ownership again when it claims IPs in a transaction.
export function availableBackupPoolIPs(response: any): string[] {
  const payload = response?.data ?? response;
  const items = Array.isArray(payload) ? payload
    : Array.isArray(payload?.items) ? payload.items
      : Array.isArray(payload?.backup_ips) ? payload.backup_ips : [];
  const seen = new Set<string>();
  return items.flatMap((item: any) => {
    const ip = typeof item?.ip === 'string' ? item.ip.trim() : '';
    const key = ip.toLowerCase();
    const disabled = [false, 0, '0'].includes(item?.enabled);
    const used = [true, 1, '1'].includes(item?.used);
    const available = item?.available == null
      ? String(item?.status || '').trim().toLowerCase() === 'available'
      : [true, 1, '1'].includes(item.available);
    if (!ip || seen.has(key) || disabled || used || !available) return [];
    seen.add(key);
    return [ip];
  });
}

export function useSplitBackupIPPool(form: { setFieldsValue: (values: any) => void }) {
  const [filling, setFilling] = useState(false);
  const [usePool, setUsePool] = useState(false);
  const requestSequence = useRef(0);
  const fillingRef = useRef(false);

  useEffect(() => () => { requestSequence.current += 1; }, []);

  // Opening, closing or manually editing a dialog invalidates its old preview.
  // A late response must never overwrite a new dialog or the user's own hosts.
  const reset = () => {
    requestSequence.current += 1;
    fillingRef.current = false;
    setFilling(false);
    setUsePool(false);
  };
  const fill = async () => {
    if (fillingRef.current) return;
    const sequence = ++requestSequence.current;
    fillingRef.current = true;
    setFilling(true);
    // A failed refresh must not turn an existing pool preview into manual
    // hosts, bypassing the server's health/ownership recheck on submission.
    try {
      const response = await apiGet('/dns-failover/entry-monitors/backup-ips');
      if (sequence !== requestSequence.current) return;
      const available = availableBackupPoolIPs(response);
      if (available.length < 2) {
        message.warning(`健康且未使用的备用 IP 不足 2 个，当前只有 ${available.length} 个`);
        return;
      }
      const [hostA, hostB] = available;
      form.setFieldsValue({ entry_host_a: hostA, entry_host_b: hostB });
      setUsePool(true);
      message.success(`已自动填入备用 IP：${hostA}、${hostB}`);
    } catch (error: any) {
      if (sequence === requestSequence.current) message.error(error?.message || '读取备用 IP 池失败');
    } finally {
      if (sequence === requestSequence.current) {
        fillingRef.current = false;
        setFilling(false);
      }
    }
  };
  return { filling, usePool, fill, reset, isFilling: () => fillingRef.current };
}
