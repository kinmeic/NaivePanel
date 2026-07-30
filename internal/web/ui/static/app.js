// NaivePanel UI helpers.
(function () {
  var SVG_OPEN = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide">';
  var TRASH_SVG = SVG_OPEN +
    '<path d="M3 6h18"/><path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6"/>' +
    '<path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2"/>' +
    '<line x1="10" x2="10" y1="11" y2="17"/><line x1="14" x2="14" y1="11" y2="17"/></svg>';

  // Confirm dialogs (re-bound after async fragments load).
  function bindConfirm(root) {
    root.querySelectorAll("[data-confirm]").forEach(function (el) {
      if (el.dataset.confirmBound) return;
      el.dataset.confirmBound = "1";
      el.addEventListener("submit", function (e) {
        if (!confirm(el.getAttribute("data-confirm"))) e.preventDefault();
      });
      if (el.tagName === "BUTTON") {
        el.addEventListener("click", function (e) {
          if (!confirm(el.getAttribute("data-confirm"))) e.preventDefault();
        });
      }
    });
  }
  bindConfirm(document);

  // Async list fragments: <div data-list-src="..."> shows a spinner until the
  // server-rendered fragment arrives.
  document.querySelectorAll("[data-list-src]").forEach(function (el) {
    fetch(el.getAttribute("data-list-src"), { credentials: "same-origin" })
      .then(function (r) {
        if (!r.ok) throw new Error("HTTP " + r.status);
        if (r.redirected && /\/login(?:\?|$)/.test(r.url)) {
          throw new Error("登录已过期，请刷新页面后重新登录");
        }
        return r.text();
      })
      .then(function (html) {
        el.innerHTML = html;
        bindConfirm(el);
      })
      .catch(function (e) {
        el.innerHTML = '<div class="error">加载失败: ' + e + '</div>';
      });
  });

  // Raw mode toggle.
  var rawToggle = document.querySelector('input[name="raw_mode"]');
  if (rawToggle) {
    var rawEditor = document.querySelector('textarea[name="raw"]');
    var rawDirty = rawToggle.checked;
    if (rawEditor) {
      rawEditor.addEventListener("input", function () { rawDirty = true; });
    }
    function refreshRawFromStructured() {
      if (!rawEditor || rawDirty) return;
      var form = document.getElementById("site-form");
      var base = form.getAttribute("action").replace(/\/sites\/.*$/, "");
      var data = new FormData(form);
      data.delete("raw_mode");
      fetch(base + "/sites/preview-render", {
        method: "POST",
        body: data,
        credentials: "same-origin"
      }).then(function (r) {
        return r.text().then(function (text) { return { ok: r.ok, text: text }; });
      }).then(function (res) {
        if (res.ok && !rawDirty) rawEditor.value = res.text;
      }).catch(function () {
        // The server-rendered initial preview remains available as fallback.
      });
    }
    function syncRawMode() {
      document.getElementById("raw-section").classList.toggle("hidden", !rawToggle.checked);
      document.getElementById("structured-section").classList.toggle("hidden", rawToggle.checked);
      if (rawToggle.checked) refreshRawFromStructured();
    }
    rawToggle.addEventListener("change", syncRawMode);
    syncRawMode();
  }

  // BypassCore owns the upstream while enabled; keep the custom field out of
  // sight and out of the submitted form until the user switches back.
  var bypassToggle = document.querySelector('input[name="fp_bypass"]');
  var upstreamField = document.getElementById("fp-upstream-field");
  if (bypassToggle && upstreamField) {
    var upstreamInput = upstreamField.querySelector('input[name="fp_upstream"]');
    function syncBypassUpstream() {
      upstreamField.classList.toggle("hidden", bypassToggle.checked);
      if (upstreamInput) upstreamInput.disabled = bypassToggle.checked;
    }
    bypassToggle.addEventListener("change", syncBypassUpstream);
    syncBypassUpstream();
  }

  // Show only fields relevant to the selected web type.
  var webType = document.querySelector('select[name="web_type"]');
  if (webType) {
    function syncWebFields() {
      var type = webType.value;
      document.getElementById("web-root-field").classList.toggle("hidden", type !== "static" && type !== "php");
      document.getElementById("web-php-field").classList.toggle("hidden", type !== "php");
      document.getElementById("web-proxy-field").classList.toggle("hidden", type !== "reverse_proxy");
    }
    webType.addEventListener("change", syncWebFields);
    syncWebFields();
  }

  // Extra block add/remove.
  var addBtn = document.getElementById("eb-add");
  if (addBtn) {
    addBtn.addEventListener("click", function () {
      var box = document.getElementById("extra-blocks");
      var row = document.createElement("div");
      row.className = "eb-row";
      row.innerHTML =
        '<select name="eb_type"><option value="handle">handle</option>' +
        '<option value="handle_path">handle_path</option></select>' +
        '<input name="eb_matcher" placeholder="/api/*">' +
        '<textarea name="eb_content" rows="3" class="mono" placeholder="reverse_proxy 127.0.0.1:8080"></textarea>' +
        '<button type="button" class="eb-del danger">' + TRASH_SVG + '删除</button>';
      box.appendChild(row);
      bindDel(row.querySelector(".eb-del"));
    });
    document.querySelectorAll(".eb-del").forEach(bindDel);
  }
  function bindDel(btn) {
    if (!btn) return;
    btn.addEventListener("click", function () { btn.closest(".eb-row").remove(); });
  }

  // Live preview render (unsaved form).
  var previewBtn = document.getElementById("btn-preview");
  if (previewBtn) {
    previewBtn.addEventListener("click", function () {
      var form = document.getElementById("site-form");
      var out = document.getElementById("preview");
      var base = form.getAttribute("action").replace(/\/sites\/.*$/, "");
      fetch(base + "/sites/preview-render", {
        method: "POST",
        body: new FormData(form),
        credentials: "same-origin"
      }).then(function (r) {
        return r.text().then(function (t) { return { ok: r.ok, text: t }; });
      }).then(function (res) {
        out.textContent = res.text;
        out.classList.toggle("preview-error", !res.ok);
      })
        .catch(function (e) { out.textContent = "预览失败: " + e; });
    });
  }

  // Generic structured JSON editor for BypassCore. It deliberately models
  // every JSON value instead of a fixed schema so new core fields survive a
  // round trip and advanced configurations remain editable.
  var jsonForm = document.getElementById("bypass-config-form");
  if (jsonForm) {
    var rawPanel = document.getElementById("json-raw-panel");
    var structuredPanel = document.getElementById("json-structured-panel");
    var rawInput = document.getElementById("bypass-config-raw");
    var tree = document.getElementById("json-tree");
    var structuredTab = document.getElementById("json-structured-tab");
    var rawTab = document.getElementById("json-raw-tab");
    var editorError = document.getElementById("json-editor-error");
    var editorMode = "raw";
    var maxTreeNodes = 2500;

    function showJSONError(message) {
      editorError.textContent = message || "";
      editorError.classList.toggle("hidden", !message);
    }

    function valueKind(value) {
      if (value === null) return "null";
      if (Array.isArray(value)) return "array";
      return typeof value;
    }

    function defaultValue(kind) {
      switch (kind) {
      case "object": return {};
      case "array": return [];
      case "number": return 0;
      case "boolean": return false;
      case "null": return null;
      default: return "";
      }
    }

    function countJSONNodes(value, limit) {
      var count = 1;
      if (value && typeof value === "object") {
        var values = Array.isArray(value) ? value : Object.keys(value).map(function (key) { return value[key]; });
        for (var i = 0; i < values.length; i++) {
          count += countJSONNodes(values[i], limit - count);
          if (count > limit) return count;
        }
      }
      return count;
    }

    function makeTypeSelect(kind, onChange) {
      var select = document.createElement("select");
      select.className = "json-type";
      ["string", "number", "boolean", "object", "array", "null"].forEach(function (name) {
        var option = document.createElement("option");
        option.value = name;
        option.textContent = name;
        option.selected = name === kind;
        select.appendChild(option);
      });
      select.addEventListener("click", function (event) { event.stopPropagation(); });
      select.addEventListener("change", function () { onChange(select.value); });
      return select;
    }

    function makeDeleteButton(onDelete) {
      var button = document.createElement("button");
      button.type = "button";
      button.className = "json-delete danger icon-btn";
      button.innerHTML = TRASH_SVG;
      button.title = "删除此项";
      button.setAttribute("aria-label", "删除此项");
      button.addEventListener("click", onDelete);
      return button;
    }

    function makeValueHost(value, depth) {
      var host = document.createElement("div");
      host.className = "json-value-host";
      function render(next) {
        host.replaceChildren(makeJSONNode(next, depth, render));
      }
      host._jsonRead = function () {
        return host.firstElementChild._jsonRead();
      };
      render(value);
      return host;
    }

    function makeJSONNode(value, depth, rerender) {
      var kind = valueKind(value);
      var node = document.createElement("div");
      node.className = "json-node json-" + kind;

      if (kind === "object" || kind === "array") {
        var details = document.createElement("details");
        details.className = "json-composite";
        details.open = depth < 2;
        var summary = document.createElement("summary");
        var count = kind === "array" ? value.length : Object.keys(value).length;
        summary.appendChild(document.createTextNode((kind === "array" ? "数组" : "对象") + " · " + count + " 项 "));
        summary.appendChild(makeTypeSelect(kind, function (nextKind) {
          if (nextKind !== kind) rerender(defaultValue(nextKind));
        }));
        details.appendChild(summary);

        var children = document.createElement("div");
        children.className = "json-children";
        details.appendChild(children);

        function refreshSummary() {
          var total = children.children.length;
          summary.firstChild.nodeValue = (kind === "array" ? "数组" : "对象") + " · " + total + " 项 ";
        }

        function addObjectEntry(key, childValue) {
          var row = document.createElement("div");
          row.className = "json-entry";
          var keyInput = document.createElement("input");
          keyInput.type = "text";
          keyInput.className = "json-key mono";
          keyInput.value = key;
          keyInput.setAttribute("aria-label", "JSON 字段名");
          var host = makeValueHost(childValue, depth + 1);
          row.appendChild(keyInput);
          row.appendChild(host);
          row.appendChild(makeDeleteButton(function () {
            row.remove();
            refreshSummary();
          }));
          row._jsonKey = keyInput;
          row._jsonValue = host;
          children.appendChild(row);
        }

        function addArrayEntry(childValue) {
          var row = document.createElement("div");
          row.className = "json-entry json-array-entry";
          var index = document.createElement("span");
          index.className = "json-index mono";
          var host = makeValueHost(childValue, depth + 1);
          row.appendChild(index);
          row.appendChild(host);
          row.appendChild(makeDeleteButton(function () {
            row.remove();
            Array.prototype.forEach.call(children.children, function (entry, i) {
              entry.querySelector(".json-index").textContent = "#" + (i + 1);
            });
            refreshSummary();
          }));
          row._jsonValue = host;
          children.appendChild(row);
          index.textContent = "#" + children.children.length;
        }

        if (kind === "object") {
          Object.keys(value).forEach(function (key) { addObjectEntry(key, value[key]); });
        } else {
          value.forEach(addArrayEntry);
        }

        var add = document.createElement("button");
        add.type = "button";
        add.className = "btn json-add";
        add.textContent = kind === "object" ? "添加字段" : "添加数组项";
        add.addEventListener("click", function () {
          if (kind === "object") {
            var base = "newField";
            var key = base;
            var used = {};
            Array.prototype.forEach.call(children.children, function (entry) {
              used[entry._jsonKey.value] = true;
            });
            for (var i = 2; used[key]; i++) key = base + i;
            addObjectEntry(key, "");
          } else {
            addArrayEntry("");
          }
          refreshSummary();
          details.open = true;
        });
        details.appendChild(add);
        node.appendChild(details);

        node._jsonRead = function () {
          if (kind === "array") {
            return Array.prototype.map.call(children.children, function (entry) {
              return entry._jsonValue._jsonRead();
            });
          }
          var result = Object.create(null);
          Array.prototype.forEach.call(children.children, function (entry) {
            var key = entry._jsonKey.value.trim();
            if (!key) throw new Error("对象字段名不能为空");
            if (Object.prototype.hasOwnProperty.call(result, key)) {
              throw new Error("对象中存在重复字段：" + key);
            }
            result[key] = entry._jsonValue._jsonRead();
          });
          return result;
        };
        return node;
      }

      var primitive = document.createElement("div");
      primitive.className = "json-primitive";
      var input;
      if (kind === "boolean") {
        input = document.createElement("select");
        ["true", "false"].forEach(function (text) {
          var option = document.createElement("option");
          option.value = text;
          option.textContent = text;
          option.selected = value === (text === "true");
          input.appendChild(option);
        });
      } else if (kind === "null") {
        input = document.createElement("span");
        input.className = "json-null mono";
        input.textContent = "null";
      } else {
        input = document.createElement("input");
        input.type = kind === "number" ? "number" : "text";
        if (kind === "number") input.step = "any";
        input.value = String(value);
        input.className = kind === "string" ? "mono" : "";
      }
      primitive.appendChild(input);
      primitive.appendChild(makeTypeSelect(kind, function (nextKind) {
        if (nextKind !== kind) rerender(defaultValue(nextKind));
      }));
      node.appendChild(primitive);
      node._jsonRead = function () {
        if (kind === "null") return null;
        if (kind === "boolean") return input.value === "true";
        if (kind === "number") {
          var number = Number(input.value);
          if (!Number.isFinite(number)) throw new Error("数字字段包含无效值");
          return number;
        }
        return input.value;
      };
      return node;
    }

    function parseRawConfig(forStructuredEditor) {
      var text = rawInput.value.trim();
      if (forStructuredEditor && hasUnsafeJSONInteger(text)) {
        throw new Error("配置包含超出 JavaScript 安全范围的整数，请使用高级 JSON 模式编辑");
      }
      var value = text ? JSON.parse(text) : {};
      if (!value || Array.isArray(value) || typeof value !== "object") {
        throw new Error("BypassCore 配置的顶层必须是 JSON 对象");
      }
      if (countJSONNodes(value, maxTreeNodes) > maxTreeNodes) {
        throw new Error("配置超过 2500 个节点，请使用高级 JSON 模式编辑");
      }
      return value;
    }

    // JSON.parse rounds integers beyond 2^53. Detect them outside quoted
    // strings and keep those uncommon configs in lossless raw mode.
    function hasUnsafeJSONInteger(text) {
      var inString = false;
      var escaped = false;
      for (var i = 0; i < text.length; i++) {
        var ch = text[i];
        if (inString) {
          if (escaped) escaped = false;
          else if (ch === "\\") escaped = true;
          else if (ch === '"') inString = false;
          continue;
        }
        if (ch === '"') {
          inString = true;
          continue;
        }
        if (ch === "-" || (ch >= "0" && ch <= "9")) {
          var match = text.slice(i).match(/^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/);
          if (!match) continue;
          var token = match[0];
          i += token.length - 1;
          if (token.indexOf(".") === -1 && !/[eE]/.test(token) &&
              !Number.isSafeInteger(Number(token))) {
            return true;
          }
        }
      }
      return false;
    }

    function buildTreeFromRaw() {
      var value = parseRawConfig(true);
      tree.replaceChildren(makeValueHost(value, 0));
      showJSONError("");
    }

    function syncTreeToRaw() {
      var host = tree.firstElementChild;
      if (!host) throw new Error("结构化配置尚未载入");
      var value = host._jsonRead();
      if (!value || Array.isArray(value) || typeof value !== "object") {
        throw new Error("BypassCore 配置的顶层必须是 JSON 对象");
      }
      rawInput.value = JSON.stringify(value, null, 2) + "\n";
      showJSONError("");
    }

    function activateStructured() {
      try {
        buildTreeFromRaw();
      } catch (err) {
        showJSONError("无法进入结构化模式：" + err.message);
        return;
      }
      editorMode = "structured";
      structuredPanel.classList.remove("hidden");
      rawPanel.classList.add("hidden");
      structuredTab.classList.add("active");
      rawTab.classList.remove("active");
    }

    function activateRaw() {
      if (editorMode === "structured") {
        try {
          syncTreeToRaw();
        } catch (err) {
          showJSONError("无法生成 JSON：" + err.message);
          return;
        }
      }
      editorMode = "raw";
      structuredPanel.classList.add("hidden");
      rawPanel.classList.remove("hidden");
      structuredTab.classList.remove("active");
      rawTab.classList.add("active");
    }

    structuredTab.addEventListener("click", activateStructured);
    rawTab.addEventListener("click", activateRaw);
    jsonForm.addEventListener("submit", function (event) {
      try {
        if (editorMode === "structured") syncTreeToRaw();
        else parseRawConfig(false);
      } catch (err) {
        event.preventDefault();
        showJSONError("配置无法提交：" + err.message);
      }
    });
    activateStructured();
  }

  // Prevent duplicate service/config operations while a form is submitting.
  document.addEventListener("submit", function (e) {
    if (e.defaultPrevented) return;
    var form = e.target;
    setTimeout(function () {
      form.querySelectorAll('button[type="submit"], button:not([type])').forEach(function (btn) {
        btn.disabled = true;
      });
    }, 0);
  });

  document.querySelectorAll("[data-copy-target]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var target = document.getElementById(btn.getAttribute("data-copy-target"));
      if (!target || !navigator.clipboard) return;
      navigator.clipboard.writeText(target.textContent).then(function () {
        var old = btn.textContent;
        btn.textContent = "已复制";
        setTimeout(function () { btn.textContent = old; }, 1200);
      });
    });
  });
})();
