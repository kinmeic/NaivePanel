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

  // Restart services as background operations. This is essential for Caddy:
  // the POST reaches NaivePanel through Caddy itself, so a synchronous restart
  // can sever the response and leave the browser on a resubmittable POST URL.
  document.addEventListener("submit", function (e) {
    if (e.defaultPrevented) return;
    var submitter = e.submitter;
    if (!submitter || !submitter.hasAttribute("data-async-restart")) return;
    e.preventDefault();

    var form = e.target;
    var label = submitter.getAttribute("data-service-label") || "服务";
    var returnURL = submitter.getAttribute("data-return-url") || window.location.pathname;
    var progress = document.getElementById(submitter.getAttribute("data-progress-target"));
    if (!progress || form.dataset.operationPending === "1") return;
    form.dataset.operationPending = "1";

    var title = progress.querySelector("[data-operation-title]");
    var message = progress.querySelector("[data-operation-message]");
    var bar = progress.querySelector("[data-operation-bar]");
    var spinner = progress.querySelector(".spinner");
    var stateWidths = { queued: 25, restarting: 60, checking: 85, succeeded: 100, failed: 100 };
    var deadline = Date.now() + 90000;

    function renderOperation(operation) {
      var state = operation.state || "queued";
      progress.classList.remove("hidden", "operation-success", "operation-failed");
      if (state === "succeeded") progress.classList.add("operation-success");
      if (state === "failed") progress.classList.add("operation-failed");
      if (title) {
        title.textContent = state === "succeeded" ? label + " 重启完成" :
          state === "failed" ? label + " 重启失败" : "正在重启 " + label;
      }
      if (message) message.textContent = operation.message || "正在处理…";
      if (bar) bar.style.width = (stateWidths[state] || 15) + "%";
      if (spinner) spinner.classList.toggle("hidden", !!operation.done);
    }

    function restoreControls() {
      form.dataset.operationPending = "0";
      form.querySelectorAll("button").forEach(function (button) {
        button.disabled = false;
      });
    }

    function poll(statusURL) {
      fetch(statusURL, {
        credentials: "same-origin",
        cache: "no-store",
        headers: { Accept: "application/json" }
      }).then(function (response) {
        if (!response.ok) throw new Error("HTTP " + response.status);
        return response.json();
      }).then(function (operation) {
        renderOperation(operation);
        if (operation.done) {
          if (operation.success) {
            // replace() guarantees that refresh/back can never replay the POST.
            setTimeout(function () { window.location.replace(returnURL); }, 900);
          } else {
            restoreControls();
          }
          return;
        }
        setTimeout(function () { poll(statusURL); }, 700);
      }).catch(function () {
        if (Date.now() >= deadline) {
          renderOperation({
            state: "failed", done: true,
            message: "暂时无法重新连接面板。请检查服务日志，或在服务器上运行 systemctl status " + label.toLowerCase()
          });
          restoreControls();
          return;
        }
        renderOperation({
          state: "checking", done: false,
          message: label === "Caddy" ?
            "面板连接暂时中断，正在等待 Caddy 恢复…" :
            "暂时无法读取进度，正在重新连接…"
        });
        setTimeout(function () { poll(statusURL); }, 1000);
      });
    }

    progress.classList.remove("hidden");
    renderOperation({ state: "queued", message: "正在提交重启请求…" });
    form.querySelectorAll("button").forEach(function (button) {
      button.disabled = true;
    });
    var body = new FormData(form);
    if (submitter.name) body.set(submitter.name, submitter.value);
    fetch(form.action, {
      method: "POST",
      body: body,
      credentials: "same-origin",
      headers: {
        Accept: "application/json",
        "X-NaivePanel-Async": "1"
      }
    }).then(function (response) {
      return response.json().catch(function () {
        throw new Error("服务器返回了无法识别的响应");
      }).then(function (operation) {
        if (!response.ok) throw new Error(operation.message || "HTTP " + response.status);
        return operation;
      });
    }).then(function (operation) {
      renderOperation(operation);
      poll(operation.status_url);
    }).catch(function (error) {
      // Never automatically repeat a restart POST: it may already have reached
      // the server before Caddy closed the connection.
      renderOperation({
        state: "checking", done: false,
        message: "提交后的连接已中断，为避免重复重启不会再次提交。正在等待页面恢复…"
      });
      setTimeout(function () { window.location.replace(returnURL); }, 5000);
    });
  });

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
    var trafficHistory = [];
    var trafficHistoryLimit = 40;
    var trafficCanvas = document.getElementById("traffic-chart");
    var systemStatsLoading = false;
    var systemStatsTimer = 0;
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
    function formatLoad(value) {
      var number = Number(value);
      return Number.isFinite(number) && number >= 0 ? number.toFixed(2) : "—";
    }
    function trafficScale(maximum) {
      if (!Number.isFinite(maximum) || maximum <= 1024) return 1024;
      var exponent = Math.floor(Math.log10(maximum));
      var magnitude = Math.pow(10, exponent);
      var normalized = maximum / magnitude;
      var factor = normalized <= 1 ? 1 : normalized <= 2 ? 2 : normalized <= 5 ? 5 : 10;
      return factor * magnitude;
    }
    function drawTrafficChart() {
      if (!trafficCanvas) return;
      var width = trafficCanvas.clientWidth;
      var height = trafficCanvas.clientHeight;
      if (width < 1 || height < 1) return;
      var ratio = Math.min(window.devicePixelRatio || 1, 2);
      trafficCanvas.width = Math.round(width * ratio);
      trafficCanvas.height = Math.round(height * ratio);
      var context = trafficCanvas.getContext("2d");
      if (!context) return;
      context.scale(ratio, ratio);
      context.clearRect(0, 0, width, height);

      var padding = { top: 16, right: 12, bottom: 27, left: 62 };
      var chartWidth = Math.max(1, width - padding.left - padding.right);
      var chartHeight = Math.max(1, height - padding.top - padding.bottom);
      var maximum = 0;
      trafficHistory.forEach(function (point) {
        maximum = Math.max(maximum, point.rx, point.tx);
      });
      var scale = trafficScale(maximum * 1.1);

      context.font = "11px -apple-system, sans-serif";
      context.textBaseline = "middle";
      context.strokeStyle = "#2a3550";
      context.fillStyle = "#8894ac";
      context.lineWidth = 1;
      for (var row = 0; row <= 4; row++) {
        var y = padding.top + chartHeight * row / 4;
        context.beginPath();
        context.moveTo(padding.left, y);
        context.lineTo(width - padding.right, y);
        context.stroke();
        var label = formatRate(scale * (4 - row) / 4);
        context.textAlign = "right";
        context.fillText(label, padding.left - 8, y);
      }
      for (var column = 0; column <= 4; column++) {
        var x = padding.left + chartWidth * column / 4;
        context.beginPath();
        context.moveTo(x, padding.top);
        context.lineTo(x, height - padding.bottom);
        context.stroke();
      }
      context.textBaseline = "alphabetic";
      context.textAlign = "left";
      context.fillText("2 分钟前", padding.left, height - 7);
      context.textAlign = "right";
      context.fillText("现在", width - padding.right, height - 7);

      function plot(key, color) {
        if (!trafficHistory.length) return;
        context.beginPath();
        trafficHistory.forEach(function (point, index) {
          var offset = trafficHistoryLimit - trafficHistory.length + index;
          var x = padding.left + chartWidth * offset / (trafficHistoryLimit - 1);
          var y = padding.top + chartHeight * (1 - Math.min(scale, point[key]) / scale);
          if (index === 0) context.moveTo(x, y);
          else context.lineTo(x, y);
        });
        context.strokeStyle = color;
        context.lineWidth = 2;
        context.lineJoin = "round";
        context.lineCap = "round";
        context.stroke();
      }
      plot("rx", "#4f8cff");
      plot("tx", "#45c98f");
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
      if (stats.load_available) {
        setText("metric-load", formatLoad(stats.load_1));
        setText("metric-load-1", formatLoad(stats.load_1));
        setText("metric-load-5", formatLoad(stats.load_5));
        setText("metric-load-15", formatLoad(stats.load_15));
        setText("metric-load-note", "1 / 5 / 15 分钟平均负载");
      } else {
        unavailable("metric-load-note", false);
      }
      if (stats.disk_available) {
        setText("metric-disk", percent(stats.disk_percent));
        setText("metric-disk-note", "已用 " + formatBytes(stats.disk_used) + "，可用 " + formatBytes(stats.disk_free));
        setBar("metric-disk-bar", stats.disk_percent);
      } else {
        unavailable("metric-disk-note", false);
      }
      if (stats.network_available) {
        setText("metric-net-rx", formatRate(stats.network_rx_bps));
        setText("metric-net-tx", formatRate(stats.network_tx_bps));
        setText("metric-net-total", "累计接收 " + formatBytes(stats.network_rx_total) + " · 发送 " + formatBytes(stats.network_tx_total));
        setText("metric-network-note", firstSystemSample ? "首次采样完成，下一次开始计算实时带宽" : "不含 loopback 回环流量");
        if (!firstSystemSample) {
          trafficHistory.push({
            rx: Math.max(0, Number(stats.network_rx_bps) || 0),
            tx: Math.max(0, Number(stats.network_tx_bps) || 0)
          });
          if (trafficHistory.length > trafficHistoryLimit) trafficHistory.shift();
          var empty = document.getElementById("traffic-chart-empty");
          if (empty) empty.classList.add("hidden");
          drawTrafficChart();
        }
      } else {
        unavailable("metric-network-note", false);
      }
      var sampled = new Date(stats.sampled_at);
      setText("system-sampled-at", Number.isNaN(sampled.getTime()) ? "刚刚更新" : "更新于 " + sampled.toLocaleTimeString());
      firstSystemSample = false;
    }
    function loadSystemStats() {
      if (systemStatsLoading) return;
      systemStatsLoading = true;
      fetch(systemCards.getAttribute("data-system-stats-url"), {
        credentials: "same-origin",
        cache: "no-store"
      }).then(function (response) {
        if (!response.ok) throw new Error("HTTP " + response.status);
        return response.json();
      }).then(renderSystemStats).catch(function () {
        setText("system-sampled-at", "资源数据暂时不可用");
      }).then(function () {
        systemStatsLoading = false;
        systemStatsTimer = window.setTimeout(loadSystemStats, document.hidden ? 15000 : 3000);
      });
    }
    loadSystemStats();
    document.addEventListener("visibilitychange", function () {
      if (document.hidden || systemStatsLoading) return;
      window.clearTimeout(systemStatsTimer);
      loadSystemStats();
    });
    window.addEventListener("resize", drawTrafficChart);
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
