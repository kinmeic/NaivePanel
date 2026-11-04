const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const core = require('../ui/static/agentcore-chat-core.js');
class Node {
  constructor(tag = '') { this.tag = tag; this.children = []; this.attrs = {}; this.events = {}; this.dataset = {}; this._value = ''; this.style = { setProperty() {} }; this.classList = { contains: name => (this.className || '').split(/\s+/).includes(name), toggle: (name, force) => { const classes = new Set((this.className || '').split(/\s+/).filter(Boolean)); const add = force === undefined ? !classes.has(name) : force; if (add) classes.add(name); else classes.delete(name); this.className = [...classes].join(' '); return add; } }; this.scrollHeight = 0; this.scrollTop = 0; this.clientHeight = 0; }
  set value(value) { this._value = String(value); }
  get value() { return this.tag === 'select' && !this.children.some(n => n.value === this._value) ? '' : this._value; }
  set textContent(value) { this.text = String(value); this.children = []; }
  get textContent() { return (this.text || '') + this.children.map(n => n.textContent).join(''); }
  appendChild(n) { if (n.tag === '#fragment') { for (const c of n.children) this.appendChild(c); return n; } n.parent = this; this.children.push(n); return n; }
  replaceChildren(...nodes) { this.text = ''; this.children = []; nodes.forEach(n => this.appendChild(n)); }
  remove() { if (this.parent) this.parent.children = this.parent.children.filter(n => n !== this); }
  setAttribute(k, v) { this.attrs[k] = v; }
  addEventListener(k, f) { this.events[k] = f; }
  querySelectorAll(selector) { return this.children.flatMap(n => ((selector[0] === '.' ? n.classList.contains(selector.slice(1)) : n.tag === selector) ? [n] : []).concat(n.querySelectorAll(selector))); }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
  focus() {}
}
function documentMock() { return { addEventListener() {}, createElementNS: (_, t) => new Node(t), createElement: t => new Node(t), createTextNode: t => { const n = new Node('#text'); n.textContent = t; return n; }, createDocumentFragment: () => new Node('#fragment') }; }
const tick = () => new Promise(resolve => setImmediate(resolve));
test('SSE handles BOM, comments, split CRLF, multiline data and EOF', () => {
  const got = [], parser = core.sse((e, name) => got.push([e, name]));
  const text = '\uFEFF: ping\r\nevent: chunk\r\ndata: {"type":"chunk",\r\ndata: "content":"你好"}\r\n\r\ndata: {"type":"done"}';
  for (const char of text) parser(char, false);
  parser('', true);
  assert.deepEqual(got, [[{ type: 'chunk', content: '你好' }, 'chunk'], [{ type: 'done' }, '']]);
});
test('SSE rejects corrupt JSON rather than silently losing events', () => { assert.throws(() => core.sse(() => {})('data: nope\n\n'), SyntaxError); });
test('Markdown renders HTML as text, blocks executable URLs and copies exact code', async () => {
  const doc = documentMock(); let copied;
  const result = core.markdown(doc, '# Title\n<script>alert(1)</script>\n[bad](javascript:alert) [good](https://example.org)\n```js\n<img onerror=x>\n```', async text => { copied = text; });
  assert.equal(result.querySelectorAll('script').length, 0);
  assert.equal(result.querySelectorAll('img').length, 0);
  assert.equal(result.querySelectorAll('a').length, 1);
  assert.equal(result.querySelectorAll('a')[0].attrs.rel, 'noopener noreferrer');
  assert.match(result.textContent, /<script>/);
  await result.querySelectorAll('button')[0].events.click();
  assert.equal(copied, '<img onerror=x>');
  for (const url of ['javascript:alert(1)', 'data:text/html,x', 'vbscript:x', 'java\nscript:alert']) assert.equal(core.safeURL(url), null);
});
test('Markdown groups paragraphs, soft/hard breaks, quotes, headings and thematic rules', () => {
  const result = core.markdown(documentMock(), 'first line\nsecond line  \nhard break\\\nanother\n\nnext paragraph\n\n> quote **bold**\n>\n> - quoted item\n> > nested quote\n\n---\n\nSetext\n===', () => {});
  assert.equal(result.children[0].tag, 'p');
  assert.equal(result.children[0].textContent, 'first line second linehard breakanother');
  assert.equal(result.children[0].querySelectorAll('br').length, 2);
  assert.equal(result.children[1].textContent, 'next paragraph');
  assert.equal(result.querySelectorAll('blockquote').length, 2);
  assert.equal(result.querySelectorAll('ul').length, 1);
  assert.equal(result.querySelectorAll('hr').length, 1);
  assert.equal(result.querySelectorAll('h1')[0].textContent, 'Setext');
});
test('Markdown renders ordered, unordered, nested and task lists as semantic DOM', () => {
  const result = core.markdown(documentMock(), '3. third\n4. fourth\n   - nested **bold**\n   - [x] done\n     1. child\n\n- [ ] pending\n- ordinary\n  continued line\n\nOutside', () => {});
  const ordered = result.querySelectorAll('ol');
  assert.equal(ordered.length, 2); assert.equal(ordered[0].attrs.start, '3');
  assert.equal(result.querySelectorAll('ul').length, 2);
  assert.equal(result.querySelectorAll('li').length, 7);
  const checks = result.querySelectorAll('input');
  assert.equal(checks.length, 2); assert.equal(checks[0].checked, true); assert.equal(checks[1].checked, false);
  assert.ok(checks.every(c => c.disabled && c.type === 'checkbox'));
  assert.match(result.textContent, /ordinary continued line/);
  assert.equal(result.children.at(-1).tag, 'p'); assert.equal(result.children.at(-1).textContent, 'Outside');
});
test('GFM tables support alignment, inline formatting, escaped pipes and code pipes', () => {
  const result = core.markdown(documentMock(), '| Name | Value | Notes |\n| :--- | ---: | :---: |\n| **bold** | `a|b` | escaped \\| pipe |\n| missing |  | ~~old~~ |\n\nAfter', () => {});
  assert.equal(result.querySelectorAll('table').length, 1);
  const th = result.querySelectorAll('th'); assert.equal(th.length, 3);
  assert.deepEqual(th.map(c => c.style.textAlign), ['left', 'right', 'center']);
  const td = result.querySelectorAll('td'); assert.equal(td.length, 6);
  assert.equal(td[0].querySelectorAll('strong')[0].textContent, 'bold');
  assert.equal(td[1].querySelectorAll('code')[0].textContent, 'a|b');
  assert.equal(td[2].textContent, 'escaped | pipe');
  assert.equal(td[5].querySelectorAll('del')[0].textContent, 'old');
  assert.equal(result.children.at(-1).textContent, 'After');
});
test('nested emphasis, deletion, escapes and code spans remain distinct', () => {
  const result = core.markdown(documentMock(), '**bold *italic*** and *italic **bold*** and ***both*** and ~~old **bold**~~\n\n\\*literal\\* snake_case_word `**literal**` ``a ` b``', () => {});
  const p = result.children[0];
  assert.equal(p.children[0].tag, 'strong'); assert.equal(p.children[0].querySelectorAll('em')[0].textContent, 'italic');
  assert.equal(p.children[2].tag, 'em'); assert.equal(p.children[2].querySelectorAll('strong')[0].textContent, 'bold');
  assert.equal(p.children[4].querySelectorAll('em')[0].textContent, 'both');
  assert.equal(p.querySelectorAll('del')[0].querySelectorAll('strong')[0].textContent, 'bold');
  assert.match(result.children[1].textContent, /\*literal\* snake_case_word/);
  assert.deepEqual(result.querySelectorAll('code').map(c => c.textContent), ['**literal**', 'a ` b']);
});
test('list continuation paragraphs, loose items and fenced code preserve structure and copying', async () => {
  let copied;
  const result = core.markdown(documentMock(), '- first\nlazy continuation\n\n  second paragraph\n\n- code\n  ```js\n  const x = "<script>";\n  ```\n\nAfter', text => { copied = text; });
  assert.equal(result.children[0].tag, 'ul');
  assert.equal(result.children[0].children.length, 2);
  const first = result.children[0].children[0];
  assert.equal(first.querySelectorAll('p').length, 2);
  assert.equal(first.querySelectorAll('p')[0].textContent, 'first lazy continuation');
  await result.querySelectorAll('button')[0].events.click(); assert.equal(copied, 'const x = "<script>";');
  assert.equal(result.children.at(-1).textContent, 'After');
  const mixed = core.markdown(documentMock(), '**outer *inner* end** and *outer **inner** end*', () => {});
  assert.equal(mixed.querySelectorAll('strong').length, 2); assert.equal(mixed.querySelectorAll('em').length, 2);
});
test('streaming incomplete Markdown prefixes remain safe and finish as structured DOM', () => {
  const source = '> - [x] **task *done***\n\n| A | B |\n| --- | --- |\n| ~~x~~ | [safe](https://example.org/a(b)) |\n\n```html\n<script>alert(1)</script>\n```';
  for (let i = 1; i <= source.length; i++) {
    const result = core.markdown(documentMock(), source.slice(0, i), () => {});
    assert.equal(result.querySelectorAll('script').length, 0);
  }
  const result = core.markdown(documentMock(), source, () => {});
  assert.equal(result.querySelectorAll('table').length, 1); assert.equal(result.querySelectorAll('input').length, 1);
  assert.equal(result.querySelectorAll('code')[0].textContent, '<script>alert(1)</script>');
});
test('raw HTML and dangerous links stay inert inside all Markdown containers', () => {
  const text = '> <script>alert(1)</script>\n\n- [bad](javascript:alert(1))\n- [bad](data:text/html,payload)\n- <img src=x onerror=alert(1)>\n\n| x |\n| --- |\n| <svg onload=alert(1)> |\n\n[**safe**](https://example.org/a(b) "title") [encoded](javascript&#58;alert)';
  const result = core.markdown(documentMock(), text, () => {});
  for (const tag of ['script', 'img', 'svg', 'iframe']) assert.equal(result.querySelectorAll(tag).length, 0);
  assert.equal(result.querySelectorAll('a').length, 2);
  assert.equal(result.querySelectorAll('a')[0].attrs.href, 'https://example.org/a(b)');
  assert.equal(result.querySelectorAll('a')[0].attrs.title, 'title');
  // Character references are literal, never entity-decoded into a javascript scheme.
  assert.equal(core.safeURL('javascript&#58;alert'), 'javascript&#58;alert');
  assert.match(result.textContent, /<svg onload=/);
});
test('usage is unknown without a window or measured usage, clamps ring only', () => {
  assert.equal(core.usage(null, { context_window: 100 }).percent, null);
  assert.equal(core.usage({ prompt_tokens: 20 }, {}).percent, null);
  assert.deepEqual(core.usage({ prompt_tokens: 200, completion_tokens: 10 }, { context_window: 100 }), { prompt: 200, completion: 10, total: 210, window: 100, context: null, estimated: true, percent: 100 });
    assert.equal(core.usage({ prompt_tokens: 900, context_tokens: 20 }, { context_window: 100 }).percent, 20);
    assert.equal(core.usage({ context_tokens: 200 }, { context_window: 100 }).percent, 100);
    assert.equal(core.usage({ context_tokens: 0 }, { context_window: 100 }).percent, 0);
});
function harness(override, confirm = () => false) {
  const doc = documentMock(), nodes = {}, calls = [], encoder = new TextEncoder(); let stream;
    const documentEvents = {}; doc.addEventListener = (name, fn) => { documentEvents[name] = fn; };
  const template = fs.readFileSync(require.resolve('../ui/templates/agentcore.html'), 'utf8');
  for (const match of template.matchAll(/<(\w+)\b[^>]*\bid="([^"]+)"[^>]*>/g)) {
    const node = nodes[match[2]] = new Node(match[1]);
    node.className = match[0].match(/\bclass="([^"]*)"/)?.[1] || '';
    node.disabled = /\sdisabled\b/.test(match[0]);
  }
  delete nodes['agent-chat-status'];
    doc.getElementById = id => nodes[id] || null;
  const root = doc.getElementById('agent-chat'); root.dataset = { enabled: 'true', csrf: 'csrf-test', api: '/panel/agentcore/api' };
  const response = data => ({ ok: true, json: async () => data });
  const fetch = async (url, init) => {
    calls.push({ url, init });
    if (override) { const result = override(url, init); if (result) return result; }
    if (url.endsWith('/options')) return response({ model: 'actual', reasoning_effort: 'high', context_window: 100, approval_mode: 'yolo' });
    if (url.endsWith('/conversations') || url.endsWith('/approvals')) return response([]);
    if (url.endsWith('/chat/stream')) return { ok: true, headers: { get: () => 'text/event-stream' }, body: new ReadableStream({ start(controller) { stream = controller; } }) };
    if (url.endsWith('/runs?limit=100')) return response([]);
    if (url.endsWith('/stop')) return response({ stopped: true });
    return response({ delivered: true });
  };
  const copied = [], timers = [], delays = [], activeTimers = new Set(), events = {}, scheduled = new Map();
  let now = 0, nextTimer = 0;
  const window = {
    AgentChatCore: core, confirm, addEventListener(name, fn) { events[name] = fn; },
    setTimeout(fn, delay) {
      const id = nextTimer++;
      const fire = () => { if (!activeTimers.delete(id)) return; scheduled.delete(id); fn(); };
      scheduled.set(id, { at: now + delay, fire }); activeTimers.add(id);
      // Keep the existing explicit retry/copy timer helpers separate from read watchdogs.
      if (delay !== 45000 && delay !== 30000) { timers.push(fire); delays.push(delay); }
      return id;
    },
    clearTimeout(id) { activeTimers.delete(id); scheduled.delete(id); }
  };
  async function advance(ms) {
    const target = now + ms;
    while (true) {
      const next = [...scheduled.values()].filter(t => t.at <= target).sort((a, b) => a.at - b.at)[0];
      if (!next) break;
      now = next.at; next.fire(); await tick();
    }
    now = target; await tick();
  }
  vm.runInNewContext(fs.readFileSync(require.resolve('../ui/static/agentcore-chat.js'), 'utf8'), { window, document: doc, fetch, AbortController, TextDecoder, Map, Set, navigator: { clipboard: { writeText: async text => { copied.push(text); } } }, console });
  const el = id => doc.getElementById('agent-chat-' + id);
  return { el, calls, copied, timers, delays, activeTimers, advance, visibility(hidden) { doc.hidden = hidden; documentEvents.visibilitychange(); }, pagehide: () => events.pagehide(), close() { stream.close(); }, emit(e) { stream.enqueue(encoder.encode('event: ' + e.type + '\ndata: ' + JSON.stringify(e) + '\n\n')); } };
}
test('streamed usage estimates from cumulative prompt and prefers reported context tokens', async () => {
  const h = harness(); await tick(); h.el('input').value = 'task';
  const run = h.el('form').events.submit({ preventDefault() {} }); await tick();
  h.emit({ type: 'thinking', usage: { prompt_tokens: 25, completion_tokens: 5 } }); await tick();
  assert.equal(h.el('usage-label').textContent, '25.0%');
  assert.doesNotMatch(h.el('usage').textContent, /模型：|圆环优先|累计输入会叠加/);
    assert.match(h.el('usage').textContent, /^窗口：/);
  h.emit({ type: 'chunk', content: 'ok', usage: { prompt_tokens: 900, context_tokens: 40 } }); await tick();
  assert.equal(h.el('usage-label').textContent, '40.0%');
  h.emit({ type: 'done', reply: 'ok', usage: { prompt_tokens: 250 } }); await run;
  assert.equal(h.el('usage-label').textContent, '100.0%');
  assert.match(h.el('usage').textContent, /累计输入：250/);
  assert.equal(core.usage({}, { context_window: 100 }).percent, null);
  assert.equal(core.usage({ prompt_tokens: -1 }, { context_window: 100 }).percent, null);
  assert.equal(core.usage({ prompt_tokens: 50, context_tokens: 0 }, { context_window: 100 }).estimated, false);
});
function assertComposerBusy(h, busy) {
  assert.equal(h.el('send').classList.contains('hidden'), busy, 'send is hidden only while busy');
  assert.equal(h.el('stop').classList.contains('hidden'), !busy, 'stop is hidden only while idle');
}
for (const terminal of ['done', 'cancelled', 'error', 'EOF']) {
  test(`send/stop hidden classes stay mutually exclusive through ${terminal}`, async () => {
    const h = harness(); await tick();
    assertComposerBusy(h, false);
    h.el('input').value = 'task';
    const run = h.el('form').events.submit({ preventDefault() {} }); await tick();
    assertComposerBusy(h, true);
    assert.equal(h.el('stop').disabled, true);
    h.emit({ type: 'conv_id', conversation_id: 'visibility' }); await tick();
    assertComposerBusy(h, true);
    assert.equal(h.el('stop').disabled, false);
    if (terminal === 'cancelled') {
      await h.el('stop').events.click();
      assertComposerBusy(h, true);
      assert.equal(h.el('stop').disabled, true);
    }
    if (terminal === 'EOF') h.close();
    else h.emit({ type: terminal, reply: 'done', message: 'failed' });
    await run;
    assertComposerBusy(h, false);
    assert.equal(h.el('stop').disabled, true);
  });
}
test('real stream contract: first-turn ID enables stop, approval uses request_id, clarification uses conversation ID, CSRF on all POSTs', async () => {
  const h = harness(); await tick(); h.el('input').value = 'hello';
  const run = h.el('form').events.submit({ preventDefault() {} }); await tick();
  assert.equal(h.el('stop').disabled, true);
  h.emit({ type: 'conv_id', conversation_id: 'conv-first' }); await tick();
  assert.equal(h.el('stop').disabled, false);
  h.emit({ type: 'approval.required', request_id: 'approval-right', event_id: 'event-wrong', tool_call_id: 'tool-wrong', tool: 'shell', preview: 'rm file' }); await tick();
  const buttons = h.el('requests').querySelectorAll('button');
  await buttons[0].events.click();
  const approval = h.calls.find(c => c.url.endsWith('/approvals/approval-right'));
  assert.ok(approval); assert.deepEqual(JSON.parse(approval.init.body), { decision: 'allow', scope: 'once' });
  h.emit({ type: 'clarify', request_id: 'clarify-id', clarify_message: 'which?' }); await tick();
  h.el('requests').querySelectorAll('textarea')[0].value = 'answer';
  await h.el('requests').querySelectorAll('button')[0].events.click();
  assert.ok(h.calls.some(c => c.url.endsWith('/chat/conv-first/clarify') && JSON.parse(c.init.body).response === 'answer'));
  await h.el('stop').events.click();
  assert.ok(h.calls.some(c => c.url.endsWith('/chat/conv-first/stop')));
  h.emit({ type: 'cancelled', conversation_id: 'conv-first' }); await run;
  assert.equal(h.el('status'), null);
  const posts = h.calls.filter(c => c.init.method === 'POST');
  assert.ok(posts.every(c => c.init.headers['X-CSRF-Token'] === 'csrf-test'));
  assert.ok(!h.calls.some(c => c.url.endsWith('/chat')));
  assert.deepEqual(JSON.parse(posts[0].init.body).metadata, { mode: 'build' });
});
test('history requests are aborted and stale responses cannot overwrite a newer selection', async () => {
  let resolveA; let signalA;
  const response = data => ({ ok: true, json: async () => data });
  const h = harness((url, init) => {
    if (url.endsWith('/conversations')) return Promise.resolve(response([{ id: 'a', title: 'A' }, { id: 'b', title: 'B' }]));
    if (url.endsWith('/conversations/a/logs')) { signalA = init.signal; return new Promise(resolve => { resolveA = resolve; }); }
    if (url.endsWith('/conversations/b/logs')) return Promise.resolve(response([{ role: 'assistant', content: 'newest B' }]));
  });
  await tick(); const buttons = h.el('list').querySelectorAll('.agent-chat-session-open');
  buttons[0].events.click(); await tick(); buttons[1].events.click(); await tick();
  assert.equal(signalA.aborted, true);
  resolveA(response([{ role: 'assistant', content: 'stale A' }])); await tick();
  assert.match(h.el('messages').textContent, /newest B/);
  assert.doesNotMatch(h.el('messages').textContent, /stale A/);
});
test('EOF before terminal event warns that the backend may still be running', async () => {
  const h = harness(); await tick(); h.el('input').value = 'hello';
  const run = h.el('form').events.submit({ preventDefault() {} }); await tick();
  h.emit({ type: 'conv_id', conversation_id: 'running' }); await tick(); h.close(); await run;
  assert.match(h.el('error').textContent, /后台继续/);
  assert.equal(h.el('status'), null);
});
test('new conversation inherits service effort, sends only selected mode and locks mode after first ID', async () => {
  const h = harness(undefined, () => true); await tick();
  assert.equal(h.el('mode').value, 'build'); // Even when /options says yolo.
  assert.equal(h.el('effort'), null);
  assert.equal(h.el('settings').tag, 'span');
  assert.equal(h.el('settings').textContent, 'actual · high');
  assert.equal(h.el('settings').children.length, 0);
  h.el('mode').value = 'edit'; h.el('mode').events.change();
  h.el('input').value = 'task'; const run = h.el('form').events.submit({ preventDefault() {} }); await tick();
  const body = JSON.parse(h.calls.find(c => c.url.endsWith('/chat/stream')).init.body);
  assert.deepEqual(body.metadata, { mode: 'edit' });
  assert.equal(Object.hasOwn(body, 'effort'), false);
  h.emit({ type: 'conv_id', conversation_id: 'new-session' }); await tick();
  assert.equal(h.el('mode').disabled, true);
  assert.match(h.el('preference-note').title, /运行中不可修改/);
  h.emit({ type: 'done', reply: 'done' }); await run;
  h.el('new').events.click(); assert.equal(h.el('mode').value, 'build');
  assert.equal(h.el('mode').disabled, false);
  assert.equal(h.el('effort'), null);
  assert.equal(h.el('settings').textContent, 'actual · high');
});
test('high-risk modes require explicit confirmation and cancellation keeps build', async () => {
  const prompts = [], h = harness(undefined, message => { prompts.push(message); return false; }); await tick();
  for (const mode of ['edit', 'guarded', 'yolo']) { h.el('mode').value = mode; h.el('mode').events.change(); assert.equal(h.el('mode').value, 'build'); }
  assert.equal(prompts.length, 3); assert.match(prompts[2], /yolo/);
  h.el('input').value = 'task'; const run = h.el('form').events.submit({ preventDefault() {} }); await tick();
  assert.equal(JSON.parse(h.calls.find(c => c.url.endsWith('/chat/stream')).init.body).metadata.mode, 'build');
  h.emit({ type: 'done', reply: 'done' }); await run;
});
test('history restores editable metadata, prioritizes context_tokens and never overwrites metadata on follow-up', async () => {
  const response = data => Promise.resolve({ ok: true, json: async () => data });
  const metadata = { effort: 'medium', mode: 'plan', model: 'custom-model', locale: 'zh-CN', goal: 'keep' };
  const h = harness(url => {
    if (url.endsWith('/conversations')) return response([{ id: 'saved', title: 'Saved' }]);
    if (url.endsWith('/conversations/saved')) return response({ id: 'saved', metadata });
    if (url.endsWith('/conversations/saved/logs')) return response([{ role: 'assistant', content: 'history', prompt_tokens: 900, context_tokens: 25 }]);
  }); await tick(); h.el('list').querySelectorAll('.agent-chat-session-open')[0].events.click(); await tick();
  assert.equal(h.el('effort'), null); assert.equal(h.el('mode').value, 'plan');
  assert.equal(h.el('settings').textContent, 'custom-model · medium');
  assert.equal(h.el('settings').tag, 'span'); assert.equal(h.el('settings').children.length, 0);
  assert.equal(h.el('mode').disabled, false); assert.match(h.el('settings').textContent, /custom-model/);
  assert.match(h.el('usage-label').textContent, /25.0%/); assert.match(h.el('usage').textContent, /累计输入：900/);
  h.el('input').value = 'followup'; const run = h.el('form').events.submit({ preventDefault() {} }); await tick();
  const body = JSON.parse(h.calls.find(c => c.url.endsWith('/chat/stream')).init.body);
  assert.equal(body.conversation_id, 'saved'); assert.equal(body.metadata, undefined);
  h.emit({ type: 'done', reply: 'done' }); await run;
});
test('history without mode has a real inherited option and all permission options are visible', async () => {
  const h = harness(url => {
    if (url.endsWith('/conversations')) return jsonResponse([{ id: 'legacy' }]);
    if (url.endsWith('/conversations/legacy')) return jsonResponse({ id: 'legacy', metadata: {} });
    if (url.endsWith('/logs')) return jsonResponse([]);
  });
  await tick(); h.el('list').querySelectorAll('.agent-chat-session-open')[0].events.click(); await tick();
  const select = h.el('mode');
  assert.equal(select.disabled, false); assert.equal(select.value, '');
  assert.deepEqual(select.children.map(n => n.value), ['plan', 'build', 'edit', 'guarded', 'yolo', '']);
  assert.ok(select.children.every(n => n.textContent));
  assert.match(select.children.at(-1).textContent, /继承服务默认（yolo）/);
});
for (const outcome of ['success', 'http failure', 'unconfirmed']) test('history permission update: ' + outcome, async () => {
  const metadata = { mode: 'plan', model: 'custom', effort: 'low', goal: 'keep', locale: 'zh-CN' };
  let finish;
  const h = harness(url => {
    if (url.endsWith('/conversations')) return jsonResponse([{ id: 'saved' }]);
    if (url.endsWith('/conversations/saved')) return jsonResponse({ id: 'saved', metadata });
    if (url.endsWith('/logs')) return jsonResponse([]);
    if (url.endsWith('/preferences')) return new Promise(resolve => { finish = resolve; });
  });
  await tick(); h.el('list').querySelectorAll('.agent-chat-session-open')[0].events.click(); await tick();
  h.el('mode').value = 'build'; const change = h.el('mode').events.change();
  assert.equal(h.el('mode').disabled, true); assert.equal(h.el('input').disabled, true);
  const post = h.calls.find(c => c.url.endsWith('/preferences'));
  assert.equal(post.init.method, 'POST'); assert.equal(post.init.headers['X-CSRF-Token'], 'csrf-test');
  assert.deepEqual(JSON.parse(post.init.body), { mode: 'build' });
  finish(outcome === 'http failure' ? { ok: false, status: 409, json: async () => ({ error: 'active run' }) } : jsonResponse({ id: 'saved', metadata: outcome === 'success' ? { ...metadata, mode: 'build' } : metadata }));
  await change;
  assert.equal(h.el('mode').disabled, false);
  assert.equal(h.el('mode').value, outcome === 'success' ? 'build' : 'plan');
  assert.equal(h.el('settings').textContent, 'custom · low');
  if (outcome !== 'success') assert.match(h.el('error').textContent, /权限模式更新失败/);
  assert.ok(!h.calls.some(c => c.url.endsWith('/chat/stream')));
  h.el('input').value = 'next'; const run = h.el('form').events.submit({ preventDefault() {} }); await tick();
  const body = JSON.parse(h.calls.find(c => c.url.endsWith('/chat/stream')).init.body);
  assert.equal(body.conversation_id, 'saved'); assert.equal(body.metadata, undefined);
  h.emit({ type: 'done', reply: 'done' }); await run;
});
test('history high-risk changes require confirmation and active runs disable changes', async () => {
  let accept = false, confirms = 0, active = false;
  const h = harness(url => {
    if (url.endsWith('/conversations')) return jsonResponse([{ id: 'saved' }]);
    if (url.endsWith('/conversations/saved')) return jsonResponse({ id: 'saved', metadata: { mode: 'plan' } });
    if (url.endsWith('/logs')) return jsonResponse([]);
    if (url.endsWith('/runs?limit=100')) return jsonResponse(active ? [{ status: 'running' }] : []);
    if (url.endsWith('/preferences')) return jsonResponse({ id: 'saved', metadata: { mode: 'yolo', goal: 'keep' } });
  }, () => { confirms++; return accept; });
  await tick(); h.el('list').querySelectorAll('.agent-chat-session-open')[0].events.click(); await tick();
  for (const mode of ['edit', 'guarded', 'yolo']) {
    h.el('mode').value = mode; await h.el('mode').events.change();
    assert.equal(h.el('mode').value, 'plan');
  }
  assert.equal(confirms, 3); assert.ok(!h.calls.some(c => c.url.endsWith('/preferences')));
  accept = true; h.el('mode').value = 'yolo'; await h.el('mode').events.change();
  assert.equal(h.el('mode').value, 'yolo'); assert.equal(confirms, 4);
  active = true; await h.advance(30000);
  assert.equal(h.el('mode').disabled, true);
  const count = h.calls.length; h.el('mode').value = 'build'; await h.el('mode').events.change();
  assert.equal(h.calls.length, count); assert.equal(h.el('mode').value, 'yolo');
});
test('reasoning aggregates per iteration, phase text survives tools and done only replaces final answer', async () => {
  const h = harness(); await tick(); h.el('input').value = 'question';
  const run = h.el('form').events.submit({ preventDefault() {} }); await tick();
  for (const e of [
    { type: 'thinking', iteration: 1, content: 'NOT_REASONING' },
    { type: 'reasoning', content: '分析' }, { type: 'reasoning', content: '计划', provider_state: 'SECRET' },
    { type: 'chunk', content: '先检查文件' },
    { type: 'tool.started', tool_call_id: 't', tool: 'read', preview: 'file' },
    { type: 'progress', tool_call_id: 't', tool: 'read', summary: 'found' },
    { type: 'thinking', iteration: 2 }, { type: 'reasoning', content: '完成检查' },
    { type: 'chunk', content: '**最终**' }, { type: 'done', reply: '**最终**' }
  ]) h.emit(e);
  await run;
  const wrap = h.el('messages').querySelectorAll('.agent-chat-message-wrap')[1], article = wrap.children[0];
  assert.equal(article.children[0].className, 'agent-chat-process');
  assert.ok(article.children[1].classList.contains('agent-chat-answer'));
  const reasoning = article.querySelectorAll('.agent-chat-reasoning');
  assert.equal(reasoning.length, 2); assert.equal(reasoning[0].querySelector('pre').textContent, '分析计划');
  assert.ok(reasoning.every(n => n.open === false && n.dataset.state === 'complete'));
  assert.equal(article.querySelector('.agent-chat-phase').textContent, '先检查文件');
  assert.equal(article.querySelector('.agent-chat-tool').dataset.state, 'returned');
  assert.doesNotMatch(article.textContent, /SECRET|NOT_REASONING|服务未公开/);
  await wrap.children[1].events.click(); assert.equal(h.copied[0], '先检查文件\n\n**最终**');
});

