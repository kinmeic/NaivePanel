(function () {
  "use strict";
  var source = document.getElementById("agent-config-json");
  if (!source) return;
  var state;
  try { state = JSON.parse(source.value); } catch (e) { showError("无法解析结构化配置：" + e.message); return; }
  function el(id) { return document.getElementById(id); }
  function obj(parent, key) { if (!parent[key] || typeof parent[key] !== "object" || Array.isArray(parent[key])) parent[key] = {}; return parent[key]; }
  function value(id) { return el(id).value.trim(); }
  function setValue(id, v) { el(id).value = v == null ? "" : String(v); }
  function showError(message) { var target = el("agent-editor-error"); if (target) { target.textContent = message; target.classList.remove("hidden"); target.scrollIntoView({ block: "nearest" }); } }
  function button(label, click) { var b = document.createElement("button"); b.type = "button"; b.className = "btn"; b.textContent = label; b.addEventListener("click", click); return b; }
  function cell(row, text) { var td = document.createElement("td"); td.textContent = text == null ? "" : String(text); row.appendChild(td); return td; }
  function tab(name) { document.querySelectorAll("[data-agent-tab]").forEach(function (b) { b.classList.toggle("active", b.dataset.agentTab === name); }); document.querySelectorAll("[data-agent-panel]").forEach(function (p) { p.classList.toggle("hidden", p.dataset.agentPanel !== name && !(name === "skills" && p.dataset.agentPanel === "skills-files")); }); }
  document.querySelectorAll("[data-agent-tab]").forEach(function (b) { b.addEventListener("click", function () { tab(b.dataset.agentTab); }); });
  var server = obj(state, "server"), llm = obj(state, "llm"), workspace = obj(state, "workspace"), storage = obj(state, "storage"), permission = obj(state, "permission"), features = obj(state, "features"), skills = obj(state, "skills");
  setValue("agent-host", server.host); setValue("agent-port", server.port); setValue("agent-api-format", llm.api_format || "openai"); setValue("agent-model", llm.model); setValue("agent-base-url", llm.base_url); setValue("agent-api-key", llm.api_key); setValue("agent-workspace", workspace.path); setValue("agent-storage-dir", storage.dir); setValue("agent-db-path", storage.sessionDbPath); setValue("agent-permission", permission.mode || "build"); el("agent-terminal").checked = server.terminalEnabled == null ? isLoopback(server.host) : !!server.terminalEnabled;
    function isLoopback(host) { return host == null || host === "" || host === "127.0.0.1" || host === "localhost" || host === "::1"; }
    setValue("agent-reasoning-effort", llm.reasoning_effort);
    setValue("agent-context-window", llm.context_window == null ? 200000 : llm.context_window);
    setValue("agent-max-output-tokens", llm.max_output_tokens == null ? 32000 : llm.max_output_tokens);
  el("agent-feature-skill").checked = features.skill !== false; el("agent-skills-enabled").checked = skills.enabled !== false; el("agent-skills-instructions").checked = skills.includeInstructions !== false; setValue("agent-skill-roots", (skills.roots || []).join("\n")); el("agent-feature-mcp").checked = features.mcp !== false;
  var overrides = obj(state, "skill"), servers = obj(obj(state, "mcp"), "servers");
  function renderOverrides() {
    var tbody = el("agent-override-rows"); tbody.replaceChildren();
    Object.keys(overrides).sort().forEach(function (path) {
      var row = document.createElement("tr"), pathCell = document.createElement("td"), input = document.createElement("input"); input.value = path; input.className = "mono"; input.setAttribute("aria-label", "SKILL.md 绝对路径"); input.addEventListener("change", function () { var next = input.value.trim(); if (!next.startsWith("/") || !next.endsWith("/SKILL.md")) { showError("Skill 路径必须是 SKILL.md 的绝对路径"); input.value = path; return; } if (next !== path) { overrides[next] = overrides[path]; delete overrides[path]; renderOverrides(); } }); pathCell.appendChild(input); row.appendChild(pathCell);
      var enabledCell = document.createElement("td"), check = document.createElement("input"); check.type = "checkbox"; check.checked = !overrides[path] || overrides[path].enable !== false; check.setAttribute("aria-label", "启用 " + path); check.addEventListener("change", function () { overrides[path] = Object.assign({}, overrides[path], { enable: check.checked }); }); enabledCell.appendChild(check); row.appendChild(enabledCell);
      var action = cell(row, ""); action.appendChild(button("删除", function () { delete overrides[path]; renderOverrides(); })); tbody.appendChild(row);
    });
    if (!tbody.children.length) { var row = document.createElement("tr"), td = document.createElement("td"); td.colSpan = 3; td.className = "config-empty-cell"; td.textContent = "暂无单项覆盖；所有已发现的 Skill 默认启用。"; row.appendChild(td); tbody.appendChild(row); }
  }
  el("agent-override-add").addEventListener("click", function () { var path = window.prompt("输入 SKILL.md 的绝对路径"); if (path == null) return; path = path.trim(); if (!path.startsWith("/") || !path.endsWith("/SKILL.md")) { showError("Skill 路径必须是 SKILL.md 的绝对路径"); return; } overrides[path] = { enable: true }; renderOverrides(); }); renderOverrides();
  var dialog = el("agent-mcp-dialog"), editingName = null;
  function openMCP(name) {
    editingName = name || null; var data = name ? servers[name] : {}; el("agent-mcp-dialog-title").textContent = name ? "编辑 MCP 服务" : "添加 MCP 服务"; setValue("agent-mcp-name", name || ""); setValue("agent-mcp-type", data.type || "stdio"); setValue("agent-mcp-command", data.command); setValue("agent-mcp-url", data.url); setValue("agent-mcp-cwd", data.cwd); setValue("agent-mcp-timeout", data.timeoutMs); setValue("agent-mcp-args", (data.args || []).join("\n")); setValue("agent-mcp-env", JSON.stringify(data.env || {}, null, 2)); setValue("agent-mcp-headers", JSON.stringify(data.headers || {}, null, 2)); el("agent-mcp-enabled").checked = data.enabled !== false; el("agent-mcp-always").checked = !!data.alwaysLoad; el("agent-mcp-error").classList.add("hidden"); dialog.showModal();
  }
  function renderMCP() {
    var tbody = el("agent-mcp-rows"); tbody.replaceChildren(); Object.keys(servers).sort().forEach(function (name) { var data = servers[name], row = document.createElement("tr"); cell(row, name); cell(row, data.type); cell(row, data.type === "stdio" ? data.command : data.url); var enabled = document.createElement("input"); enabled.type = "checkbox"; enabled.checked = data.enabled !== false; enabled.setAttribute("aria-label", "启用 " + name); enabled.addEventListener("change", function () { data.enabled = enabled.checked; }); cell(row, "").appendChild(enabled); var actions = cell(row, ""); actions.className = "config-row-actions"; actions.appendChild(button("编辑", function () { openMCP(name); })); actions.appendChild(button("删除", function () { if (window.confirm("删除 MCP 服务 " + name + "？")) { delete servers[name]; renderMCP(); } })); tbody.appendChild(row); }); if (!tbody.children.length) { var row = document.createElement("tr"), td = document.createElement("td"); td.colSpan = 5; td.className = "config-empty-cell"; td.textContent = "暂无 MCP 服务。"; row.appendChild(td); tbody.appendChild(row); }
  }
  el("agent-mcp-add").addEventListener("click", function () { openMCP(null); }); el("agent-mcp-close").addEventListener("click", function () { dialog.close(); }); el("agent-mcp-cancel").addEventListener("click", function () { dialog.close(); });
  el("agent-mcp-save").addEventListener("click", function () {
    var name = value("agent-mcp-name"), type = value("agent-mcp-type"), env, headers, timeout = value("agent-mcp-timeout"); var error = el("agent-mcp-error");
    try { if (!name || !/^[A-Za-z0-9][A-Za-z0-9_-]*$/.test(name)) throw new Error("服务名称只能使用字母、数字、下划线和连字符"); if (name !== editingName && servers[name]) throw new Error("服务名称已存在"); env = JSON.parse(value("agent-mcp-env") || "{}"); headers = JSON.parse(value("agent-mcp-headers") || "{}"); if (!env || Array.isArray(env) || typeof env !== "object" || !headers || Array.isArray(headers) || typeof headers !== "object") throw new Error("环境变量和请求头必须是 JSON 对象"); if (type === "stdio" && !value("agent-mcp-command")) throw new Error("stdio 服务需要命令"); if (type !== "stdio" && !value("agent-mcp-url")) throw new Error("HTTP/SSE 服务需要 URL"); if (timeout && (+timeout < 0 || !Number.isInteger(+timeout))) throw new Error("超时必须是非负整数"); }
    catch (e) { error.textContent = e.message; error.classList.remove("hidden"); return; }
    var data = editingName ? Object.assign({}, servers[editingName]) : {}; data.type = type; data.enabled = el("agent-mcp-enabled").checked; data.alwaysLoad = el("agent-mcp-always").checked; if (timeout) data.timeoutMs = +timeout; else delete data.timeoutMs;
    if (type === "stdio") { data.command = value("agent-mcp-command"); data.args = value("agent-mcp-args").split("\n").map(function (x) { return x.trim(); }).filter(Boolean); data.cwd = value("agent-mcp-cwd"); data.env = env; delete data.url; delete data.headers; } else { data.url = value("agent-mcp-url"); data.headers = headers; delete data.command; delete data.args; delete data.cwd; delete data.env; }
    if (editingName && editingName !== name) delete servers[editingName]; servers[name] = data; renderMCP(); dialog.close();
  }); renderMCP();
  el("agent-structured-form").addEventListener("submit", function (event) {
    var contextWindow = Number(value("agent-context-window")), maxOutputTokens = Number(value("agent-max-output-tokens"));
    if (!Number.isSafeInteger(contextWindow) || contextWindow <= 0 || !Number.isSafeInteger(maxOutputTokens) || maxOutputTokens <= 0) { event.preventDefault(); showError("上下文窗口和最大输出 Token 必须是正整数"); return; }
    if (!isLoopback(value("agent-host")) && (el("agent-terminal").checked || !server.auth_token)) { event.preventDefault(); showError("非本机监听必须关闭终端工具，并在 Raw YAML 中设置 server.auth_token；环境变量是否有效由 AgentCore 校验"); return; }
    llm.reasoning_effort = value("agent-reasoning-effort"); llm.context_window = contextWindow; llm.max_output_tokens = maxOutputTokens;
    server.host = value("agent-host"); server.port = +value("agent-port"); server.terminalEnabled = el("agent-terminal").checked; llm.api_format = value("agent-api-format"); llm.model = value("agent-model"); llm.base_url = value("agent-base-url"); llm.api_key = value("agent-api-key"); workspace.path = value("agent-workspace"); storage.dir = value("agent-storage-dir"); storage.sessionDbPath = value("agent-db-path"); permission.mode = value("agent-permission"); features.skill = el("agent-feature-skill").checked; skills.enabled = el("agent-skills-enabled").checked; skills.includeInstructions = el("agent-skills-instructions").checked; skills.roots = value("agent-skill-roots").split("\n").map(function (x) { return x.trim(); }).filter(Boolean); features.mcp = el("agent-feature-mcp").checked;
    if (!server.host || !server.port || server.port < 1 || server.port > 65535) { event.preventDefault(); showError("请填写有效的监听地址和端口"); return; }
    el("agent-structured-value").value = JSON.stringify(state);
  });
})();
