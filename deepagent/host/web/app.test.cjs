const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
class Element {
  constructor(tag = 'div') {
    this.tag = tag; this.children = []; this.value = ''; this._text = ''; this.dataset = {}; this.style = {};
    this.attributes = {}; this.classes = new Set();
    this.classList = {add: value => this.classes.add(value), remove: value => this.classes.delete(value), contains: value => this.classes.has(value), toggle: value => this.classes.has(value) ? this.classes.delete(value) : this.classes.add(value)};
  }
  get textContent() { return this._text + this.children.map(child => child.textContent).join(''); }
  set textContent(value) { this._text = value; this.children = []; }
  append(...children) { children.forEach(child => this.insertBefore(child, null)); }
  replaceChildren(...children) { this._text = ''; this.children = children; }
  insertBefore(child, before) {
    if (child.parentNode) child.parentNode.children.splice(child.parentNode.children.indexOf(child), 1);
    const index = this.children.indexOf(before);
    this.children.splice(index < 0 ? this.children.length : index, 0, child);
    child.parentNode = this;
  }
  setAttribute(name, value) { this.attributes[name] = value; }
  focus() {}
  querySelectorAll(selector = 'button,input,select,textarea') {
    const selectors = selector.split(',');
    return this.children.flatMap(child => {
      const matches = selectors.some(value => value.startsWith('.') ? value.slice(1).split('.').every(name => (child.className || '').split(' ').includes(name)) : value === child.tag);
      return [...(matches ? [child] : []), ...child.querySelectorAll(selector)];
    });
  }
}
const tick = () => new Promise(resolve => setImmediate(resolve));
function load(handler) {
  const elements = new Map();
  const get = id => { if (!elements.has(id)) elements.set(id, new Element()); return elements.get(id); };
  const timers = new Map(); let nextTimer = 0;
  const calls = [];
  const context = vm.createContext({
    document: {getElementById: get, querySelector: get, querySelectorAll: () => [], createElement: tag => new Element(tag), createTextNode: text => { const node = new Element('#text'); node.textContent = text; return node; }, body: new Element('body'), documentElement: new Element('html')},
    localStorage: {session: 'session'}, crypto: {randomUUID: () => 'session'},
    setTimeout: fn => { timers.set(++nextTimer, fn); return nextTimer; }, clearTimeout: id => timers.delete(id),
    fetch: async (url, options) => {
      calls.push({url, options});
      const result = await handler(url, options);
      return {ok: result?.ok !== false, status: result?.ok === false ? 409 : 200, json: async () => result?.data ?? []};
    }
  });
  vm.runInContext(fs.readFileSync(__dirname + '/i18n.js', 'utf8'), context);
  vm.runInContext(fs.readFileSync(__dirname + '/app.js', 'utf8'), context);
  return {context, get, timers, calls};
}

test('late poll response cannot cross conversations or start another polling chain', async () => {
  let release;
  const app = load(url => {
    if (url.startsWith('/api/threads/one/events')) return new Promise(resolve => release = resolve);
    if (url.startsWith('/api/threads/two/events')) return {data: [{sequence: 5, kind: 'input', text: 'second conversation'}]};
    if (url === '/api/threads/one' || url === '/api/threads/two') return {data: {title: url}};
  });
  await app.context.select('one');
  await app.context.select('two'); await tick();
  release({data: [{sequence: 99, kind: 'text', text: 'old response'}]}); await tick();
  assert.deepEqual(app.get('messages').children.map(e => e.textContent), ['second conversation']);
  assert.equal(app.get('messages').children[0].className, 'msg user');
  assert.equal(app.timers.size, 1);
  app.get('new').onclick(); await tick();
  assert.equal(app.timers.size, 0);
  assert.equal(app.get('messages').children.length, 0);
});

test('failed submission retains draft and exposes server error', async () => {
  const app = load((url, options) => {
    if (url === '/api/threads' && options?.method === 'POST') return {data: {id: 'one'}};
    if (url.endsWith('/messages')) return {ok: false, data: {error: 'thread is blocked'}};
  });
  app.get('input').value = 'keep this draft';
  await app.context.submit('keep this draft');
  assert.equal(app.get('input').value, 'keep this draft');
  assert.equal(app.get('status').textContent, 'thread is blocked');
  assert.equal(app.get('messages').children.length, 0);
});