test('history retains log order, tool results and phase content without fabricating reasoning', async () => {
  const h = harness(url => {
    if (url.endsWith('/conversations')) return jsonResponse([{ id: 'saved' }]);
    if (url.endsWith('/conversations/saved')) return jsonResponse({ id: 'saved', metadata: {} });
    if (url.endsWith('/logs')) return jsonResponse([
      { role: 'user', content: 'question' },
      { role: 'assistant', run_id: 'r', content: '阶段', tool_calls: [{ id: 't', function: { name: 'read', arguments: 'file' } }], provider_state: 'SECRET' },
      { role: 'tool', run_id: 'r', tool_call_id: 't', content: '失败结果', status: 'error' },
      { role: 'assistant', run_id: 'r', content: '最终' }
    ]);
  });
  await tick(); h.el('list').querySelector('.agent-chat-session-open').events.click(); await tick();
  const article = h.el('messages').querySelectorAll('article')[1];
  assert.equal(article.querySelectorAll('.agent-chat-reasoning').length, 0);
  assert.equal(article.querySelector('.agent-chat-phase').textContent, '阶段');
  assert.equal(article.querySelector('.agent-chat-tool').dataset.state, 'failed');
  assert.match(article.textContent, /阶段.*read.*失败结果.*最终/s);
  assert.doesNotMatch(article.textContent, /SECRET/);
});

