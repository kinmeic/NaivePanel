(function () {
  "use strict";
  var root = document.getElementById("agent-chat"), input = document.getElementById("agent-chat-input");
  if (!root || !input) return;
  function resize() {
    var style = window.getComputedStyle(input);
    var line = parseFloat(style.lineHeight) || parseFloat(style.fontSize) * 1.6;
    var padding = (parseFloat(style.paddingTop) || 0) + (parseFloat(style.paddingBottom) || 0);
    var border = (parseFloat(style.borderTopWidth) || 0) + (parseFloat(style.borderBottomWidth) || 0);
    var minimum = line + padding + border, maximum = line * 5 + padding + border;
    input.style.height = "auto";
    var height = input.value ? Math.max(minimum, input.scrollHeight + border) : minimum;
    input.style.height = Math.min(maximum, height) + "px";
    input.style.overflowY = height > maximum ? "auto" : "hidden";
  }
  function afterAction() { queueMicrotask(resize); }
  input.addEventListener("input", resize);
  root.addEventListener("submit", afterAction);
  root.addEventListener("click", afterAction);
  window.addEventListener("resize", resize);
  resize();
})();
