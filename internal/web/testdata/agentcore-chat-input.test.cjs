const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../ui/static/agentcore-chat-input.js'), 'utf8');
function setup() {
  const listeners = {}, rootListeners = {}, windowListeners = {}, tasks = [];
  const input = { value: '', scrollHeight: 36, style: {}, addEventListener: (name, fn) => listeners[name] = fn };
  const root = { addEventListener: (name, fn) => rootListeners[name] = fn };
  vm.runInNewContext(source, {
    document: { getElementById: id => id === 'agent-chat' ? root : input },
    window: { getComputedStyle: () => ({lineHeight: '20px', fontSize: '14px', paddingTop: '8px', paddingBottom: '8px', borderTopWidth: '1px', borderBottomWidth: '1px'}), addEventListener: (name, fn) => windowListeners[name] = fn },
    queueMicrotask: fn => tasks.push(fn),
  });
  return { input, listeners, rootListeners, windowListeners, flush: () => { while (tasks.length) tasks.shift()(); } };
}
test('composer starts at one line, grows to five and scrolls beyond five', () => {
  const t = setup();
  assert.equal(t.input.style.height, '38px');
  t.input.value = 'two lines'; t.input.scrollHeight = 56; t.listeners.input();
  assert.equal(t.input.style.height, '58px');
  t.input.scrollHeight = 116; t.listeners.input();
  assert.equal(t.input.style.height, '118px');
  assert.equal(t.input.style.overflowY, 'hidden');
  t.input.scrollHeight = 400; t.listeners.input();
  assert.equal(t.input.style.height, '118px');
  assert.equal(t.input.style.overflowY, 'auto');
});
test('clearing after submit or conversation switch shrinks back to one line', () => {
  const t = setup();
  t.input.value = 'long'; t.input.scrollHeight = 400; t.listeners.input();
  t.rootListeners.submit(); t.input.value = ''; t.flush();
  assert.equal(t.input.style.height, '38px');
  assert.equal(t.input.style.overflowY, 'hidden');
  t.input.value = 'long'; t.listeners.input();
  t.rootListeners.click(); t.input.value = ''; t.flush();
  assert.equal(t.input.style.height, '38px');
});
test('scroll control is centered and icon-only, input starts with one row', () => {
  const html = fs.readFileSync(path.join(__dirname, '../ui/templates/agentcore.html'), 'utf8');
  assert.match(html, /class="agent-chat-scroll-action"><button id="agent-chat-bottom"[^>]*aria-label="滚动到最新消息">{{icon "arrow-down"}}<\/button>/);
  assert.match(html, /id="agent-chat-input" rows="1"/);
  assert.match(html, /id="agent-chat-preference-note" class="hidden"/);
});
