(function () {
  "use strict";
  var root = document.getElementById("agent-chat"); if (!root) return;
  var core = window.AgentChatCore;
  function el(id) { return document.getElementById("agent-chat-" + id); }
  var enabled = root.dataset.enabled === "true", current = "", busy = false, loading = false, ready = true;
  var generation = 0, listGeneration = 0, historyController, streamController, stopping = false, stopped = false;
  var input = el("input"), messages = el("messages"), list = el("list"), follow = true, options = {}, usageInfo = null;
  var sessions = new Map(), deleting = new Set(), liveStates = new Map(), statusQueue = [], statusActive = 0, statusEpoch = 0, statusTimer = null, statusController, pageStopped = false;
    var pending = new Map(), optionsReady = false, conversationMetadata = {}, preferencesKnown = true;
    var selectedMode = "build", confirmedMode = "";
    var modeValues = ["plan", "build", "edit", "guarded", "yolo"];
    function fillSelect(id, values, selected, labels) {
      var select = el(id); select.replaceChildren();
      values = values.slice(); if (selected && values.indexOf(selected) < 0) values.push(selected);
      values.forEach(function (value) { var option = node("option", labels[value] || value); option.value = value; select.appendChild(option); });
      select.value = selected;
    }
    function renderPreferences() {
      var effort = current && !preferencesKnown ? "未知" : conversationMetadata.effort || options.reasoning_effort || "默认";
      var mode = current ? (preferencesKnown ? conversationMetadata.mode || "" : "unknown") : selectedMode;

      fillSelect("mode", modeValues, mode, { "": "继承服务默认（" + (options.approval_mode || "未知") + "）", plan: "plan · 限制写入", build: "build · 按风险审批", edit: "edit · 允许工作区编辑", guarded: "guarded · 广泛允许，终端仍需审批", yolo: "yolo · 高危广泛允许", unknown: "会话设置读取失败" });
      el("settings").textContent = "模型：" + (current && !preferencesKnown ? "未知" : conversationMetadata.model || options.model || "未知") + " · 思考强度：" + effort;
      el("preference-note").textContent = "";
            el("preference-note").title = current ? "已有会话权限模式只读；修改请选择新建会话。" : "权限模式仅对新会话生效，默认 build。";
      controls();
    }
    function confirmPermission(mode) {
      if (["edit", "guarded", "yolo"].indexOf(mode) < 0 || confirmedMode === mode) return true;
      if (!window.confirm("确认使用 " + mode + " 权限模式？该模式会自动允许更多工具操作，可能修改或删除文件、执行命令或产生外部影响。仅对可信任务使用。")) return false;
      confirmedMode = mode; return true;
    }

    el("mode").addEventListener("change", function () {
      if (current || busy) return;
      var value = el("mode").value;
      if (confirmPermission(value)) selectedMode = value;
      else el("mode").value = selectedMode;
    });
  function node(tag, text, cls) { var n = document.createElement(tag); n.textContent = text || ""; if (cls) n.className = cls; return n; }
  function error(text) { el("error").textContent = text; el("error").classList.toggle("hidden", !text); }
  function status(text) { el("status").textContent = text; }
  function controls() {
    input.disabled = !enabled || busy || loading || !ready;
        el("mode").disabled = !enabled || !optionsReady || !!current || busy || loading;
    el("send").disabled = input.disabled || !input.value.trim();
    el("send").classList.toggle("hidden", busy);
    el("stop").classList.toggle("hidden", !busy);
    el("new").disabled = !enabled || busy; el("refresh").disabled = !enabled || busy;
    el("stop").disabled = !busy || !current || stopping || stopped;
    el("stop").title = !current && busy ? "等待流返回会话 ID" : "请求停止当前运行";
    sessions.forEach(function (s, id) {
          s.open.disabled = busy || deleting.has(id);
          s.remove.disabled = busy || deleting.has(id) || ["unknown", "running", "waiting"].indexOf(s.state) >= 0;
        });
    messages.setAttribute("aria-busy", String(busy || loading));
  }
  async function request(path, body, signal, stream) {
    var headers = { Accept: stream ? "text/event-stream" : "application/json", "X-CSRF-Token": root.dataset.csrf };
    var init = { method: body === undefined ? "GET" : "POST", credentials: "same-origin", cache: "no-store", headers: headers, signal: signal };
    if (body !== undefined) { headers["Content-Type"] = "application/json"; init.body = JSON.stringify(body); }
    var response;
        try { response = await fetch(root.dataset.api + path, init); }
        catch (err) { if (err.name !== "AbortError") err.retryable = true; throw err; }
    if (!response.ok) {
      var detail = ""; try { var data = await response.json(); detail = data.error || data.message || ""; } catch (_) { /* Do not render server HTML. */ }
      var failure = new Error("HTTP " + response.status + (typeof detail === "string" && detail ? "：" + detail : ""));
            failure.retryable = response.status >= 500 || response.status === 408 || response.status === 429;
            throw failure;
    }
    if (stream) {
      if (!/text\/event-stream/i.test(response.headers.get("Content-Type") || "") || !response.body) throw new Error("服务未返回 SSE 流，请检查登录状态及代理配置");
      return response;
    }
    try { return await response.json(); } catch (_) { throw new Error("响应不是 JSON，请检查登录状态"); }
  }
  function scroll(force) { if (force || follow) messages.scrollTop = messages.scrollHeight; el("bottom").classList.toggle("hidden", follow); }
  messages.addEventListener("scroll", function () { follow = messages.scrollHeight - messages.scrollTop - messages.clientHeight < 64; el("bottom").classList.toggle("hidden", follow); });
  el("bottom").addEventListener("click", function () { follow = true; scroll(true); });
  function empty(text) { messages.replaceChildren(node("p", text, "agent-chat-empty muted")); }
  async function copy(text, button) {
    var label = button.dataset.copyLabel || "复制代码";
    try { await navigator.clipboard.writeText(text); button.textContent = "已复制"; }
    catch (_) { error("剪贴板不可用，请手动选择复制。"); button.textContent = "复制失败"; }
    window.setTimeout(function () { button.textContent = label; }, 1600);
  }
  function addMessage(entry) {
    var placeholder = messages.querySelector(".agent-chat-empty"); if (placeholder) placeholder.remove();
    var article = node("article", "", "agent-chat-message" + (entry.role === "user" ? " agent-chat-user" : ""));
    var head = node("div", "", "agent-chat-message-head");
    head.appendChild(node("strong", ({ user: "你", assistant: "助手", tool: "工具", system: "系统" })[entry.role] || "消息"));
    if (entry.created_at) { var date = new Date(entry.created_at); if (!isNaN(date.getTime())) head.appendChild(node("time", date.toLocaleString(), "muted")); }
    var rawContent = "", copyMessage = node("button", "复制消息", "btn"); copyMessage.type = "button"; copyMessage.dataset.copyLabel = "复制消息";
        copyMessage.setAttribute("aria-label", "复制完整消息");
        copyMessage.addEventListener("click", function () {
          var text = rawContent;
          if (entry.tool_calls && entry.tool_calls.length) text += "\n\n工具调用：\n" + JSON.stringify(entry.tool_calls, null, 2);
          if (entry.actions && entry.actions.length) text += "\n\n工具执行记录：\n" + JSON.stringify(entry.actions, null, 2);
          return copy(text, copyMessage);
        }); head.appendChild(copyMessage);
        article.appendChild(head); var body = node("div", "", "agent-chat-message-body"); article.appendChild(body); messages.appendChild(article);
    function update(text) { rawContent = text; body.replaceChildren(core.markdown(document, text, copy)); scroll(false); }
    update(entry.content || "");
    if (entry.tool_calls && entry.tool_calls.length) detail(article, "工具调用", JSON.stringify(entry.tool_calls, null, 2));
    if (entry.actions && entry.actions.length) detail(article, "工具执行记录", JSON.stringify(entry.actions, null, 2));
    if (entry.status) article.appendChild(node("small", "状态：" + entry.status, "muted"));
    return { article: article, update: update };
  }
  function detail(parent, title, content) {
    var details = node("details", "", "agent-chat-detail"); details.appendChild(node("summary", title));
    var text = node("pre", content || ""); details.appendChild(text); parent.appendChild(details); scroll(false); return { details: details, text: text };
  }
  function updateUsage(info) {
    usageInfo = info; var u = core.usage(info, options);
    el("ring").style.setProperty("--usage", (u.percent || 0) + "%");
    el("usage-label").textContent = u.percent === null ? "上下文未知" : u.percent.toFixed(1) + (u.estimated ? "% 输入估算" : "% 后端报告参考");
    el("usage").textContent = "模型：" + (conversationMetadata.model || options.model || "未知") + "\n窗口：" + (u.window || "未知") + " tokens\n最大输出：" + (options.max_output_tokens || "未知") + "\n后端 context_tokens：" + (u.context === null ? "未知" : u.context) + "\n累计输入：" + (info ? u.prompt : "未知") + "\n累计输出：" + (info ? u.completion : "未知") + "\n累计总量：" + (info ? u.total : "未知") + "\n圆环优先使用后端 context_tokens / 配置窗口；缺少时使用累计输入 prompt_tokens / 窗口作估算。累计输入会叠加多次模型调用，不是精确当前占用，可能超过窗口；圆环最多显示 100%。当前 AgentCore 历史 context_tokens 也可能是累计总量；模型覆盖后的窗口需以该模型配置为准。";
  }
  async function loadOptions() {
    try {
      options = await request("/options"); if (!options || typeof options !== "object" || Array.isArray(options)) throw new Error("配置格式错误");
      optionsReady = true;
      el("settings").title = "当前模型与思考强度；服务默认值可在配置页修改。";
      renderPreferences();
    } catch (err) { optionsReady = false; options = {}; el("settings").textContent = "配置读取失败（" + err.message + "）"; controls(); }
    updateUsage(usageInfo);
  }
  function paintSession(s, state, label) {
    s.state = state; s.dot.dataset.state = state;
    s.dot.title = label; s.dot.setAttribute("aria-label", label); controls();
  }
  function runState(data) {
    if (data === null) data = [];
    if (!Array.isArray(data)) throw new Error("运行列表格式错误");
    var runs = data.filter(function (r) { return r && typeof r.status === "string"; });
    runs.sort(function (a, b) { return (Date.parse(b.created_at) || Date.parse(b.updated_at) || 0) - (Date.parse(a.created_at) || Date.parse(a.updated_at) || 0) || (Date.parse(b.updated_at) || 0) - (Date.parse(a.updated_at) || 0); });
    var active = runs.find(function (r) { return ["running", "pending", "waiting_approval", "waiting_clarification"].indexOf(r.status) >= 0; });
    var latest = active || runs[0];
    if (!latest) return data.length ? ["unknown", "运行状态未知"] : ["idle", "无运行"];
    var value = latest.status;
    if (["running", "pending"].indexOf(value) >= 0) return ["running", "运行中（" + value + "）"];
    if (["waiting_approval", "waiting_clarification"].indexOf(value) >= 0) return ["waiting", value === "waiting_approval" ? "等待权限审批" : "等待澄清回答"];
    if (value === "failed") return ["failed", "最新运行失败"];
    if (["completed", "cancelled"].indexOf(value) >= 0) return ["idle", "最新运行：" + value];
    return ["unknown", "运行状态未知（" + value + "）"];
  }
  function stopStatus() {
    ++statusEpoch; statusQueue = []; window.clearTimeout(statusTimer); statusTimer = null;
    if (statusController) statusController.abort();
  }
  function scheduleStatus() {
    if (pageStopped || document.hidden || !sessions.size || statusQueue.length || statusActive || statusTimer !== null) return;
    statusTimer = window.setTimeout(function () { statusTimer = null; pollStatus(); }, 30000);
  }
  function pumpStatus() {
    while (statusActive < 4 && statusQueue.length) {
      var job = statusQueue.shift(); statusActive++;
      (async function (job) {
        var s = sessions.get(job.id), revision = liveStates.get(job.id);
        try {
          var data = await request("/conversations/" + encodeURIComponent(job.id) + "/runs?limit=100", undefined, job.signal);
          if (job.epoch !== statusEpoch || sessions.get(job.id) !== s || liveStates.get(job.id) !== revision || busy && current === job.id) return;
          var result = runState(data); paintSession(s, result[0], result[1]);
        } catch (err) {
          if (job.epoch === statusEpoch && sessions.get(job.id) === s && liveStates.get(job.id) === revision && !(busy && current === job.id)) paintSession(s, "unknown", "运行状态未知：" + err.message);
        } finally { statusActive--; pumpStatus(); scheduleStatus(); }
      })(job);
    }
    scheduleStatus();
  }
  function pollStatus() {
    stopStatus();
    if (pageStopped || document.hidden) return;
    statusController = new AbortController();
    sessions.forEach(function (_, id) { statusQueue.push({ id: id, epoch: statusEpoch, signal: statusController.signal }); });
    pumpStatus();
  }
  function liveStatus(state, label) {
    if (!current) return;
    var value = { state: state, label: label }; liveStates.set(current, value);
    var s = sessions.get(current); if (s) paintSession(s, state, label);
  }
  async function deleteSession(id) {
    var s = sessions.get(id);
    if (!s || s.remove.disabled || deleting.has(id) || pageStopped) return;
    if (!window.confirm("将此会话移出历史列表（归档）？相关消息仍保留，不会物理删除。")) return;
    deleting.add(id); controls(); var selection = generation;
    try {
      var result = await request("/conversations/" + encodeURIComponent(id) + "/delete", {});
      if (!result || result.status !== "archived") throw new Error("服务未确认归档");
      if (pageStopped) return;
      ++listGeneration; sessions.delete(id); liveStates.delete(id);
      list.querySelectorAll(".agent-chat-session-row").forEach(function (row) { if (row.dataset.id === id) row.remove(); });
      if (current === id && selection === generation && !busy) await openConversation("", "");
      el("list-status").textContent = sessions.size ? "" : "暂无历史会话";
    } catch (err) { if (!pageStopped) error("归档失败：" + err.message); }
    finally { deleting.delete(id); controls(); }
  }
  async function refreshList() {
    if (!enabled || pageStopped) return; var token = ++listGeneration; el("list-status").textContent = "加载中…";
    try {
      var data = await request("/conversations"); if (token !== listGeneration) return;
      if (data === null) data = []; if (!Array.isArray(data)) throw new Error("会话列表格式错误"); stopStatus(); sessions.clear(); list.replaceChildren();
      data.forEach(function (c) {
        if (!c || typeof c.id !== "string" || !c.id) return;
        var row = node("div", "", "agent-chat-session-row"); row.dataset.id = c.id;
                var dot = node("span", "", "agent-chat-session-dot"); row.appendChild(dot);
                var b = node("button", c.title || "未命名会话", "agent-chat-session agent-chat-session-open"); b.type = "button"; b.dataset.id = c.id;
        b.title = (c.title || "未命名会话") + " · " + c.id; b.setAttribute("aria-pressed", String(current === c.id));
        b.addEventListener("click", function () { openConversation(c.id, c.title); }); row.appendChild(b);
                var remove = node("button", "", "agent-chat-session-delete btn"); remove.type = "button";
                remove.title = "删除会话（归档）"; remove.setAttribute("aria-label", remove.title);
                var template = el("delete-icon");
                if (template && template.content) remove.appendChild(template.content.cloneNode(true));
                else {
                  var svg = document.createElementNS("http://www.w3.org/2000/svg", "svg"); svg.setAttribute("viewBox", "0 0 24 24"); svg.setAttribute("width", "16"); svg.setAttribute("height", "16"); svg.setAttribute("fill", "none"); svg.setAttribute("stroke", "currentColor"); svg.setAttribute("stroke-width", "2");
                  var path = document.createElementNS("http://www.w3.org/2000/svg", "path"); path.setAttribute("d", "M3 6h18M9 6V4h6v2M5 6l1 14h12l1-14M10 10v6M14 10v6"); svg.appendChild(path); remove.appendChild(svg);
                }
                remove.addEventListener("click", function () { return deleteSession(c.id); }); row.appendChild(remove); list.appendChild(row);
                var s = { open: b, remove: remove, dot: dot }; sessions.set(c.id, s);
                var live = liveStates.get(c.id); paintSession(s, live ? live.state : "unknown", live ? live.label : "运行状态未知，正在查询");
      });
      el("list-status").textContent = sessions.size ? "" : "暂无历史会话"; controls(); pollStatus();
    } catch (err) { if (token === listGeneration) el("list-status").textContent = "加载失败：" + err.message + "；点击刷新重试"; }
  }
  function clearPending() { pending.clear(); el("requests").replaceChildren(); }
  function ask(event, clarify) {
    var id = event.request_id;
    if (!id) { error("待处理请求缺少 request_id，无法安全提交。"); return; }
    if (pending.has(id)) return;
    var token = generation, conversation = current, box = node("section", "", "agent-chat-request"), sending = false;
    box.setAttribute("aria-label", clarify ? "需要澄清" : "工具权限审批");
    box.appendChild(node("strong", clarify ? "需要你的回答" : "工具执行需要授权：" + (event.tool || "工具")));
    box.appendChild(node("pre", clarify ? event.clarify_message : event.preview));
    if (!clarify) box.appendChild(node("small", "授权可能产生文件或系统变更。会话允许仅适用于当前工具，AlwaysAsk 工具仍会再次询问。"));
    var answer;
    if (clarify) {
      answer = node("textarea"); answer.setAttribute("aria-label", "澄清回答"); box.appendChild(answer);
      (event.clarify_options || []).forEach(function (value) { var b = node("button", value, "btn"); b.type = "button"; b.addEventListener("click", function () { answer.value = value; }); box.appendChild(b); });
    }
    var buttons = [];
    function action(label, decision, scope) {
      var b = node("button", label, "btn"); b.type = "button"; buttons.push(b); box.appendChild(b);
      b.addEventListener("click", async function () {
        if (sending || token !== generation || !pending.has(id)) return;
        if (clarify && !answer.value.trim()) return;
        sending = true; buttons.forEach(function (button) { button.disabled = true; });
        try {
          var result = await request(clarify ? "/chat/" + encodeURIComponent(conversation) + "/clarify" : "/approvals/" + encodeURIComponent(id), clarify ? { response: answer.value.trim() } : { decision: decision, scope: scope });
          if (token !== generation || !pending.has(id)) return;
          if (!result || result.delivered !== true) throw new Error("请求已过期或未送达");
          pending.delete(id); box.remove(); status("回答已提交，等待服务继续…");
        } catch (err) { if (token === generation) error("提交失败：" + err.message + "；可重试，或刷新历史确认运行状态。"); }
        finally { sending = false; buttons.forEach(function (button) { button.disabled = false; }); }
      });
    }
    if (clarify) action("提交回答");
    else { action("仅本次允许", "allow", "once"); action("此会话允许", "allow", "conversation"); action("拒绝", "deny", "once"); }
    pending.set(id, box); el("requests").appendChild(box);
  }
  async function recoverApprovals(token, id) {
    try { var data = await request("/approvals"); if (token !== generation || current !== id) return;
      if (!Array.isArray(data)) throw new Error("审批列表格式错误");
      data.filter(function (a) { return a.conversation_id === id; }).forEach(function (a) { ask(a, false); });
    } catch (err) { if (token === generation) error("待审批请求读取失败：" + err.message); }
  }
  async function openConversation(id, title) {
    if (busy || !enabled || deleting.has(id)) return; var token = ++generation;
    if (historyController) historyController.abort(); historyController = new AbortController();
    current = id; loading = !!id; ready = !id; follow = true; input.value = ""; error(""); clearPending();
        conversationMetadata = {}; preferencesKnown = !id; confirmedMode = "";
        if (!id) { selectedMode = "build"; confirmedMode = ""; }
        renderPreferences(); updateUsage(null);
    el("title").textContent = title || (id ? "历史会话" : "新会话");
    list.querySelectorAll(".agent-chat-session-open").forEach(function (b) { b.setAttribute("aria-pressed", String(b.dataset.id === id)); });
    empty(id ? "正在读取消息…" : "开始一个新会话，消息由 AgentCore 保存。"); status(id ? "正在读取历史…" : "准备就绪"); controls();
    if (!id) { input.focus(); return; }
    try {
      var signal = historyController.signal;
      var results = await Promise.all([
        request("/conversations/" + encodeURIComponent(id) + "/logs", undefined, signal),
        request("/conversations/" + encodeURIComponent(id), undefined, signal).then(function (conversation) {
          if (!conversation || conversation.id !== id || (conversation.metadata && (typeof conversation.metadata !== "object" || Array.isArray(conversation.metadata)))) throw new Error("会话详情格式错误");
          return conversation;
        }).catch(function (err) { return { detailError: err }; })
      ]);
      if (token !== generation) return;
      var data = results[0], conversation = results[1];
      if (conversation.detailError) { preferencesKnown = false; error("会话设置读取失败，未确认当前设置：" + conversation.detailError.message); }
      else { preferencesKnown = true; conversationMetadata = conversation.metadata || {}; }
      renderPreferences();
      if (!Array.isArray(data)) throw new Error("历史格式错误");
      empty("此会话暂无消息。"); data.forEach(function (entry) { if (entry) { addMessage(entry); if (entry.role === "assistant") updateUsage({ prompt_tokens: entry.prompt_tokens, completion_tokens: entry.completion_tokens, context_tokens: entry.context_tokens }); } });
      ready = true; scroll(true); status("");
      recoverApprovals(token, id);
    } catch (err) { if (token === generation && err.name !== "AbortError") { error("历史加载失败：" + err.message); status("请重新选择会话重试"); } }
    finally { if (token === generation) { loading = false; controls(); } }
  }
  el("new").addEventListener("click", function () { openConversation("", ""); });
  el("refresh").addEventListener("click", function () { refreshList(); loadOptions(); if (current) openConversation(current, el("title").textContent); });
  input.addEventListener("input", controls);
  input.addEventListener("keydown", function (event) { if (event.key === "Enter" && !event.shiftKey && !event.isComposing && event.keyCode !== 229) { event.preventDefault(); if (!el("send").disabled) el("form").requestSubmit(); } });
  el("form").addEventListener("submit", async function (event) {
    event.preventDefault(); var message = input.value.trim(); if (!enabled || busy || loading || !ready || !message) return;
    var effectiveMode = current ? (preferencesKnown ? conversationMetadata.mode || options.approval_mode || "" : "") : selectedMode;
        if (!confirmPermission(effectiveMode)) return;
        var token = ++generation; busy = true; liveStatus("running", "正在提交运行"); stopping = false; stopped = false; clearPending(); input.value = ""; error(""); updateUsage(null);
    addMessage({ role: "user", content: message }); var assistant = addMessage({ role: "assistant" }), reply = "", terminal = false, tools = new Map(), seen = new Set();
    streamController = new AbortController(); status("正在连接流…"); controls();
    try {
      var body = { message: message, source: "web" };
            if (current) body.conversation_id = current;
            else {
              body.metadata = { mode: selectedMode };

            }
      var runID = "", cursor = 0, attempts = 0, signal = streamController.signal;
      function rememberRun(value) {
        // Some older clients/mocks return Content-Type for every header name.
        if (!value || !/^[A-Za-z0-9_-]+$/.test(value)) return;
        if (runID && value !== runID) throw new Error("流协议错误：运行 ID 不一致");
        runID = value;
      }
      function receive(e, name) {
        if (token !== generation || !e || terminal) return;
        rememberRun(e.run_id);
                if (e.sequence !== undefined && (!Number.isSafeInteger(e.sequence) || e.sequence < 1)) throw new Error("流协议错误：无效事件序号");
                if (e.sequence !== undefined && e.sequence <= cursor || e.event_id && seen.has(e.event_id)) return;
                error("");
        if (e.conversation_id) {
                  if (!current) { conversationMetadata = Object.assign({}, body.metadata || {}); preferencesKnown = true; }
                  current = e.conversation_id; renderPreferences();
                }
        var type = e.type || name;
                if (type === "approval.required" || type === "clarify") liveStatus("waiting", type === "clarify" ? "等待澄清回答" : "等待权限审批");
                else if (type === "done" || type === "cancelled") liveStatus("idle", type === "done" ? "最新运行已完成" : "最新运行已停止");
                else if (type === "error") liveStatus("failed", "最新运行失败");
                else if (["conv_id", "resumed", "thinking", "chunk", "tool_call", "tool.started", "progress"].indexOf(type) >= 0) liveStatus("running", "运行中");
        if (e.usage) updateUsage(e.usage);
        switch (type) {
          case "conv_id": case "resumed": status("运行中"); break;
          case "thinking": status("思考中 · 第 " + (e.iteration || "?") + " 轮"); detail(assistant.article, "思考 · 第 " + (e.iteration || "?") + " 轮", e.content || "服务未公开思考正文，仅报告思考状态。"); break;
          case "chunk": reply += e.content || ""; assistant.update(reply); status("正在生成…"); break;
          case "tool_call": status("调用工具：" + (e.tools || []).join("、")); break;
          case "tool.started": tools.set(e.tool_call_id, detail(assistant.article, "执行工具：" + (e.tool || "工具"), e.preview)); status("工具执行中：" + (e.tool || "")); break;
          case "progress": var item = tools.get(e.tool_call_id); if (item) { item.text.textContent += "\n\n结果：\n" + (e.summary || ""); item.details.querySelector("summary").textContent = "工具已返回：" + (e.tool || "工具"); } else detail(assistant.article, "工具结果：" + (e.tool || "工具"), e.summary); status("工具已返回，继续运行…"); break;
          case "approval.required": status("等待权限审批"); ask(e, false); break;
          case "clarify": status("等待澄清回答"); ask(e, true); break;
          case "warning": detail(assistant.article, "运行警告", e.message || e.content); break;
          case "command.result": detail(assistant.article, "命令结果", e.content); break;
          case "done": terminal = true; if (typeof e.reply === "string") reply = e.reply; assistant.update(reply || "（无文本回复）"); status("本轮已完成"); clearPending(); break;
          case "cancelled": terminal = true; status("运行已停止"); clearPending(); break;
          case "error": terminal = true; error(e.message || e.error_code || "运行失败"); status("运行失败"); clearPending(); break;
          case "resync_required": throw new Error("事件需要重新同步，请刷新历史确认结果");
          default: detail(assistant.article, "运行事件：" + type, e.message || e.summary || e.content || "");
        }
        scroll(false);
        if (e.event_id) seen.add(e.event_id);
        if (e.sequence !== undefined) cursor = e.sequence;
      }
      function cancelled() { var err = new Error("流订阅已取消"); err.name = "AbortError"; return err; }
      function wait(delay) {
        return new Promise(function (resolve, reject) {
          if (signal.aborted) { reject(cancelled()); return; }
          var timer = window.setTimeout(function () { signal.removeEventListener("abort", abort); resolve(); }, delay);
          function abort() { window.clearTimeout(timer); signal.removeEventListener("abort", abort); reject(cancelled()); }
          signal.addEventListener("abort", abort, { once: true });
        });
      }
      function readWithTimeout(reader) {
        return new Promise(function (resolve, reject) {
          var settled = false;
          function finish(err, part) {
            if (settled) return;
            settled = true; window.clearTimeout(timer); signal.removeEventListener("abort", abort);
            if (err) reject(err); else resolve(part);
          }
          function abort() { finish(cancelled()); reader.cancel().catch(function () {}); }
          // The server sends a heartbeat every 15 seconds. Count raw bytes,
          // including SSE comments, rather than parsed application events.
          var timer = window.setTimeout(function () {
            var err = new Error("流连续 45 秒未收到数据或心跳"); err.retryable = true;
            finish(err); reader.cancel().catch(function () {});
          }, 45000);
          signal.addEventListener("abort", abort, { once: true });
          if (signal.aborted) { abort(); return; }
          function read() {
            reader.read().then(function (part) {
              if (settled) return;
              if (!part.done && !part.value.byteLength) { read(); return; }
              finish(null, part);
            }, function (err) { err.retryable = true; finish(err); });
          }
          read();
        });
      }
      while (!terminal) {
        try {
          if (signal.aborted) throw cancelled();
          var response = await request(attempts ? "/runs/" + encodeURIComponent(runID) + "/events/stream?after=" + cursor : "/chat/stream", attempts ? undefined : body, signal, true);
          rememberRun(response.headers.get("X-Run-ID"));
          if (attempts) { error(""); status("连接已恢复，继续接收原运行…"); }
          var parser = core.sse(receive), reader = response.body.getReader(), decoder = new TextDecoder();

          try {
            while (!terminal) {
              if (signal.aborted) throw cancelled();
              var part;
              part = await readWithTimeout(reader);
              if (signal.aborted) throw cancelled();
              if (part.done) break;
              parser(decoder.decode(part.value, { stream: true }), false);
            }
            if (!terminal) parser(decoder.decode(), true);
          } finally {

            try { await reader.cancel(); } catch (_) { /* The connection may already have failed. */ }
            reader.releaseLock();
          }
          if (!terminal) { var eof = new Error("流在终态事件之前断开，运行可能仍在后台继续"); eof.retryable = true; throw eof; }
        } catch (failure) {
          if (signal.aborted) throw cancelled();
          if (terminal || !failure.retryable) throw failure;
          if (!runID) throw new Error(failure.message + "；未获取运行 ID，无法安全恢复，绝不会重新提交");
          if (attempts >= 5) throw new Error("自动恢复已耗尽（5 次）：" + failure.message);
          var delay = Math.min(Math.pow(2, attempts), 10); attempts++;
          error("连接中断，正在恢复原运行；请勿重复提交。");
          status("将在 " + delay + " 秒后重连（" + attempts + "/5）…");
          await wait(delay * 1000);
        }
      }
    } catch (err) {
      if (token === generation && err.name !== "AbortError") { error(err.message + "；不要直接重发，请刷新历史确认。关闭流连接不等于停止后台运行。"); status("连接中断 / 状态未确认"); }
    } finally { if (token === generation) { busy = false; streamController = null; controls(); refreshList(); input.focus(); } }
  });
  el("stop").addEventListener("click", async function () {
    if (!busy || !current || stopping || stopped) return; var token = generation; stopping = true; controls();
    try { var result = await request("/chat/" + encodeURIComponent(current) + "/stop", {}); if (token !== generation) return;
      if (!result || result.stopped !== true) throw new Error("服务未确认停止，任务可能已结束或不在此实例中");
      stopped = true; status("已请求停止，等待 cancelled / done 事件…");
    } catch (err) { if (token === generation) error("停止失败：" + err.message); }
    finally { if (token === generation) { stopping = false; controls(); } }
  });
  document.addEventListener("visibilitychange", function () { if (document.hidden) stopStatus(); else if (!pageStopped) pollStatus(); });
    window.addEventListener("pagehide", function () { pageStopped = true; stopStatus(); ++listGeneration; ++generation; if (historyController) historyController.abort(); if (streamController) streamController.abort(); });
  empty(enabled ? "开始一个新会话，消息由 AgentCore 保存。" : "启动 AgentCore 后刷新页面即可开始会话。");
  renderPreferences(); updateUsage(null); controls(); if (enabled) { refreshList(); loadOptions(); }
})();
