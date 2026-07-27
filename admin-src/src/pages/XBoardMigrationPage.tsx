import React, { useState } from 'react';
import { Alert, Button, Card, Col, Form, Input, InputNumber, Modal, Row, Space, Statistic, Table, Tag, Typography, message } from 'antd';
import { CheckCircleOutlined, DatabaseOutlined, EyeOutlined } from '@ant-design/icons';
import { apiJsonPost } from '../lib/api';

const sourcePlanNames: Record<number, string> = {
  1: '🫧 泡泡 Lite（月付）', 2: '🫧 泡泡 Air（推荐）', 3: '🚀 泡泡 Pro（月付）',
  4: '🫧 泡泡 Ultra（月付）', 5: '独享原生 IP 节点搭建', 6: '美国独享节点-五人车',
  7: '🫧 泡泡 Lite 一次性流量包', 8: '🫧 泡泡 Air 一次性流量包', 9: '🫧 泡泡 Pro 一次性流量包',
};

type SourceConfig = { host: string; port: number; database: string; username: string; password: string };

export default function XBoardMigrationPage() {
  const [form] = Form.useForm<SourceConfig>();
  const [preview, setPreview] = useState<any>(null);
  const [source, setSource] = useState<SourceConfig | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const [executing, setExecuting] = useState(false);
  const [result, setResult] = useState<any>(null);

  const loadPreview = async () => {
    const values = await form.validateFields();
    setPreviewing(true);
    setResult(null);
    try {
      const response = await apiJsonPost('/migration/xboard/preview', { source: values });
      setSource({ ...values });
      setPreview(response.data);
      message.success('预览完成，尚未写入任何用户');
    } catch (error: any) {
      setPreview(null);
      message.error(error?.message || '预览失败');
    } finally {
      setPreviewing(false);
    }
  };

  const execute = () => {
    if (!preview || !source) return;
    Modal.confirm({
      title: '确认执行 XBoard 用户迁移？',
      width: 560,
      content: <div>
        <p>将新增 <b>{preview.ready}</b> 个用户。管理员、无套餐用户、未映射套餐及邮箱冲突用户都会跳过。</p>
        <Alert type="warning" showIcon message="开始执行后，本次预览令牌只能使用一次；执行期间请勿关闭数据库。" />
      </div>,
      okText: `确认迁移 ${preview.ready} 个用户`,
      cancelText: '取消',
      okButtonProps: { danger: true },
      onOk: async () => {
        setExecuting(true);
        try {
          const response = await apiJsonPost('/migration/xboard/execute', { source, preview_token: preview.preview_token });
          setResult(response.data);
          setPreview(null);
          message.success(`迁移完成，成功导入 ${response.data?.imported || 0} 个用户`);
        } catch (error: any) {
          message.error(error?.message || '迁移失败');
          throw error;
        } finally {
          setExecuting(false);
        }
      },
    });
  };

  const stats = preview ? [
    ['源用户', preview.source_users, undefined], ['可迁移', preview.ready, '#1677ff'], ['管理员跳过', preview.skip_admin, undefined],
    ['无套餐跳过', preview.skip_no_plan, '#fa8c16'], ['邮箱冲突跳过', preview.skip_conflict, '#fa8c16'], ['未映射跳过', preview.skip_unmapped, '#cf1322'],
  ] : [];

  const columns = [
    { title: '源套餐', dataIndex: 'source_plan_id', render: (id: number) => <><Tag>#{id}</Tag>{sourcePlanNames[id] || `套餐 ${id}`}</> },
    { title: '目标套餐', render: (_: any, row: any) => <><Tag color="blue">#{row.target_plan_id}</Tag>{row.target_name}</> },
    { title: '预计迁移用户', dataIndex: 'users', width: 150 },
  ];

  return <div className="legacy-page xboard-migration-page">
    <div className="content-heading">XBoard 用户迁移</div>
    <Card className="block-card" title={<Space><DatabaseOutlined />源 MySQL 连接</Space>}>
      <Alert type="info" showIcon style={{ marginBottom: 18 }} message="安全流程：先只读扫描并生成预览，确认后才写入目标库。连接密码只用于本次请求，不会保存。" />
      <Form form={form} layout="vertical" initialValues={{ port: 3306, database: 'xboard' }} onValuesChange={() => { setPreview(null); setResult(null); }}>
        <Row gutter={16}>
          <Col xs={24} md={10}><Form.Item name="host" label="MySQL 地址" rules={[{ required: true, message: '请输入源 MySQL 地址' }]}><Input placeholder="例如 10.0.0.8 或 mysql.example.com" /></Form.Item></Col>
          <Col xs={24} md={4}><Form.Item name="port" label="端口" rules={[{ required: true }]}><InputNumber min={1} max={65535} style={{ width: '100%' }} /></Form.Item></Col>
          <Col xs={24} md={5}><Form.Item name="database" label="数据库" rules={[{ required: true }]}><Input /></Form.Item></Col>
          <Col xs={24} md={5}><Form.Item name="username" label="只读账号" rules={[{ required: true }]}><Input autoComplete="off" /></Form.Item></Col>
          <Col xs={24} md={10}><Form.Item name="password" label="密码" rules={[{ required: true }]}><Input.Password autoComplete="new-password" /></Form.Item></Col>
        </Row>
        <Button type="primary" icon={<EyeOutlined />} loading={previewing} onClick={loadPreview}>连接并生成预览</Button>
      </Form>
    </Card>

    {preview && <Card className="block-card" title="迁移预览" style={{ marginTop: 18 }}>
      <Alert type="warning" showIcon message="当前只是预览，没有写入用户。无套餐用户固定跳过，不会自动分配套餐。" style={{ marginBottom: 18 }} />
      <Row gutter={[16, 16]}>{stats.map(([title, value, color]) => <Col xs={12} md={8} xl={4} key={String(title)}><Card size="small"><Statistic title={title} value={value as number} valueStyle={color ? { color: String(color) } : undefined} /></Card></Col>)}</Row>
      <Table style={{ marginTop: 18 }} rowKey="source_plan_id" dataSource={preview.plan_breakdown || []} columns={columns} pagination={false} size="small" />
      <Space style={{ marginTop: 18 }} wrap>
        <Button danger type="primary" icon={<CheckCircleOutlined />} loading={executing} disabled={!preview.ready} onClick={execute}>确认并执行迁移</Button>
        <Typography.Text type="secondary">预览有效期 30 分钟；源站或目标站用户变化后必须重新预览。</Typography.Text>
      </Space>
    </Card>}

    {result && <Card className="block-card" title="最近一次迁移结果" style={{ marginTop: 18 }}>
      <Alert type={result.failed ? 'warning' : 'success'} showIcon message={`批次 #${result.batch_id}：成功导入 ${result.imported} 个，失败 ${result.failed} 个`} description={`管理员跳过 ${result.skip_admin}；无套餐跳过 ${result.skip_no_plan}；邮箱冲突跳过 ${result.skip_conflict}；未映射跳过 ${result.skip_unmapped}`} />
    </Card>}
  </div>;
}
