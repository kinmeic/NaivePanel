// Structured BypassCore configuration editor. The form always submits the
// canonical Raw JSON field, while the visual tabs mutate the same object.
(function () {
  "use strict";

  var form = document.getElementById("bypass-config-form");
  var raw = document.getElementById("bypass-config-raw");
  if (!form || !raw) return;

  var editorError = document.getElementById("bypass-editor-error");
  var formatError = document.getElementById("json-format-error");
  var dialog = document.getElementById("bypass-item-dialog");
  var dialogTitle = document.getElementById("bypass-dialog-title");
  var dialogFields = document.getElementById("bypass-dialog-fields");
  var dialogExtra = document.getElementById("bypass-dialog-extra");
  var dialogError = document.getElementById("bypass-dialog-error");
  var dialogSave = document.getElementById("bypass-dialog-save");
  var activeTab = "control";
  var state = {};
  var rawDirty = false;
  var editing = null;
  // Canonical values follow BypassCore's validators and JSON model:
  // app/inbound/validate.go, app/outbound/config.go and infra/conf/router.go.
  // Existing aliases or future values are appended by fieldOptions so opening
  // an older/newer config in the panel never silently discards its value.
  var optionSets = {
    inboundTypes: [
      { value: "redirect", label: "redirect（透明代理 TCP）" },
      { value: "tproxy", label: "tproxy（透明代理 TCP/UDP）" },
      { value: "socks", label: "socks（SOCKS5）" },
      { value: "dns", label: "dns" },
      { value: "dot", label: "dot（DNS over TLS）" },
      { value: "doh", label: "doh（DNS over HTTPS）" }
    ],
    inboundNetworks: [
      { value: "tcp", label: "tcp" },
      { value: "udp", label: "udp" },
      { value: "tcp,udp", label: "tcp,udp" }
    ],
    outboundModes: [
      { value: "freedom", label: "freedom（直连）" },
      { value: "blackhole", label: "blackhole（阻断）" },
      { value: "proxy", label: "proxy（上游代理）" },
      { value: "wireguard", label: "wireguard" }
    ],
    upstreamProtocols: [
      { value: "socks", label: "socks（SOCKS5）" },
      { value: "https", label: "https（HTTP CONNECT over TLS）" }
    ],
    routingNetworks: [
      { value: "tcp", label: "tcp" },
      { value: "udp", label: "udp" },
      { value: "unix", label: "unix" }
    ]
  };

  var SVG_OPEN = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide">';
  var EDIT_SVG = SVG_OPEN +
    '<path d="M21.174 6.812a1 1 0 0 0-3.986-3.987L3.842 16.174a2 2 0 0 0-.5.83l-1.321 4.352a.5.5 0 0 0 .623.622l4.353-1.32a2 2 0 0 0 .83-.497z"/><path d="m15 5 4 4"/></svg>';
  var TRASH_SVG = SVG_OPEN +
    '<path d="M3 6h18"/><path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6"/>' +
    '<path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2"/>' +
    '<line x1="10" x2="10" y1="11" y2="17"/><line x1="14" x2="14" y1="11" y2="17"/></svg>';

  var definitions = {
    inbounds: {
      title: "入站",
      rowID: "bypass-inbounds-rows",
      columns: 6,
      fields: [
        { path: "tag", label: "tag", placeholder: "caddy-in" },
        { path: "type", label: "type", type: "select", options: "inboundTypes" },
        { path: "listen", label: "listen", placeholder: "127.0.0.1" },
        { path: "network", label: "network", type: "select", options: "inboundNetworks" },
        { path: "port", label: "port", type: "number", placeholder: "1080" }
      ]
    },
    outbounds: {
      title: "出站",
      rowID: "bypass-outbounds-rows",
      columns: 5,
      fields: [
        { path: "tag", label: "tag", placeholder: "caddy-upstream" },
        { path: "mode", label: "mode", type: "select", options: "outboundModes" },
        { path: "upstream.protocol", label: "upstream.protocol", type: "select", options: "upstreamProtocols" },
        { path: "upstream.server", label: "upstream.server", placeholder: "exit.example.com:443" },
        { path: "upstream.settings.username", label: "upstream.settings.username", placeholder: "用户名" },
        { path: "upstream.settings.password", label: "upstream.settings.password", type: "password", placeholder: "密码" }
      ]
    },
    "routing.rules": {
      title: "路由规则",
      rowID: "bypass-routing-rows",
      columns: 4,
      fields: [
        { path: "ruleTag", label: "ruleTag", placeholder: "block-cn-domain" },
        { path: "inboundTag", label: "inboundTag", type: "multi-select", optionsFrom: "inbounds" },
        { path: "domain", label: "domain", type: "list", placeholder: "例如 geosite:cn，每行一个" },
        { path: "ip", label: "ip", type: "list", placeholder: "例如 geoip:cn，每行一个" },
        { path: "port", label: "port", placeholder: "例如 80,443 或 1000-2000" },
        { path: "network", label: "network", type: "multi-select", options: "routingNetworks" },
        { path: "protocol", label: "protocol", type: "list", placeholder: "每行一个协议" },
        { path: "outboundTag", label: "outboundTag", type: "select", optionsFrom: "outbounds" }
      ]
    },
    "dns.servers": {
      title: "DNS 服务器",
      rowID: "bypass-dns-rows",
      columns: 4,
      fields: [
        { path: "address", label: "address", placeholder: "1.1.1.1" },
        { path: "tag", label: "tag", placeholder: "routing-dns" },
        { path: "outboundTag", label: "outboundTag", type: "select", optionsFrom: "outbounds" }
      ]
    }
  };

  function isObject(value) {
    return value !== null && typeof value === "object" && !Array.isArray(value);
  }

  function clone(value) {
    return JSON.parse(JSON.stringify(value));
  }

  function showError(box, message) {
    if (!box) return;
    box.textContent = message || "";
    box.classList.toggle("hidden", !message);
  }

  function getPath(root, path) {
    return path.split(".").reduce(function (value, key) {
      return value == null ? undefined : value[key];
    }, root);
  }

  function setPath(root, path, value) {
    var keys = path.split(".");
    var target = root;
    keys.slice(0, -1).forEach(function (key) {
      if (!isObject(target[key])) target[key] = {};
      target = target[key];
    });
    target[keys[keys.length - 1]] = value;
  }

  function deletePath(root, path) {
    var keys = path.split(".");
    var parents = [];
    var target = root;
    for (var i = 0; i < keys.length - 1; i++) {
      if (!isObject(target[keys[i]])) return;
      parents.push([target, keys[i]]);
      target = target[keys[i]];
    }
    delete target[keys[keys.length - 1]];
    for (var p = parents.length - 1; p >= 0; p--) {
      var child = parents[p][0][parents[p][1]];
      if (isObject(child) && Object.keys(child).length === 0) {
        delete parents[p][0][parents[p][1]];
      } else {
        break;
      }
    }
  }

  function ensureArray(path) {
    var value = getPath(state, path);
    if (!Array.isArray(value)) {
      value = [];
      setPath(state, path, value);
    }
    return value;
  }

  function containsUnsafeInteger(text) {
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
      if (ch === "-" || /\d/.test(ch)) {
        var match = text.slice(i).match(/^-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/);
        if (!match) continue;
        var token = match[0];
        i += token.length - 1;
        if (!/[.eE]/.test(token) && typeof BigInt === "function") {
          var integer = BigInt(token);
          if (integer > BigInt(Number.MAX_SAFE_INTEGER) || integer < BigInt(Number.MIN_SAFE_INTEGER)) return true;
        }
      }
    }
    return false;
  }

  function parseRoot(text, forStructuredEditor) {
    if (text.length > 2 * 1024 * 1024) throw new Error("配置超过 2 MiB");
    var parsed = JSON.parse(text);
    if (!isObject(parsed)) throw new Error("配置顶层必须是 JSON 对象");
    if (forStructuredEditor && containsUnsafeInteger(text)) {
      throw new Error("配置包含超出 JavaScript 安全范围的整数，请使用 Raw Tab 编辑，避免数值精度丢失");
    }
    return parsed;
  }

  function syncRaw() {
    raw.value = JSON.stringify(state, null, 2) + "\n";
    rawDirty = false;
  }

  function adoptRaw() {
    try {
      state = parseRoot(raw.value, true);
      rawDirty = false;
      showError(editorError, "");
      renderAll();
      return true;
    } catch (error) {
      showError(editorError, "无法进入结构化编辑：" + error.message);
      return false;
    }
  }

  function setTextCell(row, value, className) {
    var cell = document.createElement("td");
    if (className) cell.className = className;
    var text = value;
    if (Array.isArray(value)) text = value.join(", ");
    if (value === undefined || value === null || value === "") text = "—";
    cell.textContent = String(text);
    row.appendChild(cell);
  }

  function itemLabel(kind, item, index) {
    if (kind === "dns.servers" && typeof item === "string") return item;
    if (!isObject(item)) return definitions[kind].title + " #" + (index + 1);
    return item.tag || item.ruleTag || item.address || definitions[kind].title + " #" + (index + 1);
  }

  function actionCell(kind, item, index) {
    var cell = document.createElement("td");
    cell.className = "config-row-actions";
    var wrap = document.createElement("div");
    wrap.className = "site-action-buttons";

    var edit = document.createElement("button");
    edit.type = "button";
    edit.className = "btn icon-btn";
    edit.title = "编辑";
    edit.setAttribute("aria-label", "编辑 " + itemLabel(kind, item, index));
    edit.innerHTML = EDIT_SVG;
    edit.addEventListener("click", function () {
      openItemDialog(kind, index);
    });

    var remove = document.createElement("button");
    remove.type = "button";
    remove.className = "danger icon-btn";
    remove.title = "删除";
    remove.setAttribute("aria-label", "删除 " + itemLabel(kind, item, index));
    remove.innerHTML = TRASH_SVG;
    remove.addEventListener("click", function () {
      if (!confirm("确定删除“" + itemLabel(kind, item, index) + "”？")) return;
      ensureArray(kind).splice(index, 1);
      syncRaw();
      renderArray(kind);
    });

    wrap.appendChild(edit);
    wrap.appendChild(remove);
    cell.appendChild(wrap);
    return cell;
  }

  function routingMatchSummary(item) {
    if (!isObject(item)) return "非对象配置项";
    var labels = [];
    ["inboundTag", "domain", "ip", "port", "network", "protocol"].forEach(function (key) {
      var value = item[key];
      if (Array.isArray(value) ? value.length > 0 : value !== undefined && value !== "") labels.push(key);
    });
    return labels.length ? labels.join(" · ") : "无条件";
  }

  function renderArray(kind) {
    var definition = definitions[kind];
    var body = document.getElementById(definition.rowID);
    if (!body) return;
    body.textContent = "";
    var items = getPath(state, kind);
    if (!Array.isArray(items) || items.length === 0) {
      var emptyRow = document.createElement("tr");
      var emptyCell = document.createElement("td");
      emptyCell.colSpan = definition.columns;
      emptyCell.className = "config-empty-cell";
      emptyCell.textContent = "暂无配置项";
      emptyRow.appendChild(emptyCell);
      body.appendChild(emptyRow);
      return;
    }
    items.forEach(function (item, index) {
      var row = document.createElement("tr");
      if (kind === "inbounds") {
        ["tag", "type", "listen", "network", "port"].forEach(function (path) {
          setTextCell(row, isObject(item) ? getPath(item, path) : undefined);
        });
      } else if (kind === "outbounds") {
        ["tag", "mode", "upstream.protocol", "upstream.server"].forEach(function (path) {
          setTextCell(row, isObject(item) ? getPath(item, path) : undefined);
        });
      } else if (kind === "routing.rules") {
        setTextCell(row, isObject(item) ? item.ruleTag : undefined);
        setTextCell(row, routingMatchSummary(item), "config-condition-cell");
        setTextCell(row, isObject(item) ? item.outboundTag : undefined);
      } else if (kind === "dns.servers") {
        if (typeof item === "string") {
          setTextCell(row, item);
          setTextCell(row, undefined);
          setTextCell(row, undefined);
        } else {
          ["address", "tag", "outboundTag"].forEach(function (path) {
            setTextCell(row, isObject(item) ? getPath(item, path) : undefined);
          });
        }
      }
      row.appendChild(actionCell(kind, item, index));
      body.appendChild(row);
    });
  }

  function renderTopFields() {
    var control = isObject(state.control) ? state.control : {};
    document.getElementById("bypass-control-enabled").checked = control.enabled === true;
    document.getElementById("bypass-control-socket").value = control.socket == null ? "" : control.socket;
    document.getElementById("bypass-control-mode").value = control.mode == null ? "" : control.mode;

    var routing = isObject(state.routing) ? state.routing : {};
    document.getElementById("bypass-routing-domain-strategy").value = routing.domainStrategy == null ? "" : routing.domainStrategy;
    document.getElementById("bypass-routing-final-outbound").value = routing.finalOutboundTag == null ? "" : routing.finalOutboundTag;

    var dns = isObject(state.dns) ? state.dns : {};
    document.getElementById("bypass-dns-query-strategy").value = dns.queryStrategy == null ? "" : dns.queryStrategy;
  }

  function renderAll() {
    renderTopFields();
    Object.keys(definitions).forEach(renderArray);
  }

  function bindScalar(id, path, kind) {
    var input = document.getElementById(id);
    if (!input) return;
    var eventName = kind === "checkbox" ? "change" : "input";
    input.addEventListener(eventName, function () {
      if (kind === "checkbox") {
        setPath(state, path, input.checked);
      } else if (input.value === "") {
        deletePath(state, path);
      } else {
        setPath(state, path, input.value);
      }
      syncRaw();
    });
  }

  function fieldValue(item, field) {
    var value = getPath(item, field.path);
    if (field.type === "multi-select") {
      if (typeof value === "string") {
        return value.split(",").map(function (part) { return part.trim(); }).filter(Boolean);
      }
      return Array.isArray(value) ? value.map(String) : [];
    }
    if (field.type === "list") {
      if (Array.isArray(value)) return value.join("\n");
      return value == null ? "" : String(value);
    }
    return value == null ? "" : String(value);
  }

  function tagOptions(path) {
    var items = getPath(state, path);
    if (!Array.isArray(items)) return [];
    return items.reduce(function (options, item) {
      if (!isObject(item) || typeof item.tag !== "string" || item.tag.trim() === "") return options;
      var tag = item.tag.trim();
      if (!options.some(function (option) { return option.value === tag; })) {
        options.push({ value: tag, label: tag });
      }
      return options;
    }, []);
  }

  function fieldOptions(field, currentValue) {
    var options = [];
    if (field.options && Array.isArray(optionSets[field.options])) {
      options = optionSets[field.options].map(function (option) {
        return { value: option.value, label: option.label };
      });
    } else if (field.optionsFrom) {
      options = tagOptions(field.optionsFrom);
    }
    var current = Array.isArray(currentValue) ? currentValue : [currentValue];
    current.forEach(function (value) {
      if (value === undefined || value === null || value === "") return;
      value = String(value);
      if (!options.some(function (option) { return option.value === value; })) {
        options.push({ value: value, label: value + "（当前配置）" });
      }
    });
    return options;
  }

  function appendSelectOptions(select, field, currentValue) {
    if (field.type !== "multi-select") {
      var empty = document.createElement("option");
      empty.value = "";
      empty.textContent = "请选择";
      select.appendChild(empty);
    }
    var selectedValues = Array.isArray(currentValue) ? currentValue : [currentValue];
    fieldOptions(field, currentValue).forEach(function (item) {
      var option = document.createElement("option");
      option.value = item.value;
      option.textContent = item.label;
      option.selected = selectedValues.indexOf(item.value) !== -1;
      select.appendChild(option);
    });
  }

  function extraFields(item, fields) {
    var extra = clone(item);
    fields.forEach(function (field) {
      deletePath(extra, field.path);
    });
    return extra;
  }

  function createDialogField(field, item, index) {
    var label = document.createElement("label");
    label.textContent = field.label;
    var input;
    var currentValue = fieldValue(item, field);
    if (field.type === "select" || field.type === "multi-select") {
      input = document.createElement("select");
      input.multiple = field.type === "multi-select";
      if (input.multiple) input.size = Math.min(5, Math.max(2, fieldOptions(field, currentValue).length));
      appendSelectOptions(input, field, currentValue);
    } else if (field.type === "list") {
      input = document.createElement("textarea");
      input.rows = 3;
      input.className = "mono wide";
    } else {
      input = document.createElement("input");
      input.type = field.type || "text";
      if (field.type === "number") input.step = "1";
    }
    input.id = "bypass-dialog-field-" + index;
    if (field.type !== "select" && field.type !== "multi-select") input.value = currentValue;
    input.placeholder = field.placeholder || "";
    if (field.type === "list" || field.type === "multi-select") {
      var hint = document.createElement("small");
      hint.className = "muted";
      hint.textContent = field.type === "multi-select" ? "可多选；桌面端按 Ctrl/Command 选择多个值" : "每行填写一个值";
      label.appendChild(input);
      label.appendChild(hint);
    } else {
      label.appendChild(input);
    }
    dialogFields.appendChild(label);
  }

  function openItemDialog(kind, index) {
    var definition = definitions[kind];
    var item = index === null ? {} : getPath(state, kind)[index];
    var compactDNS = kind === "dns.servers" && typeof item === "string";
    if (compactDNS) item = { address: item };
    if (!isObject(item)) {
      showError(editorError, definition.title + "中的第 " + (index + 1) + " 项不是 JSON 对象，请在 Raw Tab 中编辑");
      activateTab("raw");
      return;
    }
    editing = { kind: kind, index: index, compactDNS: compactDNS };
    dialogTitle.textContent = (index === null ? "添加 " : "编辑 ") + definition.title;
    dialogFields.textContent = "";
    showError(dialogError, "");
    definition.fields.forEach(function (field, fieldIndex) {
      createDialogField(field, item, fieldIndex);
    });
    dialogExtra.value = JSON.stringify(extraFields(item, definition.fields), null, 2);
    if (typeof dialog.showModal === "function") dialog.showModal();
    else dialog.setAttribute("open", "open");
    var first = dialogFields.querySelector("input, textarea");
    if (first) first.focus();
  }

  function saveDialogItem() {
    if (!editing) return;
    var definition = definitions[editing.kind];
    var item;
    try {
      item = JSON.parse(dialogExtra.value || "{}");
      if (!isObject(item)) throw new Error("其他字段必须是 JSON 对象");
      if (containsUnsafeInteger(dialogExtra.value || "{}")) {
        throw new Error("其他字段包含超出 JavaScript 安全范围的整数，请改用 Raw Tab");
      }
      definition.fields.forEach(function (field, fieldIndex) {
        var input = document.getElementById("bypass-dialog-field-" + fieldIndex);
        var value = input.value;
        deletePath(item, field.path);
        if (field.type === "multi-select") {
          value = Array.prototype.slice.call(input.selectedOptions).map(function (option) {
            return option.value;
          });
          if (value.length === 0) return;
        } else if (value === "") {
          return;
        } else if (field.type === "number") {
          var number = Number(value);
          if (!Number.isSafeInteger(number)) throw new Error(field.label + " 必须是安全整数");
          value = number;
        } else if (field.type === "list") {
          value = value.split(/\r?\n/).map(function (part) {
            return part.trim();
          }).filter(Boolean);
          if (value.length === 0) return;
        }
        setPath(item, field.path, value);
      });
    } catch (error) {
      showError(dialogError, "无法保存：" + error.message);
      return;
    }

    var items = ensureArray(editing.kind);
    var savedItem = item;
    if (editing.kind === "dns.servers" && editing.compactDNS &&
        Object.keys(item).length === 1 && typeof item.address === "string") {
      savedItem = item.address;
    }
    if (editing.index === null) items.push(savedItem);
    else items[editing.index] = savedItem;
    syncRaw();
    renderArray(editing.kind);
    editing = null;
    dialog.close();
  }

  function activateTab(name) {
    if (name !== "raw" && activeTab === "raw" && rawDirty && !adoptRaw()) return false;
    if (name === "raw" && activeTab !== "raw") syncRaw();
    document.querySelectorAll("[data-bypass-tab]").forEach(function (button) {
      var selected = button.getAttribute("data-bypass-tab") === name;
      button.classList.toggle("active", selected);
      button.setAttribute("aria-selected", selected ? "true" : "false");
      button.tabIndex = selected ? 0 : -1;
    });
    document.querySelectorAll("[data-bypass-panel]").forEach(function (panel) {
      panel.classList.toggle("hidden", panel.getAttribute("data-bypass-panel") !== name);
    });
    activeTab = name;
    return true;
  }

  function nextJSONToken(text, start) {
    for (var i = start; i < text.length; i++) {
      if (!/\s/.test(text[i])) return text[i];
    }
    return "";
  }

  function formatJSONLosslessly(text) {
    if (text.length > 2 * 1024 * 1024) throw new Error("文件超过 2 MiB，无法在浏览器中安全格式化");
    JSON.parse(text);
    var output = "";
    var depth = 0;
    var inString = false;
    var escaped = false;
    for (var i = 0; i < text.length; i++) {
      var ch = text[i];
      if (inString) {
        output += ch;
        if (escaped) escaped = false;
        else if (ch === "\\") escaped = true;
        else if (ch === '"') inString = false;
        continue;
      }
      if (ch === '"') {
        inString = true;
        output += ch;
      } else if (/\s/.test(ch)) {
        continue;
      } else if (ch === "{" || ch === "[") {
        output += ch;
        if (nextJSONToken(text, i + 1) !== (ch === "{" ? "}" : "]")) {
          if (depth >= 200) throw new Error("JSON 嵌套超过 200 层");
          depth++;
          output += "\n" + "  ".repeat(depth);
        }
      } else if (ch === "}" || ch === "]") {
        var matchingOpen = ch === "}" ? "{" : "[";
        if (output.slice(-1) !== matchingOpen) {
          depth = Math.max(0, depth - 1);
          output += "\n" + "  ".repeat(depth);
        }
        output += ch;
      } else if (ch === ",") {
        output += ",\n" + "  ".repeat(depth);
      } else if (ch === ":") {
        output += ": ";
      } else {
        output += ch;
      }
      if (output.length > 4 * 1024 * 1024) throw new Error("格式化后的内容超过 4 MiB");
    }
    return output + "\n";
  }

  document.querySelectorAll("[data-bypass-tab]").forEach(function (button) {
    button.addEventListener("click", function () {
      activateTab(button.getAttribute("data-bypass-tab"));
    });
    button.addEventListener("keydown", function (event) {
      if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
      var tabs = Array.prototype.slice.call(document.querySelectorAll("[data-bypass-tab]"));
      var direction = event.key === "ArrowRight" ? 1 : -1;
      var next = (tabs.indexOf(button) + direction + tabs.length) % tabs.length;
      if (activateTab(tabs[next].getAttribute("data-bypass-tab"))) tabs[next].focus();
      event.preventDefault();
    });
  });

  document.querySelectorAll("[data-bypass-add]").forEach(function (button) {
    button.addEventListener("click", function () {
      openItemDialog(button.getAttribute("data-bypass-add"), null);
    });
  });

  document.querySelectorAll(".dialog-close").forEach(function (button) {
    button.addEventListener("click", function () {
      editing = null;
      dialog.close();
    });
  });
  dialogSave.addEventListener("click", saveDialogItem);

  raw.addEventListener("input", function () {
    rawDirty = true;
    showError(formatError, "");
  });

  var formatButton = document.getElementById("json-format-btn");
  formatButton.addEventListener("click", function () {
    try {
      raw.value = formatJSONLosslessly(raw.value);
      rawDirty = true;
      showError(formatError, "");
    } catch (error) {
      showError(formatError, "无法格式化：" + error.message);
    }
  });

  form.addEventListener("submit", function (event) {
    if (activeTab !== "raw") {
      syncRaw();
      return;
    }
    try {
      parseRoot(raw.value, false);
      showError(formatError, "");
    } catch (error) {
      event.preventDefault();
      showError(formatError, "无法提交：" + error.message);
    }
  });

  bindScalar("bypass-control-enabled", "control.enabled", "checkbox");
  bindScalar("bypass-control-socket", "control.socket", "text");
  bindScalar("bypass-control-mode", "control.mode", "text");
  bindScalar("bypass-routing-domain-strategy", "routing.domainStrategy", "text");
  bindScalar("bypass-routing-final-outbound", "routing.finalOutboundTag", "text");
  bindScalar("bypass-dns-query-strategy", "dns.queryStrategy", "text");

  try {
    state = parseRoot(raw.value, true);
    renderAll();
  } catch (error) {
    showError(editorError, "结构化编辑暂不可用：" + error.message);
    try {
      parseRoot(raw.value, false);
    } catch (ignored) {
      showError(formatError, "当前 JSON 无法解析，请在 Raw Tab 修复：" + ignored.message);
    }
    // Preserve the invalid or precision-sensitive source exactly as entered.
    // activateTab normally serializes structured state when entering Raw.
    activeTab = "raw";
    activateTab("raw");
  }
})();
