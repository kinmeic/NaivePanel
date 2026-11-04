(function () {
  "use strict";
  var root = document.getElementById("agent-chat");
  if (!root) return;
  function el(name) { return document.getElementById("agent-chat-" + name); }
  var enabled = root.dataset.enabled === "true", current = "", busy = false, loading = false;
  var generation = 0, listGeneration = 0, historyReady = true, stopping = false, stopRequested = false;
  var input = el("input"), messages = el("messages"), list = el("list"), follow = true;
  function textNode(tag, text, className) {
    var node = document.createElement(tag); node.textContent = text;
    if (className) node.className = className;
    return node;
  }
  function error(message) { el("error").textContent = message; el("error").classList.toggle("hidden", !message); }
  function controls() {
    input.disabled = !enabled || busy || loading || !historyReady;
    el("send").disabled = input.disabled || !input.value.trim();
    el("new").disabled = !enabled || busy;
    el("refresh").disabled = !enabled || busy;
    el("stop").disabled = !busy || !current || stopping || stopRequested;
    el("stop").title = busy && !current ? "首轮同步请求尚未返回会话 ID，暂不能停止" : "请求停止当前会话；等待服务端确认结束";
    list.querySelectorAll("button").forEach(function (button) { button.disabled = busy; });
    messages.setAttribute("aria-busy", String(busy || loading));
  }
  async function request(path, body) {
    var url = new URL(root.dataset.api + path, window.location.origin);

    var options = { method: body === undefined ? "GET" : "POST", credentials: "same-origin", cache: "no-store", headers: { Accept: "application/json", "X-CSRF-Token": root.dataset.csrf } };
    if (body !== undefined) { options.headers["Content-Type"] = "application/json"; options.body = JSON.stringify(body); }
    var response = await fetch(url.href, options), data;
    try { data = await response.json(); } catch (_) { throw new Error("服务返回非 JSON 响应（HTTP " + response.status + "），请检查登录状态和服务。"); }
    if (!response.ok) {
      var detail = data && (data.error || data.message);
      throw new Error("请求失败（HTTP " + response.status + "）" + (typeof detail === "string" ? "：" + detail : ""));
    }
    return data;
  }
  function scroll(force) {
    if (force || follow) { messages.scrollTop = messages.scrollHeight; follow = true; }
    el("bottom").classList.toggle("hidden", follow);
  }
  messages.addEventListener("scroll", function () {
    follow = messages.scrollHeight - messages.scrollTop - messages.clientHeight < 64;
    el("bottom").classList.toggle("hidden", follow);
  });
  el("bottom").addEventListener("click", function () { scroll(true); });
  function empty(text) { messages.replaceChildren(textNode("p", text, "agent-chat-empty muted")); }
  function addMessage(entry) {
    var placeholder = messages.querySelector(".agent-chat-empty"); if (placeholder) placeholder.remove();
    var roles = { user: "你", assistant: "AgentCore", system: "系统", tool: "工具" };
    var article = textNode("article", "", "agent-chat-message" + (entry.role === "user" ? " agent-chat-user" : ""));
    var head = textNode("div", "", "agent-chat-message-head");
    head.appendChild(textNode("strong", roles[entry.role] || "消息"));
    if (entry.created_at) {
      var date = new Date(entry.created_at);
      if (!isNaN(date.getTime())) head.appendChild(textNode("time", date.toLocaleString(), "muted"));
    }
    var content = typeof entry.content === "string" ? entry.content : "";
    if (Array.isArray(entry.tool_calls) && entry.tool_calls.length) {
      content += (content ? "\n\n" : "") + "工具调用：\n" + entry.tool_calls.map(function (call) {
        return call.function ? String(call.function.name || "工具") + "\n" + String(call.function.arguments || "") : "工具";
      }).join("\n\n");
    }
    var copy = textNode("button", "复制", "btn"); copy.type = "button";
    copy.addEventListener("click", async function () {
      try { await navigator.clipboard.writeText(content); copy.textContent = "已复制"; }
      catch (_) { copy.textContent = "复制失败"; error("无法访问剪贴板，请手动选择消息复制。"); }
      window.setTimeout(function () { copy.textContent = "复制"; }, 1600);
    });
    head.appendChild(copy); article.appendChild(head);
    article.appendChild(textNode("div", content || "（无文本内容）", "agent-chat-message-body"));
    if (entry.status) article.appendChild(textNode("small", "状态：" + entry.status, "muted"));
    messages.appendChild(article); scroll(false);
  }
  async function refreshList() {
    if (!enabled) return;
    var token = ++listGeneration;
    el("list-status").textContent = "正在加载历史…";
    try {
      var data = await request("/conversations");
      if (data === null) data = [];
      if (token !== listGeneration) return;
      if (!Array.isArray(data)) throw new Error("历史列表格式不正确");
      list.replaceChildren();
      data.forEach(function (conversation) {
        if (!conversation || typeof conversation.id !== "string" || !conversation.id) return;
        var button = textNode("button", conversation.title || "未命名会话", "agent-chat-session");
        button.type = "button"; button.dataset.id = conversation.id;
        button.title = (conversation.title || "未命名会话") + " · " + conversation.id;
        button.setAttribute("aria-pressed", String(current === conversation.id));
        button.addEventListener("click", function () { openConversation(conversation.id, conversation.title); });
        list.appendChild(button);
      });
      el("list-status").textContent = list.children.length ? "最近会话（后端返回列表）" : "暂无历史会话";
      controls();
    } catch (err) {
      if (token === listGeneration) el("list-status").textContent = "历史加载失败：" + err.message + "；仍可新建会话，或点击刷新重试。";
    }
  }
  async function openConversation(id, title) {
    if (busy || !enabled) return;
    var token = ++generation;
    current = id; loading = !!id; historyReady = !id; follow = true; input.value = ""; error("");
    el("title").textContent = title || (id ? "历史会话" : "新会话");
    list.querySelectorAll("button").forEach(function (button) { button.setAttribute("aria-pressed", String(button.dataset.id === id)); });
    empty(id ? "正在加载消息…" : "开始一个新会话，消息将由 AgentCore 保存。");
    el("status").textContent = id ? "正在读取历史消息…" : "准备就绪"; controls();
    if (!id) { input.focus(); return; }
    try {
      var data = await request("/conversations/" + encodeURIComponent(id) + "/logs");
      if (token !== generation) return;
      if (!Array.isArray(data)) throw new Error("历史消息格式不正确");
      empty("此会话暂无消息。"); data.forEach(function (entry) { if (entry) addMessage(entry); });
      historyReady = true; scroll(true);
      el("status").textContent = "已加载最近 " + data.length + " 条日志（可能不包含更早消息）";
    } catch (err) {
      if (token !== generation) return;
      empty("历史消息未能加载。"); error(err.message + "；请重新选择此会话重试，或新建会话。"); el("status").textContent = "历史加载失败，暂不能向此会话发送";
    } finally { if (token === generation) { loading = false; controls(); } }
  }
  el("new").addEventListener("click", function () { openConversation("", ""); });
  el("refresh").addEventListener("click", refreshList);
  input.addEventListener("input", controls);
  input.addEventListener("keydown", function (event) {
    if (event.key === "Enter" && !event.shiftKey && !event.isComposing && event.keyCode !== 229) {
      event.preventDefault(); if (!el("send").disabled) el("form").requestSubmit();
    }
  });
  el("form").addEventListener("submit", async function (event) {
    event.preventDefault(); var message = input.value.trim();
    if (!enabled || busy || loading || !historyReady || !message) return;
    busy = true; stopping = false; stopRequested = false;
    input.value = ""; error("");
    addMessage({ role: "user", content: message });
    el("status").textContent = "AgentCore 正在处理…"; controls();
    try {
      var body = { message: message, source: "web" };
      if (current) body.conversation_id = current;
      var result = await request("/chat", body);
      if (result.conversation_id) current = result.conversation_id;
      addMessage({ role: "assistant", content: result.reply || "（服务未返回文本回复）", created_at: result.created_at, status: result.status });
      el("status").textContent = stopRequested ? "本轮已结束（已请求停止）" : "本轮已完成";
      el("title").textContent = "当前会话";
      refreshList();
    } catch (err) {
      error(err.message + "；请求可能已被服务端接收，请刷新历史确认后再重试。");
      el("status").textContent = "请求失败";
      if (!input.value) input.value = message;
    } finally { busy = false; controls(); input.focus(); }
  });
  el("stop").addEventListener("click", async function () {
    if (!busy || !current || stopping || stopRequested) return;
    stopping = true; controls();
    try {
      await request("/chat/" + encodeURIComponent(current) + "/stop", {});
      stopRequested = true; el("status").textContent = "已请求停止，等待本轮结束…";
    } catch (err) { error("停止失败：" + err.message); }
    finally { stopping = false; controls(); }
  });
  empty(enabled ? "开始一个新会话，消息将由 AgentCore 保存。" : "启动 AgentCore 后刷新页面即可开始会话。");
  controls(); refreshList();
})();