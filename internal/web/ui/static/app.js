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
      .then(function (r) { return r.text(); })
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
    rawToggle.addEventListener("change", function () {
      document.getElementById("raw-section").classList.toggle("hidden", !rawToggle.checked);
      document.getElementById("structured-section").classList.toggle("hidden", rawToggle.checked);
    });
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
      }).then(function (res) { out.textContent = res.text; })
        .catch(function (e) { out.textContent = "预览失败: " + e; });
    });
  }
})();
