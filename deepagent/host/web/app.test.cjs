const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
class Element {
  constructor(tag = 'div') { this.tag = tag; this.children = []; this.value = ''; this.textContent = ''; }
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.children = children; }
  querySelectorAll() { return this.children.flatMap(c => [...(['button', 'input'].includes(c.tag) ? [c] : []), ...c.querySelectorAll()]); }
}
const tick = () => new Promise(resolve => setImmediate(resolve));
function load(handler) {
  const elements = new Map();
  const get = id => { if (!elements.has(id)) elements.set(id, new Element()); return elements.get(id); };
  const timers = new Map(); let nextTimer = 0;
  const calls = [];
  const context = vm.createContext({
    document: {getElementById: get, querySelector: get, createElement: tag => new Element(tag)},
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

test('poll cursor preserves full int64 message identity', async () => {
  const app = load(url => url.includes('/events?') ? {data: [{sequence: '9007199254740993', kind: 'text', text: 'answer'}]} : undefined);
  await app.context.select('large-id'); await tick();
  await app.context.poll(); await tick();
  const requests = app.calls.filter(c => c.url.includes('/events?'));
  assert.equal(requests[1].url, '/api/threads/large-id/events?after=9007199254740993');
});
