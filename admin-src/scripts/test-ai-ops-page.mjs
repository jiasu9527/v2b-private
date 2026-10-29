import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';
const source = await readFile(new URL('../src/pages/aiOpsModel.ts', import.meta.url), 'utf8');
const page = await readFile(new URL('../src/pages/AIOpsPage.tsx', import.meta.url), 'utf8');
const app = await readFile(new URL('../src/App.tsx', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } });
const { AI_DEFAULT_MODELS, AI_DEFAULT_ACCESS_MODE, AI_REASONING_OPTIONS, aiReasoningEffort, aiSessionReasoning, aiReasoningDefaults, aiReasoningDefaultLabel, aiActionStatus, aiCanApprove, aiCanReadSession, aiSessionBusy, aiLatestTurn, aiParseModels, aiFingerprintMatches, aiRequestID, aiAccessMode, aiOwnsFullAccess, aiMergeSessionAccess } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);
const now = 1900000000000;
const action = { status: 'pending', expires_at: now / 1000 + 60 };

test('approval requires a pending unexpired action and enabled AI; result states stay immutable', () => {
  assert.equal(aiCanApprove(action, true, now), true);
  assert.equal(aiCanApprove(action, false, now), false);
  assert.equal(aiCanApprove(action, true, now + 60000), false);
  assert.equal(aiActionStatus(action, now + 60000), 'expired');
  for (const status of ['running', 'succeeded', 'failed', 'unknown', 'rejected', 'expired']) {
    assert.equal(aiCanApprove({ ...action, status }, true, now), false);
    assert.equal(aiActionStatus({ ...action, status }, now + 60000), status);
  }
});

test('sending stays blocked while any session turn or action is running', () => {
  assert.equal(aiSessionBusy(null), false);
  const detail = { session: { status: 'idle' }, turns: [], actions: [] };
  assert.equal(aiSessionBusy(detail), false);
  assert.equal(aiSessionBusy({ ...detail, session: { status: 'running' } }), true);
  assert.equal(aiSessionBusy({ ...detail, turns: [{ status: 'running' }] }), true);
  assert.equal(aiSessionBusy({ ...detail, actions: [{ status: 'running' }] }), true);
  assert.equal(aiSessionBusy({ ...detail, actions: [{ status: 'pending' }] }), false);
});

test('four requested IDs are preserved and model list editing trims and deduplicates only', () => {
  assert.deepEqual(AI_DEFAULT_MODELS, ['gpt-6.1-sol', 'claude-fable-5-1', 'deepseek-v4.1-flash', 'glm-5.3']);
  assert.deepEqual(aiParseModels(' gpt-6.1-sol \n\nCustom-Model\r\nCustom-Model\ncustom-model'), ['gpt-6.1-sol', 'Custom-Model', 'custom-model']);
});

