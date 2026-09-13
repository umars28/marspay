(function () {
  var roleTabs = Array.prototype.slice.call(document.querySelectorAll('.rolebar button'));

  function showRole(name) {
    roleTabs.forEach(function (t) {
      t.setAttribute('aria-selected', String(t.dataset.role === name));
    });
    document.querySelectorAll('.role').forEach(function (r) {
      r.classList.toggle('active', r.id === 'role-' + name);
    });
    window.scrollTo({ top: 0, behavior: 'auto' });
  }

  roleTabs.forEach(function (tab, i) {
    tab.addEventListener('click', function () {
      showRole(tab.dataset.role);
    });
    tab.addEventListener('keydown', function (e) {
      var next = null;
      if (e.key === 'ArrowRight') next = roleTabs[(i + 1) % roleTabs.length];
      if (e.key === 'ArrowLeft') next = roleTabs[(i - 1 + roleTabs.length) % roleTabs.length];
      if (!next) return;
      e.preventDefault();
      next.focus();
      showRole(next.dataset.role);
    });
  });

  function showScreen(root, name) {
    root.querySelectorAll('.screen').forEach(function (s) {
      s.classList.toggle('active', s.dataset.screen === name);
    });
    root.querySelectorAll('.sidebar nav button').forEach(function (b) {
      if (b.dataset.screen === name) b.setAttribute('aria-current', 'page');
      else b.removeAttribute('aria-current');
    });
    var main = root.querySelector('.main');
    if (main) main.scrollIntoView({ block: 'start', behavior: 'auto' });
  }

  function showPhoneScreen(root, name) {
    root.querySelectorAll('.pscreen').forEach(function (s) {
      s.classList.toggle('active', s.dataset.pscreen === name);
    });
    root.querySelectorAll('.tabbar button').forEach(function (b) {
      if (b.dataset.goto === name) b.setAttribute('aria-current', 'page');
      else b.removeAttribute('aria-current');
    });
    var view = root.querySelector('.view');
    if (view) view.scrollTop = 0;
  }

  document.addEventListener('click', function (e) {
    var screenBtn = e.target.closest('[data-screen]');
    if (screenBtn && !screenBtn.classList.contains('screen')) {
      var role = screenBtn.closest('.role');
      if (role) {
        e.preventDefault();
        showScreen(role, screenBtn.dataset.screen);
        return;
      }
    }

    var toggleBtn = e.target.closest('.toggle button');
    if (toggleBtn) {
      toggleBtn.parentElement.querySelectorAll('button').forEach(function (b) {
        b.setAttribute('aria-pressed', String(b === toggleBtn));
      });
      return;
    }

    var gotoEl = e.target.closest('[data-goto]');
    if (gotoEl) {
      var phone = gotoEl.closest('.phone');
      if (phone) {
        e.preventDefault();
        showPhoneScreen(phone, gotoEl.dataset.goto);
      }
    }
  });
})();
