import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Form, Input, Modal, message } from 'antd';
import { apiPost } from '../lib/api';

type Props = {
  id: number;
  tradeNo: string;
  disabled?: boolean;
  compact?: boolean;
  onChanged: () => void;
  onStart?: () => void;
  onBusyChange?: (busy: boolean) => void;
};

export default function AppleIDCancelOrderButton({ id, tradeNo, disabled, compact, onChanged, onStart, onBusyChange }: Props) {
  const [open, setOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [form] = Form.useForm<{ reason: string }>();
  const pending = useRef(false);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; };
  }, []);

  const submit = async () => {
    if (pending.current) return;
    let values: { reason: string };
    try { values = await form.validateFields(); } catch { return; }
    if (pending.current || !alive.current) return;
    pending.current = true;
    setSaving(true);
    onBusyChange?.(true);
    try {
      await apiPost('/apple-id/order/cancel', { id, reason: values.reason.trim() });
      if (!alive.current) return;
      setOpen(false);
      form.resetFields();
      message.success('订单已取消，预留库存已释放');
      onChanged();
    } catch (e: any) {
      if (alive.current) message.error(e.message || '取消失败，请刷新订单状态后重试');
    } finally {
      pending.current = false;
      if (alive.current) {
        setSaving(false);
        onBusyChange?.(false);
      }
    }
  };

  return <>
    <Button danger type={compact ? 'link' : 'default'} disabled={disabled} loading={saving} onClick={() => {
      onStart?.();
      form.resetFields();
      setOpen(true);
    }}>{compact ? '取消订单' : '取消订单并释放库存'}</Button>
    <Modal open={open} title="取消待支付订单" onCancel={() => { if (!pending.current) setOpen(false); }} onOk={submit}
      confirmLoading={saving} okButtonProps={{ danger: true }} cancelButtonProps={{ disabled: saving }}
      okText="确认取消并释放库存" cancelText="返回" closable={!saving} maskClosable={!saving} keyboard={!saving} destroyOnHidden>
      <Alert type="warning" showIcon message="取消后立即释放预留库存"
        description="仅待支付订单可取消。原支付链接若仍收到付款，系统会重新分配可售账号；缺货时转人工处理。" style={{ marginBottom: 16 }} />
      <p style={{ overflowWrap: 'anywhere' }}>订单：{tradeNo}</p>
      <Form form={form} layout="vertical">
        <Form.Item name="reason" label="取消原因" rules={[{ required: true, whitespace: true, message: '请填写取消原因' }]}>
          <Input.TextArea rows={3} maxLength={500} showCount disabled={saving} placeholder="例如：用户申请取消，释放预留账号" />
        </Form.Item>
      </Form>
    </Modal>
  </>;
}
