let session = localStorage.session || crypto.randomUUID();
localStorage.session = session;
let thread = null, cursor = 0n, generation = 0, timer = null, polling = false, submitting = false;
let followConversation = true, activityPhase = '';
let eventSource = null, taskRows = [], lastRefresh = 0, fileRequest = 0, taskRequest = 0;
const messagesByKey = new Map(), toolsByKey = new Map(), promptsByKey = new Map();
const roundsByRunID = new Map(), changedFilesByPath = new Map();
const endedRuns = new Set(), inputModesByID = new Map();
const $ = id => document.getElementById(id);
const panelNames = ['Talk', 'Plan', 'Output', 'File', 'Tasks'];
const statusLabels = {idle: '就绪', ready: '等待执行', running: '执行中', blocked: '等待你的回复', closing: '关闭中', closed: '已关闭'};
function report(error, source = 'action') {
  $('status').dataset.errorSource = error ? source : '';
  const disconnected = source === 'poll' && error?.name === 'TypeError';
  if (disconnected) setLabel($('status'), '本地服务暂时无法连接，正在重试…');
  else { delete $('status').dataset.i18n; $('status').textContent = error ? error.message : ''; }
  $('status').hidden = !error;
}
function save(key, value) {
  try { localStorage[key] = value; } catch { /* Storage may be full or disabled; keep the current draft in the editor. */ }
}
function draftKey(id) { return 'draft:' + session + ':' + (id || 'new'); }
function updateComposer() {
  const closed = ['closing', 'closed'].includes($('runStatus').dataset.status);
  document.querySelector('.composer').hidden = closed;
  document.querySelector('.composer-note').hidden = closed;
  $('input').readOnly = closed;
  $('send').disabled = closed || submitting || !$('input').value.trim();
  $('input').style.height = 'auto';
  $('input').style.height = Math.min($('input').scrollHeight || 54, 160) + 'px';
}
async function api(url, options) {
  const response = await fetch(url, {headers: {'content-type': 'application/json'}, ...options});
  const data = await response.json();
  if (!response.ok) {
    const error = new Error(data.error || t('请求失败 ({status})', {status: response.status}));
    error.status = response.status;
    throw error;
  }
  return data;
}
function getTaskTitle(row) { return row.untitled || !row.title ? t('未命名任务') : row.title; }
function showTasks() {
  const query = $('search').value.trim().toLowerCase();
  const tasksByID = new Map(taskRows.map(row => [row.id, row]));
  const childrenByParent = new Map(), visibleIDs = new Set(), renderedIDs = new Set();
  for (const row of taskRows) {
    const children = childrenByParent.get(row.parent_thread_id) || [];
    children.push(row); childrenByParent.set(row.parent_thread_id, children);
    if (!getTaskTitle(row).toLowerCase().includes(query)) continue;
    let ancestor = row;
    while (ancestor && !visibleIDs.has(ancestor.id)) {
      visibleIDs.add(ancestor.id); ancestor = tasksByID.get(ancestor.parent_thread_id);
    }
  }
  $('threads').replaceChildren();
  const appendTask = (row, depth = 0) => {
    if (!visibleIDs.has(row.id) || renderedIDs.has(row.id)) return;
    renderedIDs.add(row.id);
    const child = !!row.parent_thread_id, status = row.status || 'idle';
    const button = document.createElement('button');
    button.className = 'thread' + (child ? ' child' : '') + (row.id === thread ? ' active' : '');
    button.dataset.id = row.id; button.dataset.depth = String(depth); button.dataset.status = status;
    button.style.paddingLeft = 12 + Math.min(depth, 6) * 18 + 'px';
    button.title = getTaskTitle(row);
    button.setAttribute('aria-current', row.id === thread ? 'true' : 'false');
    const line = document.createElement('div'); line.className = 'task-line';
    const dot = document.createElement('span'); dot.className = 'task-dot'; dot.setAttribute('aria-hidden', 'true');
    const name = document.createElement('span'); name.className = 'task-name'; name.textContent = getTaskTitle(row);
    const type = document.createElement('span'); type.className = 'task-type'; setLabel(type, child ? '子任务' : '主任务');
    line.append(dot, name, type);
    const state = document.createElement('small'); state.className = 'task-status';
    state.textContent = t(statusLabels[status] || status) + (['closing', 'closed'].includes(status) ? ' · ' + t('仅供查看') : '');
    button.append(line, state); button.onclick = () => select(row.id).catch(report);
    $('threads').append(button);
    (childrenByParent.get(row.id) || []).forEach(child => appendTask(child, depth + 1));
  };
  taskRows.filter(row => !tasksByID.has(row.parent_thread_id)).forEach(row => appendTask(row));
  // Keep orphaned or cyclic metadata inspectable without duplicating any task.
  taskRows.forEach(row => appendTask(row));
}
async function list() {
  const request = ++taskRequest;
  const rows = await api('/api/threads?session_id=' + encodeURIComponent(session));
  if (request !== taskRequest) return;
  taskRows = rows; showTasks();
}
function showThread(row) {
  if (row.untitled || !row.title) setLabel($('title'), row.id ? '未命名任务' : '新任务');
  else { delete $('title').dataset.i18n; $('title').textContent = row.title; }
  if (row.work_dir) { delete $('workspace').dataset.i18n; $('workspace').textContent = row.work_dir; }
  else setLabel($('workspace'), '从一个问题或任务开始');
  $('workspace').title = row.work_dir || '';
  const status = row.status || 'idle';
  $('taskContext').hidden = !row.parent_thread_id;
  $('parentTask').dataset.id = row.parent_thread_id || '';
  setLabel($('taskContextLabel'), status === 'closed' ? '子任务已关闭，仅可查看记录' : status === 'closing' ? '子任务正在关闭' : '子任务');
  setRunStatus(status, row.run_id || '', row.run_status);
  $('mode').disabled = ['ready', 'running', 'blocked', 'closing', 'closed'].includes(status);
  setLabel($('composerHint'), status === 'blocked' ? '请先回复上方的问题，再继续任务' : $('mode').value === 'plan' ? '按步骤执行并跟踪进度' : '在当前项目中工作');
}
function setRunStatus(status, runID = $('runStatus').dataset.runID || '', runStatus) {
  if (runID !== $('runStatus').dataset.runID || status !== 'running' || $('runStatus').dataset.status !== 'running') activityPhase = '';
  $('runStatus').dataset.runID = runID;
  setLabel($('runStatus'), statusLabels[status] || status);
  $('runStatus').dataset.status = status;
  setLabel($('phaseLabel'), statusLabels[status] || status);
  setLabel($('phaseCaption'), {idle: '给搭档一个任务，我们一起推进', ready: '任务已收到，准备开始', running: '任务执行中，可以随时补充想法', blocked: '任务暂时停在这里，等待你的回复', closing: '正在结束任务', closed: '这次任务已结束'}[status] || status);
  if (status === 'closed') setLabel($('phaseCaption'), '任务已关闭，仅可查看记录');
  updateComposer();
  $('office').dataset.status = status; $('office').dataset.outcome = runStatus || '';
  setLabel($('agentStatus'), {idle: '随时可以开始', ready: '任务已收到', running: '正在处理任务', blocked: '需要你的回复', closing: '正在结束任务', closed: '任务已关闭'}[status] || status);
  $('stop').disabled = !['ready', 'running'].includes(status);
  const result = [...messagesByKey.values()].findLast(message => message.finished && message.runID === runID);
  const completed = status === 'idle' && runStatus === 'finished' && !!result;
  setLabel($('boardTitle'), completed ? '成果已放到柜子里' : {idle: '想一起完成什么？', ready: '搭档正在接收任务', running: '我们正在推进这件事', blocked: '需要你做一个选择', closing: '正在结束任务', closed: '这次任务已结束'}[status] || status);
  $('deliverable').hidden = !completed;
  $('resultPreview').textContent = completed ? result.text.slice(0, 70) : '';
  setLabel($('boardStatus'), completed ? '已完成 · 可以继续追问或回看过程' : $('agentStatus').dataset.i18n);
  if (completed) {
    setLabel($('phaseCaption'), '任务完成：打开成果，或继续和搭档讨论');
    setLabel($('agentStatus'), '成果整理好了，点开一起看看。');
  }
  if (status === 'idle' && ['failed', 'interrupted'].includes(runStatus)) {
    setLabel($('boardTitle'), runStatus === 'failed' ? '这次任务没有完成' : '这次任务已停止');
    setLabel($('boardStatus'), '可以查看执行记录，或补充消息后继续');
    setLabel($('phaseCaption'), '可以查看执行记录，或补充消息后继续');
  }
  if (runStatus === 'finished') {
    messagesByKey.forEach(record => {
      if (record.runID === runID && record.finished) { record.trainingEligible = true; renderTrainingButton(record); }
    });
  }
  updateActivity();
}
function updateActivity() {
  const status = $('runStatus').dataset.status;
  const phase = status === 'running' ? activityPhase || 'working' : status === 'blocked' ? 'waiting' : 'queued';
  const label = {thinking: '正在思考', tools: '正在调用工具', browser: '正在浏览网页', computer: '正在操作电脑', responding: '正在回复', working: '正在处理任务', waiting: '等待你的回复', queued: '等待执行'}[phase];
  $('activity').hidden = !['ready', 'running', 'blocked'].includes(status);
  $('activity').dataset.phase = phase; setLabel($('activity'), label);
  if (status === 'running') { setLabel($('phaseLabel'), label); setLabel($('agentStatus'), label); }
}
function renderActivity(event) {
  // Stored history and late output from another Run cannot revive its live status.
  if (event.sequence || !event.run_id || endedRuns.has(event.run_id) || ['closing', 'closed'].includes($('runStatus').dataset.status)) return;
  const payload = event.payload || {};
  if (event.kind === 'run_status' && payload.status === 'started') {
    setRunStatus('running', event.run_id); return;
  }
  if (event.run_id !== $('runStatus').dataset.runID) return;
  if (event.kind === 'run_status') {
    if (payload.status === 'blocked') setRunStatus('blocked', event.run_id);
    else if (['finished', 'failed', 'interrupted'].includes(payload.status)) {
      endedRuns.add(event.run_id); setRunStatus('idle', event.run_id, payload.status);
    }
    return;
  }
  if ($('runStatus').dataset.status !== 'running') return;
  if (event.kind === 'agent_activity' && payload.phase === 'thinking') activityPhase = 'thinking';
  else if (event.kind === 'tool_call' && payload.status === 'started' && !toolsByKey.get(event.run_id + ':' + payload.tool_call_id)?.finished) activityPhase = payload.tool_name?.startsWith('browser_') ? 'browser' : payload.tool_name?.startsWith('computer_') ? 'computer' : 'tools';
  else if (event.kind === 'assistant_delta') {
    if (payload.delta) activityPhase = 'responding';
    else if (payload.thinking_content_delta) activityPhase = 'thinking';
  }
  updateActivity();
}
function reset(id, draft = '') {
  save(draftKey(thread), $('input').value);
  generation++;
  clearTimeout(timer); timer = null; polling = false;
  if (eventSource) eventSource.close();
  eventSource = null;
  thread = id; cursor = 0n; lastRefresh = 0; fileRequest++;
  messagesByKey.clear(); roundsByRunID.clear(); toolsByKey.clear(); promptsByKey.clear(); endedRuns.clear(); inputModesByID.clear();
  followConversation = true;
  $('messages').replaceChildren();
  $('welcome').hidden = !!id;
  $('jumpBottom').hidden = true;
  $('toolOutput').replaceChildren();
  $('boardPlan').replaceChildren(); setLabel($('goalSummary'), '阅读代码、处理文件，或把一个想法变成现实。');
  $('plan').replaceChildren();
  changedFilesByPath.clear(); $('changedFiles').replaceChildren();
  $('file').textContent = ''; $('file').hidden = true; delete $('file').dataset.path;
  delete $('fileStatus').dataset.i18n; $('fileStatus').textContent = '';
  $('input').value = draft || localStorage[draftKey(id)] || '';
  save(draftKey(id), $('input').value);
  $('mode').value = '';
  save('selectedThread', id || '');
  trainingSource = null; $('trainingReview').hidden = true; $('trainingData').hidden = false;
  showThread({}); updateComposer(); report(null); showPanel('Talk');
}
function connectStream(target) {
  if (typeof EventSource === 'undefined') return;
  if (eventSource) eventSource.close();
  const current = generation;
  eventSource = new EventSource('/api/threads/' + target + '/stream');
  eventSource.onmessage = message => {
    if (current !== generation) return;
    try {
      const event = JSON.parse(message.data);
      // Questions come from durable history; live deltas only decorate those same messages.
      if (!['assistant_delta', 'assistant_message', 'tool_call', 'plan_updated', 'agent_activity', 'run_status'].includes(event.kind)) return;
      const follow = isNearBottom();
      renderEvent(event, target);
      if (follow) $('messages').scrollTop = $('messages').scrollHeight;
    } catch (error) { report(error); }
  };
}
async function select(id) {
  reset(id);
  showPanel('Talk');
  const current = generation;
  try {
    const row = await api('/api/threads/' + id);
    if (current !== generation) return;
    showThread(row);
    await list();
    if (current !== generation) return;
    poll();
  } catch (error) {
    if (current !== generation) return;
    if (error.status !== 404) throw error;
    reset(null, $('input').value);
    await list();
  }
}
async function submit(text) {
  if (submitting || ['closing', 'closed'].includes($('runStatus').dataset.status)) return;
  submitting = true; updateComposer();
  const current = generation, draft = $('input').value, mode = $('mode').value || '';
  try {
    if (!thread) {
      const row = await api('/api/threads', {method: 'POST', body: JSON.stringify({session_id: session, name: text.split('\n')[0].slice(0, 48)})});
      if (current !== generation) return;
      thread = row.id; save('selectedThread', thread); showThread(row);
    }
    const target = thread;
    await api('/api/threads/' + target + '/messages', {method: 'POST', body: JSON.stringify({text, mode})});
    if (current !== generation) return;
    if ($('input').value === draft) $('input').value = '';
    save(draftKey(target), $('input').value); save(draftKey(null), '');
    $('welcome').hidden = true;
    setRunStatus('ready', ''); showPanel('Talk');
    report(null); await list();
    if (current === generation) poll();
  } catch (error) {
    if (current !== generation) return;
    if (error.status === 404) {
      reset(null, $('input').value);
      await list().catch(report);
    } else { save(draftKey(thread), $('input').value); report(error); }
  } finally { submitting = false; updateComposer(); }
}
function resolvePrompt(box, label = '已处理') {
  if (label === '已处理' && box.classList.contains('resolved')) return;
  box.querySelectorAll('button,input,select,textarea').forEach(control => control.disabled = true);
  box.classList.add('resolved');
  setLabel(box.children[0], label);
}
async function resume(target, run, payload, box) {
  const controls = box.querySelectorAll('button,input,select,textarea');
  controls.forEach(control => control.disabled = true);
  const current = generation;
  try {
    await api('/api/threads/' + target + '/messages', {method: 'POST', body: JSON.stringify({mode: box.dataset.mode || '', resume: {
      run_id: run, checkpoint_id: payload.checkpoint_id, interrupt_id: payload.interrupt_id, ...payload.response
    }})});
    resolvePrompt(box, payload.response?.approval?.always_allow ? '已始终允许此工具' : '回复已提交');
    if (current === generation) { report(null); poll(); }
  } catch (error) {
    controls.forEach(control => control.disabled = false);
    if (current === generation) report(error);
  }
}
function required(ev, p, target) {
  const box = document.createElement('div'); box.className = 'prompt'; box.dataset.run = ev.run_id || ''; box.dataset.interrupt = p.interrupt_id;
  const title = document.createElement('strong');
  setLabel(title, p.kind === 'approval' ? '需要批准' : '需要回答'); box.append(title);
  const button = (label, response) => {
    const b = document.createElement('button'); setLabel(b, label);
    b.onclick = async () => {
      try { await resume(target, ev.run_id, {...p, response: response()}, box); }
      catch (error) { report(error); }
    }; box.append(b);
  };
  if (p.kind === 'batch') {
    const answers = [];
    (p.items || []).forEach(item => {
      const row = document.createElement('div'); row.className = 'batch-item';
      const label = document.createElement('div');
      label.textContent = item.tool_name || item.info?.question || item.kind;
      row.append(label);
      if (item.kind === 'approve') {
        if (item.arguments_json) {
          const args = document.createElement('div'); args.textContent = item.arguments_json; row.append(args);
        }
        const select = document.createElement('select');
        [['请选择', ''], ['允许', 'allow'], ['始终允许此工具', 'always'], ['拒绝', 'deny']].forEach(([text, value]) => {
          const option = document.createElement('option'); setLabel(option, text); option.value = value; select.append(option);
        });
        row.append(select);
        answers.push(() => ({interrupt_id: item.interrupt_id, approval: {approved: ['allow', 'always'].includes(select.value), always_allow: select.value === 'always', reason: select.value === 'deny' ? 'user denied' : ''}}));
      } else {
        const input = document.createElement('input'); input.dataset.i18nPlaceholder = '输入回复'; input.placeholder = t('输入回复'); row.append(input);
        answers.push(() => ({interrupt_id: item.interrupt_id, interrupt: {kind: item.kind, info_type: item.info_type, data: {user_answer: input.value}}}));
      }
      box.append(row);
    });
    button('提交全部', () => {
      const responses = answers.map(answer => answer());
      const undecided = [...box.querySelectorAll('select')].some(control => !control.value);
      if (undecided) throw new Error(t('请为每个审批项选择允许或拒绝'));
      return {answers: responses};
    });
  } else if (p.kind === 'approval') {
    const detail = document.createElement('div'); detail.textContent = (p.tool_name || t('工具')) + (p.arguments_json ? ' ' + p.arguments_json : ''); box.append(detail);
    button('允许', () => ({approval: {approved: true}}));
    button('始终允许此工具', () => ({approval: {approved: true, always_allow: true}}));
    button('拒绝', () => ({approval: {approved: false, reason: 'user denied'}}));
    const scope = document.createElement('small'); setLabel(scope, '当前任务中不再询问此工具'); box.append(scope);
  } else if (p.kind === 'plan_input') {
    const answers = {};
    (p.questions || []).forEach(q => {
      const label = document.createElement('label'); label.textContent = q.question;
      const input = document.createElement('input'); input.placeholder = (q.options || []).map(o => o.label).join(' / ');
      input.oninput = () => answers[q.id] = {answers: [input.value]}; label.append(input); box.append(label);
    });
    button('提交', () => {
      const missing = (p.questions || []).some(question => !answers[question.id]?.answers[0]?.trim());
      if (missing) throw new Error(t('请回答每一个问题'));
      return {request_user_input: {answers}};
    });
  } else {
    const question = document.createElement('div');
    const info = p.info || p;
    question.textContent = info.question || info.message || (info.questions || []).join('\n') || t('请补充信息'); box.append(question);
    if (info.question && info.questions && info.questions.length) {
      const options = document.createElement('div'); options.textContent = info.questions.join(' / '); box.append(options);
    }
    const input = document.createElement('input'); input.dataset.i18nPlaceholder = '输入回复'; input.placeholder = t('输入回复'); box.append(input);
    button('继续', () => {
      if (!input.value.trim()) throw new Error(t('请先输入回答'));
      return {interrupt: {kind: p.kind, info_type: p.info_type, data: {user_answer: input.value}}};
    });
  }
  return box;
}
// Build a small Markdown subset with DOM nodes. Model output never becomes executable HTML.
function appendInline(element, text) {
  const pattern = /(`[^`\n]+`|\*\*[^*\n]+\*\*|\[[^\]\n]+\]\(https?:\/\/[^\s)]+\))/g;
  let start = 0;
  for (const match of text.matchAll(pattern)) {
    element.append(document.createTextNode(text.slice(start, match.index)));
    const token = match[0];
    const tag = token.startsWith('`') ? 'code' : token.startsWith('**') ? 'strong' : 'a';
    const node = document.createElement(tag);
    if (tag === 'a') {
      const end = token.indexOf('](');
      node.textContent = token.slice(1, end); node.href = token.slice(end + 2, -1);
      node.target = '_blank'; node.rel = 'noopener noreferrer';
    } else node.textContent = token.slice(tag === 'code' ? 1 : 2, tag === 'code' ? -1 : -2);
    element.append(node); start = match.index + token.length;
  }
  element.append(document.createTextNode(text.slice(start)));
}
function renderMarkdown(element, text) {
  element.replaceChildren();
  const blocks = text.split(/(```[^\n]*\n[\s\S]*?(?:```|$))/g);
  blocks.forEach(block => {
    if (!block) return;
    if (block.startsWith('```')) {
      const pre = document.createElement('pre'), code = document.createElement('code');
      code.textContent = block.slice(block.indexOf('\n') + 1).replace(/```$/, '').replace(/\n$/, '');
      pre.append(code); element.append(pre); return;
    }
    block.split(/\n\s*\n/).filter(Boolean).forEach(paragraph => {
      const heading = paragraph.match(/^#{1,6} (.*)$/);
      const bulletLines = paragraph.split('\n');
      if (bulletLines.every(line => /^\s*(?:[-*]|\d+\.) /.test(line))) {
        const list = document.createElement(/^\s*\d+\./.test(bulletLines[0]) ? 'ol' : 'ul');
        bulletLines.forEach(line => { const item = document.createElement('li'); appendInline(item, line.replace(/^\s*(?:[-*]|\d+\.) /, '')); list.append(item); });
        element.append(list); return;
      }
      const node = document.createElement(heading ? 'h3' : 'p');
      appendInline(node, heading ? heading[1] : paragraph); element.append(node);
    });
  });
}
function eventText(event) { return event.text || (event.payload?.parts || []).map(part => part.text || '').join('\n') || event.payload?.message || ''; }
function pretty(value) {
  if (!value) return '';
  try { return JSON.stringify(JSON.parse(value), null, 2); } catch { return String(value); }
}
function showPanel(name, focus = false) {
  if (!$('notebook').hidden && !$('panelTalk').hidden) followConversation = isNearBottom();
  $('notebook').hidden = false; $('notebook').dataset.panel = name;
  document.querySelector('.composer-area').hidden = name === 'Training';
  panelNames.forEach(panel => {
    $('panel' + panel).hidden = panel !== name;
    $('tab' + panel).setAttribute('aria-selected', String(panel === name));
    $('tab' + panel).tabIndex = panel === name ? 0 : -1;
  });
  if (name === 'Talk' && followConversation) $('messages').scrollTop = $('messages').scrollHeight;
  $('jumpBottom').hidden = isNearBottom();
  if (focus) $('tab' + name).focus();
}
// Live output may arrive before the input is assigned a Run. Keep both in the same round.
function appendToLog(event, element) {
  let runID = event.run_id;
  if (!runID && event.kind === 'input') {
    runID = [...roundsByRunID].find(([, round]) => round.inputIDs.has(String(event.sequence)))?.[0];
  }
  if (!runID) { $('messages').append(element); return; }
  let round = roundsByRunID.get(runID);
  const inputIDs = (event.payload?.consumed_message_ids || []).map(String);
  if (!round) {
    round = document.createElement('section'); round.className = 'round'; round.dataset.run = runID;
    round.inputIDs = new Set();
    const heading = document.createElement('div'); heading.className = 'round-heading';
    setLabel(heading, '第 {number} 轮', {number: roundsByRunID.size + 1}); round.append(heading);
    const firstInput = inputIDs.map(id => messagesByKey.get('input:' + id)).find(record => record && !record.runID);
    $('messages').insertBefore(round, firstInput?.element || null);
    roundsByRunID.set(runID, round);
  }
  const findConsumer = id => [...round.children].find(child => child.consumedInputIDs?.has(id)) || null;
  inputIDs.forEach(id => {
    round.inputIDs.add(id);
    const record = messagesByKey.get('input:' + id);
    if (record && !record.runID) { round.insertBefore(record.element, findConsumer(id)); record.runID = runID; }
  });
  element.consumedInputIDs ||= new Set();
  inputIDs.forEach(id => element.consumedInputIDs.add(id));
  if (element.parentNode !== round) round.insertBefore(element, event.kind === 'input' ? findConsumer(String(event.sequence)) : null);
}
function renderTool(event) {
  const payload = event.payload || {}, key = event.run_id + ':' + payload.tool_call_id;
  let record = toolsByKey.get(key);
  if (!record) {
    const detail = document.createElement('details'); detail.className = 'tool-record';
    const summary = document.createElement('summary'), name = document.createElement('span'), state = document.createElement('span');
    state.className = 'tool-state'; summary.append(name, state); detail.append(summary);
    const args = document.createElement('pre'), result = document.createElement('pre'); detail.append(args, result);
    record = {detail, name, state, args, result, output: '', finished: false, order: toolsByKey.size}; toolsByKey.set(key, record);
    if (toolsByKey.size === 1) $('toolOutput').replaceChildren();
    detail.dataset.call = key; detail.tabIndex = -1; $('toolOutput').append(detail);
    const link = document.createElement('button'); link.className = 'tool-link'; link.dataset.call = key;
    link.onclick = () => { showPanel('Output'); detail.open = true; detail.scrollIntoView({block: 'nearest'}); detail.focus(); };
    appendToLog(event, link); record.link = link;
  }
  if (record.finished && payload.status !== 'finished') return;
  record.name.textContent = payload.tool_name || t('工具');
  record.finished = payload.status === 'finished';
  const label = record.finished ? payload.is_error ? '失败' : '已完成' : '运行中';
  setLabel(record.state, payload.elapsed_ms != null ? '{status} · {seconds}s' : '{status}', {status: t(label), seconds: (payload.elapsed_ms / 1000).toFixed(1)});
  if (payload.arguments_json != null) record.args.textContent = pretty(payload.arguments_json);
  let args;
  try { args = JSON.parse(record.args.textContent || '{}'); } catch { args = {}; }
  const subject = args?.path || args?.command || args?.query || args?.url || '';
  const toolSummary = record.name.textContent + (typeof subject === 'string' && subject ? ' ' + subject.replace(/\s+/g, ' ').slice(0, 100) : '');
  setLabel(record.link, '{tool} · {status}', {tool: toolSummary, status: t(label)});
  record.state.dataset.toolStatus = record.link.dataset.toolStatus = label;
  record.link.title = toolSummary; record.link.dataset.finished = String(record.finished);
  record.link.dataset.failed = String(!!payload.is_error);
  if (payload.output_delta) record.output += payload.output_delta;
  record.result.textContent = payload.result_json != null ? pretty(payload.result_json) : record.output;
  record.result.hidden = !record.result.textContent; record.args.hidden = !record.args.textContent;
  if (payload.parts && !record.screenshots) {
    record.screenshots = true;
    for (const part of payload.parts) {
      if (part.type !== 'image' || part.mime_type !== 'image/png' || !part.base64_data) continue;
      const image = document.createElement('img'); image.className = 'tool-screenshot';
      image.src = 'data:image/png;base64,' + part.base64_data; image.alt = payload.tool_name || 'Screenshot';
      record.detail.append(image);
    }
  }
  // Completed tool rows are updated in place; live completion must update the list too.
  if (record.finished && !record.filesRecorded) {
    record.filesRecorded = true; recordChangedFiles(payload, record.order);
  }
}
function recordChangedFiles(payload, order) {
  if (payload.is_error) return;
  const changes = new Map(), result = payload.result_json || '';
  let args;
  try { args = JSON.parse(payload.arguments_json || '{}'); } catch { return; }
  const operation = {write_file: ['wrote ', 'W'], edit_file: ['edited ', 'M'], delete_file: ['Deleted file ', 'D']}[payload.tool_name];
  if (operation && typeof args.path === 'string' && result === operation[0] + args.path) changes.set(args.path, operation[1]);
  if (payload.tool_name === 'apply_patch') {
    for (const match of result.matchAll(/^([AMD]) (.+)$/gm)) changes.set(match[2], match[1]);
    // A successful move reports the destination; retain the removed source too.
    let source;
    for (const line of (args.patch || '').replace(/\r\n/g, '\n').split('\n')) {
      if (line.startsWith('*** Update File: ')) source = line.slice(17);
      else if (line.startsWith('*** Move to: ') && changes.has(line.slice(13)) && source) changes.set(source, 'D');
      else if (line.startsWith('*** ')) source = undefined;
    }
  }
  if (!changes.size) return;
  const root = $('workspace').title.replace(/\/$/, '');
  for (let [path, operation] of changes) {
    if (root && path.startsWith(root + '/')) path = path.slice(root.length + 1);
    const parts = [];
    for (const part of path.split('/')) {
      if (part === '..') parts.pop();
      else if (part && part !== '.') parts.push(part);
    }
    path = parts.join('/');
    if (!path) continue;
    if ((changedFilesByPath.get(path)?.order ?? -1) > order) continue;
    changedFilesByPath.set(path, {operation, order});
    if ($('file').dataset.path === path) { $('file').hidden = true; $('file').textContent = ''; $('fileStatus').textContent = ''; delete $('fileStatus').dataset.i18n; fileRequest++; }
  }
  $('changedFiles').replaceChildren();
  for (const [path, {operation}] of changedFilesByPath) {
    const row = document.createElement('div'); row.className = 'file-change'; row.dataset.operation = operation;
    const badge = document.createElement('span'); badge.className = 'file-operation';
    setLabel(badge, {W: '写入', A: '新增', M: '修改', D: '删除'}[operation]);
    const link = document.createElement(operation === 'D' ? 'span' : 'a'); link.className = 'file-name'; link.textContent = path;
    if (operation !== 'D') {
      link.href = '/api/threads/' + thread + '/file?path=' + encodeURIComponent(path);
      link.onclick = event => { event.preventDefault(); return openChangedFile(path); };
    }
    row.append(badge, link); $('changedFiles').append(row);
  }
}

function renderPlan(payload) {
  $('plan').replaceChildren(); $('boardPlan').replaceChildren();
  if (payload.explanation) { const explanation = document.createElement('div'); explanation.className = 'plan-explanation'; explanation.textContent = payload.explanation; $('plan').append(explanation); }
  (payload.items || []).forEach(item => {
    const row = document.createElement('div'); row.className = 'plan-step'; row.dataset.status = item.status;
    const marker = document.createElement('span'); marker.className = 'step-marker'; marker.textContent = item.status === 'completed' ? '✓' : item.status === 'in_progress' ? '◉' : '○';
    const text = document.createElement('span'); text.textContent = item.content; row.append(marker, text); $('plan').append(row);
    if ($('boardPlan').children.length < 3) $('boardPlan').append(row.cloneNode(true));
  });
}
function renderEvent(event, target) {
  renderActivity(event);
  if (['agent_activity', 'run_status'].includes(event.kind)) return;
  const payload = event.payload || {};
  if (event.kind === 'input') {
    inputModesByID.set(String(event.sequence), payload.mode || '');
    if (event.run_id && ['completed', 'interrupted', 'canceled'].includes(event.status)) endedRuns.add(event.run_id);
  }
  if (event.kind === 'resume_run') {
    const box = promptsByKey.get(payload.interrupt_id);
    const answer = payload.interrupt?.data?.user_answer;
    if (box) {
      const label = payload.approval ? (payload.approval.always_allow ? '已始终允许此工具' : payload.approval.approved ? '已允许' : '已拒绝') : answer ? '已回答：{answer}' : '已回答';
      resolvePrompt(box, label); box.children[0].i18nValues = {answer}; setLabel(box.children[0], label, {answer});
    }
    return;
  }
  if (payload.interrupt_id && payload.checkpoint_id) {
    if (promptsByKey.has(payload.interrupt_id)) return;
    const box = required(event, payload, target);
    box.dataset.mode = (payload.consumed_message_ids || []).some(id => inputModesByID.get(id) === 'plan') ? 'plan' : '';
    const resolved = endedRuns.has(event.run_id) || ['closing', 'closed'].includes($('runStatus').dataset.status);
    if (resolved) resolvePrompt(box);
    promptsByKey.set(payload.interrupt_id, box); appendToLog(event, box);
    if (!resolved) { setRunStatus('blocked', event.run_id); showPanel('Talk'); }
  } else if (event.kind === 'tool' || event.kind === 'tool_call') {
    renderTool(event);
  } else if (event.kind === 'plan' || event.kind === 'plan_updated') {
    renderPlan(payload);
  } else if (['assistant', 'assistant_message', 'assistant_delta'].includes(event.kind)) {
    const key = event.run_id + ':' + (payload.llm_response_id || event.sequence || 'stream');
    let record = messagesByKey.get(key);
    const delta = event.kind === 'assistant_delta';
    const text = delta ? payload.delta || '' : eventText(event);
    if (!text) return;
    if (!record) {
      const element = document.createElement('div'); element.className = 'msg assistant'; element.dataset.i18nRole = '搭档'; element.dataset.role = t('搭档');
      record = {element, runID: event.run_id, text: '', finished: false}; messagesByKey.set(key, record);
    }
    appendToLog(event, record.element);
    if (record.finished && delta) return;
    record.text = delta ? record.text + text : text; record.finished = !delta;
    renderMarkdown(record.element, record.text);
    if (!delta && event.sequence) {
      record.messageID = String(event.sequence); record.threadID = target;
      record.trainingEligible = event.training_eligible || record.trainingEligible;
    }
    record.trainingButton = null; renderTrainingButton(record);
    if (event.created_at) record.element.dataset.time = new Date(event.created_at).toLocaleTimeString([], {hour: '2-digit', minute: '2-digit'});
  } else if (['input', 'error', 'text'].includes(event.kind)) {
    const key = event.kind + ':' + event.sequence;
    if (messagesByKey.has(key)) return;
    const element = document.createElement('div'); element.className = 'msg' + (event.kind === 'input' ? ' user' : event.kind === 'error' ? ' error' : '');
    element.textContent = eventText(event) || event.kind;
    if (event.kind === 'input') { element.dataset.i18nRole = '你'; element.dataset.role = t('你'); delete $('goalSummary').dataset.i18n; $('goalSummary').textContent = eventText(event); }
    if (event.created_at) element.dataset.time = new Date(event.created_at).toLocaleTimeString([], {hour: '2-digit', minute: '2-digit'});
    appendToLog(event, element); messagesByKey.set(key, {element, runID: element.parentNode?.dataset.run || event.run_id});
  }
  $('welcome').hidden = true;
}
function isNearBottom() {
  if ($('panelTalk').hidden) return false;
  const messages = $('messages');
  return messages.scrollHeight - messages.scrollTop - messages.clientHeight <= 48;
}
async function poll() {
  if (!thread || polling) return;
  clearTimeout(timer); timer = null; polling = true;
  const current = generation, target = thread;
  try {
    const followBottom = isNearBottom();
    let changed = false, rows;
    do {
      rows = await api('/api/threads/' + target + '/events?after=' + cursor);
      if (current !== generation) return;
      const previousCursor = cursor;
      rows.forEach(event => {
        const sequence = BigInt(event.sequence);
        if (sequence <= cursor) return;
        cursor = sequence; renderEvent(event, target); changed = true;
      });
      if (cursor === previousCursor) break;
    } while (rows.length === 200);
    if (changed && trainingEnabled) renderTrainingData();
    if (changed && followBottom) $('messages').scrollTop = $('messages').scrollHeight;
    // Open live delivery only after history is loaded, preserving message order.
    if (!eventSource) connectStream(target);
    if (Date.now() - lastRefresh > 2000) {
      const row = await api('/api/threads/' + target);
      if (current !== generation) return;
      showThread(row); lastRefresh = Date.now();
      if (['idle', 'closed'].includes(row.status)) promptsByKey.forEach(box => resolvePrompt(box));
      await list();
      if (trainingEnabled && !$('panelTraining').hidden) await loadTrainingData();
    }
    if ($('status').dataset.errorSource === 'poll') report(null);
  } catch (error) {
    if (current !== generation) return;
    if (error.status === 404) {
      reset(null, $('input').value);
      await list().catch(report);
    } else report(error, 'poll');
  } finally {
    if (current === generation) { polling = false; timer = setTimeout(poll, 500); }
  }
}
document.querySelector('.composer').onsubmit = event => { event.preventDefault(); const text = $('input').value.trim(); if (text) submit(text); };
$('input').oninput = () => { save(draftKey(thread), $('input').value); updateComposer(); };
$('input').onkeydown = event => {
  if (event.key === 'Enter' && !event.shiftKey && !event.isComposing && event.keyCode !== 229) {
    event.preventDefault(); const text = $('input').value.trim(); if (text) submit(text);
  }
};
$('new').onclick = () => { reset(null); list().catch(report); $('input').focus(); };
$('search').oninput = showTasks;
$('parentTask').onclick = () => select($('parentTask').dataset.id).catch(report);
$('mode').onchange = () => setLabel($('composerHint'), $('mode').value === 'plan' ? '按步骤执行并跟踪进度' : '在当前项目中工作');
$('messages').onscroll = () => {
  if ($('notebook').hidden || $('panelTalk').hidden) return;
  followConversation = isNearBottom(); $('jumpBottom').hidden = followConversation;
};
$('jumpBottom').onclick = () => { $('messages').scrollTop = $('messages').scrollHeight; $('jumpBottom').hidden = true; };
async function openChangedFile(path) {
  const current = generation, target = thread, request = ++fileRequest;
  $('file').hidden = true; $('file').textContent = ''; $('file').dataset.path = path;
  setLabel($('fileStatus'), '正在读取…');
  try {
    const result = await api('/api/threads/' + target + '/file?path=' + encodeURIComponent(path));
    if (current !== generation || request !== fileRequest) return;
    $('file').textContent = result.content || ''; $('file').hidden = false;
    delete $('fileStatus').dataset.i18n; $('fileStatus').textContent = result.path;
  } catch (error) {
    if (current === generation && request === fileRequest) { delete $('fileStatus').dataset.i18n; $('fileStatus').textContent = error.message; }
  }
}
document.querySelectorAll('[data-panel]').forEach(button => {
  button.onclick = () => showPanel(button.dataset.panel, true);
  button.onkeydown = event => {
    if (button.getAttribute('role') !== 'tab' || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const index = panelNames.indexOf(button.dataset.panel);
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? panelNames.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : panelNames.length - 1)) % panelNames.length;
    showPanel(panelNames[next]); $('tab' + panelNames[next]).focus();
  };
});
document.querySelectorAll('[data-prompt]').forEach(button => button.onclick = () => {
  $('input').value = t(button.dataset.prompt);
  $('mode').value = button.dataset.mode || '';
  $('mode').onchange(); $('input').oninput(); $('input').focus();
});
$('closeNotebook').onclick = () => {
  if (!$('panelTalk').hidden) followConversation = isNearBottom();
  $('notebook').hidden = true; $('input').focus();
};
$('stop').onclick = async () => {
  if (!thread) return;
  const current = generation, target = thread;
  $('stop').disabled = true;
  try {
    await api('/api/threads/' + target + '/cancel', {method: 'POST'});
    if (current !== generation) return;
    setLabel($('agentStatus'), '正在停止任务');
    report(null); poll();
  } catch (error) {
    if (current === generation) { setRunStatus($('runStatus').dataset.status); report(error); }
  }
};
function setTheme(theme) { document.documentElement.dataset.theme = theme; save('theme', theme); }
$('language').onclick = () => { setLanguage(language === 'en' ? 'zh' : 'en'); showTasks(); };
setLanguage(language);
$('theme').onclick = () => setTheme(document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark');
setTheme(localStorage.theme || 'light');
document.onkeydown = event => {
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') { event.preventDefault(); $('new').onclick(); }
  if (event.key === 'Escape') $('closeNotebook').onclick();
};
$('input').value = localStorage[draftKey(null)] || ''; updateComposer();
initializeTraining();
const selectedThread = localStorage.selectedThread;
if (selectedThread) select(selectedThread).catch(report); else list().catch(report);