test('message bubbles have no sender or timestamp and copy icons stay below the article', async () => {
  const h = harness(url => {
    if (url.endsWith('/conversations')) return jsonResponse([{ id: 'saved' }]);
    if (url.endsWith('/conversations/saved')) return jsonResponse({ id: 'saved', metadata: {} });
    if (url.endsWith('/logs')) return jsonResponse([
      { role: 'user', content: 'question', created_at: '2026-01-01T00:00:00Z' },
      { role: 'assistant', content: 'answer', actions: [{ tool: 'read' }] }
    ]);
  });
  await tick(); h.el('list').querySelectorAll('.agent-chat-session-open')[0].events.click(); await tick();
  assert.equal(h.el('status'), null);
  const wraps = h.el('messages').querySelectorAll('.agent-chat-message-wrap');
  assert.equal(wraps.length, 2); assert.ok(wraps[0].classList.contains('agent-chat-user'));
  assert.ok(wraps[1].classList.contains('agent-chat-assistant'));
  for (const wrap of wraps) {
    const [article, button] = wrap.children;
    assert.equal(article.tag, 'article'); assert.equal(button.tag, 'button');
    assert.equal(article.querySelectorAll('button').length, 0);
    assert.equal(article.querySelectorAll('time').length, 0);
    assert.equal(article.querySelectorAll('.agent-chat-message-head').length, 0);
    const svg = button.querySelector('svg'); assert.ok(svg);
    assert.equal(button.attrs['aria-label'], '复制完整消息');
    await button.events.click(); assert.equal(button.title, '已复制'); assert.equal(button.querySelector('svg'), svg);
  }
  assert.equal(wraps[1].children[0].querySelectorAll('details').length, 1);
  assert.equal(h.copied[0], 'question'); assert.equal(h.copied[1], 'answer');
  h.timers.forEach(fn => fn());
  assert.ok(wraps.every(w => w.children[1].title === '复制消息' && w.children[1].querySelector('svg')));
});
test('restored high-risk permissions still require confirmation before sending', async () => {
  const response = data => Promise.resolve({ ok: true, json: async () => data }); let confirms = 0;
  const h = harness(url => {
    if (url.endsWith('/conversations')) return response([{ id: 'danger' }]);
    if (url.endsWith('/conversations/danger')) return response({ id: 'danger', metadata: { mode: 'yolo' } });
    if (url.endsWith('/conversations/danger/logs')) return response([]);
  }, () => { confirms++; return false; });
  await tick(); h.el('list').querySelectorAll('.agent-chat-session-open')[0].events.click(); await tick();
  assert.equal(h.el('mode').value, 'yolo'); assert.equal(confirms, 0);
  h.el('input').value = 'task'; await h.el('form').events.submit({ preventDefault() {} });
  assert.equal(confirms, 1); assert.ok(!h.calls.some(c => c.url.endsWith('/chat/stream')));
  assert.equal(h.el('input').value, 'task');
});
test('stale detail metadata cannot overwrite a newer conversation or its context', async () => {
  const response = data => ({ ok: true, json: async () => data }); let resolveA;
  const h = harness(url => {
    if (url.endsWith('/conversations')) return Promise.resolve(response([{ id: 'a' }, { id: 'b' }]));
    if (url.endsWith('/conversations/a')) return new Promise(resolve => { resolveA = resolve; });
    if (url.endsWith('/conversations/b')) return Promise.resolve(response({ id: 'b', metadata: { effort: 'low', mode: 'plan' } }));
    if (url.endsWith('/logs')) return Promise.resolve(response([{ role: 'assistant', content: 'saved', context_tokens: 10 }]));
  }); await tick(); const buttons = h.el('list').querySelectorAll('.agent-chat-session-open');
  buttons[0].events.click(); await tick(); buttons[1].events.click(); await tick();
  resolveA(response({ id: 'a', metadata: { effort: 'high', mode: 'yolo' } })); await tick();
  assert.equal(h.el('effort'), null); assert.equal(h.el('mode').value, 'plan');
  assert.equal(h.el('settings').textContent, 'actual · low');
  assert.match(h.el('usage-label').textContent, /10.0%/);
});
test('failed conversation detail does not invent restored preferences', async () => {
  const response = data => Promise.resolve({ ok: true, json: async () => data });
  const h = harness(url => {
    if (url.endsWith('/conversations')) return response([{ id: 'saved' }]);
    if (url.endsWith('/conversations/saved')) return Promise.resolve({ ok: false, status: 503, json: async () => ({ error: 'unavailable' }) });
    if (url.endsWith('/conversations/saved/logs')) return response([]);
  }); await tick(); h.el('list').querySelectorAll('.agent-chat-session-open')[0].events.click(); await tick();
  assert.equal(h.el('effort'), null); assert.equal(h.el('mode').value, 'unknown');
  assert.equal(h.el('settings').textContent, '未知 · 未知');
  assert.match(h.el('settings').textContent, /未知/); assert.match(h.el('error').textContent, /未确认当前设置/);
});
test('full-message copy uses latest streamed raw Markdown and resets the correct button label', async () => {
  const h = harness(); await tick(); h.el('input').value = 'user message';
  const run = h.el('form').events.submit({ preventDefault() {} }); await tick();
  h.emit({ type: 'chunk', content: '**partial**' }); await tick();
  const copies = h.el('messages').querySelectorAll('button').filter(b => b.dataset.copyLabel === '复制消息');
  await copies[0].events.click(); assert.equal(h.copied[0], 'user message');
  await copies[1].events.click(); assert.equal(h.copied[1], '**partial**');
  h.emit({ type: 'done', reply: '# Final\n```js\nconst x = 1;\n```' }); await run;
  await copies[1].events.click(); assert.equal(h.copied[2], '# Final\n```js\nconst x = 1;\n```');
  h.timers.forEach(fn => fn()); assert.equal(copies[1].title, '复制消息'); assert.equal(copies[1].querySelectorAll('svg').length, 1);
});
function retryStream(runID = 'run-1') {
  let controller, cancelled = false;
  const response = { ok: true, headers: { get: name => name === 'Content-Type' ? 'text/event-stream' : runID }, body: new ReadableStream({ start(c) { controller = c; }, cancel() { cancelled = true; } }) };
  return { response, get cancelled() { return cancelled; }, heartbeat() { controller.enqueue(new TextEncoder().encode(': keep-alive\n\n')); }, emit(e) { controller.enqueue(new TextEncoder().encode('data: ' + JSON.stringify(e) + '\n\n')); }, fail() { controller.error(new TypeError('network lost')); }, close() { controller.close(); } };
}
function onlyOneSubmission(h) { assert.equal(h.calls.filter(c => c.init.method === 'POST' && c.url.endsWith('/chat/stream')).length, 1); }
async function startRetry(h) { await tick(); h.el('input').value = 'hello'; const run = h.el('form').events.submit({ preventDefault() {} }); await tick(); return { run }; }