test('follow-up shows typed question and options; failed resume remains retryable', async () => {
  const app = load(url => url.endsWith('/messages') ? {ok: false, data: {error: 'checkpoint unavailable'}} : undefined);
  const box = app.context.required({run_id: 'run'}, {
    kind: 'follow_up', checkpoint_id: 'checkpoint', interrupt_id: 'interrupt',
    info: {question: 'Which format?', questions: ['JSON', 'YAML']}
  }, 'original');
  assert.ok(box.children.some(e => e.textContent === 'Which format?'));
  assert.ok(box.children.some(e => e.textContent === 'JSON / YAML'));
  box.children.find(e => e.tag === 'input').value = 'YAML';
  await box.children.find(e => e.tag === 'button').onclick();
  const sent = app.calls.find(c => c.url.endsWith('/messages'));
  assert.equal(sent.url, '/api/threads/original/messages');
  const body = JSON.parse(sent.options.body).resume;
  assert.equal(body.run_id, 'run');
  assert.equal(body.checkpoint_id, 'checkpoint');
  assert.equal(body.interrupt_id, 'interrupt');
  assert.equal(body.interrupt.data.user_answer, 'YAML');
  assert.ok(box.querySelectorAll().every(e => !e.disabled));
  assert.equal(app.get('status').textContent, 'checkpoint unavailable');
});

test('parallel approvals submit all correlated answers together', async () => {
  const app = load(() => ({data: {}}));
  const box = app.context.required({run_id: 'run'}, {
    kind: 'batch', checkpoint_id: 'checkpoint', interrupt_id: 'first',
    items: [
      {kind: 'approve', interrupt_id: 'first', tool_name: 'write_file'},
      {kind: 'approve', interrupt_id: 'second', tool_name: 'execute'}
    ]
  }, 'thread');
  const first = box.children[1].children.find(e => e.tag === 'select');
  first.value = 'allow';
  const second = box.children[2].children.find(e => e.tag === 'select');
  second.value = 'deny';
  await box.children.find(e => e.tag === 'button').onclick();
  const body = JSON.parse(app.calls.find(c => c.url.endsWith('/messages')).options.body).resume;
  assert.equal(body.interrupt_id, 'first');
  assert.deepEqual(body.answers.map(a => a.interrupt_id), ['first', 'second']);
  assert.equal(body.answers[0].approval.approved, true);
  assert.equal(body.answers[1].approval.approved, false);
});

test('poll cursor preserves full int64 message identity', async () => {
  const app = load(url => url.includes('/events?') ? {data: [{sequence: '9007199254740993', kind: 'text', text: 'answer'}]} : undefined);
  await app.context.select('large-id'); await tick();
  await app.context.poll(); await tick();
  const requests = app.calls.filter(c => c.url.includes('/events?'));
  assert.equal(requests[1].url, '/api/threads/large-id/events?after=9007199254740993');
});

test('poll preserves reading position and only follows new messages near the bottom', async () => {
  let rows = [];
  const app = load(url => url.includes('/events?') ? {data: rows} : undefined);
  await app.context.select('scroll'); await tick();
  const messages = app.get('messages');
  messages.clientHeight = 400;
  messages.scrollHeight = 2000;
  messages.scrollTop = 200;
  await app.context.poll();
  assert.equal(messages.scrollTop, 200, 'empty poll must not move the reader');
  rows = [{sequence: '1', kind: 'assistant', text: 'new answer'}];
  await app.context.poll();
  assert.equal(messages.scrollTop, 200, 'new messages must not interrupt reading history');
  messages.scrollTop = 1580;
  rows = [];
  await app.context.poll();
  assert.equal(messages.scrollTop, 1580, 'empty poll must not snap to the bottom');
  rows = [{sequence: '2', kind: 'assistant', text: 'next answer'}];
  await app.context.poll();
  assert.equal(messages.scrollTop, 2000, 'follow new messages when near the bottom');
});


test('live chunks and durable final answer render once without replay duplication', () => {
  const app = load(() => undefined);
  app.context.renderEvent({run_id: 'run', kind: 'assistant_delta', payload: {llm_response_id: 'response', delta: '你'}}, 'thread');
  app.context.renderEvent({run_id: 'run', kind: 'assistant_delta', payload: {llm_response_id: 'response', delta: '好'}}, 'thread');
  app.context.renderEvent({sequence: '10', run_id: 'run', kind: 'assistant', text: '你好！', payload: {llm_response_id: 'response'}}, 'thread');
  app.context.renderEvent({run_id: 'run', kind: 'assistant_delta', payload: {llm_response_id: 'response', delta: '好'}}, 'thread');
  const replies = app.get('messages').querySelectorAll('.msg.assistant');
  assert.equal(replies.length, 1);
  assert.equal(replies[0].textContent, '你好！');
});

