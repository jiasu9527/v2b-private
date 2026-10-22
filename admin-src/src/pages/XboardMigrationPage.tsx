import React, { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Checkbox, Col, Descriptions, Form, Input, InputNumber, Modal, Result, Row, Select, Space, Steps, Switch, Table, Tag, Typography, message } from 'antd';
import { DatabaseOutlined, ReloadOutlined } from '@ant-design/icons';
import { money } from '../lib/api';
import { applyXboard, getXboardReceipt, inspectXboard, MigrationRequestError, previewXboard } from '../lib/xboardMigration';
import type { LockingBalancePolicy, MigrationCounts, MigrationInspection, MigrationPlan, MigrationPreview, MigrationReceipt, XboardSource } from '../lib/xboardMigration';

const { Paragraph, Text } = Typography;
const infoStyle = { background: '#f5f7fa', borderColor: '#d9e2ed' };
const cardStyles = { body: { padding: 20 } };
const countLabels: Record<string, string> = {
  total: '源用户总数', importable: '可新增用户', skipped_privileged: '排除管理员／员工',
  skipped_existing: '跳过目标已存在邮箱', skipped_duplicate_email: '跳过源重复邮箱',
  skipped_locked_balance: '跳过待解锁佣金用户', skipped_plan: '按套餐选择跳过',
  invalid_email: '无效邮箱', unsupported_password: '不支持的密码', invalid_values: '无效用户数据',
  unmapped_plan: '缺少套餐映射', no_plan: '无套餐用户', regenerated_tokens: '重新生成订阅 Token',
  regenerated_uuids: '重新生成 UUID', locking_balance_users: '含待解锁佣金用户',
  locking_balance_total: '待解锁佣金合计',
};
const policyLabels: Record<LockingBalancePolicy, string> = {
  discard: '迁入用户，不迁移待解锁佣金',
  skip: '跳过含待解锁佣金的整个用户',
  to_balance: '待解锁佣金转入可消费余额',
};
const resetLabels: Record<number, string> = { 0: '每月 1 日', 1: '按月', 2: '不重置', 3: '每年 1 月 1 日', 4: '按年' };
const formatTime = (value: number) => new Date(value * 1000).toLocaleString('zh-CN', { hour12: false });

function Counts({ value }: { value: MigrationCounts }) {
  return <Descriptions size="small" bordered column={{ xs: 1, sm: 2, lg: 3 }}>
    {Object.entries(countLabels).filter(([key]) => value[key] !== undefined).map(([key, label]) =>
      <Descriptions.Item key={key} label={label}>{key === 'locking_balance_total' ? money(value[key]) : Number(value[key]).toLocaleString()}</Descriptions.Item>)}
  </Descriptions>;
}

function Warnings({ items }: { items?: string[] }) {
  return items?.length ? <Alert type="warning" showIcon message="迁移注意事项" description={<ul style={{ margin: 0, paddingLeft: 20 }}>{items.map((item, index) => <li key={index}>{item}</li>)}</ul>} /> : null;
}

function CredentialOptions({ value }: { value: Pick<MigrationPreview, 'regenerate_tokens' | 'regenerate_uuids'> }) {
  return <Descriptions size="small" bordered column={{ xs: 1, sm: 2 }}>
    <Descriptions.Item label="强制重生成订阅 Token">{value.regenerate_tokens ? '开' : '关'}</Descriptions.Item>
    <Descriptions.Item label="强制重生成 UUID">{value.regenerate_uuids ? '开' : '关'}</Descriptions.Item>
  </Descriptions>;
}

