const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const template = fs.readFileSync(path.join(__dirname, '../ui/templates/agentcore.html'), 'utf8');

// Parse only the composer: Go template icon calls are text, not HTML nodes.
function composerTree(source) {
  const form = source.match(/<form\b[^>]*id="agent-chat-form"[^>]*>[\s\S]*?<\/form>/);
  assert.ok(form, 'composer form exists');
  const root = { children: [] }, stack = [root];
  for (const match of form[0].matchAll(/<\/?[a-z][^>]*>/g)) {
    const token = match[0];
    if (token.startsWith('</')) {
      assert.equal(stack.pop().tag, token.match(/^<\/(\w+)/)[1], 'balanced composer tags');
      continue;
    }
    const node = { tag: token.match(/^<(\w+)/)[1], attrs: {}, children: [] };
    for (const attr of token.matchAll(/([\w-]+)="([^"]*)"/g)) node.attrs[attr[1]] = attr[2];
    stack.at(-1).children.push(node);
    if (!['input', 'br', 'hr', 'img'].includes(node.tag)) stack.push(node);
  }
  assert.equal(stack.length, 1);
  return root;
}
function all(node) { return [node, ...node.children.flatMap(all)]; }
function checkToolbar(source) {
  const nodes = all(composerTree(source));
  const toolbar = nodes.find(n => n.attrs?.class === 'agent-chat-composer-toolbar');
  assert.ok(toolbar, 'composer toolbar exists');
  assert.deepEqual(toolbar.children.map(n => n.attrs.class), ['agent-chat-composer-options', 'agent-chat-send-controls']);
  const options = toolbar.children[0].children;
  assert.deepEqual(options.map(n => n.attrs.id), ['agent-chat-mode']);
  assert.equal(options[0].tag, 'select');
  assert.equal(options[0].attrs['aria-label'], '权限模式');
  const controls = toolbar.children[1].children;
  assert.deepEqual(controls.map(n => n.attrs.id || n.attrs.class), ['agent-chat-settings', 'agent-chat-context', 'agent-chat-buttons']);
  assert.deepEqual(controls.map(n => n.tag), ['span', 'div', 'div']);
  assert.equal(controls[0].children.length, 0, 'model and effort are plain text');
  assert.deepEqual(controls[2].children.map(n => n.attrs.id), ['agent-chat-stop', 'agent-chat-send']);
}

test('composer toolbar has only mode on left, settings, context and actions on right', () => {
  checkToolbar(template);
});

test('toolbar CSS keeps mode left and send controls right', () => {
  const css = fs.readFileSync(path.join(__dirname, '../ui/static/agentcore-chat.css'), 'utf8');
  function rule(selector) {
    const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    const match = css.match(new RegExp(escaped + '\\s*\\{([^}]+)\\}'));
    assert.ok(match, `missing CSS rule ${selector}`);
    return Object.fromEntries(match[1].split(';').filter(Boolean).map(declaration => declaration.split(':').map(s => s.trim())));
  }
  assert.equal(rule('.agent-chat-composer-toolbar').display, 'flex');
  assert.equal(rule('.agent-chat-composer-options')['justify-content'], 'flex-start');
  const controls = rule('.agent-chat-send-controls');
  assert.equal(controls.display, 'flex');
  assert.equal(controls['justify-content'], 'flex-end');
  assert.equal(controls['margin-left'], 'auto');
});

test('chat template has no effort selector or keyboard shortcut hint', () => {
  assert.doesNotMatch(template, /agent-chat-effort|<select\b[^>]*(?:id|name)="[^"]*effort/i);
  assert.doesNotMatch(template, /<kbd\b|\b(?:Ctrl|Control|Cmd|Command|Enter)\b|⌘|快捷键|回车/i);
  const nodes = all(composerTree(template));
  assert.deepEqual(nodes.filter(n => n.tag === 'select').map(n => n.attrs.id), ['agent-chat-mode']);
});

test('composer starts idle with send visible and stop hidden', () => {
  const nodes = all(composerTree(template));
  for (const [id, hidden, type] of [['agent-chat-send', false, 'submit'], ['agent-chat-stop', true, 'button']]) {
    const node = nodes.find(n => n.attrs?.id === id);
    assert.equal(node.tag, 'button');
    assert.equal(node.attrs.type, type);
    assert.equal(node.attrs.class.split(/\s+/).includes('hidden'), hidden);
  }
});

test('toolbar validation rejects misplaced controls and interactive settings', () => {
  const settings = template.match(/<span id="agent-chat-settings"[^>]*>[\s\S]*?<\/span>/)[0];
  const mode = template.match(/<select id="agent-chat-mode"[^>]*>[\s\S]*?<\/select>/)[0];
  assert.throws(() => checkToolbar(template.replace(settings, '<!-- settings slot -->').replace(mode, settings).replace('<!-- settings slot -->', mode)));
  assert.throws(() => checkToolbar(template.replace(settings, settings.replace('<span', '<button').replace('</span>', '</button>'))));
  const context = template.match(/<div class="agent-chat-context"[^>]*>[\s\S]*?<\/div><\/div>/)[0];
  assert.throws(() => checkToolbar(template.replace(settings, '<!-- settings slot -->').replace(context, settings).replace('<!-- settings slot -->', context)));
  assert.throws(() => checkToolbar(template.replace('class="agent-chat-send-controls"', 'class="agent-chat-buttons"')));
});