test('reasoning replay uses the existing cursor and does not duplicate deltas or tool rows', async () => {
  const first = retryStream('reason-run'), second = retryStream('reason-run');
  const h = harness(url => url.endsWith('/chat/stream') ? first.response : url.includes('/events/stream?') ? second.response : undefined);
  const { run } = await startRetry(h);
  first.emit({ type: 'thinking', sequence: 1, iteration: 1 });
  first.emit({ type: 'reasoning', sequence: 2, content: '分析' }); await tick();
  first.fail(); await tick(); h.timers.shift()(); await tick();
  second.emit({ type: 'reasoning', sequence: 2, content: '分析' });
  second.emit({ type: 'reasoning', sequence: 3, content: '继续' });
  second.emit({ type: 'tool.started', sequence: 4, tool_call_id: 't', tool: 'read' });
  second.emit({ type: 'tool.started', sequence: 4, tool_call_id: 't', tool: 'read' });
  second.emit({ type: 'done', sequence: 5, reply: 'answer' }); await run;
  assert.equal(h.el('messages').querySelector('.agent-chat-reasoning').querySelector('pre').textContent, '分析继续');
  assert.equal(h.el('messages').querySelectorAll('.agent-chat-tool').length, 1);
  assert.ok(h.calls.some(c => c.url.endsWith('/events/stream?after=2')));
});

