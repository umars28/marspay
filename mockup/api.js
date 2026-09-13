window.MP = (function () {
  var STORE = 'marspay.ui';

  function load() {
    try {
      return JSON.parse(sessionStorage.getItem(STORE)) || {};
    } catch (e) {
      return {};
    }
  }

  var state = load();

  function save() {
    sessionStorage.setItem(STORE, JSON.stringify(state));
  }

  function baseURL() {
    return state.base || localStorage.getItem(STORE + '.base') || 'http://127.0.0.1:8080';
  }

  function setBaseURL(url) {
    state.base = url.replace(/\/+$/, '');
    localStorage.setItem(STORE + '.base', state.base);
    save();
  }

  function credential(role) {
    if (role === 'merchant') return state.merchantKey || '';
    var s = state[role];
    return s ? s.access_token : '';
  }

  function signedIn(role) {
    return Boolean(credential(role));
  }

  function setSession(role, tokens) {
    state[role] = tokens;
    save();
  }

  function setMerchantKey(key) {
    state.merchantKey = key;
    save();
  }

  function forget(role) {
    if (role === 'merchant') delete state.merchantKey;
    else delete state[role];
    save();
  }

  function idempotencyKey(path) {
    var random = Math.random().toString(36).slice(2, 10);
    return 'ui-' + path.replace(/[^a-z]+/gi, '-') + '-' + Date.now() + '-' + random;
  }

  async function send(method, path, options) {
    options = options || {};
    var headers = { Accept: 'application/json' };
    var token = options.role ? credential(options.role) : '';

    if (token) headers.Authorization = 'Bearer ' + token;
    if (options.body !== undefined) headers['Content-Type'] = 'application/json';
    if (method !== 'GET') headers['Idempotency-Key'] = options.idem || idempotencyKey(path);

    var resp;
    try {
      resp = await fetch(baseURL() + path, {
        method: method,
        headers: headers,
        body: options.body === undefined ? undefined : JSON.stringify(options.body),
      });
    } catch (e) {
      return { ok: false, status: 0, offline: true, message: 'The API is not reachable at ' + baseURL() + '.' };
    }

    var data = null;
    if (resp.status !== 204) {
      var text = await resp.text();
      if (text) {
        try {
          data = JSON.parse(text);
        } catch (e) {
          data = { raw: text };
        }
      }
    }

    if (resp.ok) return { ok: true, status: resp.status, data: data };

    var err = (data && data.error) || {};
    return {
      ok: false,
      status: resp.status,
      type: err.type || 'error',
      message: err.message || 'Request failed with status ' + resp.status + '.',
      requestId: err.request_id || resp.headers.get('X-Request-Id') || '',
      retryAfter: resp.headers.get('Retry-After') || '',
      details: err.details || null,
    };
  }

  async function request(method, path, options) {
    options = options || {};
    var first = await send(method, path, options);

    var refreshable = options.role && options.role !== 'merchant';
    if (first.ok || first.status !== 401 || !refreshable) return first;

    var session = state[options.role];
    if (!session || !session.refresh_token) return first;

    var rotated = await send('POST', '/v1/auth/refresh', {
      body: { refresh_token: session.refresh_token },
    });
    if (!rotated.ok) {
      forget(options.role);
      return first;
    }

    setSession(options.role, rotated.data);
    return send(method, path, options);
  }

  function rupiah(minor) {
    if (minor === null || minor === undefined) return '—';
    var negative = minor < 0;
    var whole = Math.abs(Math.trunc(minor / 100));
    var grouped = String(whole).replace(/\B(?=(\d{3})+(?!\d))/g, ',');
    return (negative ? '−' : '') + 'Rp ' + grouped;
  }

  function plain(minor) {
    if (minor === null || minor === undefined) return '—';
    var negative = minor < 0;
    var whole = Math.abs(Math.trunc(minor / 100));
    return (negative ? '−' : '') + String(whole).replace(/\B(?=(\d{3})+(?!\d))/g, ',');
  }

  function toMinor(input) {
    var digits = String(input).replace(/[^\d]/g, '');
    if (!digits) return 0;
    return parseInt(digits, 10) * 100;
  }

  function clock(iso) {
    if (!iso) return '';
    var d = new Date(iso);
    if (isNaN(d)) return '';
    var today = new Date();
    var sameDay = d.toDateString() === today.toDateString();
    var time = String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0');
    if (sameDay) return time;
    return d.toLocaleDateString('en-GB', { day: '2-digit', month: 'short' }) + ' · ' + time;
  }

  function escape(value) {
    return String(value === null || value === undefined ? '' : value)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  return {
    baseURL: baseURL,
    setBaseURL: setBaseURL,
    request: request,
    signedIn: signedIn,
    credential: credential,
    setSession: setSession,
    setMerchantKey: setMerchantKey,
    forget: forget,
    rupiah: rupiah,
    plain: plain,
    toMinor: toMinor,
    clock: clock,
    escape: escape,
  };
})();