test('failed resume restores select and textarea controls as well as buttons', async () => {
  const app = load(url => url.endsWith('/messages') ? {ok: false, data: {error: 'retry'}} : undefined);
  const box = app.context.required({run_id: 'run'}, {kind: 'batch', checkpoint_id: 'c', interrupt_id: 'i', items: [{kind: 'approve', interrupt_id: 'i', tool_name: 'execute'}]}, 'thread');
  const controls = box.querySelectorAll();
  await box.children.find(e => e.tag === 'button').onclick();
  assert.ok(controls.every(e => !e.disabled));
});


test('replaying a completed resume disables its original question', () => {
  const app = load(() => undefined);
  app.context.renderEvent({sequence: '1', run_id: 'run', kind: 'interrupt', payload: {kind: 'follow_up', checkpoint_id: 'run', interrupt_id: 'question', info: {question: '颜色？'}}}, 'thread');
  app.context.renderEvent({sequence: '2', run_id: 'run', kind: 'resume_run', payload: {run_id: 'run', checkpoint_id: 'run', interrupt_id: 'question', interrupt: {kind: 'follow_up', data: {user_answer: '蓝色'}}}}, 'thread');
  const prompt = app.get('messages').querySelectorAll('.prompt')[0];
  assert.ok(prompt.querySelectorAll().every(control => control.disabled));
});


test('planning selection is sent with the message instead of silently using execution mode', async () => {
  const app = load((url, options) => url === '/api/threads' && options?.method === 'POST' ? {data: {id: 'plan-thread'}} : undefined);
  app.get('input').value = '准备一个计划'; app.get('mode').value = 'plan';
  await app.context.submit('准备一个计划');
  const message = app.calls.find(call => call.url.endsWith('/messages'));
  assert.equal(JSON.parse(message.options.body).mode, 'plan');
});


test('finished runs cannot expose old questions while another run is blocked', () => {
  const app = load(() => undefined);
  app.context.renderEvent({sequence: '1', run_id: 'old', kind: 'input', status: 'interrupted', text: '旧任务'}, 'thread');
  app.context.renderEvent({sequence: '2', run_id: 'old', kind: 'interrupt', payload: {checkpoint_id: 'old', interrupt_id: 'old-question', kind: 'follow_up', info: {question: '旧问题'}}}, 'thread');
  const question = app.get('messages').querySelectorAll('.prompt')[0];
  assert.ok(question.querySelectorAll().every(control => control.disabled));
});


test('a planning question resumes in its original mode after history reload', async () => {
  const app = load(url => url.endsWith('/messages') ? {ok: false, data: {error: 'retry'}} : undefined);
  app.context.renderEvent({sequence: '1', kind: 'input', run_id: 'plan-run', text: '做一个计划', payload: {mode: 'plan'}}, 'thread');
  app.context.renderEvent({sequence: '2', kind: 'interrupt', run_id: 'plan-run', payload: {kind: 'follow_up', checkpoint_id: 'plan-run', interrupt_id: 'question', consumed_message_ids: ['1'], info: {question: '下一步？'}}}, 'thread');
  const box = app.get('messages').querySelectorAll('.prompt')[0];
  box.children.find(child => child.tag === 'input').value = '继续';
  await box.children.find(child => child.tag === 'button').onclick();
  const request = app.calls.find(call => call.url.endsWith('/messages'));
  assert.equal(JSON.parse(request.options.body).mode, 'plan');
});

test('history pages finish loading before live updates can overtake older messages', async () => {
  const events = Array.from({length: 201}, (_, index) => ({sequence: String(index + 1), kind: 'input', text: '消息 ' + (index + 1)}));
  const app = load(url => {
    if (url.includes('/events?')) {
      const after = BigInt(url.split('after=')[1]);
      return {data: events.filter(event => BigInt(event.sequence) > after).slice(0, 200)};
    }
    if (url === '/api/threads/history') return {data: {title: 'history'}};
  });
  await app.context.select('history'); await tick();
  assert.equal(app.get('messages').children.length, 201);
  assert.equal(app.get('messages').children[200].textContent, '消息 201');
  assert.ok(app.calls.some(call => call.url.endsWith('after=200')));
});


test('untitled task names never fall back to internal database IDs', async () => {
  const app = load(url => url.startsWith('/api/threads?') ? {data: [{id: '2000000000000000001', title: ''}]} : undefined);
  await tick();
  assert.equal(app.get('threads').querySelectorAll('.task-name')[0].textContent, '未命名任务');
});


test('office reflects runtime state and only offers stop during active execution', () => {
  const app = load(() => undefined);
  app.context.showThread({id: 'one', title: '读代码', status: 'running'});
  assert.equal(app.get('office').dataset.status, 'running');
  assert.equal(app.get('agentStatus').textContent, '正在处理任务');
  app.context.showThread({id: 'one', status: 'blocked'});
  assert.equal(app.get('agentStatus').textContent, '需要你的回复');
  assert.equal(app.get('stop').disabled, true);
});

