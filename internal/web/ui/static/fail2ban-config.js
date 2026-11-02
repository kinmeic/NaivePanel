// Edit known INI fields by source ranges, retaining all untouched bytes.
(function () {
  "use strict";

  function stripComment(value) { return value.replace(/\s+;.*$/, "").trim(); }

  function parseINI(text) {
    if (text.length > 128 * 1024) throw new Error("规则超过 128 KiB，请使用 Raw 或在服务器上编辑。");
    var doc = { text: text, sections: [], newline: text.indexOf("\r\n") >= 0 ? "\r\n" : "\n" };
    var lines = text.match(/[^\n]*\n|[^\n]+$/g) || [];
    var section = null;
    var option = null;
    var offset = 0;
    lines.forEach(function (rawLine) {
      var line = rawLine.replace(/\r?\n$/, "");
      var start = offset;
      offset += rawLine.length;
      if (/^\s*(?:[#;].*)?$/.test(line)) return;
      var indent = (line.match(/^\s*/) || [""])[0].length;
      if (option && indent > option.indent) {
        option.value += "\n" + stripComment(line);
        option.end = offset;
        return;
      }
      var header = line.match(/^\[([^\]\r\n]+)\]\s*(?:[#;].*)?$/);
      if (header) {
        if (doc.sections.some(function (s) { return s.name === header[1]; })) throw new Error("检测到重复的节 [" + header[1] + "]，请在 Raw 中编辑。");
        if (section) section.end = start;
        section = { name: header[1], start: start, headerEnd: offset, end: text.length, options: Object.create(null) };
        doc.sections.push(section);
        option = null;
        return;
      }
      var entry = line.match(/^(\s*)([^\s=:#][^=:]*?)(\s*[=:]\s*)(.*)$/);
      if (!section || !entry) throw new Error("无法安全解析此 INI 语法，原文已保留，请在 Raw 中编辑。");
      var key = entry[2].trim().toLowerCase();
      if (section.options[key]) throw new Error("[" + section.name + "] 中存在重复选项 " + key + "，请在 Raw 中编辑。");
      option = {
        start: start, end: offset, indent: indent,
        prefix: entry[1] + entry[2] + entry[3],
        suffix: (entry[4].match(/\s+;.*$/) || [""])[0],
        value: stripComment(entry[4]),
        newline: rawLine.endsWith("\n") ? (rawLine.endsWith("\r\n") ? "\r\n" : "\n") : ""
      };
      section.options[key] = option;
    });
    return doc;
  }

  function findSection(doc, name) { return doc.sections.find(function (s) { return s.name === name; }); }
  function valueOf(section, key) { return section && section.options[key] ? section.options[key].value : ""; }

  function addSection(text, name) {
    if (!/^[A-Za-z0-9][A-Za-z0-9_.-]{0,79}$/.test(name) || name === "DEFAULT" || name === "INCLUDES") throw new Error("jail 名称应为 1–80 位字母、数字、下划线、点或连字符。");
    var doc = parseINI(text);
    if (findSection(doc, name)) throw new Error("此 jail 已存在。");
    return text + (text && !text.endsWith("\n") ? doc.newline : "") + (text ? doc.newline : "") + "[" + name + "]" + doc.newline;
  }

  function setOption(text, sectionName, key, value) {
    var doc = parseINI(text);
    var section = findSection(doc, sectionName);
    var existing = section && section.options[key];
    value = value.trim();
    if ((!existing && !value) || (existing && existing.value === value)) return text;
    if (existing) {
      var replacement = "";
      if (value) {
        replacement = existing.prefix + value.replace(/\r?\n/g, doc.newline + "    ") + existing.suffix + existing.newline;
      }
      // Preserve standalone comments/blank lines between continuation lines.
      var oldText = text.slice(existing.start, existing.end);
      var retained = (oldText.match(/[^\n]*\n|[^\n]+$/g) || []).slice(1).filter(function (line) { return /^\s*(?:[#;].*)?(?:\r?\n)?$/.test(line); }).join("");
      return text.slice(0, existing.start) + replacement + retained + text.slice(existing.end);
    }
    if (!section) {
      if (sectionName === "DEFAULT") {
        text += (text && !text.endsWith("\n") ? doc.newline : "") + (text ? doc.newline : "") + "[DEFAULT]" + doc.newline;
      } else {
        text = addSection(text, sectionName);
      }
      doc = parseINI(text);
      section = findSection(doc, sectionName);
    }
    var before = text.slice(0, section.end);
    var addition = (before && !before.endsWith("\n") ? doc.newline : "") + key + " = " + value.replace(/\r?\n/g, doc.newline + "    ") + doc.newline;
    return before + addition + text.slice(section.end);
  }

  function removeSection(text, name) {
    var section = findSection(parseINI(text), name);
    if (!section || name === "DEFAULT" || name === "INCLUDES") throw new Error("无法删除此节。");
    return text.slice(0, section.start) + text.slice(section.end);
  }

  // Node tests exercise parsing and source-preserving mutations without a DOM.
  if (typeof module !== "undefined" && module.exports) module.exports = { parseINI: parseINI, setOption: setOption, addSection: addSection, removeSection: removeSection, valueOf: valueOf, findSection: findSection };
  if (typeof document === "undefined") return;

  var form = document.getElementById("fail2ban-config-form");
  var raw = document.getElementById("fail2ban-config-raw");
  if (!form || !raw) return;
  var tabField = document.getElementById("fail2ban-editor-tab");
  var errorBox = document.getElementById("fail2ban-editor-error");
  var rows = document.getElementById("fail2ban-jail-rows");
  var dialog = document.getElementById("fail2ban-jail-dialog");
  var jailForm = document.getElementById("fail2ban-jail-form");
  var jailName = document.getElementById("fail2ban-jail-name");
  var jailError = document.getElementById("fail2ban-jail-error");
  var defaultFields = Array.from(form.querySelectorAll("[data-fail2ban-default]"));
  var jailFields = Array.from(dialog.querySelectorAll("[data-fail2ban-jail]"));
  var activeTab = "raw";
  var editingName = null;

  function message(box, text) { box.textContent = text || ""; box.classList.toggle("hidden", !text); }
  function fieldKey(field, kind) { return field.getAttribute("data-fail2ban-" + kind); }

  function populate(field, value) {
    if (field.tagName === "SELECT") {
      field.querySelectorAll("[data-custom-value]").forEach(function (o) { o.remove(); });
      if (value && !Array.from(field.options).some(function (o) { return o.value === value; })) {
        var option = document.createElement("option");
        option.value = value;
        option.textContent = value + "（自定义值）";
        option.setAttribute("data-custom-value", "");
        field.appendChild(option);
      }
    }
    // A multiline value cannot be represented by a single-line input/select.
    // Leave it untouched unless the user explicitly edits it in Raw.
    field.dataset.originalValue = value;
    field.dataset.preserveValue = value.indexOf("\n") >= 0 && field.tagName !== "TEXTAREA" ? "1" : "0";
    field.disabled = field.dataset.preserveValue === "1";
    field.value = field.disabled && field.tagName === "INPUT" ? value.replace(/\n/g, " ↵ ") : value;
    field.title = field.disabled ? "多行值已保留，请在 Raw 中编辑" : "";
  }

  function updatedFields(text, fields, section, kind) {
    fields.forEach(function (field) {
      if (field.dataset.preserveValue !== "1") text = setOption(text, section, fieldKey(field, kind), field.value);
    });
    return text;
  }

  function renderStructured() {
    var doc = parseINI(raw.value);
    var defaults = findSection(doc, "DEFAULT");
    defaultFields.forEach(function (f) { populate(f, valueOf(defaults, fieldKey(f, "default"))); });
    rows.replaceChildren();
    doc.sections.filter(function (s) { return s.name !== "DEFAULT" && s.name !== "INCLUDES"; }).forEach(function (section) {
      var row = document.createElement("tr");
      [section.name, valueOf(section, "enabled") || "继承", valueOf(section, "port") || "继承", valueOf(section, "backend") || "继承"].forEach(function (value) {
        var cell = document.createElement("td");
        cell.textContent = value;
        row.appendChild(cell);
      });
      var actions = document.createElement("td");
      actions.className = "config-row-actions";
      var edit = document.createElement("button");
      edit.type = "button"; edit.className = "btn"; edit.textContent = "编辑";
      edit.setAttribute("aria-label", "编辑 jail " + section.name);
      edit.addEventListener("click", function () { openJail(section.name); });
      var remove = document.createElement("button");
      remove.type = "button"; remove.className = "btn"; remove.textContent = "删除";
      remove.setAttribute("aria-label", "删除 jail " + section.name);
      remove.addEventListener("click", function () {
        if (!window.confirm("删除本文件中的 [" + section.name + "]？删除后可能重新继承系统其他文件的配置；如需关闭防护，请将 enabled 设为 false。")) return;
        try { raw.value = removeSection(raw.value, section.name); renderStructured(); }
        catch (error) { message(errorBox, error.message); }
      });
      actions.append(edit, remove); row.appendChild(actions); rows.appendChild(row);
    });
    if (!rows.children.length) {
      var row = document.createElement("tr"); var cell = document.createElement("td");
      cell.colSpan = 5; cell.className = "config-empty-cell"; cell.textContent = "本文件暂无 jail，可添加规则或使用 Raw 编辑。";
      row.appendChild(cell); rows.appendChild(row);
    }
    message(errorBox, "");
  }

  function switchTab(name) {
    try {
      if (activeTab === "base") raw.value = updatedFields(raw.value, defaultFields, "DEFAULT", "default");
      if (name !== "raw") renderStructured();
    } catch (error) {
      message(errorBox, error.message);
      if (name !== "raw") return false;
    }
    activeTab = name;
    tabField.value = name;
    form.querySelectorAll("[data-fail2ban-tab]").forEach(function (tab) {
      var selected = tab.getAttribute("data-fail2ban-tab") === name;
      tab.classList.toggle("active", selected); tab.setAttribute("aria-selected", String(selected));
    });
    form.querySelectorAll("[data-fail2ban-panel]").forEach(function (panel) { panel.classList.toggle("hidden", panel.getAttribute("data-fail2ban-panel") !== name); });
    return true;
  }

  function openJail(name) {
    try {
      var doc = parseINI(raw.value);
      var section = name === null ? null : findSection(doc, name);
      editingName = name;
      jailName.value = name || "";
      jailName.readOnly = name !== null;
      document.getElementById("fail2ban-jail-dialog-title").textContent = name === null ? "添加 jail" : "编辑 jail " + name;
      jailFields.forEach(function (field) { populate(field, valueOf(section, fieldKey(field, "jail"))); });
      if (name === null) dialog.querySelector('[data-fail2ban-jail="enabled"]').value = "false";
      message(jailError, ""); dialog.showModal();
    } catch (error) { message(errorBox, error.message); }
  }

  defaultFields.forEach(function (field) {
    field.addEventListener("change", function () {
      try { raw.value = setOption(raw.value, "DEFAULT", fieldKey(field, "default"), field.value); message(errorBox, ""); }
      catch (error) { message(errorBox, error.message); }
    });
  });
  form.querySelectorAll("[data-fail2ban-tab]").forEach(function (tab) { tab.addEventListener("click", function () { switchTab(tab.getAttribute("data-fail2ban-tab")); }); });
  document.getElementById("fail2ban-jail-add").addEventListener("click", function () { openJail(null); });
  dialog.querySelectorAll("[data-fail2ban-dialog-close]").forEach(function (button) { button.addEventListener("click", function () { dialog.close(); }); });
  jailForm.addEventListener("submit", function (event) {
    event.preventDefault();
    try {
      var name = editingName === null ? jailName.value.trim() : editingName;
      var text = editingName === null ? addSection(raw.value, name) : raw.value;
      text = updatedFields(text, jailFields, name, "jail");
      parseINI(text);
      raw.value = text; renderStructured(); dialog.close();
    } catch (error) { message(jailError, error.message); }
  });
  form.addEventListener("submit", function (event) {
    try { if (activeTab === "base") raw.value = updatedFields(raw.value, defaultFields, "DEFAULT", "default"); }
    catch (error) { event.preventDefault(); message(errorBox, error.message); }
  });
  var initial = form.getAttribute("data-initial-tab") || "base";
  if (["base", "jails", "raw"].indexOf(initial) < 0) initial = "raw";
  if (!switchTab(initial)) switchTab("raw");
})();
