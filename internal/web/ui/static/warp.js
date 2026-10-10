(function () {
  var page = document.querySelector('[data-warp-page]');
  if (!page) return;
  var progress = document.getElementById('warp-operation-progress');
  var pending = false;
  var controls = [];
  var deadline;
  var returnURL = page.dataset.returnUrl;

  function show(state, message, done) {
    progress.classList.remove('hidden', 'operation-success', 'operation-failed');
    if (state === 'succeeded') progress.classList.add('operation-success');
    if (state === 'failed') progress.classList.add('operation-failed');
    progress.querySelector('[data-operation-title]').textContent = done ?
      (state === 'succeeded' ? 'WARP 操作完成' : 'WARP 操作失败') : '正在处理 WARP 操作';
    progress.querySelector('[data-operation-message]').textContent = message;
    progress.querySelector('[data-operation-bar]').style.width = done ? '100%' : state === 'queued' ? '25%' : '65%';
    progress.querySelector('.spinner').classList.toggle('hidden', !!done);
  }
  function lock() {
    pending = true;
    deadline = Date.now() + 12 * 60 * 1000;
    controls = Array.from(page.querySelectorAll('button, input:not([type="hidden"])')).map(function (el) {
      var control = { el: el, disabled: el.disabled };
      el.disabled = true;
      return control;
    });
  }
  function unlock() {
    pending = false;
    controls.forEach(function (control) { control.el.disabled = control.disabled; });
  }
  function readJSON(response) {
    if (response.redirected) throw new Error('登录已过期，请重新登录');
    return response.json().catch(function () {
      throw new Error('请求失败（HTTP ' + response.status + '），请刷新页面后重试');
    }).then(function (body) {
      if (!response.ok) throw new Error(body.message || 'HTTP ' + response.status);
      return body;
    });
  }
  function poll(url) {
    fetch(url, { credentials: 'same-origin', cache: 'no-store', headers: { Accept: 'application/json' } })
      .then(readJSON).then(function (op) {
        show(op.state, op.message, op.done);
        if (op.done) {
          if (op.success) setTimeout(function () { window.location.replace(returnURL); }, 900);
          else unlock();
          return;
        }
        if (Date.now() >= deadline) {
          show('failed', '等待超时；操作可能仍在执行，请刷新状态查看结果。', true);
          unlock();
          return;
        }
        setTimeout(function () { poll(url); }, 1000);
      }).catch(function (error) {
        if (Date.now() >= deadline) {
          show('failed', '读取操作进度失败：' + error.message + '。请刷新状态。', true);
          unlock();
        } else {
          show('running', '暂时无法读取进度，正在重试…', false);
          setTimeout(function () { poll(url); }, 2000);
        }
      });
  }
  page.querySelectorAll('[data-warp-form]').forEach(function (form) {
    form.addEventListener('submit', function (event) {
      event.preventDefault();
      if (pending) return;
      var body = new FormData(form);
      if (event.submitter && event.submitter.name) body.set(event.submitter.name, event.submitter.value);
      var key = form.querySelector('input[name="license"]');
      if (key) key.value = '';
      lock();
      show('queued', '正在提交请求…', false);
      fetch(form.action, {
        method: 'POST', body: body, credentials: 'same-origin',
        headers: { Accept: 'application/json', 'X-NaivePanel-Async': '1' }
      }).then(readJSON).then(function (op) {
        show(op.state, op.message, op.done);
        poll(op.status_url);
      }).catch(function (error) {
        show('failed', error.message + '。请刷新状态确认结果。', true);
        unlock();
      });
    });
  });
  var egressForm = page.querySelector('[data-warp-egress-form]');
  if (egressForm) {
    var result = document.getElementById('warp-egress-result');
    egressForm.addEventListener('submit', function (event) {
      event.preventDefault();
      if (pending) return;
      var body = new FormData(egressForm);
      lock();
      result.classList.remove('hidden');
      var title = result.querySelector('[data-egress-title]');
      var summary = result.querySelector('[data-egress-summary]');
      var details = result.querySelector('[data-egress-details]');
      var raw = result.querySelector('[data-egress-raw]');
      title.className = 'muted';
      title.textContent = '正在验证出口…';
      summary.textContent = '通过客户端当前 SOCKS5 端口请求 Cloudflare trace，请稍候。';
      details.classList.add('hidden');
      raw.textContent = '';
      fetch(egressForm.action, {
        method: 'POST', body: body, credentials: 'same-origin',
        headers: { Accept: 'application/json' }
      }).then(readJSON).then(function (data) {
        title.className = data.verified ? 'ok' : 'warn';
        title.textContent = data.verified ? '出口验证通过' : '请求可达，但未经过 WARP';
        summary.textContent = '出口 IP：' + data.ip + ' · 地区：' + (data.location || '未知') +
          ' · 节点：' + (data.colo || '未知') + ' · WARP：' + data.warp + ' · 代理：' + data.proxy;
        raw.textContent = data.raw;
        details.classList.remove('hidden');
      }).catch(function (error) {
        title.className = 'bad';
        title.textContent = '出口验证失败';
        summary.textContent = error.message;
      }).finally(unlock);
    });
  }
  if (page.dataset.operationUrl) {
    lock();
    show('queued', '正在读取 WARP 操作进度…', false);
    poll(page.dataset.operationUrl);
  } else if (page.hasAttribute('data-connection-pending')) {
    setTimeout(function () {
      // Do not interrupt a settings/license edit or an operation.
      var edited = Array.from(page.querySelectorAll('input')).some(function (el) { return el.value !== el.defaultValue; });
      if (!pending && !edited) window.location.replace(returnURL);
    }, 3000);
  }
})();