test('office opens one notebook at the selected destination', () => {
  const app = load(() => undefined);
  app.get('notebook').hidden = true;
  app.context.showPanel('Output');
  assert.equal(app.get('notebook').hidden, false);
  assert.equal(app.get('panelOutput').hidden, false);
  assert.equal(app.get('panelTalk').hidden, true);
  assert.equal(app.get('panelTasks').hidden, true);
});


test('hidden conversation does not opt into automatic scrolling', () => {
  const app = load(() => undefined);
  app.get('messages').clientHeight = 400;
  app.get('messages').scrollHeight = 2000;
  app.get('messages').scrollTop = 1580;
  app.get('panelTalk').hidden = true;
  assert.equal(app.context.isNearBottom(), false);
});


test('returning to conversation follows latest only when the reader previously followed it', () => {
  const app = load(() => undefined);
  const messages = app.get('messages');
  messages.clientHeight = 400; messages.scrollHeight = 2000; messages.scrollTop = 1600;
  app.get('notebook').hidden = false; app.get('panelTalk').hidden = false;
  app.context.showPanel('Output');
  messages.scrollHeight = 2500;
  app.context.showPanel('Talk');
  assert.equal(messages.scrollTop, 2500);
  messages.scrollTop = 100;
  app.context.showPanel('File');
  messages.scrollHeight = 3000;
  app.context.showPanel('Talk');
  assert.equal(messages.scrollTop, 100);
});

test('task board only presents a successful current Run as completed', () => {
  const app = load(() => undefined);
  app.context.renderEvent({sequence: '1', run_id: 'old-run', kind: 'assistant', text: '旧成果'}, 'thread');
  app.context.showThread({status: 'idle', run_id: 'new-run', run_status: 'failed'});
  assert.equal(app.get('deliverable').hidden, true, 'failure must not reuse an old result');
  assert.equal(app.get('boardTitle').textContent, '这次任务没有完成');
  app.context.showThread({status: 'idle', run_id: 'new-run', run_status: 'interrupted'});
  assert.equal(app.get('deliverable').hidden, true, 'interruption must not look successful');
  assert.equal(app.get('boardTitle').textContent, '这次任务已停止');
  app.context.showThread({status: 'idle', run_id: 'new-run', run_status: 'finished'});
  assert.equal(app.get('deliverable').hidden, true, 'an old Run cannot supply the current result');
  app.context.renderEvent({sequence: '2', run_id: 'new-run', kind: 'assistant', text: '新成果'}, 'thread');
  app.context.showThread({status: 'idle', run_id: 'new-run', run_status: 'finished'});
  assert.equal(app.get('deliverable').hidden, false);
  assert.equal(app.get('resultPreview').textContent, '新成果');
});


test('work log separates actual Runs and keeps appended inputs in the same round', () => {
  const app = load(() => undefined);
  app.context.renderEvent({sequence: '1', kind: 'input', run_id: 'first', text: '第一轮任务'}, 'thread');
  app.context.renderEvent({sequence: '2', kind: 'assistant', run_id: 'first', text: '第一轮成果'}, 'thread');
  app.context.renderEvent({sequence: '3', kind: 'input', run_id: 'second', text: '第二轮任务'}, 'thread');
  app.context.renderEvent({sequence: '4', kind: 'input', run_id: 'second', text: '补充第二轮'}, 'thread');
  const headings = app.get('messages').querySelectorAll('.round-heading');
  assert.deepEqual(headings.map(element => element.textContent), ['第 1 轮', '第 2 轮']);
  assert.equal(app.get('messages').querySelectorAll('.msg.user').length, 3);
});

test('a live response groups its already stored pending input before the reply without duplicate headings', () => {
  const app = load(() => undefined);
  app.context.renderEvent({sequence: '1', kind: 'input', text: '待领取的任务'}, 'thread');
  app.context.renderEvent({kind: 'assistant_delta', run_id: 'run', payload: {llm_response_id: 'reply', consumed_message_ids: ['1'], delta: '开始'}}, 'thread');
  app.context.renderEvent({sequence: '2', kind: 'assistant', run_id: 'run', text: '完成', payload: {llm_response_id: 'reply', consumed_message_ids: ['1']}}, 'thread');
  const entries = app.get('messages').children[0].children;
  assert.equal(entries[0].className, 'round-heading');
  assert.equal(entries[1].textContent, '待领取的任务');
  assert.equal(entries[2].textContent, '完成');
  assert.equal(entries.length, 3);
});


