/* AgentCore chat helpers. No HTML from messages is parsed or inserted. */
(function (host) {
  "use strict";
  function sse(emit) {
    var buffer = "", data = [], event = "", first = true;
    function line(value) {
      if (!value) {
        if (data.length) { var raw = data.join("\n"); if (raw !== "[DONE]") emit(JSON.parse(raw), event); }
        data = []; event = ""; return;
      }
      if (value[0] === ":") return;
      var colon = value.indexOf(":"), key = colon < 0 ? value : value.slice(0, colon);
      var val = colon < 0 ? "" : value.slice(colon + 1).replace(/^ /, "");
      if (key === "data") data.push(val);
      if (key === "event") event = val;
    }
    return function (chunk, end) {
      if (first && chunk) { chunk = chunk.replace(/^\uFEFF/, ""); first = false; }
      buffer += chunk;
      var match;
      while ((match = /\r\n|\r|\n/.exec(buffer))) {
        if (!end && match[0] === "\r" && match.index === buffer.length - 1) break;
        line(buffer.slice(0, match.index)); buffer = buffer.slice(match.index + match[0].length);
      }
      if (end) { if (buffer) line(buffer); buffer = ""; line(""); }
    };
  }
  function safeURL(value) {
    try { var url = new URL(value, "https://local.invalid/"); return /^(https?:|mailto:)$/.test(url.protocol) ? value : null; } catch (_) { return null; }
  }
  function node(doc, tag, text) { var n = doc.createElement(tag); if (text !== undefined) n.textContent = text; return n; }
  // A deliberately bounded Markdown subset. All output uses fixed DOM tags and
  // text nodes; raw HTML and image URLs are never interpreted by the browser.
  function inline(doc, target, text, depth, inLink) {
    depth = depth || 0;
    if (depth > 24) { target.appendChild(doc.createTextNode(text)); return; }
    var i = 0, plain = "";
    function flush() { if (plain) { target.appendChild(doc.createTextNode(plain)); plain = ""; } }
    function closing(marker, start) {
      var index = start;
      while ((index = text.indexOf(marker, index)) >= 0) {
        var escapes = 0; for (var k = index - 1; k >= 0 && text[k] === "\\"; k--) escapes++;
        if (marker === "*" || marker === "_") {
                  var runLength = 1; while (text[index + runLength] === marker) runLength++;
                  if (runLength % 2 === 0) { index += runLength; continue; }
                  index += runLength - 1;
                }
                if (!(escapes % 2) && index > start && !/\s/.test(text[index - 1])) {
          // A strong delimiter closes at the end of a run, leaving a nested
          // single emphasis delimiter inside (**bold *italic***).
          if (marker === "**" || marker === "__") while (text[index + marker.length] === marker[0]) index++;
          return index;
        }
        index += marker.length;
      }
      return -1;
    }
    while (i < text.length) {
      var rest = text.slice(i), match, end, item;
      if ((match = /^\\([!"#$%&'()*+,\-./:;<=>?@[\]\\^_`{|}~])/.exec(rest))) { plain += match[1]; i += 2; continue; }
      if ((match = /^( {2,}|\\)\n/.exec(rest))) { flush(); target.appendChild(node(doc, "br")); i += match[0].length; continue; }
      if (rest[0] === "\n") { plain += " "; i++; continue; }
      if ((match = /^(`+)/.exec(rest))) {
        var ticks = match[1], search = i + ticks.length;
        end = text.indexOf(ticks, search);
        while (end >= 0 && (text[end - 1] === "`" || text[end + ticks.length] === "`")) end = text.indexOf(ticks, end + ticks.length);
        if (end >= 0) {
          flush(); var code = text.slice(search, end).replace(/\n/g, " ");
          if (/^ .* $/.test(code) && /[^ ]/.test(code)) code = code.slice(1, -1);
          target.appendChild(node(doc, "code", code)); i = end + ticks.length; continue;
        }
      }
      if (!inLink && rest[0] === "[" && (match = /^\[([^\]\n]+)\]\(/.exec(rest))) {
        var start = i + match[0].length, cursor = start, balance = 1;
        for (; cursor < text.length; cursor++) {
          if (text[cursor] === "\\") { cursor++; continue; }
          if (text[cursor] === "(") balance++;
          if (text[cursor] === ")" && --balance === 0) break;
        }
        if (balance === 0) {
          var destination = text.slice(start, cursor).trim(), link = /^(?:<([^<>\n]*)>|(\S+?))(?:\s+["']([^\n]*)["'])?$/.exec(destination);
          if (link) {
            var href = safeURL((link[1] || link[2] || "").replace(/\\([()\\])/g, "$1"));
            flush(); item = node(doc, href ? "a" : "span");
            if (href) { item.setAttribute("href", href); item.setAttribute("target", "_blank"); item.setAttribute("rel", "noopener noreferrer"); if (link[3]) item.setAttribute("title", link[3]); }
            inline(doc, item, match[1], depth + 1, true); target.appendChild(item); i = cursor + 1; continue;
          }
        }
      }
      var marker = /^(\*\*\*|___|\*\*|__|~~|\*|_)/.exec(rest);
      if (marker && !/\s/.test(rest[marker[0].length] || " ") && !(marker[0][0] === "_" && /[\p{L}\p{N}]/u.test(text[i - 1] || ""))) {
        end = closing(marker[0], i + marker[0].length);
        if (end >= 0) {
          flush(); var token = marker[0], content = text.slice(i + token.length, end);
          item = node(doc, token === "~~" ? "del" : token.length === 1 ? "em" : "strong");
          var inner = item; if (token.length === 3) { inner = node(doc, "em"); item.appendChild(inner); }
          inline(doc, inner, content, depth + 1, inLink); target.appendChild(item); i = end + token.length; continue;
        }
      }
      plain += text[i++];
    }
    flush();
  }
  function tableCells(line) {
    line = line.trim().replace(/^\|/, "").replace(/(?<!\\)\|$/, "");
    var cells = [], cell = "", ticks = "";
    for (var i = 0; i < line.length; i++) {
      if (line[i] === "\\" && i + 1 < line.length) { cell += line[i] + line[++i]; continue; }
      if (line[i] === "`") { var run = /^`+/.exec(line.slice(i))[0]; ticks = ticks === run ? "" : ticks || run; cell += run; i += run.length - 1; continue; }
      if (line[i] === "|" && !ticks) { cells.push(cell.trim()); cell = ""; } else cell += line[i];
    }
    cells.push(cell.trim()); return cells;
  }
  function markdown(doc, text, copy, depth) {
    depth = depth || 0;
    var fragment = doc.createDocumentFragment(), lines = String(text || "").replace(/\r\n?/g, "\n").split("\n"), i = 0;
    if (depth > 24) { fragment.appendChild(node(doc, "p", text)); return fragment; }
    function listMatch(line) { return /^( *)([-+*]|\d{1,9}[.)])\s+(.*)$/.exec(line); }
    function rule(line) { return /^ {0,3}(?:(?:\*\s*){3,}|(?:-\s*){3,}|(?:_\s*){3,})$/.test(line); }
    function tableAt(index) {
      if (index + 1 >= lines.length || !lines[index].includes("|")) return null;
      var cells = tableCells(lines[index + 1]);
      return cells.length === tableCells(lines[index]).length && cells.every(function (c) { return /^:?-{3,}:?$/.test(c); }) ? cells : null;
    }
    function startsBlock(index) { var line = lines[index]; return /^\s*$/.test(line) || /^ {0,3}(?:#{1,6}\s|>|`{3,}|~{3,})/.test(line) || rule(line) || !!listMatch(line) || !!tableAt(index); }
    while (i < lines.length) {
      var value = lines[i], fence = /^ {0,3}(`{3,}|~{3,})(.*)$/.exec(value), match;
      if (!value.trim()) { i++; continue; }
      if (fence) {
        var code = [], marker = fence[1][0], length = fence[1].length; i++;
        while (i < lines.length && !(new RegExp("^ {0,3}" + marker + "{" + length + ",}\\s*$")).test(lines[i])) code.push(lines[i++]);
        if (i < lines.length) i++;
        var box = node(doc, "div"), button = node(doc, "button", "复制代码"), pre = node(doc, "pre");
        box.className = "agent-chat-code"; button.type = "button"; button.className = "btn";
        var source = code.join("\n");
        button.addEventListener("click", (function (text, btn) { return function () { copy(text, btn); }; })(source, button));
        box.appendChild(node(doc, "small", fence[2].trim())); box.appendChild(button); pre.appendChild(node(doc, "code", source)); box.appendChild(pre); fragment.appendChild(box); continue;
      }
      if (rule(value)) { fragment.appendChild(node(doc, "hr")); i++; continue; }
      if ((match = /^ {0,3}(#{1,6})\s+(.*)$/.exec(value))) {
        var heading = node(doc, "h" + match[1].length); inline(doc, heading, match[2].replace(/\s+#+\s*$/, "")); fragment.appendChild(heading); i++; continue;
      }
      if (/^ {0,3}>/.test(value)) {
        var quote = [];
        while (i < lines.length && /^ {0,3}>/.test(lines[i])) quote.push(lines[i++].replace(/^ {0,3}> ?/, ""));
        var blockquote = node(doc, "blockquote"); blockquote.appendChild(markdown(doc, quote.join("\n"), copy, depth + 1)); fragment.appendChild(blockquote); continue;
      }
      var alignments = tableAt(i);
      if (alignments) {
        var table = node(doc, "table"), thead = node(doc, "thead"), tbody = node(doc, "tbody");
        function row(values, tag, parent) {
          var tr = node(doc, "tr");
          alignments.forEach(function (alignment, index) {
            var cell = node(doc, tag); if (alignment[0] === ":" || alignment.endsWith(":")) cell.style.textAlign = alignment[0] === ":" && alignment.endsWith(":") ? "center" : alignment.endsWith(":") ? "right" : "left";
            inline(doc, cell, values[index] || ""); tr.appendChild(cell);
          }); parent.appendChild(tr);
        }
        row(tableCells(value), "th", thead); i += 2;
        while (i < lines.length && lines[i].trim() && lines[i].includes("|") && !/^ {0,3}(?:>|`{3,}|~{3,})/.test(lines[i])) row(tableCells(lines[i++]), "td", tbody);
        table.appendChild(thead); table.appendChild(tbody); var wrap = node(doc, "div"); wrap.className = "agent-chat-table"; wrap.appendChild(table); fragment.appendChild(wrap); continue;
      }
      var first = listMatch(value);
      if (first) {
        var indent = first[1].length, ordered = /^\d/.test(first[2]), list = node(doc, ordered ? "ol" : "ul");
        if (ordered) list.setAttribute("start", String(parseInt(first[2], 10)));
        while (i < lines.length) {
          var entry = listMatch(lines[i]);
          if (!entry || entry[1].length !== indent || /^\d/.test(entry[2]) !== ordered || rule(lines[i])) break;
          var contentIndent = lines[i].length - entry[3].length, itemLines = [entry[3]]; i++;
          while (i < lines.length) {
            if (!lines[i].trim()) {
              var next = i + 1; while (next < lines.length && !lines[next].trim()) next++;
              var nextEntry = next < lines.length && listMatch(lines[next]);
              if (next < lines.length && (/^ */.exec(lines[next])[0].length > indent || nextEntry && nextEntry[1].length === indent)) { itemLines.push(""); i++; continue; }
              break;
            }
            var spaces = /^ */.exec(lines[i])[0].length;
            if (spaces <= indent) {
              // Unindented prose can continue the current list paragraph, but
              // never absorb a new block or prose after a blank separator.
              if (!itemLines[itemLines.length - 1] || startsBlock(i)) break;
              itemLines.push(lines[i++]); continue;
            }
            itemLines.push(lines[i++].slice(Math.min(spaces, contentIndent)));
          }
          var li = node(doc, "li"), task = /^\[([ xX])\]\s+(.*)$/.exec(itemLines[0]);
          if (task) {
            itemLines[0] = task[2]; var checkbox = node(doc, "input"); checkbox.type = "checkbox"; checkbox.disabled = true; checkbox.checked = task[1].toLowerCase() === "x";
            checkbox.setAttribute("aria-label", checkbox.checked ? "已完成" : "未完成"); li.className = "agent-chat-task"; li.appendChild(checkbox);
          }
          li.appendChild(markdown(doc, itemLines.join("\n"), copy, depth + 1)); list.appendChild(li);
        }
        fragment.appendChild(list); continue;
      }
      var paragraph = [value]; i++;
      while (i < lines.length && !startsBlock(i) && !/^ {0,3}(?:=+|-+)\s*$/.test(lines[i])) paragraph.push(lines[i++]);
      var tag = "p";
      if (i < lines.length && /^ {0,3}(?:=+|-+)\s*$/.test(lines[i])) { tag = lines[i].trim()[0] === "=" ? "h1" : "h2"; i++; }
      var p = node(doc, tag); inline(doc, p, paragraph.join("\n")); fragment.appendChild(p);
    }
    return fragment;
  }
  function usage(info, options) {
    var prompt = Number(info && info.prompt_tokens) || 0, completion = Number(info && info.completion_tokens) || 0;
    var windowSize = Number(options && options.context_window) || 0;
    var context = info && Number.isFinite(info.context_tokens) && info.context_tokens >= 0 ? info.context_tokens : null;
    var measuredPrompt = info && Number.isFinite(info.prompt_tokens) && info.prompt_tokens >= 0 ? info.prompt_tokens : null;
    var used = context !== null ? context : measuredPrompt;
    return { prompt: prompt, completion: completion, total: Number(info && info.total_tokens) || prompt + completion, window: windowSize, context: context,
      estimated: context === null && measuredPrompt !== null,
      percent: windowSize > 0 && used !== null ? Math.min(100, Math.max(0, used / windowSize * 100)) : null };
  }
  var api = { sse: sse, safeURL: safeURL, markdown: markdown, usage: usage };
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  else host.AgentChatCore = api;
})(typeof window === "undefined" ? globalThis : window);
