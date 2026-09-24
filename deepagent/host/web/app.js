let session = localStorage.session || crypto.randomUUID();
localStorage.session = session;
let thread = null, cursor = 0n, generation = 0, timer = null, polling = false, submitting = false;
const $ = id => document.getElementById(id);
function report(error) { $('status').textContent = error ? error.message : ''; }
async function api(url, options) {
  const response = await fetch(url, {headers: {'content-type': 'application/json'}, ...options});
  const data = await response.json();
  if (!response.ok) throw new Error(data.error || `请求失败 (${response.status})`);
  return data;
}
async function list() {
  const rows = await api('/api/threads?session_id=' + encodeURIComponent(session));
  $('threads').replaceChildren();
  rows.forEach(t => {
    const e = document.createElement('div');
    e.className = 'thread ' + (t.id === thread ? 'active' : '');
    e.textContent = t.title || t.id;
    e.onclick = () => select(t.id).catch(report);
    $('threads').append(e);
  });
}
function reset(id) {
  generation++;
  clearTimeout(timer);
  timer = null;
  polling = false;
  thread = id;
  cursor = 0n;
  $('messages').replaceChildren();
  report(null);
}
async function select(id) {
  reset(id);
  const current = generation;
  const row = await api('/api/threads/' + id);
  if (current !== generation) return;
  $('title').textContent = row.title || 'DeepAgent';
  await list();
  if (current === generation) poll();
}
async function submit(text) {
  if (submitting) return;
  submitting = true;
  const current = generation, draft = $('input').value;
  try {
    if (!thread) {
      const row = await api('/api/threads', {method: 'POST', body: JSON.stringify({session_id: session})});
      if (current !== generation) return;
      thread = row.id;
    }
    const target = thread;
    await api('/api/threads/' + target + '/messages', {method: 'POST', body: JSON.stringify({text})});
    if (current !== generation) return;
    if ($('input').value === draft) $('input').value = '';
    report(null);
    await list();
    if (current === generation) poll();
  } catch (error) {
    if (current === generation) report(error);
  } finally { submitting = false; }
}
async function resume(target, run, payload, box) {
  const controls = box.querySelectorAll('button,input');
  controls.forEach(e => e.disabled = true);
  const current = generation;
  try {
    await api('/api/threads/' + target + '/messages', {method: 'POST', body: JSON.stringify({resume: {
      run_id: run, checkpoint_id: payload.checkpoint_id, interrupt_id: payload.interrupt_id, ...payload.response
    }})});
    if (current === generation) report(null);
  } catch (error) {
    controls.forEach(e => e.disabled = false);
    if (current === generation) report(error);
  }
}
function required(ev, p, target) {
  const box = document.createElement('div'); box.className = 'prompt';
  const title = document.createElement('strong');
  title.textContent = p.kind === 'approval' ? '需要批准' : '需要回答'; box.append(title);
  const button = (label, response) => {
    const b = document.createElement('button'); b.textContent = label;
    b.onclick = () => resume(target, ev.run_id, {...p, response: response()}, box); box.append(b);
  };
  if (p.kind === 'approval') {
    const detail = document.createElement('div'); detail.textContent = (p.tool_name || '工具') + (p.arguments_json ? ' ' + p.arguments_json : ''); box.append(detail);
    [['允许', true], ['拒绝', false]].forEach(([label, approved]) => button(label, () => ({approval: {approved, reason: approved ? '' : 'user denied'}})));
  } else if (p.kind === 'plan_input') {
    const answers = {};
    (p.questions || []).forEach(q => {
      const label = document.createElement('label'); label.textContent = q.question;
      const input = document.createElement('input'); input.placeholder = (q.options || []).map(o => o.label).join(' / ');
      input.oninput = () => answers[q.id] = {answers: [input.value]}; label.append(input); box.append(label);
    });
    button('提交', () => ({request_user_input: {answers}}));
  } else {
    const question = document.createElement('div');
    const info = p.info || p;
    question.textContent = info.question || info.message || (info.questions || []).join('\n') || '请补充信息'; box.append(question);
    if (info.question && info.questions && info.questions.length) {
      const options = document.createElement('div'); options.textContent = info.questions.join(' / '); box.append(options);
    }
    const input = document.createElement('input'); input.placeholder = '输入回复'; box.append(input);
    button('继续', () => ({interrupt: {kind: p.kind, info_type: p.info_type, data: {user_answer: input.value}}}));
  }
  return box;
}
async function poll() {
  if (!thread || polling) return;
  clearTimeout(timer); timer = null;
  polling = true;
  const current = generation, target = thread;
  try {
    const rows = await api('/api/threads/' + target + '/events?after=' + cursor);
    if (current !== generation) return;
    rows.forEach(ev => {
      const sequence = BigInt(ev.sequence);
      if (sequence > cursor) cursor = sequence;
      const p = ev.payload || {};
      if (p.interrupt_id && p.checkpoint_id) { $('messages').append(required(ev, p, target)); return; }
      const e = document.createElement('div'); e.className = 'msg' + (ev.kind === 'input' ? ' user' : '');
      e.textContent = ev.text || ev.kind; $('messages').append(e);
    });
    $('messages').scrollTop = $('messages').scrollHeight;
  } catch (error) { if (current === generation) report(error); }
  finally {
    if (current === generation) { polling = false; timer = setTimeout(poll, 500); }
  }
}
document.querySelector('.composer').onsubmit = e => { e.preventDefault(); const text = $('input').value.trim(); if (text) submit(text); };
$('new').onclick = () => { reset(null); $('title').textContent = '新对话'; list().catch(report); };
$('openFile').onclick = async () => {
  if (!thread) return;
  const path = $('filePath').value.trim(), current = generation, target = thread;
  if (!path) return;
  try { const result = await api('/api/threads/' + target + '/file?path=' + encodeURIComponent(path)); if (current === generation) $('file').textContent = result.content || ''; }
  catch (error) { if (current === generation) report(error); }
};
list().catch(report);