test('conversation shows compact tool activity that opens the matching details in Tools', () => {
  const app = load(() => undefined);
  app.context.renderEvent({run_id: 'run', kind: 'tool_call', payload: {tool_call_id: 'call', tool_name: 'read_file', status: 'started', arguments_json: '{"path":"README.md"}'}}, 'thread');
  app.context.renderEvent({sequence: '2', run_id: 'run', kind: 'tool', payload: {tool_call_id: 'call', tool_name: 'read_file', status: 'finished', result_json: 'file contents'}}, 'thread');
  app.context.renderEvent({run_id: 'run', kind: 'tool_call', payload: {tool_call_id: 'call', tool_name: 'read_file', status: 'started'}}, 'thread');
  const links = app.get('messages').querySelectorAll('.tool-link');
  const details = app.get('toolOutput').querySelectorAll('.tool-record');
  assert.equal(links.length, 1);
  assert.equal(app.get('messages').querySelectorAll('.tool-record').length, 0);
  assert.ok(links[0].textContent.includes('README.md'));
  assert.ok(links[0].textContent.includes('已完成'));
  assert.ok(!app.get('messages').textContent.includes('file contents'));
  assert.equal(details.length, 1);
  assert.ok(details[0].textContent.includes('file contents'));
  assert.equal(details[0].open, undefined);
  let scrolled = false;
  details[0].scrollIntoView = () => scrolled = true;
  links[0].onclick();
  assert.equal(app.get('panelOutput').hidden, false);
  assert.equal(app.get('panelTalk').hidden, true);
  assert.equal(details[0].open, true);
  assert.equal(scrolled, true);
});

test('live activity follows model, tool, reply and blocked events; finished Runs hide it', () => {
  const app = load(() => undefined);
  app.context.setRunStatus('running', 'run');
  app.context.renderEvent({run_id: 'run', kind: 'agent_activity', payload: {phase: 'thinking'}}, 'thread');
  assert.equal(app.get('activity').textContent, '正在思考');
  assert.equal(app.get('activity').hidden, false);
  app.context.renderEvent({run_id: 'run', kind: 'tool_call', payload: {tool_call_id: 'call', tool_name: 'execute', status: 'started', arguments_json: '{"command":"pwd"}'}}, 'thread');
  assert.equal(app.get('activity').textContent, '正在调用工具');
  app.context.renderEvent({run_id: 'run', kind: 'agent_activity', payload: {phase: 'thinking'}}, 'thread');
  assert.equal(app.get('activity').textContent, '正在思考');
  app.context.renderEvent({run_id: 'run', kind: 'assistant_delta', payload: {llm_response_id: 'reply', delta: '结果'}}, 'thread');
  assert.equal(app.get('activity').textContent, '正在回复');
  app.context.setRunStatus('running', 'run');
  assert.equal(app.get('activity').textContent, '正在回复', 'polling the same Run must not lose its phase');
  app.context.renderEvent({run_id: 'run', kind: 'run_status', payload: {status: 'blocked'}}, 'thread');
  assert.equal(app.get('activity').textContent, '等待你的回复');
  app.context.renderEvent({run_id: 'run', kind: 'run_status', payload: {status: 'started'}}, 'thread');
  app.context.renderEvent({run_id: 'run', kind: 'assistant_delta', payload: {thinking_content_delta: 'private reasoning'}}, 'thread');
  assert.equal(app.get('activity').textContent, '正在思考');
  assert.ok(!app.get('messages').textContent.includes('private reasoning'));
  app.context.renderEvent({run_id: 'run', kind: 'run_status', payload: {status: 'finished'}}, 'thread');
  assert.equal(app.get('activity').hidden, true);
});

test('history, ended Runs and other Runs cannot overwrite current live activity', () => {
  const app = load(() => undefined);
  app.context.setRunStatus('running', 'current');
  app.context.renderEvent({run_id: 'current', kind: 'agent_activity', payload: {phase: 'thinking'}}, 'thread');
  app.context.renderEvent({sequence: '1', run_id: 'current', kind: 'tool', payload: {tool_call_id: 'past', tool_name: 'execute', status: 'finished'}}, 'thread');
  app.context.renderEvent({run_id: 'old', kind: 'tool_call', payload: {tool_call_id: 'old', tool_name: 'execute', status: 'started'}}, 'thread');
  assert.equal(app.get('activity').textContent, '正在思考');
  app.context.renderEvent({run_id: 'current', kind: 'run_status', payload: {status: 'finished'}}, 'thread');
  app.context.renderEvent({run_id: 'current', kind: 'agent_activity', payload: {phase: 'thinking'}}, 'thread');
  assert.equal(app.get('activity').hidden, true);
  app.context.setRunStatus('closed', 'current');
  app.context.renderEvent({run_id: 'next', kind: 'run_status', payload: {status: 'started'}}, 'thread');
  assert.equal(app.get('activity').hidden, true);
  assert.equal(app.get('runStatus').dataset.status, 'closed');
});