test('network recovery uses GET after successful sequence, deduplicates text and keeps composer busy', async () => {
  const first = retryStream(), second = retryStream();
  const h = harness((url) => url.endsWith('/chat/stream') ? first.response : url.includes('/events/stream?') ? second.response : null);
  const { run } = await startRetry(h);
  first.emit({ type: 'chunk', run_id: 'run-1', sequence: 1, event_id: 'a', content: 'Hello' }); await tick(); first.fail(); await tick();
  assertComposerBusy(h, true); assert.equal(h.el('new').disabled, true); assert.deepEqual(h.delays, [1000]);
  await h.el('form').events.submit({ preventDefault() {} }); onlyOneSubmission(h);
  h.timers[0](); await tick();
  assert.ok(h.calls.some(c => c.url.endsWith('/runs/run-1/events/stream?after=1') && c.init.method === 'GET'));
  second.emit({ type: 'chunk', sequence: 1, event_id: 'a', content: 'Hello' });
  second.emit({ type: 'chunk', sequence: 2, content: ' world' }); await tick();
  const copyButtons = h.el('messages').querySelectorAll('button').filter(b => b.dataset.copyLabel === '复制消息');
  await copyButtons[1].events.click(); assert.equal(h.copied.at(-1), 'Hello world');
  second.emit({ type: 'done', sequence: 3 }); await run;
  assert.equal(h.el('error').textContent, ''); assert.equal(h.el('status'), null); onlyOneSubmission(h); assertComposerBusy(h, false);
});

