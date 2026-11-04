const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const css = fs.readFileSync(path.join(__dirname, '../ui/static/agentcore-chat.css'), 'utf8');
const layout = fs.readFileSync(path.join(__dirname, '../ui/templates/layout.html'), 'utf8');
function rule(selector) {
  const start = css.indexOf(selector + '{');
  assert.notEqual(start, -1, 'missing rule: ' + selector);
  return css.slice(start + selector.length + 1, css.indexOf('}', start));
}
test('AgentCore alone uses a viewport-height flex page and flexible chat region', () => {
  assert.match(layout, /<main{{if eq .Title "AgentCore"}} class="agentcore-page"{{end}}>/);
  assert.match(rule('.agentcore-page'), /display:flex/);
  assert.match(rule('.agentcore-page'), /height:100dvh/);
  assert.match(rule('.agent-chat'), /flex:1/);
  assert.match(rule('.agent-chat'), /grid-template-rows:minmax\(0,1fr\)/);
  assert.match(rule('.agent-chat-main'), /flex-direction:column/);
  assert.match(rule('.agent-chat-message-viewport'), /flex:1/);
  assert.match(rule('.agent-chat-messages'), /position:absolute;inset:0/);
  assert.match(rule('.agent-chat-messages'), /overflow:auto/);
  assert.doesNotMatch(rule('.agent-chat-messages'), /(?:^|;)height:\d+px/);
  assert.match(rule('.agent-chat-list'), /flex:1;min-height:0/);
});
test('short and mobile viewports keep a usable minimum and document scrolling', () => {
  assert.match(rule('.agentcore-page'), /min-height:760px/);
  assert.match(css, /@media\(max-width:820px\)\{\.agentcore-page\{height:auto/);
  assert.match(css, /min-height:650px;grid-template-rows:auto minmax\(0,1fr\)/);
});
test('process details have distinct reasoning, tool result and answer styling', () => {
  assert.match(css, /\.agent-chat-process:empty,\.agent-chat-answer:empty\{display:none\}/);
  assert.match(rule('.agent-chat-reasoning'), /border-left-color:var\(--accent\)/);
  assert.match(rule('.agent-chat-tool[data-state=success]'), /border-left-color:var\(--ok\)/);
});
