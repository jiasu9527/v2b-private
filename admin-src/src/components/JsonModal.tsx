import React from 'react';
import { Modal } from 'antd';

export default function JsonModal({ open, title, data, onClose }: { open: boolean; title: string; data: any; onClose: () => void }) {
  return <Modal open={open} title={title} onCancel={onClose} footer={null} width={900}>
    <pre className="json-view">{JSON.stringify(data, null, 2)}</pre>
  </Modal>;
}