test('event run_id recovers without header and preserves tools and pending approval across replay', async () => {
  const first = retryStream(null), second = retryStream(null);
  const h = harness(url => url.endsWith('/chat/stream') ? first.response : url.includes('/events/stream?') ? second.response : null);
  const { run } = await startRetry(h);
  const tool = { type: 'tool.started', run_id: 'event-run', sequence: 1, tool_call_id: 'tool1', tool: 'shell', preview: 'echo hi' };
  const approval = { type: 'approval.required', run_id: 'event-run', sequence: 2, conversation_id: 'conv1', request_id: 'approval1', tool: 'shell', preview: 'echo hi' };
  first.emit(tool); first.emit(approval); await tick();
  const pending = h.el('requests').textContent, details = h.el('messages').querySelectorAll('details').length;
  first.fail(); await tick(); assert.equal(h.el('requests').textContent, pending);
  h.timers[0](); await tick(); assert.ok(h.calls.some(c => c.url.endsWith('/runs/event-run/events/stream?after=2')));
  second.emit(tool); second.emit(approval); second.emit({ type: 'progress', sequence: 3, tool_call_id: 'tool1', tool: 'shell', summary: 'hi' }); await tick();
  assert.equal(h.el('requests').textContent, pending); assert.equal(h.el('messages').querySelectorAll('details').length, details);
  assert.match(h.el('messages').textContent, /hi/);
  second.emit({ type: 'done', sequence: 4 }); await run; assert.equal(h.el('requests').textContent, ''); onlyOneSubmission(h);
});