test('appended inputs retain their position after earlier output in the same Run', () => {
  const app = load(() => undefined);
  app.context.renderEvent({sequence: '1', run_id: 'run', kind: 'input', text: '初始输入'}, 'thread');
  app.context.renderEvent({sequence: '2', run_id: 'run', kind: 'assistant', text: '初次回答'}, 'thread');
  app.context.renderEvent({sequence: '3', run_id: 'run', kind: 'input', text: '追加输入'}, 'thread');
  assert.deepEqual(app.get('messages').children[0].children.map(element => element.textContent), ['第 1 轮', '初始输入', '初次回答', '追加输入']);
});

test('late pending input is placed before the live response that consumed it', () => {
  const app = load(() => undefined);
  app.context.renderEvent({run_id: 'run', kind: 'assistant_delta', payload: {llm_response_id: 'reply', consumed_message_ids: ['1'], delta: '答复'}}, 'thread');
  app.context.renderEvent({sequence: '1', kind: 'input', text: '原始问题'}, 'thread');
  const entries = app.get('messages').children[0].children;
  assert.deepEqual(entries.map(element => element.textContent), ['第 1 轮', '原始问题', '答复']);
});


test('always allow submits an explicit per-tool grant with the original interrupt identity', async () => {
  const app = load(() => ({data: {}}));
  const box = app.context.required({run_id: 'run'}, {kind: 'approval', checkpoint_id: 'checkpoint', interrupt_id: 'call', tool_name: 'execute'}, 'thread');
  await box.children.find(element => element.textContent === '始终允许此工具').onclick();
  const resume = JSON.parse(app.calls.find(call => call.url.endsWith('/messages')).options.body).resume;
  assert.equal(resume.run_id, 'run'); assert.equal(resume.interrupt_id, 'call');
  assert.equal(resume.approval.approved, true); assert.equal(resume.approval.always_allow, true);
});

test('English interface labels do not translate user messages or model replies', () => {
  const app = load(() => undefined);
  app.context.setLanguage('en');
  const box = app.context.required({run_id: 'run'}, {kind: 'approval', tool_name: 'execute'}, 'thread');
  assert.equal(box.children[0].textContent, 'Approval needed');
  assert.ok(box.children.some(element => element.textContent === 'Always allow this tool'));
  app.context.renderEvent({sequence: '1', run_id: 'run', kind: 'input', text: '原始中文任务'}, 'thread');
  app.context.renderEvent({sequence: '2', run_id: 'run', kind: 'assistant', text: '原始中文回答'}, 'thread');
  assert.equal(app.get('messages').querySelectorAll('.msg.user')[0].textContent, '原始中文任务');
  assert.equal(app.get('messages').querySelectorAll('.msg.assistant')[0].textContent, '原始中文回答');
});


test('task list groups children beneath their parent and distinguishes task types', async () => {
  const rows = [
    {id: 'child', parent_thread_id: 'main', title: '代码分析', status: 'closed'},
    {id: 'other', title: '另一个主任务', status: 'idle'},
    {id: 'main', title: '理解项目', status: 'idle'},
    {id: 'nested', parent_thread_id: 'child', title: '子任务的子任务', status: 'idle'}
  ];
  const app = load(url => url.startsWith('/api/threads?') ? {data: rows} : undefined);
  await app.context.list();
  const buttons = app.get('threads').children;
  assert.deepEqual(buttons.map(button => button.dataset.id), ['other', 'main', 'child', 'nested']);
  assert.deepEqual(buttons.map(button => button.dataset.depth), ['0', '0', '1', '2']);
  assert.ok(buttons[1].textContent.includes('主任务'));
  assert.ok(buttons[2].textContent.includes('子任务'));
  assert.ok(buttons[2].textContent.includes('已关闭'));
});

test('searching for a child retains its parent context without unrelated tasks', async () => {
  const rows = [
    {id: 'child', parent_thread_id: 'main', title: 'HTTP 分析', status: 'closed'},
    {id: 'sibling', parent_thread_id: 'main', title: '沙箱分析', status: 'closed'},
    {id: 'main', title: '理解项目', status: 'idle'},
    {id: 'other', title: '另一个任务', status: 'idle'}
  ];
  const app = load(url => url.startsWith('/api/threads?') ? {data: rows} : undefined);
  await app.context.list();
  app.get('search').value = 'HTTP'; app.context.showTasks();
  assert.deepEqual(app.get('threads').children.map(button => button.dataset.id), ['main', 'child']);
});

