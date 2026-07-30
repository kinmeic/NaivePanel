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

  // Preserve route semantics explicitly. Most Caddyfiles should use the
  // normal directive ordering; route is opt-in and retained when imported.
  var useRouteToggle = document.getElementById("use-route-toggle");
  var handlersBlock = document.getElementById("caddy-handlers-block");
  if (useRouteToggle && handlersBlock) {
    function syncRouteBlock() {
      handlersBlock.classList.toggle("caddy-route-block", useRouteToggle.checked);
      handlersBlock.classList.toggle("caddy-directives-block", !useRouteToggle.checked);
      handlersBlock.querySelectorAll(".caddy-route-brace").forEach(function (brace) {
        brace.classList.toggle("hidden", !useRouteToggle.checked);
      });
    }
    useRouteToggle.addEventListener("change", syncRouteBlock);
    syncRouteBlock();
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

  // Dashboard resource sampler. Values are inserted with textContent so a
  // malformed response can never inject markup into the page.
  var systemCards = document.querySelector("[data-system-stats-url]");
  if (systemCards) {
    var firstSystemSample = true;
    function formatBytes(value) {
      var number = Number(value);
      if (!Number.isFinite(number) || number < 0) return "—";
      var units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
      var index = 0;
      while (number >= 1024 && index < units.length - 1) {
        number /= 1024;
        index++;
      }
      var digits = number >= 100 || index === 0 ? 0 : number >= 10 ? 1 : 2;
      return number.toFixed(digits) + " " + units[index];
    }
    function formatRate(value) {
      return formatBytes(value) + "/s";
    }
    function percent(value) {
      var number = Number(value);
      if (!Number.isFinite(number)) return "—";
      return Math.max(0, Math.min(100, number)).toFixed(1) + "%";
    }
    function setText(id, text) {
      var element = document.getElementById(id);
      if (element) element.textContent = text;
    }
    function setBar(id, value) {
      var element = document.getElementById(id);
      if (!element) return;
      var number = Number(value);
      element.style.width = (Number.isFinite(number) ? Math.max(0, Math.min(100, number)) : 0) + "%";
    }
    function unavailable(noteID, available) {
      if (!available) setText(noteID, "当前系统无法读取此项指标");
    }
    function renderSystemStats(stats) {
      if (stats.cpu_available) {
        setText("metric-cpu", percent(stats.cpu_percent));
        setText("metric-cpu-note", firstSystemSample ? "首次采样完成，下一次开始计算区间使用率" : "当前总 CPU 使用率");
        setBar("metric-cpu-bar", stats.cpu_percent);
      } else {
        unavailable("metric-cpu-note", false);
      }
      if (stats.memory_available) {
        setText("metric-memory", percent(stats.memory_percent));
        setText("metric-memory-note", formatBytes(stats.memory_used) + " / " + formatBytes(stats.memory_total));
        setBar("metric-memory-bar", stats.memory_percent);
      } else {
        unavailable("metric-memory-note", false);
      }
      if (stats.disk_available) {
        setText("metric-disk", percent(stats.disk_percent));
        setText("metric-disk-note", "已用 " + formatBytes(stats.disk_used) + "，可用 " + formatBytes(stats.disk_free));
        setBar("metric-disk-bar", stats.disk_percent);
      } else {
        unavailable("metric-disk-note", false);
      }
      if (stats.io_available) {
        setText("metric-disk-read", formatRate(stats.disk_read_bps));
        setText("metric-disk-write", formatRate(stats.disk_write_bps));
      }
      if (stats.network_available) {
        setText("metric-net-rx", formatRate(stats.network_rx_bps));
        setText("metric-net-tx", formatRate(stats.network_tx_bps));
        setText("metric-net-total", "累计接收 " + formatBytes(stats.network_rx_total) + " · 发送 " + formatBytes(stats.network_tx_total));
        setText("metric-network-note", firstSystemSample ? "首次采样完成，下一次开始计算实时带宽" : "不含 loopback 回环流量");
      } else {
        unavailable("metric-network-note", false);
      }
      var sampled = new Date(stats.sampled_at);
      setText("system-sampled-at", Number.isNaN(sampled.getTime()) ? "刚刚更新" : "更新于 " + sampled.toLocaleTimeString());
      firstSystemSample = false;
    }
    function loadSystemStats() {
      fetch(systemCards.getAttribute("data-system-stats-url"), {
        credentials: "same-origin",
        cache: "no-store"
      }).then(function (response) {
        if (!response.ok) throw new Error("HTTP " + response.status);
        return response.json();
      }).then(renderSystemStats).catch(function () {
        setText("system-sampled-at", "资源数据暂时不可用");
      });
    }
    loadSystemStats();
    setInterval(loadSystemStats, 3000);
  }

  // Cron presets are intentionally local text helpers. The server still
  // performs complete schedule/script validation before persisting anything.
  var cronSchedule = document.getElementById("cron-schedule");
  if (cronSchedule) {
    document.querySelectorAll("[data-cron-schedule]").forEach(function (button) {
      button.addEventListener("click", function () {
        cronSchedule.value = button.getAttribute("data-cron-schedule");
        cronSchedule.focus();
      });
    });
  }
  var cronScript = document.getElementById("cron-script");
  if (cronScript) {
    var cronTemplates = {
      backup: "#!/bin/sh\nset -eu\n\nbackup_dir=/var/backups/naivepanel\nmkdir -p \"$backup_dir\"\ntar -czf \"$backup_dir/config-$(date +%Y%m%d-%H%M%S).tar.gz\" /etc/caddy /etc/naivepanel\nfind \"$backup_dir\" -type f -name 'config-*.tar.gz' -mtime +14 -delete\n",
      cleanup: "#!/bin/sh\nset -eu\n\n# 删除 30 天前的压缩日志；请按实际路径调整。\nfind /var/log -type f -name '*.gz' -mtime +30 -delete\njournalctl --vacuum-time=30d\n",
      custom: "#!/bin/sh\nset -eu\n\n"
    };
    document.querySelectorAll("[data-cron-template]").forEach(function (button) {
      button.addEventListener("click", function () {
        var kind = button.getAttribute("data-cron-template");
        if (cronScript.value.trim() && !confirm("替换当前脚本内容？")) return;
        cronScript.value = cronTemplates[kind] || cronTemplates.custom;
        cronScript.focus();
      });
    });
  }

  // Lossless JSON formatter for the raw BypassCore editor. It validates with
  // JSON.parse but formats lexically, preserving duplicate keys and integers
  // larger than JavaScript's safe numeric range exactly as the user typed.
  var jsonFormatButton = document.getElementById("json-format-btn");
  var bypassRawConfig = document.getElementById("bypass-config-raw");
  if (jsonFormatButton && bypassRawConfig) {
    function nextJSONToken(text, start) {
      for (var i = start; i < text.length; i++) {
        if (!/\s/.test(text[i])) return text[i];
      }
      return "";
    }
    function formatJSONLosslessly(text) {
      if (text.length > 2 * 1024 * 1024) {
        throw new Error("文件超过 2 MiB，无法在浏览器中安全格式化");
      }
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
        if (output.length > 4 * 1024 * 1024) {
          throw new Error("格式化后的内容超过 4 MiB");
        }
      }
      return output + "\n";
    }
    jsonFormatButton.addEventListener("click", function () {
      var errorBox = document.getElementById("json-format-error");
      try {
        bypassRawConfig.value = formatJSONLosslessly(bypassRawConfig.value);
        errorBox.textContent = "";
        errorBox.classList.add("hidden");
      } catch (error) {
        errorBox.textContent = "无法格式化：" + error.message;
        errorBox.classList.remove("hidden");
      }
    });
  }
})();