export default function XboardMigrationPage() {
  const [form] = Form.useForm<XboardSource>();
  const [inspection, setInspection] = useState<MigrationInspection>();
  const [mapping, setMapping] = useState<Record<string, number>>({});
  const [policy, setPolicy] = useState<LockingBalancePolicy>('discard');
  const [confirmLocked, setConfirmLocked] = useState(false);
  const [regenerateTokens, setRegenerateTokens] = useState(true);
  const [regenerateUUIDs, setRegenerateUUIDs] = useState(true);
  const [preview, setPreview] = useState<MigrationPreview>();
  const [receipt, setReceipt] = useState<MigrationReceipt>();
  const [pendingID, setPendingID] = useState('');
  const [recoveryID, setRecoveryID] = useState('');
  const [busy, setBusy] = useState<'' | 'inspect' | 'preview' | 'apply' | 'receipt'>('');
  const [error, setError] = useState('');
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [backedUp, setBackedUp] = useState(false);
  const [sourceStopped, setSourceStopped] = useState(false);
  const [confirmText, setConfirmText] = useState('');
  const [now, setNow] = useState(Date.now());
  const requestRunning = useRef(false);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => { mounted.current = false; window.clearInterval(timer); };
  }, []);
  useEffect(() => {
    if (!pendingID && busy !== 'apply') return;
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ''; };
    window.addEventListener('beforeunload', warn);
    return () => window.removeEventListener('beforeunload', warn);
  }, [pendingID, busy]);

  const expired = !!preview && preview.expires_at * 1000 <= now;
  const locked = !!busy || !!pendingID;
  // Preview re-reads the source; locked balances may appear after inspection.
  const lockingConfirmationRequired = policy !== 'skip';
  const clearPreview = () => { setPreview(undefined); setReceipt(undefined); setError(''); setConfirmOpen(false); };
  const sourceChanged = () => { setInspection(undefined); setMapping({}); setConfirmLocked(false); clearPreview(); };
  const begin = (operation: typeof busy) => {
    if (requestRunning.current) return false;
    requestRunning.current = true; setBusy(operation); setError(''); return true;
  };
  const finish = () => { requestRunning.current = false; if (mounted.current) setBusy(''); };
  const showError = (e: any) => {
    if (mounted.current && !e?.errorFields) setError(e?.message || '操作失败，请检查网络或后台服务');
  };
  const readSource = async (): Promise<XboardSource> => {
    const values = await form.validateFields();
    return { ...values, host: values.host.trim(), database: values.database.trim(), username: values.username.trim(), port: Number(values.port), tls: !!values.tls };
  };
  const inspect = async () => {
    if (!begin('inspect')) return;
    clearPreview(); setInspection(undefined); setMapping({}); setConfirmLocked(false);
    try { const data = await inspectXboard(await readSource()); if (mounted.current) setInspection(data); }
    catch (e) { showError(e); }
    finally { finish(); }
  };
  const makePreview = async () => {
    if (!begin('preview')) return;
    clearPreview();
    try {
      const data = await previewXboard({ source: await readSource(), plan_mapping: mapping, locking_balance_policy: policy, confirm_locking_balance: confirmLocked, regenerate_tokens: regenerateTokens, regenerate_uuids: regenerateUUIDs });
      if (mounted.current) { setPreview(data); setNow(Date.now()); }
    } catch (e) { showError(e); }
    finally { finish(); }
  };
  const receiveReceipt = (data: MigrationReceipt) => {
    if (data.status !== 'completed') throw new Error('导入尚未确认完成，请稍后查询回执；不要重复导入');
    if (!mounted.current) return;
    setReceipt(data); setPendingID(''); setRecoveryID(data.preview_id); setError('');
    form.setFieldValue('password', '');
    message.success(`已新增 ${data.imported.toLocaleString()} 个用户`);
  };
  const apply = async () => {
    if (!preview || expired || !preview.can_apply || !backedUp || !sourceStopped || confirmText !== 'IMPORT_USERS' || !begin('apply')) return;
    const id = preview.preview_id;
    setConfirmOpen(false); setPendingID(id); setRecoveryID(id);
    try { receiveReceipt(await applyXboard(id)); }
    catch (e) {
      // Only explicit pre-commit / rolled-back errors are safe to unlock.
      // A timeout, transport error, busy response or missing receipt is not proof of failure.
      if (e instanceof MigrationRequestError && ['invalid_parameter', 'preview_expired', 'preview_blocked', 'preview_stale', 'target_write'].includes(e.code) && mounted.current) {
        setPendingID(''); setPreview(undefined);
      }
      showError(e);
    }
    finally { finish(); }
  };
  const lookupReceipt = async () => {
    const id = (pendingID || recoveryID).trim();
    if (!id || !begin('receipt')) return;
    try { receiveReceipt(await getXboardReceipt(id)); }
    catch (e) { showError(e); }
    finally { finish(); }
  };
  const askConfirmation = () => {
    setBackedUp(false); setSourceStopped(false); setConfirmText(''); setConfirmOpen(true);
  };
  const missingMappings = inspection?.source_plans.filter((plan) => mapping[String(plan.id)] === undefined).length || 0;
  const planOptions = [
    { value: 0, label: '不迁入这个源套餐下的用户' },
    ...(inspection?.target_plans || []).map((plan) => ({ value: plan.id, label: `ID ${plan.id} · ${plan.name} · ${plan.transfer_enable} GB` })),
  ];

  return <div className="legacy-page">
    <div className="content-heading">用户迁移</div>
    <Space direction="vertical" size={16} style={{ width: '100%', boxSizing: 'border-box', padding: '0 clamp(8px, 2vw, 24px)' }}>
      <Alert type="info" style={infoStyle} showIcon message="Xboard → 当前 V2B：仅追加普通用户" description="重复邮箱不覆盖，源管理员和员工不迁入。不会导入订单、节点、支付配置、邀请码或邀请关系，也不会创建或修改套餐；请手动选择目标站已有套餐。" />
      <Card className="migration-card" styles={cardStyles}><Steps size="small" current={receipt ? 3 : preview ? 2 : inspection ? 1 : 0} items={[{ title: '读取数据库' }, { title: '映射套餐' }, { title: '预览与确认' }, { title: '迁移结果' }]} /></Card>
      {error && <Alert type="error" showIcon message="操作未完成" description={error} />}
      {pendingID && <Alert type="warning" showIcon message={busy === 'apply' ? '正在导入，请勿关闭、刷新或切换页面' : '提交结果尚未确认，请先查询回执'} description={<Space direction="vertical"><span>请保存下方迁移编号。网络中断不代表导入失败。请先查询同一编号的结果；未查到回执时也不要立即重新导入，应等待或由管理员核对。</span><Text copyable>{pendingID}</Text><Button icon={<ReloadOutlined aria-hidden />} loading={busy === 'receipt'} disabled={!!busy} onClick={lookupReceipt}>查询本次导入回执</Button></Space>} />}

      <Card className="migration-card" title="1. 读取源 Xboard 数据库" styles={cardStyles}>
        <Paragraph type="secondary">连接由 V2B 后端发起，地址必须能被 V2B 服务器访问。这里填写 MySQL 端口，不是 SSH 端口。建议使用只读数据库账号，并通过内网、TLS 或受控 SSH 隧道连接，不要将 MySQL 暴露给所有公网 IP。</Paragraph>
        <Form form={form} layout="vertical" initialValues={{ port: 3306, tls: false }} onValuesChange={sourceChanged} disabled={locked} autoComplete="off">
          <Row gutter={16}>
            <Col xs={24} md={16}><Form.Item name="host" label="MySQL 主机 / IP" rules={[{ required: true, whitespace: true, message: '请输入源数据库主机' }]}><Input autoComplete="off" placeholder="V2B 后端可访问的数据库地址" /></Form.Item></Col>
            <Col xs={24} md={8}><Form.Item name="port" label="MySQL 端口" rules={[{ required: true }]}><InputNumber min={1} max={65535} precision={0} style={{ width: '100%' }} /></Form.Item></Col>
            <Col xs={24} md={8}><Form.Item name="database" label="数据库名称" rules={[{ required: true, whitespace: true }]}><Input autoComplete="off" /></Form.Item></Col>
            <Col xs={24} md={8}><Form.Item name="username" label="数据库用户名" rules={[{ required: true, whitespace: true }]}><Input autoComplete="off" /></Form.Item></Col>
            <Col xs={24} md={8}><Form.Item name="password" label="数据库密码" rules={[{ required: true, message: '请输入数据库密码' }]}><Input.Password autoComplete="new-password" /></Form.Item></Col>
          </Row>
          <Form.Item name="tls" valuePropName="checked" label="启用 TLS 并验证数据库服务器证书"><Switch /></Form.Item>
          <Space wrap><Button type="primary" icon={<DatabaseOutlined aria-hidden />} onClick={inspect} loading={busy === 'inspect'} disabled={locked}>读取用户与套餐</Button><Text type="secondary">密码只保留在当前页面内存，不保存到浏览器或配置文件。</Text></Space>
        </Form>
      </Card>

      {inspection && <Card className="migration-card" title="2. 选择套餐对应关系" styles={cardStyles}>
        <Space direction="vertical" size={16} style={{ width: '100%' }}>
          <Descriptions size="small" bordered column={{ xs: 1, sm: 2, lg: 4 }}>
            <Descriptions.Item label="源用户">{inspection.total_users.toLocaleString()}</Descriptions.Item>
            <Descriptions.Item label="源管理员">{inspection.source_admins}</Descriptions.Item>
            <Descriptions.Item label="源员工">{inspection.source_staff}</Descriptions.Item>
            <Descriptions.Item label="待解锁佣金用户">{inspection.locking_balance_users} 人 / {money(inspection.locking_balance_total)}</Descriptions.Item>
          </Descriptions>
          <Paragraph type="secondary" style={{ marginBottom: 0 }}>输入套餐名称或 ID 搜索并选择目标套餐。源站无套餐用户按无套餐迁入；每个源套餐都需选择目标套餐或明确跳过。目标套餐对应的权限组、限速及设备限制将生效，具体流量和重置规则以预览说明为准。</Paragraph>
          <Table<MigrationPlan> rowKey="id" dataSource={inspection.source_plans} pagination={false} scroll={{ x: 780 }} columns={[
            { title: '源套餐', render: (_, plan) => <Space direction="vertical" size={0}><span>{plan.name}</span><Text type="secondary">ID {plan.id} · {plan.transfer_enable} GB</Text></Space> },
            { title: '源用户数', dataIndex: 'users', width: 110 },
            { title: '目标套餐（可按名称 / ID 搜索）', width: 360, render: (_, plan) => <Select style={{ width: '100%' }} showSearch allowClear optionFilterProp="label" options={planOptions} value={mapping[String(plan.id)]} disabled={locked} placeholder="请选择已有目标套餐或明确跳过" onChange={(id?: number) => { setMapping((previous) => { const next = { ...previous }; if (id === undefined) delete next[String(plan.id)]; else next[String(plan.id)] = id; return next; }); clearPreview(); }} /> },
            { title: '目标重置周期', width: 150, render: (_, plan) => { const target = inspection.target_plans.find((item) => item.id === mapping[String(plan.id)]); return target ? (target.reset_traffic_method == null ? '跟随目标站系统设置' : resetLabels[target.reset_traffic_method] || `模式 ${target.reset_traffic_method}`) : '—'; } },
          ]} />
          <Warnings items={inspection.warnings} />
          <div><Text strong>待解锁佣金处理</Text><Select style={{ width: '100%', marginTop: 8 }} disabled={locked} value={policy} options={Object.entries(policyLabels).map(([value, label]) => ({ value, label }))} onChange={(value) => { setPolicy(value); setConfirmLocked(false); clearPreview(); }} /></div>
          {policy === 'discard' && <Alert type="warning" showIcon message="用户正常迁入，待解锁佣金不迁移" description="只舍弃源站的待解锁佣金字段，不因此跳过用户。目标站不会保留这部分待解锁权益；源数据库不修改。" />}
          {policy === 'to_balance' && <Alert type="error" showIcon message="会将待解锁佣金变成可直接消费的余额" description="这会改变用户资金性质并增加目标站可消费余额。仅在明确完成资金核对后使用。" />}
          {policy === 'skip' && <Alert type="warning" showIcon message="含待解锁佣金的用户将整人跳过" description="这些用户的账号、套餐、普通余额和流量均不会在本次迁入。" />}
          {lockingConfirmationRequired && <Checkbox disabled={locked} checked={confirmLocked} onChange={(event) => { setConfirmLocked(event.target.checked); clearPreview(); }}>我已核对并确认：{policyLabels[policy]}。</Checkbox>}
          <div>
            <Paragraph strong>迁入用户的订阅凭据</Paragraph>
            <Space wrap size={[24, 12]}>
              <Space><Switch aria-label="强制重生成订阅 Token" checkedChildren="开" unCheckedChildren="关" checked={regenerateTokens} disabled={locked} onChange={(checked) => { setRegenerateTokens(checked); clearPreview(); }} /><Text>强制重生成订阅 Token</Text></Space>
              <Space><Switch aria-label="强制重生成 UUID" checkedChildren="开" unCheckedChildren="关" checked={regenerateUUIDs} disabled={locked} onChange={(checked) => { setRegenerateUUIDs(checked); clearPreview(); }} /><Text>强制重生成 UUID</Text></Space>
            </Space>
          </div>
          <Alert type={regenerateTokens || regenerateUUIDs ? 'warning' : 'info'} showIcon message={regenerateTokens || regenerateUUIDs ? '已启用的凭据将对本次所有新增用户重新生成' : '保留有效且不冲突的原订阅凭据'} description="两个开关独立生效，仅影响本次迁入的新增用户，不修改目标站已有邮箱的用户。开启任一开关后，用户需重新获取目标站订阅。关闭时，缺失、无效或冲突的凭据仍会自动重生成。只修改目标站，源站旧凭据仍然存在；登录密码不变，不能据此认定已彻底消除旧库泄露风险。" />
          <Space wrap><Button type="primary" loading={busy === 'preview'} disabled={locked || missingMappings > 0 || (lockingConfirmationRequired && !confirmLocked)} onClick={makePreview}>生成只读预览</Button>{missingMappings > 0 && <Text type="warning">还有 {missingMappings} 个源套餐未选择对应关系</Text>}<Text type="secondary">连接或选项修改后，旧预览立即失效。</Text></Space>
        </Space>
      </Card>}

      {preview && !receipt && <Card className="migration-card" title="3. 核对预览并确认导入" styles={cardStyles}>
        <Space direction="vertical" size={16} style={{ width: '100%' }}>
          <CredentialOptions value={preview} />
          <Counts value={preview.counts} />
          <Warnings items={preview.warnings} />
          {!!preview.blockers?.length && <Alert type="error" showIcon message="以下问题阻止导入" description={<ul style={{ margin: 0, paddingLeft: 20 }}>{preview.blockers.map((item, index) => <li key={index}>{item}</li>)}</ul>} />}
          <Alert type="info" style={infoStyle} showIcon message="不会自动接管旧站订阅域名" description="用户需要通过目标站登录并获取订阅；启用强制重生成的凭据、缺失、无效或冲突的 Token / UUID 会重新生成，实际数量见上方统计。预览之后源站的充值、用量或权益变化不会自动同步，请先安排冻结写入和备份。" />
          <Space wrap><Tag color={expired ? 'error' : 'blue'}>{expired ? '预览已过期，请重新生成' : `有效至 ${formatTime(preview.expires_at)}`}</Tag><Text type="secondary">迁移编号：</Text><Text copyable>{preview.preview_id}</Text></Space>
          <Button type="primary" danger disabled={locked || expired || !preview.can_apply || !preview.counts.importable} onClick={askConfirmation}>确认新增 {preview.counts.importable || 0} 个用户</Button>
        </Space>
      </Card>}

      {receipt && <Card className="migration-card" title="4. 迁移结果" styles={cardStyles}><Result status="success" title={`已新增 ${receipt.imported.toLocaleString()} 个用户`} subTitle={`完成时间：${formatTime(receipt.completed_at)}；只新增普通用户，不覆盖目标站已有用户。`} /><Space direction="vertical" style={{ width: '100%' }} size={16}><CredentialOptions value={receipt} /><Counts value={receipt.counts} /><Space><Text>迁移编号：</Text><Text copyable>{receipt.preview_id}</Text></Space><Alert type="info" style={infoStyle} message="数据库密码已从本页清除。建议抽查账号登录、套餐权限、已用流量和重置时间，再安排用户切换订阅。" showIcon />{(receipt.regenerate_tokens || receipt.regenerate_uuids) && <Alert type="warning" showIcon message="本次已强制重生成订阅凭据，请用户重新获取目标站订阅" description="只影响此次新增用户。源站旧凭据仍然存在，用户登录密码未修改。" />}</Space></Card>}

      <Card className="migration-card" title="查询已有迁移回执" size="small" styles={cardStyles}><Space direction="vertical" style={{ width: '100%' }}><Text type="secondary">页面意外关闭后，可使用此前保存的迁移编号查询结果；仅当前管理员创建的迁移可查询，不需要源数据库密码。</Text><Space.Compact style={{ width: '100%' }}><Input value={pendingID || recoveryID} disabled={!!busy || !!pendingID} placeholder="迁移编号（preview_id）" onChange={(event) => setRecoveryID(event.target.value)} /><Button disabled={!!busy || !(pendingID || recoveryID).trim()} loading={busy === 'receipt'} onClick={lookupReceipt}>查询回执</Button></Space.Compact></Space></Card>
    </Space>

    <Modal title="最终确认：新增普通用户" open={confirmOpen} onCancel={() => setConfirmOpen(false)} onOk={apply} okText="执行迁移" okButtonProps={{ danger: true, disabled: expired || !backedUp || !sourceStopped || confirmText !== 'IMPORT_USERS' }} confirmLoading={busy === 'apply'}>
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <Alert type="warning" showIcon message={`将向当前 V2B 新增 ${preview?.counts.importable || 0} 个用户`} description="迁移不是双向同步。源数据库只读，但目标数据库会新增用户；提交后不要反复点击或在不确定结果时重新导入。" />
        {preview && <CredentialOptions value={preview} />}
        {(preview?.regenerate_tokens || preview?.regenerate_uuids) && <Alert type="warning" showIcon message="将强制重生成所选订阅凭据，用户需重新获取目标站订阅" description="仅影响本次新增用户，不修改源站旧凭据及登录密码。" />}
        <Checkbox checked={backedUp} onChange={(event) => setBackedUp(event.target.checked)}>我已完成源站和目标站数据库备份，并确认可恢复。</Checkbox>
        <Checkbox checked={sourceStopped} onChange={(event) => setSourceStopped(event.target.checked)}>我已暂停源站相关写入并重新生成当前预览，或已核对预览后的变化，接受未同步差异风险。</Checkbox>
        <div><Paragraph>请输入 <Text code>IMPORT_USERS</Text> 确认此次导入：</Paragraph><Input autoComplete="off" value={confirmText} onChange={(event) => setConfirmText(event.target.value)} /></div>
        {expired && <Text type="danger">预览已过期，请关闭并重新生成。</Text>}
      </Space>
    </Modal>
  </div>;
}
