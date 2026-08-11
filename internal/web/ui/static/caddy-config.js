// Conservative structured editor for the main Caddyfile. It only recognizes
// top-level blocks and preserves every unrecognized byte in Raw.
(function () {
  "use strict";

  var form = document.getElementById("caddy-config-form");
  var raw = document.getElementById("caddy-config-raw");
  if (!form || !raw) return;

  var baseEditor = document.getElementById("caddy-base-block");
  var siteRows = document.getElementById("caddy-sites-rows");
  var editorTab = document.getElementById("caddy-editor-tab");
  var editorError = document.getElementById("caddy-editor-error");
  var editorWarning = document.getElementById("caddy-editor-warning");
  var siteDialog = document.getElementById("caddy-site-dialog");
  var siteDialogTitle = document.getElementById("caddy-site-dialog-title");
  var siteDialogError = document.getElementById("caddy-site-dialog-error");
  var siteDomain = document.getElementById("caddy-site-domain");
  var siteBlock = document.getElementById("caddy-site-block");
  var activeTab = "base";
  var rawDirty = false;
  var parts = [];
  var editingPart = null;

  var SVG_OPEN = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide">';
  var EDIT_SVG = SVG_OPEN +
    '<path d="M21.174 6.812a1 1 0 0 0-3.986-3.987L3.842 16.174a2 2 0 0 0-.5.83l-1.321 4.352a.5.5 0 0 0 .623.622l4.353-1.32a2 2 0 0 0 .83-.497z"/><path d="m15 5 4 4"/></svg>';
  var TRASH_SVG = SVG_OPEN +
    '<path d="M3 6h18"/><path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6"/>' +
    '<path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2"/>' +
    '<line x1="10" x2="10" y1="11" y2="17"/><line x1="14" x2="14" y1="11" y2="17"/></svg>';

  function showMessage(box, message) {
    if (!box) return;
    box.textContent = message || "";
    box.classList.toggle("hidden", !message);
  }

  function isWhitespace(value) {
    return /\s/.test(value);
  }

  function isBraceToken(text, index) {
    var before = index === 0 || isWhitespace(text[index - 1]);
    var after = index === text.length - 1 || isWhitespace(text[index + 1]);
    return before && after;
  }

  function isHeredocStart(text, index) {
    if (text[index] !== "<" || text[index + 1] !== "<") return false;
    return index === 0 || isWhitespace(text[index - 1]);
  }

  function classifyBlock(header) {
    if (header === "") return "base";
    if (/^\([^\r\n]+\)$/.test(header)) return "other";
    return "site";
  }

  function parseCaddyfile(text) {
    if (text.length > 2 * 1024 * 1024) throw new Error("Caddyfile 超过 2 MiB，请使用 Raw 编辑");
    var parsed = [];
    var cursor = 0;
    var depth = 0;
    var quote = "";
    var escaped = false;
    var comment = false;
    var blockStart = -1;
    var openIndex = -1;
    var header = "";

    for (var i = 0; i < text.length; i++) {
      var ch = text[i];
      if (comment) {
        if (ch === "\n") comment = false;
        continue;
      }
      if (quote) {
        if (quote === '"' && escaped) {
          escaped = false;
        } else if (quote === '"' && ch === "\\") {
          escaped = true;
        } else if (ch === quote) {
          quote = "";
        }
        continue;
      }
      if (ch === "#") {
        comment = true;
        continue;
      }
      if (ch === '"' || ch === "`") {
        quote = ch;
        continue;
      }
      if (isHeredocStart(text, i)) {
        throw new Error("检测到 heredoc（<<），请在 Raw Tab 中编辑此配置");
      }
      if ((ch !== "{" && ch !== "}") || !isBraceToken(text, i)) continue;

      if (ch === "{") {
        if (depth === 0) {
          var lineStart = text.lastIndexOf("\n", i - 1) + 1;
          blockStart = lineStart;
          openIndex = i;
          header = text.slice(lineStart, i).trim();
          if (cursor < blockStart) parsed.push({ type: "text", raw: text.slice(cursor, blockStart) });
        }
        depth++;
        continue;
      }

      if (depth === 0) throw new Error("发现没有对应开始括号的顶层 }");
      depth--;
      if (depth === 0) {
        var rawBlock = text.slice(blockStart, i + 1);
        parsed.push({
          type: classifyBlock(header),
          raw: rawBlock,
          header: header,
          block: text.slice(openIndex, i + 1)
        });
        cursor = i + 1;
        blockStart = -1;
        openIndex = -1;
        header = "";
      }
    }

    if (quote) throw new Error("发现未结束的引号");
    if (depth !== 0) throw new Error("发现未闭合的顶层配置块");
    if (cursor < text.length) parsed.push({ type: "text", raw: text.slice(cursor) });
    if (parsed.filter(function (part) { return part.type === "base"; }).length > 1) {
      throw new Error("发现多个顶层全局配置块");
    }
    return parsed;
  }

  function serialize() {
    return parts.map(function (part) { return part.raw; }).join("");
  }

  function syncRaw() {
    raw.value = serialize();
    rawDirty = false;
  }

  function meaningfulTopLevelText(value) {
    return value.split(/\r?\n/).some(function (line) {
      return line.replace(/#.*/, "").trim() !== "";
    });
  }

  function renderWarning() {
    var snippets = parts.filter(function (part) { return part.type === "other"; }).length;
    var directives = parts.filter(function (part) {
      return part.type === "text" && meaningfulTopLevelText(part.raw);
    }).length;
    var messages = [];
    if (snippets) messages.push(snippets + " 个 snippet/其他顶层块");
    if (directives) messages.push("import 或其他顶层文本");
    showMessage(editorWarning, messages.length ? "以下内容会原样保留，但不在结构化区域展示：" + messages.join("、") + "。" : "");
  }

  function renderBase() {
    var base = parts.find(function (part) { return part.type === "base"; });
    baseEditor.value = base ? base.raw.trim() : "";
  }

  function siteLabel(part, index) {
    return part.header || "站点 #" + (index + 1);
  }

  function makeActionButton(label, className, svg, handler) {
    var button = document.createElement("button");
    button.type = "button";
    button.className = className;
    button.title = label;
    button.setAttribute("aria-label", label);
    button.innerHTML = svg;
    button.addEventListener("click", handler);
    return button;
  }

  function renderSites() {
    siteRows.textContent = "";
    var sites = parts.map(function (part, partIndex) {
      return { part: part, partIndex: partIndex };
    }).filter(function (entry) { return entry.part.type === "site"; });
    if (sites.length === 0) {
      var emptyRow = document.createElement("tr");
      var emptyCell = document.createElement("td");
      emptyCell.colSpan = 2;
      emptyCell.className = "config-empty-cell";
      emptyCell.textContent = "当前主 Caddyfile 中没有直接定义的站点";
      emptyRow.appendChild(emptyCell);
      siteRows.appendChild(emptyRow);
      return;
    }

    sites.forEach(function (entry, index) {
      var row = document.createElement("tr");
      var domainCell = document.createElement("td");
      domainCell.className = "caddy-site-domain";
      domainCell.textContent = entry.part.header;
      row.appendChild(domainCell);

      var actionCell = document.createElement("td");
      actionCell.className = "config-row-actions";
      var actions = document.createElement("div");
      actions.className = "site-action-buttons";
      actions.appendChild(makeActionButton("编辑 " + siteLabel(entry.part, index), "btn icon-btn", EDIT_SVG, function () {
        openSiteDialog(entry.partIndex);
      }));
      actions.appendChild(makeActionButton("删除 " + siteLabel(entry.part, index), "danger icon-btn", TRASH_SVG, function () {
        if (!confirm("确定删除站点“" + siteLabel(entry.part, index) + "”？")) return;
        parts.splice(entry.partIndex, 1);
        syncRaw();
        renderSites();
      }));
      actionCell.appendChild(actions);
      row.appendChild(actionCell);
      siteRows.appendChild(row);
    });
  }

  function renderStructured() {
    renderBase();
    renderSites();
    renderWarning();
  }

  function adoptRaw() {
    try {
      parts = parseCaddyfile(raw.value);
      rawDirty = false;
      showMessage(editorError, "");
      renderStructured();
      return true;
    } catch (error) {
      showMessage(editorError, "无法进入结构化编辑：" + error.message);
      showMessage(editorWarning, "");
      return false;
    }
  }

  function activateTab(name) {
    if (name !== "raw" && activeTab === "raw" && rawDirty && !adoptRaw()) return false;
    if (name === "raw" && activeTab !== "raw") syncRaw();
    document.querySelectorAll("[data-caddy-tab]").forEach(function (button) {
      var selected = button.getAttribute("data-caddy-tab") === name;
      button.classList.toggle("active", selected);
      button.setAttribute("aria-selected", selected ? "true" : "false");
      button.tabIndex = selected ? 0 : -1;
    });
    document.querySelectorAll("[data-caddy-panel]").forEach(function (panel) {
      panel.classList.toggle("hidden", panel.getAttribute("data-caddy-panel") !== name);
    });
    activeTab = name;
    editorTab.value = name;
    return true;
  }

  function updateBase(value) {
    var index = parts.findIndex(function (part) { return part.type === "base"; });
    if (value.trim() === "") {
      if (index >= 0) parts.splice(index, 1);
    } else if (index >= 0) {
      parts[index].raw = value;
      parts[index].block = value;
    } else {
      parts.unshift({ type: "text", raw: "\n\n" });
      parts.unshift({ type: "base", raw: value, header: "", block: value });
    }
    syncRaw();
  }

  function openSiteDialog(partIndex) {
    editingPart = partIndex;
    var part = partIndex === null ? null : parts[partIndex];
    siteDialogTitle.textContent = part ? "编辑站点" : "添加站点";
    siteDomain.value = part ? part.header : "";
    siteBlock.value = part ? part.block : "{\n\t\n}";
    showMessage(siteDialogError, "");
    if (typeof siteDialog.showModal === "function") siteDialog.showModal();
    else siteDialog.setAttribute("open", "open");
    siteDomain.focus();
  }

  function appendSite(part) {
    var current = serialize();
    if (current !== "") {
      var separator = /\n\s*\n$/.test(current) ? "" : /\n$/.test(current) ? "\n" : "\n\n";
      if (separator) parts.push({ type: "text", raw: separator });
    }
    parts.push(part);
    parts.push({ type: "text", raw: "\n" });
  }

  function saveSiteDialog() {
    var domain = siteDomain.value.trim();
    var block = siteBlock.value.trim();
    if (domain === "") {
      showMessage(siteDialogError, "domain 不能为空");
      return;
    }
    if (/[{}#\r\n]/.test(domain)) {
      showMessage(siteDialogError, "domain 不能包含换行、括号或注释符号");
      return;
    }
    try {
      var candidate = parseCaddyfile(domain + " " + block);
      var candidateSites = candidate.filter(function (part) { return part.type === "site"; });
      if (candidateSites.length !== 1 || candidate.some(function (part) {
        return part.type === "base" || part.type === "other" ||
          (part.type === "text" && meaningfulTopLevelText(part.raw));
      })) {
        throw new Error("站点配置必须是一个完整的顶层 { } 块");
      }
      var parsedSite = candidateSites[0];
      parsedSite.raw = domain + " " + block;
      parsedSite.header = domain;
      parsedSite.block = block;
      if (editingPart === null) appendSite(parsedSite);
      else parts[editingPart] = parsedSite;
      syncRaw();
      renderSites();
      siteDialog.close();
      editingPart = null;
    } catch (error) {
      showMessage(siteDialogError, "无法保存站点：" + error.message);
    }
  }

  document.querySelectorAll("[data-caddy-tab]").forEach(function (button) {
    button.addEventListener("click", function () {
      activateTab(button.getAttribute("data-caddy-tab"));
    });
    button.addEventListener("keydown", function (event) {
      if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
      var tabs = Array.prototype.slice.call(document.querySelectorAll("[data-caddy-tab]"));
      var direction = event.key === "ArrowRight" ? 1 : -1;
      var next = (tabs.indexOf(button) + direction + tabs.length) % tabs.length;
      if (activateTab(tabs[next].getAttribute("data-caddy-tab"))) tabs[next].focus();
      event.preventDefault();
    });
  });

  baseEditor.addEventListener("input", function () {
    updateBase(baseEditor.value);
  });
  raw.addEventListener("input", function () {
    rawDirty = true;
    showMessage(editorError, "");
    showMessage(editorWarning, "");
  });
  document.getElementById("caddy-site-add").addEventListener("click", function () {
    openSiteDialog(null);
  });
  document.getElementById("caddy-site-dialog-save").addEventListener("click", saveSiteDialog);
  document.querySelectorAll(".caddy-site-dialog-close").forEach(function (button) {
    button.addEventListener("click", function () {
      editingPart = null;
      siteDialog.close();
    });
  });
  form.addEventListener("submit", function () {
    editorTab.value = activeTab;
    if (activeTab !== "raw") syncRaw();
  });

  try {
    parts = parseCaddyfile(raw.value);
    renderStructured();
    var initialTab = form.getAttribute("data-initial-tab");
    if (initialTab !== "base" && initialTab !== "sites" && initialTab !== "raw") initialTab = "base";
    activateTab(initialTab);
  } catch (error) {
    showMessage(editorError, "结构化编辑暂不可用：" + error.message);
    showMessage(editorWarning, "");
    activeTab = "raw";
    activateTab("raw");
  }
})();
