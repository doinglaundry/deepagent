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
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this._text = ''; this.children = children; }
  setAttribute(name, value) { this.attributes[name] = value; }
  focus() {}
  querySelectorAll(selector = 'button,input,select,textarea') {
    const tags = selector.split(',');
    return this.children.flatMap(child => [...(tags.includes(child.tag) ? [child] : []), ...child.querySelectorAll(selector)]);
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
  assert.equal(app.get('messages').children.length, 1);
  assert.equal(app.get('messages').children[0].textContent, '你好！');
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
  const prompt = app.get('messages').children[0];
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
  const question = app.get('messages').children[1];
  assert.ok(question.querySelectorAll().every(control => control.disabled));
});


test('a planning question resumes in its original mode after history reload', async () => {
  const app = load(url => url.endsWith('/messages') ? {ok: false, data: {error: 'retry'}} : undefined);
  app.context.renderEvent({sequence: '1', kind: 'input', run_id: 'plan-run', text: '做一个计划', payload: {mode: 'plan'}}, 'thread');
  app.context.renderEvent({sequence: '2', kind: 'interrupt', run_id: 'plan-run', payload: {kind: 'follow_up', checkpoint_id: 'plan-run', interrupt_id: 'question', consumed_message_ids: ['1'], info: {question: '下一步？'}}}, 'thread');
  const box = app.get('messages').children[1];
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
  assert.equal(app.get('threads').children[0].textContent, '未命名任务');
});
