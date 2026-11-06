const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const css = fs.readFileSync(path.join(__dirname, '../ui/static/agentcore-chat.css'), 'utf8');
const layout = fs.readFileSync(path.join(__dirname, '../ui/templates/layout.html'), 'utf8');
const chatTemplate = fs.readFileSync(path.join(__dirname, '../ui/templates/agentcore.html'), 'utf8');
function rule(selector) {
  const start = css.indexOf(selector + '{');
  assert.notEqual(start, -1, 'missing rule: ' + selector);
  return css.slice(start + selector.length + 1, css.indexOf('}', start));
}
function updatedRule(selector) {
  const start = css.indexOf(selector + '{', css.indexOf('/* Use the available timeline width'));
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
test('message column fills wide viewports without the old centered 780px cap', () => {
  assert.match(updatedRule('.agent-chat-message-wrap'), /width:100%;max-width:none;margin:0 0 24px/);
  assert.doesNotMatch(css, /max-width:\s*780px/);
  assert.match(updatedRule('.agent-chat-messages'), /padding:24px 12px/);
});
test('user bubbles keep natural width on the right and assistant messages use a plain column', () => {
  assert.match(updatedRule('.agent-chat-user'), /align-items:flex-end/);
  assert.match(updatedRule('.agent-chat-user .agent-chat-message'), /width:fit-content;max-width:min\(100%,36rem\)/);
  assert.match(updatedRule('.agent-chat-user .agent-chat-message'), /text-align:right/);
  assert.match(updatedRule('.agent-chat-message'), /border:0/);
});
test('pending requests overlay the scroll viewport and have an internally scrolling card', () => {
  const viewport = chatTemplate.match(/<div class="agent-chat-message-viewport">([\s\S]*?)<\/div>\s*<p id="agent-chat-error"/);
  assert.ok(viewport);
  assert.match(viewport[1], /id="agent-chat-requests"/);
  assert.doesNotMatch(chatTemplate.match(/<form id="agent-chat-form"[\s\S]*?<\/form>/)[0], /agent-chat-requests/);
  assert.match(updatedRule('.agent-chat-requests'), /position:absolute/);
  assert.match(updatedRule('.agent-chat-request-body'), /overflow-y:auto/);
  assert.match(updatedRule('.agent-chat-messages'), /--agent-request-height/);
});
