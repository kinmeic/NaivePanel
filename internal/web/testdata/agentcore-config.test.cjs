const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const script = fs.readFileSync(path.join(__dirname, '../ui/static/agentcore-config.js'), 'utf8');
const template = fs.readFileSync(path.join(__dirname, '../ui/templates/agentcore_config.html'), 'utf8');

function editor(state) {
  const elements = new Map();
  function element() {
    return { value: '', checked: false, children: [], listeners: {}, dataset: {},
      classList: { add() {}, remove() {}, toggle() {} },
      addEventListener(name, fn) { this.listeners[name] = fn; },
      appendChild(child) { this.children.push(child); },
      replaceChildren() { this.children = []; }, setAttribute() {}, scrollIntoView() {} };
  }
  for (const match of template.matchAll(/id="([^"]+)"/g)) elements.set(match[1], element());
  elements.get('agent-config-json').value = JSON.stringify(state);
  vm.runInNewContext(script, { document: {
    getElementById(id) { assert.ok(elements.has(id), `missing template element ${id}`); return elements.get(id); },
    querySelectorAll() { return []; }, createElement: element,
  }, window: {} });
  return { get: id => elements.get(id), submit() {
    let prevented = false;
    elements.get('agent-structured-form').listeners.submit({ preventDefault() { prevented = true; } });
    return { prevented, state: prevented ? null : JSON.parse(elements.get('agent-structured-value').value) };
  } };
}
function config(server = {}) { return { server: { host: '127.0.0.1', port: 3030, ...server }, llm: { model: 'test', custom: 'keep' }, unknown: { keep: true } }; }

test('actual defaults and model settings round-trip without dropping unknown fields', () => {
  const e = editor(config());
  assert.equal(e.get('agent-context-window').value, '200000');
  assert.equal(e.get('agent-max-output-tokens').value, '32000');
  assert.equal(e.get('agent-reasoning-effort').value, '');
  assert.equal(e.get('agent-terminal').checked, true);
  e.get('agent-context-window').value = '128000';
  e.get('agent-max-output-tokens').value = '8192';
  e.get('agent-reasoning-effort').value = 'provider-specific';
  const result = e.submit();
  assert.equal(result.prevented, false);
  assert.equal(result.state.llm.context_window, 128000);
  assert.equal(result.state.llm.max_output_tokens, 8192);
  assert.equal(result.state.llm.reasoning_effort, 'provider-specific');
  assert.equal(result.state.llm.custom, 'keep');
  assert.deepEqual(result.state.unknown, { keep: true });
});
test('existing values, explicit terminal false and clearing effort are preserved', () => {
  const state = config({ terminalEnabled: false });
  Object.assign(state.llm, { context_window: 64000, max_output_tokens: 4096, reasoning_effort: 'high' });
  const e = editor(state);
  assert.equal(e.get('agent-reasoning-effort').value, 'high');
  e.get('agent-reasoning-effort').value = '';
  const result = e.submit().state;
  assert.equal(result.server.terminalEnabled, false);
  assert.equal(result.llm.context_window, 64000);
  assert.equal(result.llm.max_output_tokens, 4096);
  assert.equal(result.llm.reasoning_effort, '');
});
test('token limits must be positive safe integers', () => {
  for (const id of ['agent-context-window', 'agent-max-output-tokens']) {
    for (const invalid of ['', '0', '-1', '1.5', 'NaN', 'Infinity', '9007199254740992']) {
      const e = editor(config()); e.get(id).value = invalid;
      assert.equal(e.submit().prevented, true, `${id}: ${invalid}`);
    }
  }
});
test('network binding requires disabled terminal and authentication', () => {
  const e = editor(config({ host: '0.0.0.0' }));
  assert.equal(e.get('agent-terminal').checked, false);
  assert.equal(e.submit().prevented, true);
  const authenticated = editor(config({ host: '0.0.0.0', auth_token: '${AUTH_TOKEN}' }));
  assert.equal(authenticated.submit().prevented, false);
  authenticated.get('agent-terminal').checked = true;
  assert.equal(authenticated.submit().prevented, true);
  const changed = editor(config());
  changed.get('agent-host').value = '0.0.0.0';
  assert.equal(changed.submit().prevented, true);
});
