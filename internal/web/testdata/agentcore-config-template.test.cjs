const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const template = fs.readFileSync(path.join(__dirname, '../ui/templates/agentcore_config.html'), 'utf8');
const expectedFields = new Map([
  ['agent-host', '监听地址'],
  ['agent-port', '监听端口'],
  ['agent-api-format', '模型接口'],
  ['agent-model', '模型 ID'],
  ['agent-base-url', '模型 Base URL'],
  ['agent-api-key', '模型 API Key'],
  ['agent-reasoning-effort', '模型思考强度'],
  ['agent-context-window', '上下文窗口'],
  ['agent-max-output-tokens', '最大输出 Token'],
  ['agent-workspace', '工作区路径'],
  ['agent-storage-dir', '存储目录'],
  ['agent-db-path', '数据库路径'],
  ['agent-permission', '权限模式'],
]);

// Validate source nesting strictly: browser error recovery can hide unmatched labels.
function checkBaseForm(source) {
  const section = source.match(/<section\b[^>]*data-agent-panel="base"[^>]*>([\s\S]*?)<\/section>/);
  assert.ok(section, 'base settings section exists');
  const stack = [];
  const fields = new Map();
  let gridCount = 0;
  for (const token of section[1].matchAll(/<[^>]+>|[^<]+/g)) {
    const text = token[0];
    if (!text.startsWith('<')) {
      assert.ok(stack.at(-1)?.tag !== 'div' || !stack.at(-1).grid || !text.trim(), 'no floating grid text');
      if (stack.at(-1)?.tag === 'label') stack.at(-1).text += text;
      continue;
    }
    const closing = text.match(/^<\/(\w+)\s*>$/);
    if (closing) {
      const node = stack.pop();
      assert.equal(node?.tag, closing[1], `balanced closing tag ${text}`);
      if (node.gridLabel) {
        assert.equal(node.controls.length, 1, 'each grid label wraps exactly one control');
        const id = node.controls[0];
        assert.ok(expectedFields.has(id), `known field ${id}`);
        assert.ok(!fields.has(id), `unique field ${id}`);
        fields.set(id, node.text.trim());
      }
      continue;
    }
    const opening = text.match(/^<(\w+)\b/);
    assert.ok(opening, `recognized tag ${text}`);
    const tag = opening[1];
    const parent = stack.at(-1);
    if (parent?.grid) assert.equal(tag, 'label', 'grid direct children must be labels');
    if (tag === 'label') assert.ok(!stack.some(node => node.tag === 'label'), 'no nested labels');
    if (['input', 'select', 'textarea'].includes(tag)) {
      const label = stack.findLast(node => node.tag === 'label');
      assert.ok(label, 'base control is inside a label');
      if (label.gridLabel) label.controls.push(text.match(/\bid="([^"]+)"/)?.[1]);
    }
    const grid = tag === 'div' && /class="config-field-grid"/.test(text);
    if (grid) gridCount++;
    if (!['input', 'br', 'hr', 'img', 'meta', 'link'].includes(tag)) {
      stack.push({ tag, grid, gridLabel: tag === 'label' && parent?.grid, text: '', controls: [] });
    }
  }
  assert.equal(stack.length, 0, 'all base settings tags close');
  assert.equal(gridCount, 1);
  assert.deepEqual(fields, expectedFields);
}

test('base settings grid labels fully contain every field with no floating text', () => {
  checkBaseForm(template);
});

test('structure validation rejects premature labels and floating Token text', () => {
  for (const [valid, broken] of [
    ['<label>上下文窗口<input', '<label>上下文窗口</label>（Token）<input'],
    ['<label>最大输出 Token<input', '<label>最大</label>输出 Token<input'],
    ['<label>工作区路径<input', '<label></label>工作区路径<input'],
    ['<label>上下文窗口<input', '（Token）<label>上下文窗口<input'],
  ]) {
    assert.ok(template.includes(valid));
    assert.throws(() => checkBaseForm(template.replace(valid, broken)));
  }
});
