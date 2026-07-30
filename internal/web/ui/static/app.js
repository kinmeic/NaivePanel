// NaivePanel UI helpers.
(function () {
  // Render Lucide icons (<i data-lucide="name">).
  if (window.lucide && window.lucide.createIcons) {
    window.lucide.createIcons();
  }

  // Confirm dialogs.
  document.querySelectorAll("[data-confirm]").forEach(function (el) {
    el.addEventListener("submit", function (e) {
      if (!confirm(el.getAttribute("data-confirm"))) e.preventDefault();
    });
    if (el.tagName === "BUTTON") {
      el.addEventListener("click", function (e) {
        if (!confirm(el.getAttribute("data-confirm"))) e.preventDefault();
      });
    }
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
        '<button type="button" class="eb-del danger"><i data-lucide="trash-2"></i>删除</button>';
      box.appendChild(row);
      bindDel(row.querySelector(".eb-del"));
      if (window.lucide && window.lucide.createIcons) {
        window.lucide.createIcons();
      }
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