test('closing and closed tasks hide the composer and cannot submit even with a draft', async () => {
  const app = load(() => undefined);
  app.get('input').value = '保留这条草稿';
  for (const status of ['closing', 'closed']) {
    app.context.showThread({id: 'child', parent_thread_id: 'main', status});
    assert.equal(app.get('.composer').hidden, true);
    assert.equal(app.get('.composer-note').hidden, true);
    assert.equal(app.get('send').disabled, true);
    await app.context.submit('保留这条草稿');
  }
  assert.equal(app.calls.filter(call => call.options?.method === 'POST').length, 0);
  assert.equal(app.get('input').value, '保留这条草稿');
  app.context.showThread({id: 'main', status: 'idle'});
  assert.equal(app.get('.composer').hidden, false);
  assert.equal(app.get('send').disabled, false);
});

test('a closed child can return to its parent and restore the composer', async () => {
  const app = load(url => {
    if (url === '/api/threads/child') return {data: {id: 'child', parent_thread_id: 'main', status: 'closed'}};
    if (url === '/api/threads/main') return {data: {id: 'main', title: '主任务', status: 'idle'}};
  });
  await app.context.select('child'); await tick();
  assert.equal(app.get('taskContext').hidden, false);
  assert.equal(app.get('.composer').hidden, true);
  await app.get('parentTask').onclick(); await tick();
  assert.equal(app.context.localStorage.selectedThread, 'main');
  assert.equal(app.get('taskContext').hidden, true);
  assert.equal(app.get('.composer').hidden, false);
});


test('changed files come only from completed successful mutations and survive replay', () => {
  const app = load(() => undefined);
  app.context.reset('one');
  app.context.showThread({work_dir: '/project'});
  const event = (id, tool, path, result, extra = {}) => ({sequence:'1', kind: 'tool', run_id: 'run', payload: {
    tool_call_id: id, tool_name: tool, arguments_json: JSON.stringify({path}), result_json: result, status: 'finished', ...extra
  }});
  app.context.renderEvent(event('read', 'read_file', 'README.md', 'contents'), 'one');
  app.context.renderEvent(event('pending', 'write_file', 'pending.md', 'wrote pending.md', {status:'started'}), 'one');
  app.context.renderEvent(event('error', 'write_file', 'failed.md', 'wrote failed.md', {is_error:true}), 'one');
  app.context.renderEvent(event('denied', 'write_file', 'denied.md', 'tool denied by user'), 'one');
  app.context.renderEvent(event('missing', 'delete_file', 'missing.md', 'File does not exist: missing.md'), 'one');
  assert.equal(app.get('changedFiles').children.length, 0);
  const write = event('write', 'write_file', '/project/src/a.go', 'wrote /project/src/a.go');
  app.context.renderEvent(write, 'one'); app.context.renderEvent(write, 'one');
  app.context.renderEvent(event('edit', 'edit_file', './src/a.go', 'edited ./src/a.go'), 'one');
  const links = app.get('changedFiles').querySelectorAll('a');
  assert.equal(links.length, 1);
  assert.equal(links[0].textContent, 'src/a.go');
  assert.ok(app.get('changedFiles').textContent.includes('修改'));
  app.context.reset('two');
  assert.equal(app.get('changedFiles').children.length, 0);
  assert.equal(app.get('file').hidden, true);
});

test('patch results list added, modified, deleted and moved files without guessing failed patches', () => {
  const app = load(() => undefined);
  app.context.reset('one');
  const patch = '*** Begin Patch\n*** Update File: old.md\n*** Move to: new.md\n@@\n-old\n+new\n*** End Patch';
  const event = {sequence:'1', kind:'tool', run_id:'run', payload:{tool_call_id:'patch', tool_name:'apply_patch',
    status:'finished', arguments_json:JSON.stringify({patch}), result_json:'A added.md\nM src/a.go\nD removed.md\nM new.md'}};
  app.context.renderEvent(event, 'one');
  app.context.renderEvent(event, 'one');
  const rows = app.get('changedFiles').children;
  assert.equal(rows.length, 5);
  assert.equal(app.get('changedFiles').querySelectorAll('a').length, 3);
  assert.ok(rows.some(row => row.textContent === '删除old.md'));
  assert.ok(rows.some(row => row.textContent === '删除removed.md'));
  app.context.renderEvent({...event, payload:{...event.payload, tool_call_id:'failed', is_error:true, result_json:'A failed.md'}}, 'one');
  assert.equal(app.get('changedFiles').children.length, 5);
});