test('X-Run-ID permits recovery before any event and terminal event prevents retries', async () => {
  const first = retryStream(), second = retryStream();
  const h = harness(url => url.endsWith('/chat/stream') ? first.response : url.includes('/events/stream?') ? second.response : null);
  const { run } = await startRetry(h); first.close(); await tick(); h.timers[0](); await tick();
  assert.ok(h.calls.some(c => c.url.endsWith('/runs/run-1/events/stream?after=0')));
  second.emit({ type: 'done', sequence: 1 }); await run;
  assert.equal(h.delays.length, 1); onlyOneSubmission(h); assert.equal(second.cancelled, true);
});

test('retry delays are bounded and exhaustion never resubmits POST', async () => {
  const first = retryStream();
  const h = harness(url => url.endsWith('/chat/stream') ? first.response : url.includes('/events/stream?') ? Promise.reject(new TypeError('offline')) : null);
  const { run } = await startRetry(h); first.fail(); await tick();
  for (let i = 0; i < 5; i++) { assertComposerBusy(h, true); h.timers[i](); await tick(); }
  await run; assert.deepEqual(h.delays, [1000, 2000, 4000, 8000, 10000]);
  assert.match(h.el('error').textContent, /耗尽/); assert.equal(h.activeTimers.size, 0); onlyOneSubmission(h); assertComposerBusy(h, false);
});

test('pagehide cancels retry wait without leaving a timer or making a GET', async () => {
  const first = retryStream(); const h = harness(url => url.endsWith('/chat/stream') ? first.response : null);
  const { run } = await startRetry(h); first.fail(); await tick(); assert.equal(h.activeTimers.size, 1);
  h.pagehide(); await run; assert.equal(h.activeTimers.size, 0); h.timers[0](); await tick();
  assert.equal(h.calls.filter(c => c.url.includes('/events/stream?')).length, 0); onlyOneSubmission(h);
});

test('pagehide immediately cancels a blocked reader', async () => {
  const first = retryStream(); const h = harness(url => url.endsWith('/chat/stream') ? first.response : null);
  const { run } = await startRetry(h); h.pagehide(); await run;
  assert.equal(first.cancelled, true); assert.equal(h.activeTimers.size, 0); onlyOneSubmission(h);
});

for (const status of [401, 403, 404, 400, 408, 429, 503]) test('HTTP recovery classification ' + status, async () => {
  const first = retryStream();
  const h = harness(url => url.endsWith('/chat/stream') ? first.response : url.includes('/events/stream?') ? { ok: false, status, json: async () => ({ error: 'unavailable' }) } : null);
  const { run } = await startRetry(h); first.fail(); await tick(); h.timers[0](); await tick();
  if ([408, 429, 503].includes(status)) { assert.equal(h.delays.length, 2); h.pagehide(); }
  else { assert.equal(h.delays.length, 1); assert.match(h.el('error').textContent, new RegExp('HTTP ' + status)); }
  await run; onlyOneSubmission(h);
});

for (const type of ['resync_required', 'bad-json', 'bad-content-type']) test('protocol failure stops safely: ' + type, async () => {
  const first = retryStream();
  if (type === 'bad-content-type') first.response.headers.get = () => 'text/html';
  const h = harness(url => url.endsWith('/chat/stream') ? first.response : null);
  const { run } = await startRetry(h);
  if (type === 'bad-json') first.emit({ type: 'chunk', sequence: 'invalid' });
  else if (type !== 'bad-content-type') first.emit({ type });
  await run; assert.equal(h.delays.length, 0); assert.notEqual(h.el('error').textContent, ''); onlyOneSubmission(h);
});

test('raw heartbeat bytes reset the silence deadline without application events', async () => {
  const first = retryStream(); const h = harness(url => url.endsWith('/chat/stream') ? first.response : null);
  const { run } = await startRetry(h);
  for (let i = 0; i < 8; i++) {
    await h.advance(15000); first.heartbeat(); await tick();
    assert.equal(first.cancelled, false); assert.equal(h.activeTimers.size, 1);
  }
  await h.advance(44999); assert.equal(first.cancelled, false); assert.equal(h.delays.length, 0);
  first.emit({ type: 'done', sequence: 1 }); await run;
  assert.equal(h.activeTimers.size, 0); await h.advance(120000);
  assert.equal(h.delays.length, 0); onlyOneSubmission(h);
});

test('45 seconds of silence cancels reader and resumes original run with GET cursor', async () => {
  const first = retryStream(), second = retryStream();
  const h = harness(url => url.endsWith('/chat/stream') ? first.response : url.includes('/events/stream?') ? second.response : null);
  const { run } = await startRetry(h);
  first.emit({ type: 'chunk', sequence: 7, content: 'partial' }); await tick();
  await h.advance(44999); assert.equal(first.cancelled, false);
  await h.advance(1); assert.equal(first.cancelled, true); assertComposerBusy(h, true);
  assert.deepEqual(h.delays, [1000]); assert.equal(h.activeTimers.size, 1);
  await h.advance(1000);
  assert.ok(h.calls.some(c => c.url.endsWith('/runs/run-1/events/stream?after=7') && c.init.method === 'GET'));
  second.emit({ type: 'done', sequence: 8 }); await run;
  assert.equal(h.el('error').textContent, ''); assert.equal(h.activeTimers.size, 0);
  await h.advance(120000); assert.equal(h.delays.length, 1); onlyOneSubmission(h);
});

test('silence without run ID stops safely without POST resubmission or timers', async () => {
  const first = retryStream(null); const h = harness(url => url.endsWith('/chat/stream') ? first.response : null);
  const { run } = await startRetry(h); await h.advance(45000); await run;
  assert.equal(first.cancelled, true); assert.match(h.el('error').textContent, /45 秒/);
  assert.match(h.el('error').textContent, /未获取运行 ID/);
  assert.equal(h.activeTimers.size, 0); assert.equal(h.delays.length, 0); onlyOneSubmission(h);
});

test('pagehide clears active silence watchdog and cannot reconnect later', async () => {
  const first = retryStream(); const h = harness(url => url.endsWith('/chat/stream') ? first.response : null);
  const { run } = await startRetry(h); await h.advance(30000);
  assert.equal(h.activeTimers.size, 1); h.pagehide(); await run;
  assert.equal(first.cancelled, true); assert.equal(h.activeTimers.size, 0);
  await h.advance(120000); assert.equal(h.delays.length, 0);
  assert.equal(h.calls.filter(c => c.url.includes('/events/stream?')).length, 0); onlyOneSubmission(h);
});

