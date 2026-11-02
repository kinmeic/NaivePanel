const test = require('node:test');
const assert = require('node:assert/strict');
const editor = require('../ui/static/fail2ban-config.js');

test('editing one field preserves comments, CRLF, custom options and other sections', () => {
  const source = '# comment\r\n[DEFAULT]\r\nbantime = 1h ; keep this\r\ncustom = %(known/custom)s\r\n\r\n[sshd]\r\nenabled = false\r\naction = first\r\n    second\r\n\r\n[INCLUDES]\r\nbefore = custom.conf\r\n';
  assert.equal(editor.setOption(source, 'DEFAULT', 'bantime', '2h'), source.replace('bantime = 1h', 'bantime = 2h'));
  assert.equal(editor.setOption(source, 'sshd', 'enabled', 'false'), source);
  assert.equal(editor.setOption(source, 'DEFAULT', 'findtime', ''), source);
  const doc = editor.parseINI(source);
  assert.equal(editor.valueOf(editor.findSection(doc, 'sshd'), 'action'), 'first\nsecond');
});

test('multiline options round trip without creating new sections', () => {
  const source = '[sshd]\nenabled = false\nlogpath = /var/log/one\n    /var/log/two\n# retain me\n    /var/log/three\nport = 22\n';
  const changed = editor.setOption(source, 'sshd', 'logpath', '/new/one\n/new/two');
  const doc = editor.parseINI(changed);
  assert.equal(editor.valueOf(editor.findSection(doc, 'sshd'), 'logpath'), '/new/one\n/new/two');
  assert.match(changed, /# retain me/);
  assert.match(changed, /port = 22/);
  const injected = editor.setOption('[sshd]\n', 'sshd', 'logpath', '/a\n[other]\nenabled = true');
  assert.equal(editor.parseINI(injected).sections.length, 1);
});

test('removing an explicit value restores inheritance without rewriting neighbors', () => {
  const source = '[DEFAULT]\nmaxretry = 5\n[sshd]\nenabled = false\nport = 2222\n';
  const changed = editor.setOption(source, 'sshd', 'enabled', '');
  assert.equal(changed, source.replace('enabled = false\n', ''));
  assert.equal(editor.valueOf(editor.findSection(editor.parseINI(changed), 'sshd'), 'enabled'), '');
});

test('adding and removing jails preserves unrelated sections', () => {
  const source = '# header\n[DEFAULT]\nbantime = 1h\n[sshd]\nenabled = true\n';
  let changed = editor.addSection(source, 'recidive');
  changed = editor.setOption(changed, 'recidive', 'enabled', 'false');
  changed = editor.setOption(changed, 'recidive', 'findtime', '1d');
  const jail = editor.findSection(editor.parseINI(changed), 'recidive');
  assert.equal(editor.valueOf(jail, 'findtime'), '1d');
  assert.ok(editor.removeSection(changed, 'recidive').startsWith(source));
  assert.throws(() => editor.addSection(source, 'sshd'));
  assert.throws(() => editor.addSection(source, 'bad]\n[sshd'));
  assert.throws(() => editor.removeSection(source, 'DEFAULT'));
});

test('unrecognized/ambiguous syntax cannot be rewritten through structured editing', () => {
  for (const source of ['[sshd]\nenabled = false\n[sshd]\nenabled = true\n', '[sshd]\nenabled = false\nenabled = true\n', 'orphan option\n[sshd]\n']) {
    assert.throws(() => editor.parseINI(source));
    assert.throws(() => editor.setOption(source, 'sshd', 'enabled', 'true'));
  }
});

test('custom option names cannot alter parser prototypes', () => {
  const doc = editor.parseINI('[sshd]\n__proto__ = custom\nconstructor = preserved\nenabled = false\n');
  assert.equal(editor.valueOf(editor.findSection(doc, 'sshd'), 'constructor'), 'preserved');
  assert.equal({}.custom, undefined);
});