test('changed-file links load the current file, clear stale previews and ignore an old task response', async () => {
  let release;
  const app = load(url => {
    if (url.includes('/file?path=src%2Fa.go')) return {data:{path:'src/a.go', content:'new content'}};
    if (url.includes('/file?path=old.md')) return new Promise(resolve => release = resolve);
    if (url.includes('/file?path=missing.md')) return {ok:false, data:{error:'file no longer exists'}};
  });
  app.context.reset('one');
  for (const path of ['src/a.go', 'old.md', 'missing.md']) app.context.renderEvent({sequence:'1', kind:'tool', run_id:'run', payload:{
    tool_call_id:path, tool_name:'write_file', status:'finished', arguments_json:JSON.stringify({path}), result_json:'wrote '+path
  }}, 'one');
  const links = app.get('changedFiles').querySelectorAll('a');
  await links[0].onclick({preventDefault(){}});
  assert.equal(app.get('file').textContent, 'new content');
  assert.equal(app.get('file').hidden, false);
  await links[2].onclick({preventDefault(){}});
  assert.equal(app.get('file').textContent, '');
  assert.equal(app.get('file').hidden, true);
  assert.equal(app.get('fileStatus').textContent, 'file no longer exists');
  const pending = links[1].onclick({preventDefault(){}});
  app.context.reset('two');
  release({data:{path:'old.md', content:'wrong task'}}); await pending;
  assert.equal(app.get('file').textContent, '');
  assert.equal(app.get('changedFiles').children.length, 0);
});

test('live tool completion updates files even when the saved tool row stays behind the poll cursor', () => {
  const app = load(() => undefined);
  app.context.reset('one');
  const event = (id, tool, result, sequence) => ({kind:sequence ? 'tool':'tool_call', sequence, run_id:'run', payload:{
    tool_call_id:id, tool_name:tool, arguments_json:'{"path":"a.md"}', result_json:result, status:'finished'
  }});
  app.context.renderEvent({...event('old','delete_file','', '1'),payload:{...event('old','delete_file','', '1').payload,status:'started'}}, 'one');
  app.context.renderEvent(event('new','write_file','wrote a.md'), 'one');
  assert.equal(app.get('changedFiles').querySelectorAll('a').length,1);
  app.context.renderEvent(event('old','delete_file','Deleted file a.md','1'), 'one');
  app.context.renderEvent(event('new','write_file','wrote a.md','2'), 'one');
  assert.equal(app.get('changedFiles').children.length,1);
  assert.equal(app.get('changedFiles').querySelectorAll('a').length,1, 'older saved completion must not undo a later live write');
});

test('loading old questions cannot reopen a closed task or show its composer', () => {
  const app = load(() => undefined);
  app.context.reset('child');
  app.context.showThread({id:'child', parent_thread_id:'main', status:'closed'});
  app.context.renderEvent({sequence:'1', run_id:'old-run', kind:'approval', payload:{
    kind:'approval', interrupt_id:'old-approval', checkpoint_id:'old-run', tool_name:'write_file'
  }}, 'child');
  assert.equal(app.get('runStatus').dataset.status,'closed');
  assert.equal(app.get('.composer').hidden,true);
  assert.ok(app.get('messages').querySelectorAll('.prompt')[0].querySelectorAll().every(control => control.disabled));
});


test('live blocked status updates activity without creating a duplicate generic question', () => {
  const app = load(() => undefined);
  app.context.setRunStatus('running', 'run');
  app.context.renderEvent({run_id: 'run', kind: 'run_status', payload: {status: 'blocked', checkpoint_id: 'run', interrupt_id: 'approval'}}, 'thread');
  assert.equal(app.get('messages').querySelectorAll('.prompt').length, 0);
  assert.equal(app.get('activity').textContent, '等待你的回复');
  app.context.renderEvent({sequence: '3', run_id: 'run', kind: 'approval', payload: {kind: 'approval', checkpoint_id: 'run', interrupt_id: 'approval', tool_name: 'execute', arguments_json: '{"command":"pwd"}'}}, 'thread');
  const prompts = app.get('messages').querySelectorAll('.prompt');
  assert.equal(prompts.length, 1);
  assert.ok(prompts[0].textContent.includes('execute'));
  assert.ok(prompts[0].querySelectorAll('button').some(button => button.textContent === '允许'));
});

test('poll connection failure clears after recovery without hiding a failed submission', async () => {
  let disconnected = true;
  const app = load(url => {
    if (url.includes('/events?') && disconnected) throw new TypeError('Failed to fetch');
  });
  await app.context.select('network'); await tick();
  assert.equal(app.get('status').hidden, false);
  assert.equal(app.get('status').textContent, '本地服务暂时无法连接，正在重试…');
  disconnected = false;
  await app.context.poll();
  assert.equal(app.get('status').hidden, true);
  app.context.report({message: 'submission denied'});
  await app.context.poll();
  assert.equal(app.get('status').textContent, 'submission denied');
  assert.equal(app.get('status').hidden, false);
});