test('server error event is not presented as completion', async () => {
  const h = harness(); await tick(); h.el('input').value = 'hello';
  const run = h.el('form').events.submit({ preventDefault() {} }); await tick();
  h.emit({ type: 'error', message: 'run failed' }); await run;
  assert.equal(h.el('status'), null); assert.match(h.el('error').textContent, /run failed/);
});

const jsonResponse = data => ({ ok: true, json: async () => data });
const sessionRows = h => h.el('list').children;
test('history dots use real runs, prefer active and do not retain an older failure', async () => {
  const states = [
    [{ status: 'failed', created_at: '2025-01-01' }, { status: 'completed', created_at: '2025-02-01' }],
    [{ status: 'completed', created_at: '2025-02-01' }, { status: 'pending', created_at: '2025-01-01' }],
    [{ status: 'waiting_approval' }], [{ status: 'waiting_clarification' }], [{ status: 'failed' }], null, [{ status: 'future' }]
  ];
  const h = harness(url => {
    if (url.endsWith('/conversations')) return jsonResponse(states.map((_, i) => ({ id: String(i) })));
    const m = url.match(/conversations\/(\d+)\/runs/); if (m) return jsonResponse(states[Number(m[1])]);
  }); await tick(); await tick();
  assert.deepEqual(sessionRows(h).map(r => r.children[0].dataset.state), ['idle', 'running', 'waiting', 'waiting', 'failed', 'idle', 'unknown']);
  assert.match(sessionRows(h)[6].children[0].title, /未知/);
  assert.deepEqual(sessionRows(h).map(r => r.children[2].disabled), [false, true, true, true, false, false, true]);
  h.pagehide(); assert.equal(h.activeTimers.size, 0);
});

for (const outcome of ['cancel', 'success', 'fail', 'conflict', 'archived', 'missing-status']) test('permanent deletion confirmation ' + outcome, async () => {
  let prompts = 0, resolveDelete;
  const h = harness(url => {
    if (url.endsWith('/conversations')) return jsonResponse([{ id: 'saved' }]);
    if (url.endsWith('/conversations/saved')) return jsonResponse({ id: 'saved' });
    if (url.endsWith('/logs')) return jsonResponse([]);
    if (url.endsWith('/delete')) return new Promise(resolve => { resolveDelete = resolve; });
  }, text => { prompts++; assert.match(text, /永久删除/); assert.match(text, /会话/); assert.match(text, /消息/); assert.match(text, /不可恢复/); assert.doesNotMatch(text, /仍保留/); return outcome !== 'cancel'; });
  await tick(); const row = sessionRows(h)[0]; await row.children[1].events.click(); await tick();
  assert.equal(h.el('status'), null);
  const action = row.children[2].events.click();
  if (outcome === 'cancel') {
      await action; assert.equal(resolveDelete, undefined);
      assert.equal(sessionRows(h).length, 1); assert.equal(h.el('title').textContent, '历史会话');
      assert.equal(h.calls.filter(c => c.url.endsWith('/delete')).length, 0);
    }
  else {
    await row.children[2].events.click(); assert.equal(prompts, 1);
    const call = h.calls.find(c => c.url.endsWith('/delete'));
    assert.equal(call.init.body, '{}'); assert.equal(call.init.headers['X-CSRF-Token'], 'csrf-test');
    assert.equal(call.init.method, 'POST');
        assert.equal(h.calls.filter(c => c.url.endsWith('/delete')).length, 1);
        const status = outcome === 'conflict' ? 409 : 500;
        resolveDelete(outcome === 'success' ? jsonResponse({ status: 'deleted' })
          : outcome === 'archived' ? jsonResponse({ status: 'archived' })
          : outcome === 'missing-status' ? jsonResponse({})
          : { ok: false, status, json: async () => ({ error: outcome === 'conflict' ? 'active' : 'internal' }) }); await action;
    if (outcome === 'success') { assert.equal(sessionRows(h).length, 0); assert.equal(h.el('title').textContent, '新会话'); }
    else {
          assert.equal(sessionRows(h).length, 1); assert.equal(h.el('title').textContent, '历史会话');
          assert.ok(h.el('error').textContent);
          if (outcome === 'fail' || outcome === 'conflict') assert.match(h.el('error').textContent, new RegExp(String(status)));
        }
  }
  h.pagehide();
});

test('status query failure stays unknown and disables deletion', async () => {
  const h = harness(url => {
    if (url.endsWith('/conversations')) return jsonResponse([{ id: 'x' }]);
    if (url.includes('/runs?')) return Promise.reject(new Error('offline'));
  }); await tick();
  const row = sessionRows(h)[0]; assert.equal(row.children[0].dataset.state, 'unknown'); assert.match(row.children[0].title, /未知.*offline/); assert.equal(row.children[2].disabled, true); h.pagehide();
});

test('status queries cap concurrency at four, discard old generations and pause/clean polling', async () => {
  const jobs = []; let active = 0, maximum = 0;
  const h = harness((url, init) => {
    if (url.endsWith('/conversations')) return jsonResponse(Array.from({ length: 8 }, (_, i) => ({ id: String(i) })));
    if (url.includes('/runs?')) return new Promise(resolve => {
      active++; maximum = Math.max(maximum, active);
      jobs.push({ signal: init.signal, finish(data) { active--; resolve(jsonResponse(data)); } });
    });
  }); await tick(); assert.equal(jobs.length, 4);
  h.visibility(true); assert.ok(jobs.every(j => j.signal.aborted));
  h.visibility(false);
  jobs.splice(0).forEach(j => j.finish([{ status: 'failed' }])); await tick();
  assert.equal(sessionRows(h)[0].children[0].dataset.state, 'unknown');
  jobs.splice(0).forEach(j => j.finish([])); await tick();
  jobs.splice(0).forEach(j => j.finish([])); await tick();
  assert.equal(maximum, 4); assert.equal(h.activeTimers.size, 1);
  const count = h.calls.length; h.visibility(true); await h.advance(60000); assert.equal(h.calls.length, count);
  h.visibility(false); await tick(); jobs.splice(0).forEach(j => j.finish([])); await tick(); jobs.splice(0).forEach(j => j.finish([])); await tick();
  await h.advance(30000); assert.equal(jobs.length, 4);
  h.pagehide(); jobs.splice(0).forEach(j => j.finish([])); await tick(); assert.equal(h.activeTimers.size, 0);
  const stopped = h.calls.length; await h.advance(60000); assert.equal(h.calls.length, stopped);
});

test('SSE state wins over an older in-flight status response', async () => {
  let resolveRuns;
  const h = harness(url => {
    if (url.endsWith('/conversations')) return jsonResponse([{ id: 'live' }]);
    if (url.includes('/runs?')) return new Promise(resolve => { resolveRuns = resolve; });
  }); await tick();
  h.el('input').value = 'task'; const run = h.el('form').events.submit({ preventDefault() {} }); await tick();
  h.emit({ type: 'conv_id', conversation_id: 'live' }); h.emit({ type: 'approval.required', request_id: 'approval' }); await tick();
  assert.equal(sessionRows(h)[0].children[0].dataset.state, 'waiting');
  resolveRuns(jsonResponse([{ status: 'completed' }])); await tick();
  assert.equal(sessionRows(h)[0].children[0].dataset.state, 'waiting');
  h.emit({ type: 'done' }); await run; h.pagehide();
});