test('reasoning preserves each relay option without assuming model capability and legacy values default safely', () => {
  assert.deepEqual(AI_REASONING_OPTIONS.map((item) => item.value), ['auto', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh']);
  for (const {value} of AI_REASONING_OPTIONS) {
    assert.equal(aiReasoningEffort(value), value);
    assert.equal(aiSessionReasoning({reasoning_effort:value}), value);
  }
  for (const invalid of [undefined, null, '', 'ultra', 'default']) assert.equal(aiReasoningEffort(invalid), 'auto');
  for (const session of [undefined, {}, {reasoning_effort:'default'}, {reasoning_effort:'ultra'}]) assert.equal(aiSessionReasoning(session), 'default');
});

test('per-model reasoning defaults track active model IDs and distinguish inherited defaults from explicit auto', () => {
  const original = { 'gpt-6.1-sol': 'high', 'claude-fable-5-1': 'auto', removed: 'xhigh' };
  assert.deepEqual(aiReasoningDefaults(['gpt-6.1-sol', 'claude-fable-5-1', 'new-model'], original), {'gpt-6.1-sol':'high','claude-fable-5-1':'auto','new-model':'auto'});
  assert.equal(original.removed, 'xhigh');
  assert.deepEqual(aiReasoningDefaults(['missing'], {}), {missing:'auto'});
  assert.equal(aiReasoningDefaults(['__proto__'], JSON.parse('{"__proto__":"low"}')).__proto__, 'low');
  assert.equal(aiReasoningDefaultLabel({reasoning_efforts:original}, 'gpt-6.1-sol'), '跟随设置（高）');
  assert.equal(aiReasoningDefaultLabel({reasoning_efforts:original}, 'claude-fable-5-1'), '跟随设置（中转默认）');
  assert.equal(aiReasoningDefaultLabel({}, 'new-model'), '跟随设置（中转默认）');
  assert.match(page, /aiParseModels\(configuredModelsText \?\? config.models.join/);
  assert.match(page, /reasoning_efforts: aiReasoningDefaults\(models, values.reasoning_efforts\)/);
});

test('reasoning choices restore per session, survive incoming snapshots while edited and participate in retry identity', () => {
  assert.match(page, /if \(!reasoningEdited.current\) setReasoningEffort\(aiSessionReasoning\(value.session\)\)/);
  const select = page.slice(page.indexOf('const selectSession ='), page.indexOf('const accessMode ='));
  assert.match(select, /setReasoningEffort\(aiSessionReasoning\(session\)\)/);
  assert.match(select, /reasoningEdited.current = false; setReasoningEffort\('default'\)/);
  assert.match(page, /sendRetry.reasoningEffort === reasoningChoice/);
  assert.match(page, /reasoning_effort: retry.reasoningEffort/);
  assert.match(page, /aria-label="推理强度"[\s\S]*reasoningEdited.current = true; setReasoningEffort\(value\)/);
  assert.match(page, /value: 'default', label: aiReasoningDefaultLabel\(config, model\)/);
});

test('staff have no reasoning selector and retry requests always use inherited defaults, including legacy explicit sessions', () => {
  assert.match(page, /const reasoningChoice: AIReasoningChoice = isAdmin \? reasoningEffort : 'default';/);
  assert.match(page, /\{isAdmin && <Select<AIReasoningChoice> className="ai-ops-reasoning-select"/);
  const send = page.slice(page.indexOf('const send ='), page.indexOf('const saveConfig'));
  assert.match(send, /sendRetry.reasoningEffort === reasoningChoice/);
  assert.match(send, /reasoningEffort: reasoningChoice/);
  assert.match(send, /reasoning_effort: retry.reasoningEffort/);
  assert.doesNotMatch(send, /reasoning_effort: reasoningEffort\b|reasoning_effort: detail/);
});

test('observed fingerprint is scoped to exact current host and port, never auto-trusted', () => {
  const observed = { host: 'node.example.test', port: 2222, fingerprint: 'SHA256:observed' };
  assert.equal(aiFingerprintMatches(observed, ' node.example.test ', 2222), true);
  assert.equal(aiFingerprintMatches(observed, 'another.example.test', 2222), false);
  assert.equal(aiFingerprintMatches(observed, 'node.example.test', 22), false);
  assert.equal(aiFingerprintMatches(null, 'node.example.test', 2222), false);
  const probe = page.slice(page.indexOf('const probeFingerprint'), page.indexOf('const saveServer'));
  assert.doesNotMatch(probe, /setFieldsValue|setFieldValue/);
  assert.match(probe, /aiFingerprintMatches/);
  assert.match(page, /获取主机指纹（仅观察）/);
});

test('administrators and staff can reach AI operations while ordinary users cannot enter the backend', () => {
  assert.match(app, /key: '\/config\/ai-ops'.*label: 'AI 运维'/);
  assert.match(app, /route === '\/config\/ai-ops'\) return <AIOpsPage/);
  assert.match(app, /const visibleMenu = supportOnly \? \[.*员工工作台.*key: '\/config\/ai-ops'.*\] : menu/);
  assert.match(app, /supportOnly \? staffAIOpsPage \? <AIOpsPage key=\{path\} \/> : <SupportWorkbenchPage/);
  assert.match(app, /if \(!identity\.is_admin && !identity\.is_staff\) \{ rejectIdentity\(\); return; \}/);
  assert.doesNotMatch(page, /localStorage|sessionStorage|console\.|dangerouslySetInnerHTML|eval\(|MCP/);
  assert.doesNotMatch(app, /MCPPage|config\/mcp/);
});

test('staff only see their own conversations and all conversation deletion and global settings remain administrator-only', () => {
  const own = { id: 1, created_by: 7 };
  assert.equal(aiCanReadSession(own, 7, false), true);
  for (const actorID of [0, 8, NaN]) assert.equal(aiCanReadSession(own, actorID, false), false);
  assert.equal(aiCanReadSession(own, 8, true), true);
  assert.match(page, /filter\(\(session\) => aiCanReadSession\(session, actorID, isAdmin\)\)/);
  assert.match(page, /if \(!aiCanReadSession\(value.session, actorID, isAdmin\)\) throw/);
  assert.match(page, /const settings = isAdmin \?/);
  assert.match(page, /\.\.\.\(isAdmin \? \[\{ key: 'settings'/);
  assert.match(page, /if \(!isAdmin \|\| !begin\('config'\)\) return/);
  assert.match(page, /\(confirmation.kind === 'delete' \|\| confirmation.kind === 'clear' \|\| confirmation.kind === 'reset'\) && !isAdmin/);
  assert.match(page, /\{isAdmin && <Button type="text" danger aria-label=\{`删除会话 \$\{session.id\}`\}/);
  const remove = page.slice(page.indexOf('const remove ='), page.indexOf('const checkServer ='));
  assert.doesNotMatch(remove, /aiCanReadSession/);
  assert.match(remove, /if \(confirmation.kind === 'server-delete'\) await apiJsonPost\('\/ai-ops\/server\/delete'/);
  const accessOwner = page.slice(page.indexOf('{foreignFullAccess &&'), page.indexOf('<div className="ai-ops-messages"'));
  assert.match(accessOwner, /onClick=\{\(\) => void changeAccess\('full'\)\}>由我启用完全访问/);
  assert.doesNotMatch(accessOwner, /isAdmin/);
});

test('explicit server form credentials clear on save/close and chat sends only its content envelope', () => {
  const send = page.slice(page.indexOf('const send ='), page.indexOf('const saveConfig'));
  assert.doesNotMatch(send, /password|private_key|api_key:/);
  assert.match(page, /configForm\.setFieldsValue\(\{ api_key: '', clear_api_key: false \}\)/);
  assert.match(page, /setServerOpen\(false\); setFingerprint\(null\); serverForm\.resetFields\(\)/);
  assert.match(page, /setServerOpen\(false\); serverForm\.resetFields\(\)/);
  assert.match(page, /api_key: values\.api_key \|\| ''/);
});

test('standard mode retains manual one-action approval with complete command and output disclosure', () => {
  assert.match(page, /<pre className="ai-ops-pre ai-ops-command">\{command\}<\/pre>/);
  assert.match(page, /逐次确认模式下，每条服务器命令和部署操作均须批准/);
  assert.match(page, /输出会进入对话并发往中转模型/);
  assert.match(page, /setReview\(\{ action, decision: 'approve' \}\)/);
  assert.match(page, /!confirmedOutput \|\| !aiCanApprove/);
  assert.match(page, /apiJsonPost\(`\/ai-ops\/action\/\$\{review\.decision\}`/);
  assert.match(page, /session_id: review\.action\.session_id, id: review\.action\.id/);
});

test('destructive actions require explicit confirmation and reset distinguishes all three data categories', () => {
  assert.match(page, /resetText !== 'RESET_AI_DATA'/);
  assert.match(page, /apiJsonPost\('\/ai-ops\/reset', \{ confirm: true, confirm_text: 'RESET_AI_DATA' \}\)/);
  assert.match(page, /apiJsonPost\('\/ai-ops\/session\/clear', \{ confirm: true \}\)/);
  assert.match(page, /包括中转配置及 API Key、全部服务器 SSH 凭据、全部操作人的会话与操作记录/);
  assert.match(page, /已在远程启动的命令不保证停止或回滚/);
});

test('async send uses stable retry request IDs and does not replace a subsequently selected conversation', () => {
  assert.notEqual(aiRequestID(), aiRequestID());
  assert.match(page, /sendRetry\.sessionID === originalID && sendRetry\.model === model && sendRetry\.content === content/);
  assert.match(page, /request_id: retry\.requestID/);
  assert.match(page, /selectionSequence\.current === selection/);
  assert.match(page, /requestSequence|detailSequence/);
  assert.match(page, /cache: 'no-store'/);
});

test('chat preserves raw SSH password whitespace for sending and retry identity while rejecting blank input', () => {
  const send = page.slice(page.indexOf('const send ='), page.indexOf('const saveConfig'));
  assert.match(send, /const content = contentOverride \?\? draft;/);
  assert.match(send, /if \(!content\.trim\(\) \|\|/);
  assert.match(send, /sendRetry\.content === content/);
  assert.match(send, /model, content, request_id: retry\.requestID/);
  assert.doesNotMatch(send, /content:\s*content\.trim\(\)|const content =[^;]*\.trim\(\)/);
});

test('latest turn selection is stable for descending API order and shows the newest failure', () => {
  const old = { id: 1, status: 'completed', error: '' };
  const recent = { id: 3, status: 'failed', error: 'new failure' };
  assert.equal(aiLatestTurn([recent, old]), recent);
  assert.equal(aiLatestTurn([old, recent]), recent);
  assert.equal(aiLatestTurn([]), undefined);
  assert.match(page, /aiLatestTurn\(detail\?\.turns \|\| \[\]\)/);
});

test('action cards expose optional approver/rejector IDs without showing zero or undefined IDs', () => {
  for (const field of ['approved_by', 'rejected_by']) {
    assert.ok(source.includes(`${field}?: number`));
    assert.ok(page.includes(`(action.${field} ?? 0) > 0`));
    assert.ok(page.includes(`操作人 #{action.${field}}`));
  }
});

test('an unconfigured disabled provider can be saved and enabling it requires its Base URL', () => {
  const baseURLField = page.split('<Form.Item name="base_url"')[1].split('</Form.Item>')[0];
  assert.ok(baseURLField.includes("dependencies={['enabled']}"));
  assert.ok(baseURLField.includes("required: !!getFieldValue('enabled')"));
  assert.ok(baseURLField.includes("whitespace: !!getFieldValue('enabled')"));
});

const extra = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);
test('Enter sends only once; Shift+Enter, IME composition and held keys never send', () => {
  assert.equal(extra.aiShouldSendKey({key:'Enter'}),true);
  for (const option of ['shiftKey','ctrlKey','altKey','metaKey','repeat','isComposing']) assert.equal(extra.aiShouldSendKey({key:'Enter',[option]:true}),false);
  assert.equal(extra.aiShouldSendKey({key:'Enter',keyCode:229}),false);
  assert.equal(extra.aiShouldSendKey({key:'Enter'},true),false);
  assert.equal(extra.aiShouldSendKey({key:'a'}),false);
  assert.match(page,/event\.preventDefault\(\); void send\(\)/);
  assert.match(page,/onCompositionStart/); assert.match(page,/onCompositionEnd/);
});

test('each conversation turn has one collapsed activity group and approval still shows the full command', () => {
  assert.match(page,/conversationTurns\.map/);
  assert.equal((page.match(/<details\b/g) || []).length,1);
  assert.match(page,/<details className="ai-ops-process" key="process">/);
  assert.doesNotMatch(page,/<Collapse|defaultActiveKey|activeKey=\{\[|<details[^>]*\bopen[=>\s]/);
  assert.match(page,/\(item\.role === 'user' \|\| item\.role === 'assistant'\) && item\.content\.trim\(\)/);
  assert.match(page,/className="ai-ops-process-review"/);
  const modal=page.slice(page.indexOf('<Modal title={review?.decision'));
  assert.match(modal,/<ActionDetails action=\{review.action\}/);
});

test('draft snapshots are monotonic, session-scoped, and never duplicate final assistant messages', () => {
  const base={session:{id:1,status:'running'},messages:[],actions:[],turns:[{id:10,status:'running',draft_round:1,draft_content:'你好'}]};
  const newer={session_id:1,turn_id:10,round:1,content:'你好世界'};
  const next=extra.aiApplyDraft(base,newer);
  assert.equal(next.turns[0].draft_content,'你好世界');
  assert.equal(extra.aiApplyDraft(next,{...newer,content:'你'}),next);
  assert.equal(extra.aiApplyDraft(next,{...newer,session_id:2}),next);
  assert.equal(extra.aiApplyDraft(next,{...newer,round:0}),next);
  assert.equal(extra.aiVisibleDraft(next).draft_content,'你好世界');
  assert.equal(extra.aiVisibleDraft({...next,messages:[{turn_id:10,role:'assistant',content:'你好世界'}]}),undefined);
  assert.equal(extra.aiVisibleDraft({...next,turns:[{...next.turns[0],status:'completed'}]}),undefined);
  assert.equal(extra.aiApplyDraft(next,{...newer,round:2,content:''}).turns[0].draft_content,'');
  assert.equal(extra.aiApplyDraft(next,{...newer,status:'completed',content:''}).turns[0].draft_content,'');
  assert.equal(extra.aiMergeSnapshot(next,base).turns[0].draft_content,'你好世界');
  assert.equal(extra.aiMergeSnapshot(next,{...base,turns:[{id:10,status:'completed',draft_content:''}]}).turns[0].draft_content,'');
});

test('streaming follows only a reader at the bottom; idle sessions use polling fallback', () => {
  assert.equal(extra.aiNearBottom(0,500,2000),false);
  assert.equal(extra.aiNearBottom(1450,500,2000),true);
  assert.match(page,/pane && followOutput.current/);
  assert.match(page,/onScroll=.*aiNearBottom/);
  assert.match(page,/const streamWanted = tab === 'chat'.*aiSessionBusy/);
  assert.match(page,/if \(!streamWanted\) return/);
  assert.match(page,/streamActive.current \|\| pollRunning/);
});

test('draft versions preserve explicit clears and reject stale snapshots from another connection',()=>{
 const base={session:{id:1,status:'running'},messages:[],actions:[],turns:[{id:10,status:'running',draft_round:1,draft_content:'长文本',draft_version:5}]};
 const cleared=extra.aiApplyDraft(base,{session_id:1,turn_id:10,round:1,content:'',version:6});
 assert.equal(cleared.turns[0].draft_content,'');
 assert.equal(extra.aiApplyDraft(cleared,{session_id:1,turn_id:10,round:1,content:'长文本',version:5}),cleared);
 assert.equal(extra.aiMergeSnapshot(cleared,base).turns[0].draft_content,'');
 const completed={...base,turns:[{id:10,status:'completed',draft_version:7,draft_content:''}]};
 assert.equal(extra.aiMergeSnapshot(base,completed).turns[0].status,'completed');
});


test('full access belongs to one session and one administrator, never inherited from missing or foreign identity', () => {
  const full = { id: 4, access_mode: 'full', full_access_by: 7, access_version: 3 };
  assert.equal(aiAccessMode(undefined), 'standard');
  assert.equal(aiAccessMode({ access_mode: 'unexpected' }), 'standard');
  assert.equal(aiOwnsFullAccess(full, 7), true);
  for (const actor of [0, 6, NaN]) assert.equal(aiOwnsFullAccess(full, actor), false);
  assert.equal(aiOwnsFullAccess({ ...full, access_mode: 'standard' }, 7), false);
  assert.equal(aiOwnsFullAccess({ ...full, full_access_by: undefined }, 7), false);
  assert.match(page, /由我启用完全访问/);
  assert.match(page, /foreignFullAccess \|\| !begin\('send'\)/);
});

test('revoked access survives stale stream and list snapshots without overriding other session data', () => {
  const revoked = { id: 4, access_mode: 'standard', full_access_by: 0, access_version: 4, status: 'running' };
  const stale = { id: 4, access_mode: 'full', full_access_by: 7, access_version: 3, status: 'idle' };
  assert.deepEqual(aiMergeSessionAccess(revoked, stale), { ...stale, access_mode: 'standard', full_access_by: 0, access_version: 4 });
  assert.equal(aiMergeSessionAccess(revoked, { ...stale, id: 5 }).access_mode, 'full');
  const fresh = { ...stale, access_version: 5 };
  assert.equal(aiMergeSessionAccess(revoked, fresh), fresh);
  const merged = extra.aiMergeSnapshot({ session: revoked, messages: [], turns: [], actions: [] }, { session: stale, messages: [], turns: [], actions: [] });
  assert.equal(merged.session.access_mode, 'standard');
  assert.equal(merged.session.access_version, 4);
  assert.equal(merged.session.status, 'idle');
});

test('new chats default to full access while existing standard mode and exact retry grants are preserved', () => {
  assert.equal(AI_DEFAULT_ACCESS_MODE, 'full');
  assert.equal(aiAccessMode({ access_mode: 'standard' }), 'standard');
  assert.match(page, /useState<AIAccessMode>\(AI_DEFAULT_ACCESS_MODE\)/);
  assert.match(page, /apiJsonPost\('\/ai-ops\/session\/access', \{ id, mode, expected_version: detail.session.access_version \|\| 0 \}\)/);
  assert.match(page, /session.access_mode !== mode/);
  assert.match(page, /sendRetry.accessMode === accessMode && sendRetry.accessVersion === accessVersion/);
  assert.match(page, /access_mode: retry.accessMode, access_version: retry.accessVersion/);
  const newSession = page.slice(page.indexOf('const newSession ='), page.indexOf('const accessMode ='));
  assert.match(newSession, /setNewAccessMode\(AI_DEFAULT_ACCESS_MODE\)/);
  assert.match(page, /const accessMode = selectedID \? aiAccessMode\(detail\?\.session\) : newAccessMode/);
  assert.doesNotMatch(page, /localStorage|sessionStorage/);
});

test('running work exposes stop and permits revocation, while automatic actions identify the grant instead of manual approval', () => {
  assert.match(page, /apiJsonPost\('\/ai-ops\/chat\/stop', \{ id \}\)/);
  assert.match(page, /aiSessionBusy\(detail\) && <Button danger icon=\{<StopOutlined/);
  const select = page.split('aria-label="AI 访问权限"')[1].split('</Space>')[0];
  assert.match(select, /disabled=\{!!busy \|\| \(selectedID > 0 && !detail\)\}/);
  assert.match(select, /value: 'standard', label: '逐次确认'/);
  assert.match(page, /action.approval_mode === 'full_access' \? '完全访问授权：' : '人工批准：'/);
});

test('tool result labels use their preceding batch within the same turn when providers reuse call IDs', () => {
  const assistant = (turn, name, id = 'reused') => ({ session_id: 1, turn_id: turn, role: 'assistant', tool_calls: [{ id, function: { name, arguments: '{}' } }] });
  const result = (turn, id = 'reused') => ({ session_id: 1, turn_id: turn, role: 'tool', tool_call_id: id });
  const messages = [assistant(1, 'old_turn'), result(1), assistant(2, 'entry_health'), result(2), assistant(2, 'nodes_list'), result(2), assistant(2, 'future_tool')];
  assert.equal(extra.aiToolResultName(messages, 1), 'old_turn');
  assert.equal(extra.aiToolResultName(messages, 3), 'entry_health');
  assert.equal(extra.aiToolResultName(messages, 5), 'nodes_list');
  assert.equal(extra.aiToolResultName(messages, 4), '');
  assert.equal(extra.aiToolResultName(messages, 99), '');
  assert.equal(extra.aiToolResultName([assistant(1, 'foreign_turn'), result(2)], 1), '');
  assert.equal(extra.aiToolResultName([{ ...assistant(2, 'foreign_session'), session_id: 2 }, result(2)], 1), '');
  assert.equal(extra.aiToolResultName([assistant(2, 'old_batch'), assistant(2, 'new_batch', 'new-id'), result(2)], 2), '');
});

test('conversation activity groups all tools and actions by turn, including in-flight actions and truncated history', () => {
  const messages = [
    { id: 4, session_id: 1, turn_id: 12, role: 'tool', tool_call_id: 'lookup', content: 'output' },
    { id: 2, session_id: 1, turn_id: 11, role: 'assistant', tool_calls: [{ id: 'legacy-call', function: { name: 'ssh_status', arguments: '{}' } }] },
    { id: 1, session_id: 1, turn_id: 11, role: 'user', content: 'check port' },
    { id: 3, session_id: 1, turn_id: 12, role: 'assistant', tool_calls: [{ id: 'lookup', function: { name: 'nodes_list', arguments: '{}' } }] },
    { id: 5, session_id: 2, turn_id: 90, role: 'user', content: 'other session' },
  ];
  const actions = [
    { id: 6, session_id: 1, tool_call_id: 'legacy-call' },
    { id: 7, session_id: 1, tool_call_id: 'turn:12:round:1:lookup' },
    { id: 8, session_id: 1, tool_call_id: 'turn:13:round:1:deploy', authorization_turn_id: 13 },
    { id: 9, session_id: 1, tool_call_id: 'turn:10:old-call' },
    { id: 10, session_id: 2, tool_call_id: 'turn:90:foreign' },
  ];
  const detail = { session: { id: 1 }, messages, actions, turns: [{ id: 13, status: 'running', draft_content: 'checking' }, { id: 12, status: 'completed' }] };
  const original = JSON.stringify(detail);
  const groups = extra.aiConversationTurns(detail);
  assert.deepEqual(groups.map((group) => group.turnID), [10, 11, 12, 13]);
  assert.deepEqual(groups.map((group) => group.actions.map((action) => action.id)), [[9], [6], [7], [8]]);
  assert.deepEqual(groups[1].messages.map((message) => message.id), [1, 2]);
  assert.equal(groups[3].turn.draft_content, 'checking');
  assert.equal(JSON.stringify(detail), original);
  assert.deepEqual(extra.aiConversationTurns(null), []);
});

test('ambiguous legacy execution records remain visible without being assigned to the wrong reply', () => {
  const call = (turn) => ({ id: turn, session_id: 1, turn_id: turn, role: 'assistant', tool_calls: [{ id: 'reused', function: { name: 'ssh_status', arguments: '{}' } }] });
  const detail = { session: { id: 1 }, messages: [call(2), call(3)], turns: [{ id: 4, status: 'running' }], actions: [{ id: 5, session_id: 1, tool_call_id: 'reused' }, { id: 6, session_id: 1, tool_call_id: 'missing-history' }] };
  const groups = extra.aiConversationTurns(detail);
  assert.deepEqual(groups.map((group) => group.turnID), [0, 2, 3, 4]);
  assert.deepEqual(groups[0].actions.map((action) => action.id), [5, 6]);
  assert.equal(groups[1].actions.length, 0);
  assert.deepEqual(groups.map((group) => group.key), ['1:0', '1:2', '1:3', '1:4']);
  const truncated = extra.aiConversationTurns({ ...detail, messages: [call(3)], messages_truncated: true });
  assert.deepEqual(truncated[0].actions.map((action) => action.id), [5, 6], 'a reused ID in the visible window cannot identify a truncated old turn');
  assert.equal(truncated.find((group) => group.turnID === 3).actions.length, 0);
});
