let session = localStorage.session || crypto.randomUUID();
localStorage.session = session;
let thread = null, cursor = 0n, generation = 0, timer = null, polling = false, submitting = false;
let eventSource = null, taskRows = [], lastRefresh = 0, fileRequest = 0, taskRequest = 0;
const messagesByKey = new Map(), toolsByKey = new Map(), promptsByKey = new Map();
const endedRuns = new Set(), inputModesByID = new Map();
const $ = id => document.getElementById(id);
const statusLabels = {idle: '就绪', ready: '等待执行', running: '执行中', blocked: '等待你的回复', closing: '关闭中', closed: '已关闭'};
function report(error) {
  $('status').textContent = error ? error.message : '';
  $('status').hidden = !error;
}
function save(key, value) {
  try { localStorage[key] = value; } catch { /* Storage may be full or disabled; keep the current draft in the editor. */ }
}
function draftKey(id) { return 'draft:' + session + ':' + (id || 'new'); }
function updateComposer() {
  $('send').disabled = submitting || !$('input').value.trim();
  $('input').style.height = 'auto';
  $('input').style.height = Math.min($('input').scrollHeight || 54, 160) + 'px';
}
async function api(url, options) {
  const response = await fetch(url, {headers: {'content-type': 'application/json'}, ...options});
  const data = await response.json();
  if (!response.ok) throw new Error(data.error || `请求失败 (${response.status})`);
  return data;
}
function showTasks() {
  const query = $('search').value.trim().toLowerCase();
  $('threads').replaceChildren();
  taskRows.filter(row => (row.title || '未命名任务').toLowerCase().includes(query)).forEach(row => {
    const button = document.createElement('button');
    button.className = 'thread' + (row.id === thread ? ' active' : '');
    button.dataset.status = row.status || 'idle';
    button.title = row.title || '未命名任务';
    button.setAttribute('aria-current', row.id === thread ? 'true' : 'false');
    const dot = document.createElement('span'); dot.className = 'task-dot';
    button.append(dot, document.createTextNode(row.title || '未命名任务'));
    button.onclick = () => select(row.id).catch(report);
    $('threads').append(button);
  });
}
async function list() {
  const request = ++taskRequest;
  const rows = await api('/api/threads?session_id=' + encodeURIComponent(session));
  if (request !== taskRequest) return;
  taskRows = rows; showTasks();
}
function showThread(row) {
  $('title').textContent = row.title || (row.id ? '未命名任务' : '新任务');
  $('workspace').textContent = row.work_dir || '从一个问题或任务开始';
  $('workspace').title = row.work_dir || '';
  const status = row.status || 'idle';
  $('runStatus').textContent = statusLabels[status] || status;
  $('runStatus').dataset.status = status;
  $('mode').disabled = ['ready', 'running', 'blocked', 'closing', 'closed'].includes(status);
  $('composerHint').textContent = status === 'blocked' ? '请先回复上方的问题，再继续任务' : $('mode').value === 'plan' ? '按步骤执行并跟踪进度' : '在当前项目中工作';
}
function reset(id) {
  save(draftKey(thread), $('input').value);
  generation++;
  clearTimeout(timer); timer = null; polling = false;
  if (eventSource) eventSource.close();
  eventSource = null;
  thread = id; cursor = 0n; lastRefresh = 0; fileRequest++;
  messagesByKey.clear(); toolsByKey.clear(); promptsByKey.clear(); endedRuns.clear(); inputModesByID.clear();
  $('messages').replaceChildren();
  $('welcome').hidden = !!id;
  $('jumpBottom').hidden = true;
  $('toolOutput').replaceChildren(); $('toolOutput').textContent = '工具调用的参数、结果和运行状态会出现在这里。';
  $('plan').replaceChildren(); $('plan').textContent = 'Agent 更新计划后，步骤会出现在这里。';
  $('file').textContent = '输入相对路径查看文件'; $('fileStatus').textContent = ''; $('filePath').value = '';
  $('input').value = localStorage[draftKey(id)] || '';
  $('mode').value = '';
  save('selectedThread', id || '');
  showThread({}); updateComposer(); report(null);
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
      if (!['assistant_delta', 'assistant_message', 'tool_call', 'plan_updated'].includes(event.kind)) return;
      const follow = isNearBottom();
      renderEvent(event, target);
      if (follow) $('messages').scrollTop = $('messages').scrollHeight;
    } catch (error) { report(error); }
  };
}
async function select(id) {
  reset(id);
  const current = generation;
  const row = await api('/api/threads/' + id);
  if (current !== generation) return;
  showThread(row);
  await list();
  if (current !== generation) return;
  poll();
}
async function submit(text) {
  if (submitting) return;
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
    $('runStatus').textContent = '等待执行';
    report(null); await list();
    if (current === generation) poll();
  } catch (error) {
    if (current === generation) { save(draftKey(thread), $('input').value); report(error); }
  } finally { submitting = false; updateComposer(); }
}
function resolvePrompt(box, label = '已处理') {
  if (label === '已处理' && box.classList.contains('resolved')) return;
  box.querySelectorAll('button,input,select,textarea').forEach(control => control.disabled = true);
  box.classList.add('resolved');
  box.children[0].textContent = label;
}
async function resume(target, run, payload, box) {
  const controls = box.querySelectorAll('button,input,select,textarea');
  controls.forEach(control => control.disabled = true);
  const current = generation;
  try {
    await api('/api/threads/' + target + '/messages', {method: 'POST', body: JSON.stringify({mode: box.dataset.mode || '', resume: {
      run_id: run, checkpoint_id: payload.checkpoint_id, interrupt_id: payload.interrupt_id, ...payload.response
    }})});
    resolvePrompt(box, '回复已提交');
    if (current === generation) { report(null); poll(); }
  } catch (error) {
    controls.forEach(control => control.disabled = false);
    if (current === generation) report(error);
  }
}
function required(ev, p, target) {
  const box = document.createElement('div'); box.className = 'prompt'; box.dataset.run = ev.run_id || ''; box.dataset.interrupt = p.interrupt_id;
  const title = document.createElement('strong');
  title.textContent = p.kind === 'approval' ? '需要批准' : '需要回答'; box.append(title);
  const button = (label, response) => {
    const b = document.createElement('button'); b.textContent = label;
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
        [['请选择', ''], ['允许', 'allow'], ['拒绝', 'deny']].forEach(([text, value]) => {
          const option = document.createElement('option'); option.textContent = text; option.value = value; select.append(option);
        });
        row.append(select);
        answers.push(() => ({interrupt_id: item.interrupt_id, approval: {approved: select.value === 'allow', reason: select.value === 'allow' ? '' : 'user denied'}}));
      } else {
        const input = document.createElement('input'); input.placeholder = '输入回复'; row.append(input);
        answers.push(() => ({interrupt_id: item.interrupt_id, interrupt: {kind: item.kind, info_type: item.info_type, data: {user_answer: input.value}}}));
      }
      box.append(row);
    });
    button('提交全部', () => {
      const responses = answers.map(answer => answer());
      const undecided = [...box.querySelectorAll('select')].some(control => !control.value);
      if (undecided) throw new Error('请为每个审批项选择允许或拒绝');
      return {answers: responses};
    });
  } else if (p.kind === 'approval') {
    const detail = document.createElement('div'); detail.textContent = (p.tool_name || '工具') + (p.arguments_json ? ' ' + p.arguments_json : ''); box.append(detail);
    [['允许', true], ['拒绝', false]].forEach(([label, approved]) => button(label, () => ({approval: {approved, reason: approved ? '' : 'user denied'}})));
  } else if (p.kind === 'plan_input') {
    const answers = {};
    (p.questions || []).forEach(q => {
      const label = document.createElement('label'); label.textContent = q.question;
      const input = document.createElement('input'); input.placeholder = (q.options || []).map(o => o.label).join(' / ');
      input.oninput = () => answers[q.id] = {answers: [input.value]}; label.append(input); box.append(label);
    });
    button('提交', () => {
      const missing = (p.questions || []).some(question => !answers[question.id]?.answers[0]?.trim());
      if (missing) throw new Error('请回答每一个问题');
      return {request_user_input: {answers}};
    });
  } else {
    const question = document.createElement('div');
    const info = p.info || p;
    question.textContent = info.question || info.message || (info.questions || []).join('\n') || '请补充信息'; box.append(question);
    if (info.question && info.questions && info.questions.length) {
      const options = document.createElement('div'); options.textContent = info.questions.join(' / '); box.append(options);
    }
    const input = document.createElement('input'); input.placeholder = '输入回复'; box.append(input);
    button('继续', () => {
      if (!input.value.trim()) throw new Error('请先输入回答');
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
function showPanel(name) {
  ['Plan', 'Output', 'File'].forEach(panel => {
    $('panel' + panel).hidden = panel !== name;
    $('tab' + panel).setAttribute('aria-selected', String(panel === name));
    $('tab' + panel).tabIndex = panel === name ? 0 : -1;
  });
}
function openInspector() {
  document.body.classList.remove('inspector-hidden'); document.body.classList.add('inspector-open');
  $('toggleInspector').setAttribute('aria-expanded', 'true');
}
function renderTool(event) {
  const payload = event.payload || {}, key = event.run_id + ':' + payload.tool_call_id;
  let record = toolsByKey.get(key);
  if (!record) {
    const detail = document.createElement('details'); detail.className = 'tool-record';
    const summary = document.createElement('summary'), name = document.createElement('span'), state = document.createElement('span');
    state.className = 'tool-state'; summary.append(name, state); detail.append(summary);
    const args = document.createElement('pre'), result = document.createElement('pre'); detail.append(args, result);
    record = {detail, name, state, args, result, output: '', finished: false}; toolsByKey.set(key, record);
    if (toolsByKey.size === 1) $('toolOutput').replaceChildren();
    $('toolOutput').append(detail);
    const chat = document.createElement('details'); chat.className = 'tool-record';
    const chatSummary = document.createElement('summary'); chat.append(chatSummary);
    const inspect = document.createElement('button'); inspect.textContent = '查看参数和结果';
    inspect.onclick = () => { showPanel('Output'); openInspector(); detail.open = true; detail.scrollIntoView({block: 'nearest'}); };
    chat.append(inspect); $('messages').append(chat); record.chatSummary = chatSummary;
  }
  if (record.finished && payload.status !== 'finished') return;
  record.name.textContent = payload.tool_name || '工具';
  record.finished = payload.status === 'finished';
  const label = record.finished ? '已完成' : '运行中';
  record.state.textContent = label + (payload.elapsed_ms != null ? ' · ' + (payload.elapsed_ms / 1000).toFixed(1) + 's' : '');
  record.chatSummary.textContent = (payload.tool_name || '工具') + ' · ' + label;
  if (payload.arguments_json != null) record.args.textContent = pretty(payload.arguments_json);
  if (payload.output_delta) record.output += payload.output_delta;
  record.result.textContent = payload.result_json != null ? pretty(payload.result_json) : record.output;
  record.result.hidden = !record.result.textContent; record.args.hidden = !record.args.textContent;
}
function renderPlan(payload) {
  $('plan').replaceChildren();
  if (payload.explanation) { const explanation = document.createElement('div'); explanation.className = 'plan-explanation'; explanation.textContent = payload.explanation; $('plan').append(explanation); }
  (payload.items || []).forEach(item => {
    const row = document.createElement('div'); row.className = 'plan-step'; row.dataset.status = item.status;
    const marker = document.createElement('span'); marker.className = 'step-marker'; marker.textContent = item.status === 'completed' ? '✓' : item.status === 'in_progress' ? '◉' : '○';
    const text = document.createElement('span'); text.textContent = item.content; row.append(marker, text); $('plan').append(row);
  });
}
function renderEvent(event, target) {
  const payload = event.payload || {};
  if (event.kind === 'input') {
    inputModesByID.set(String(event.sequence), payload.mode || '');
    if (event.run_id && ['completed', 'interrupted', 'canceled'].includes(event.status)) endedRuns.add(event.run_id);
  }
  if (event.kind === 'resume_run') {
    const box = promptsByKey.get(payload.interrupt_id);
    const answer = payload.interrupt?.data?.user_answer;
    if (box) resolvePrompt(box, payload.approval ? (payload.approval.approved ? '已允许' : '已拒绝') : '已回答' + (answer ? '：' + answer : ''));
    return;
  }
  if (payload.interrupt_id && payload.checkpoint_id) {
    if (promptsByKey.has(payload.interrupt_id)) return;
    const box = required(event, payload, target);
    box.dataset.mode = (payload.consumed_message_ids || []).some(id => inputModesByID.get(id) === 'plan') ? 'plan' : '';
    if (endedRuns.has(event.run_id)) resolvePrompt(box);
    promptsByKey.set(payload.interrupt_id, box); $('messages').append(box);
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
      const element = document.createElement('div'); element.className = 'msg assistant'; $('messages').append(element);
      record = {element, text: '', finished: false}; messagesByKey.set(key, record);
    }
    if (record.finished && delta) return;
    record.text = delta ? record.text + text : text; record.finished = !delta;
    renderMarkdown(record.element, record.text);
  } else if (['input', 'error', 'text'].includes(event.kind)) {
    const key = event.kind + ':' + event.sequence;
    if (messagesByKey.has(key)) return;
    const element = document.createElement('div'); element.className = 'msg' + (event.kind === 'input' ? ' user' : event.kind === 'error' ? ' error' : '');
    element.textContent = eventText(event) || event.kind; $('messages').append(element); messagesByKey.set(key, {element});
  }
  $('welcome').hidden = true;
}
function isNearBottom() { const messages = $('messages'); return messages.scrollHeight - messages.scrollTop - messages.clientHeight <= 48; }
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
    if (changed && followBottom) $('messages').scrollTop = $('messages').scrollHeight;
    // Open live delivery only after history is loaded, preserving message order.
    if (!eventSource) connectStream(target);
    if (Date.now() - lastRefresh > 2000) {
      const row = await api('/api/threads/' + target);
      if (current !== generation) return;
      showThread(row); lastRefresh = Date.now();
      if (['idle', 'closed'].includes(row.status)) promptsByKey.forEach(box => resolvePrompt(box));
      await list();
    }
  } catch (error) { if (current === generation) report(error); }
  finally {
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
$('mode').onchange = () => showThread({title: $('title').textContent, work_dir: $('workspace').textContent, status: $('runStatus').dataset.status});
$('messages').onscroll = () => $('jumpBottom').hidden = isNearBottom();
$('jumpBottom').onclick = () => { $('messages').scrollTop = $('messages').scrollHeight; $('jumpBottom').hidden = true; };
$('fileForm').onsubmit = async event => {
  event.preventDefault();
  if (!thread) { $('fileStatus').textContent = '请先创建或选择任务'; return; }
  const path = $('filePath').value.trim(), current = generation, target = thread, request = ++fileRequest;
  if (!path) return;
  $('fileStatus').textContent = '正在读取…';
  try {
    const result = await api('/api/threads/' + target + '/file?path=' + encodeURIComponent(path));
    if (current !== generation || request !== fileRequest) return;
    $('file').textContent = result.content || ''; $('fileStatus').textContent = result.path;
  } catch (error) {
    if (current === generation && request === fileRequest) $('fileStatus').textContent = error.message;
  }
};
document.querySelectorAll('[data-panel]').forEach(button => {
  button.onclick = () => showPanel(button.dataset.panel);
  button.onkeydown = event => {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const panels = ['Plan', 'Output', 'File'], index = panels.indexOf(button.dataset.panel);
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? 2 : (index + (event.key === 'ArrowRight' ? 1 : 2)) % 3;
    showPanel(panels[next]); $('tab' + panels[next]).focus();
  };
});
document.querySelectorAll('[data-prompt]').forEach(button => button.onclick = () => { $('input').value = button.dataset.prompt; $('mode').value = button.dataset.mode || ''; $('mode').onchange(); $('input').oninput(); $('input').focus(); });
$('toggleInspector').onclick = () => {
  const narrow = typeof matchMedia !== 'undefined' && matchMedia('(max-width: 960px)').matches;
  if (narrow) document.body.classList.toggle('inspector-open');
  else document.body.classList.toggle('inspector-hidden');
  const open = narrow ? document.body.classList.contains('inspector-open') : !document.body.classList.contains('inspector-hidden');
  $('toggleInspector').setAttribute('aria-expanded', String(open));
};
$('closeInspector').onclick = () => { document.body.classList.remove('inspector-open'); $('toggleInspector').setAttribute('aria-expanded', 'false'); };
function setTheme(theme) { document.documentElement.dataset.theme = theme; save('theme', theme); }
$('theme').onclick = () => setTheme(document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark');
setTheme(localStorage.theme || (typeof matchMedia !== 'undefined' && matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'));
document.onkeydown = event => {
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') { event.preventDefault(); $('new').onclick(); }
  if (event.key === 'Escape') $('closeInspector').onclick();
};
$('input').value = localStorage[draftKey(null)] || ''; updateComposer();
showPanel('Plan');
$('toggleInspector').setAttribute('aria-expanded', String(typeof matchMedia !== 'undefined' && !matchMedia('(max-width: 960px)').matches));
const selectedThread = localStorage.selectedThread;
if (selectedThread) select(selectedThread).catch(report); else list().catch(report);
