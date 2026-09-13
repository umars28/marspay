(function () {
  var live = { user: false, operator: false, merchant: false };
  var sample = {};

  function $(sel, root) {
    return (root || document).querySelector(sel);
  }

  function slot(name) {
    return document.querySelector('[data-live="' + name + '"]');
  }

  function remember(name) {
    var el = slot(name);
    if (el && !(name in sample)) sample[name] = el.innerHTML;
    return el;
  }

  function set(name, html) {
    var el = remember(name);
    if (el) el.innerHTML = html;
  }

  function restore(name) {
    var el = slot(name);
    if (el && name in sample) el.innerHTML = sample[name];
  }

  function note(name, kind, message) {
    var el = slot(name);
    if (!el) return;
    el.hidden = false;
    el.className = 'note ' + kind + ' mt4';
    el.innerHTML = '<div>' + MP.escape(message) + '</div>';
  }

  function clearNote(name) {
    var el = slot(name);
    if (el) el.hidden = true;
  }

  function failure(result) {
    if (result.offline) return result.message;
    var text = result.message;
    if (result.requestId) text += ' (' + result.requestId + ')';
    return text;
  }

  function badge(status) {
    var map = {
      succeeded: 'Succeeded',
      pending: 'Pending',
      failed: 'Failed',
      sending: 'Sending',
      settled: 'Settled',
      degraded_to_batch: 'Batch',
    };
    return map[status] || status || '';
  }

  function icon(kind, direction) {
    if (kind === 'payment') return 'i-store';
    if (kind === 'transfer') return 'i-send';
    if (kind === 'topup') return 'i-in';
    if (kind === 'withdrawal') return 'i-out';
    if (kind === 'bill_payment') return 'i-file';
    if (kind === 'refund') return 'i-in';
    return direction > 0 ? 'i-in' : 'i-out';
  }

  function txRow(tx) {
    var incoming = tx.direction === 'in';
    var title = tx.counterpart || (tx.kind || 'movement').replace(/_/g, ' ');
    var when = MP.clock(tx.created_at);

    return '<div class="txrow">' +
      '<div class="ic ' + (incoming ? 'in' : 'out') + '"><svg><use href="#' +
      icon(tx.kind, incoming ? 1 : -1) + '"/></svg></div>' +
      '<div><div class="t">' + MP.escape(title) + '</div>' +
      '<div class="s">' + MP.escape((tx.kind || '').replace(/_/g, ' ')) +
      (when ? ' · ' + MP.escape(when) : '') + '</div></div>' +
      '<div class="amt' + (incoming ? ' in' : '') + '">' +
      (incoming ? '+' : '−') + MP.plain(Math.abs(tx.amount)) +
      '<span class="st">' + MP.escape(badge(tx.status)) + '</span></div></div>';
  }

  function empty(message) {
    return '<div class="txrow"><div><div class="t">' + MP.escape(message) +
      '</div><div class="s">Nothing to show yet</div></div></div>';
  }

  async function loadConsumer() {
    if (!live.user) return;

    var balance = await MP.request('GET', '/v1/balance', { role: 'user' });
    if (balance.ok) {
      set('balance', MP.rupiah(balance.data.available));
      set('held', 'On hold ' + MP.rupiah(balance.data.held));
      if (balance.data.cache_agreed === false) {
        set('tier', 'Cache disagrees with the ledger');
      }
    }

    var me = await MP.request('GET', '/v1/me', { role: 'user' });
    if (me.ok) {
      var name = me.data.name || '';
      set('me-name', MP.escape(name));
      set('me-phone', MP.escape(me.data.phone || ''));
      set('me-initials', MP.escape(name.split(/\s+/).map(function (w) {
        return w.charAt(0);
      }).join('').slice(0, 2).toUpperCase()));
      var tier = me.data.kyc_tier || '';
      set('tier', MP.escape(tier.charAt(0).toUpperCase() + tier.slice(1) + ' tier'));
      set('me-tier', MP.escape(tier));
      set('me-tier-long', MP.escape(tier));
      if (me.data.limits) {
        set('me-max-balance', MP.rupiah(me.data.limits.max_balance));
        set('me-max-month', MP.rupiah(me.data.limits.max_monthly));
        set('me-used', MP.rupiah(me.data.limits.used_this_month));
      }
    }

    var history = await MP.request('GET', '/v1/transactions?limit=25', { role: 'user' });
    if (history.ok) {
      var rows = (history.data.data || []).map(txRow);
      set('history', rows.length ? rows.join('') : empty('No transactions'));
      set('recent', rows.length ? rows.slice(0, 4).join('') : empty('No transactions'));
    }

    var points = await MP.request('GET', '/v1/points', { role: 'user' });
    if (points.ok && points.data.balance) {
      var wallet = points.data.balance;
      set('points', MP.escape(String(wallet.points || 0)));
      set('points-note', wallet.expiring_soon
        ? MP.escape(wallet.expiring_soon + ' points expiring soon')
        : MP.escape(wallet.rate || 'Live points balance'));
    }

    await loadWallet();

    var devices = await MP.request('GET', '/v1/devices', { role: 'user' });
    if (devices.ok) {
      var list = (devices.data.data || []).map(function (d) {
        var label = d.model || d.platform;
        var when = d.revoked_at ? 'Revoked' : 'Last seen ' + MP.clock(d.last_seen_at);
        return '<div class="txrow">' +
          '<div class="ic"><svg><use href="#i-activity"/></svg></div>' +
          '<div><div class="t">' + MP.escape(label) +
          (d.current ? ' · this device' : '') + '</div>' +
          '<div class="s">' + MP.escape(when) + '</div></div>' +
          '<div class="amt">' + (d.revoked_at || d.current ? '' :
            '<button class="btn sm" data-revoke-device="' + MP.escape(d.id) + '">Revoke</button>') +
          '</div></div>';
      });
      set('devices', list.length ? list.join('') : empty('No devices'));
    }
  }

  var billers = [];
  var quotedBill = null;

  async function loadWallet() {
    var list = await MP.request('GET', '/v1/billers', { role: 'user' });
    if (list.ok) {
      billers = list.data.data || list.data || [];
      var rows = billers.map(function (b) {
        return '<div class="txrow" data-goto="billform" data-biller="' + MP.escape(b.code) + '">' +
          '<div class="ic out"><svg><use href="#i-file"/></svg></div>' +
          '<div><div class="t">' + MP.escape(b.name) + '</div>' +
          '<div class="s">' + MP.escape(b.category) +
          (b.prepaid ? ' · prepaid' : ' · postpaid') + '</div></div>' +
          '<div class="amt"><span class="st">fee ' + MP.rupiah(b.admin_fee) + '</span></div></div>';
      });
      set('bills-list', rows.length ? rows.join('') : empty('No billers'));
      set('bill-codes', billers.map(function (b) {
        return '<option value="' + MP.escape(b.code) + '">' + MP.escape(b.name) + '</option>';
      }).join(''));
    }

    var promos = await MP.request('GET', '/v1/promos', { role: 'user' });
    if (promos.ok) {
      var offers = (promos.data.data || []).map(function (p) {
        var headline = p.value_bps ? (p.value_bps / 100) + '%' : MP.rupiah(p.value);
        var detail = [];
        if (p.max_benefit) detail.push('max ' + MP.rupiah(p.max_benefit));
        if (p.min_spend) detail.push('min spend ' + MP.rupiah(p.min_spend));
        detail.push(p.remaining_quota + ' left');
        return '<div class="qitem">' +
          '<div class="av" style="background:var(--brand-soft);color:var(--brand)">' +
          MP.escape(headline) + '</div>' +
          '<div><div class="t">' + MP.escape(p.name) + '</div>' +
          '<div class="s">' + MP.escape(detail.join(' · ')) + '</div></div>' +
          '<button class="btn sm brand" data-promo="' + MP.escape(p.code) + '">Apply</button>' +
          '</div>';
      });
      set('promo-list', offers.length ? offers.join('') :
        '<div class="qitem"><div><div class="t">No offers right now</div></div></div>');
    }

    var points = await MP.request('GET', '/v1/points', { role: 'user' });
    if (points.ok && points.data.balance) {
      var wallet = points.data.balance;
      set('points-total', MP.escape(String(wallet.points || 0)));
      set('points-meta', '<span>' + MP.escape(String(wallet.expiring_soon || 0)) +
        ' expiring soon</span><span>·</span><span>' + MP.escape(wallet.rate || '') + '</span>');

      var history = (points.data.history || []).map(function (e) {
        var positive = e.amount > 0;
        return '<div class="txrow">' +
          '<div class="ic ' + (positive ? 'in' : 'out') + '"><svg><use href="#' +
          (positive ? 'i-in' : 'i-out') + '"/></svg></div>' +
          '<div><div class="t">' + MP.escape(e.kind) + '</div>' +
          '<div class="s">' + MP.escape(e.source || '') + '</div></div>' +
          '<div class="amt' + (positive ? ' in' : '') + '">' +
          (positive ? '+' : '') + e.amount + '</div></div>';
      });
      set('points-history', history.length ? history.join('') : empty('No points yet'));
    }

    var requests = await MP.request('GET', '/v1/money-requests', { role: 'user' });
    if (requests.ok) {
      var incoming = (requests.data.data || []).map(function (r) {
        var initials = (r.requester_name || r.requester_id).slice(0, 2).toUpperCase();
        return '<div class="qitem"><div class="av">' + MP.escape(initials) + '</div>' +
          '<div><div class="t">' + MP.escape(r.requester_name || r.requester_id) +
          ' is requesting</div>' +
          '<div class="s num">' + MP.rupiah(r.amount) +
          (r.note ? ' · ' + MP.escape(r.note) : '') + '</div></div>' +
          '<div class="ops"><button class="btn sm" data-decline="' + MP.escape(r.id) +
          '">Decline</button></div></div>';
      });
      set('requests-incoming', incoming.length ? incoming.join('') :
        '<div class="qitem"><div><div class="t">Nobody is asking you for money</div></div></div>');
    }

    var inbox = await MP.request('GET', '/v1/notifications', { role: 'user' });
    if (inbox.ok) {
      var items = (inbox.data.data || []).map(function (n) {
        return '<div class="txrow">' +
          '<div class="ic' + (n.unread ? ' in' : '') + '"><svg><use href="#i-inbox"/></svg></div>' +
          '<div><div class="t">' + MP.escape(n.title) + '</div>' +
          '<div class="s">' + MP.escape(n.detail || '') + ' · ' +
          MP.escape(MP.clock(n.created_at)) + '</div></div>' +
          '<div class="amt">' + (n.unread ? '<span class="badge info flat">New</span>' : '') +
          '</div></div>';
      });
      set('inbox-list', items.length ? items.join('') : empty('Nothing here yet'));
    }
  }

  async function signIn(role, phone, pin, noteName) {
    var challenge = await MP.request('POST', '/v1/auth/otp', { body: { phone: phone } });
    if (!challenge.ok) return { ok: false, message: failure(challenge) };

    if (!challenge.data.code) {
      return {
        ok: false,
        message: 'The API did not return the code. Start it with MARSPAY_REVEAL_OTP=true, ' +
          'or use ./scripts/demo.sh.',
      };
    }

    var tokens = await MP.request('POST', '/v1/auth/token', {
      body: {
        challenge_id: challenge.data.id,
        code: challenge.data.code,
        pin: pin,
        platform: 'web',
        model: 'Mockup browser',
      },
    });
    if (!tokens.ok) return { ok: false, message: failure(tokens) };

    MP.setSession(role, tokens.data);
    return { ok: true };
  }

  function status() {
    var dot = $('#connect-dot');
    var label = $('#connect-label');
    var connected = live.user || live.operator || live.merchant;

    dot.className = 'statusdot' + (connected ? ' on' : '');
    if (!connected) {
      label.textContent = 'Sample data';
      return;
    }

    var parts = [];
    if (live.user) parts.push('consumer');
    if (live.merchant) parts.push('merchant');
    if (live.operator) parts.push('operator');
    label.textContent = 'Live · ' + parts.join(', ');
  }

  function markRole(role, on) {
    live[role] = on;
    status();
  }

  async function check() {
    var health = await MP.request('GET', '/healthz', {});
    var el = $('#api-note');
    if (health.ok) {
      el.textContent = 'Reachable.';
      el.className = 'cbnote ok';
    } else {
      el.textContent = failure(health);
      el.className = 'cbnote bad';
    }
    return health.ok;
  }

  function wireConnectBar() {
    var bar = $('#connectbar');
    var toggle = $('#connect-toggle');

    toggle.addEventListener('click', function () {
      var open = bar.hidden;
      bar.hidden = !open;
      toggle.setAttribute('aria-expanded', String(open));
    });

    $('#api-base').value = MP.baseURL();
    $('#api-base').addEventListener('change', function (e) {
      MP.setBaseURL(e.target.value);
    });
    $('#api-check').addEventListener('click', check);

    $('#cons-signin').addEventListener('click', async function () {
      var el = $('#cons-note');
      el.textContent = 'Signing in…';
      el.className = 'cbnote';

      var result = await signIn('user', $('#cons-phone').value || '081200000001',
        $('#cons-pin').value || '294715');
      if (!result.ok) {
        el.textContent = result.message;
        el.className = 'cbnote bad';
        return;
      }

      el.textContent = 'Signed in. Consumer screens are live.';
      el.className = 'cbnote ok';
      $('#cons-signout').hidden = false;
      markRole('user', true);
      await loadConsumer();
    });

    $('#cons-signout').addEventListener('click', function () {
      MP.request('POST', '/v1/auth/logout', { role: 'user' });
      MP.forget('user');
      markRole('user', false);
      ['balance', 'held', 'tier', 'recent', 'history', 'points', 'points-note',
        'me-name', 'me-phone', 'me-initials', 'me-tier', 'me-tier-long',
        'me-max-balance', 'me-max-month', 'me-used', 'devices',
        'bills-list', 'bill-codes', 'bill-inquiry', 'promo-list', 'points-total',
        'points-meta', 'points-history', 'requests-incoming', 'inbox-list'].forEach(restore);
      $('#cons-note').textContent = 'Signed out. Showing sample data again.';
      $('#cons-note').className = 'cbnote';
      $('#cons-signout').hidden = true;
    });

    $('#ops-signin').addEventListener('click', async function () {
      var el = $('#ops-note');
      el.textContent = 'Signing in…';
      el.className = 'cbnote';

      var result = await signIn('operator', $('#ops-phone').value || '081200000009',
        $('#ops-pin').value || '294715');
      if (!result.ok) {
        el.textContent = result.message;
        el.className = 'cbnote bad';
        return;
      }

      el.textContent = 'Signed in as an operator.';
      el.className = 'cbnote ok';
      $('#ops-signout').hidden = false;
      markRole('operator', true);
      if (window.MPOps) await window.MPOps.load();
    });

    $('#ops-signout').addEventListener('click', function () {
      MP.request('POST', '/v1/auth/logout', { role: 'operator' });
      MP.forget('operator');
      markRole('operator', false);
      if (window.MPOps) window.MPOps.reset();
      $('#ops-signout').hidden = true;
      $('#ops-note').textContent = 'Signed out.';
      $('#ops-note').className = 'cbnote';
    });

    $('#merch-save').addEventListener('click', async function () {
      var key = $('#merch-key').value.trim();
      var el = $('#merch-note');
      if (!key) {
        el.textContent = 'Paste the API key that ./scripts/demo.sh printed.';
        el.className = 'cbnote bad';
        return;
      }

      MP.setMerchantKey(key);
      var probe = await MP.request('GET', '/v1/api-keys', { role: 'merchant' });
      if (!probe.ok) {
        MP.forget('merchant');
        el.textContent = failure(probe);
        el.className = 'cbnote bad';
        return;
      }

      el.textContent = 'Key accepted. Merchant screens are live.';
      el.className = 'cbnote ok';
      $('#merch-clear').hidden = false;
      markRole('merchant', true);
      if (window.MPMerchant) await window.MPMerchant.load();
    });

    $('#merch-clear').addEventListener('click', function () {
      MP.forget('merchant');
      markRole('merchant', false);
      if (window.MPMerchant) window.MPMerchant.reset();
      $('#merch-clear').hidden = true;
      $('#merch-note').textContent = 'A real dashboard would hold a session, not a raw API key.';
      $('#merch-note').className = 'cbnote';
    });
  }

  async function submit(button, run) {
    if (!button) return;
    button.addEventListener('click', async function (e) {
      if (!live.user) return;
      e.preventDefault();
      e.stopPropagation();
      button.disabled = true;
      try {
        await run();
      } finally {
        button.disabled = false;
      }
    }, true);
  }

  function wireConsumerActions() {
    submit($('#pay-submit'), async function () {
      clearNote('pay-result');
      var result = await MP.request('POST', '/v1/payments', {
        role: 'user',
        body: {
          merchant_id: $('#pay-merchant').value.trim(),
          method: 'qris',
          amount: MP.toMinor($('#pay-amount').value),
          currency: 'IDR',
        },
      });

      if (!result.ok) {
        note('pay-result', 'warn', failure(result));
        return;
      }
      note('pay-result', 'ok', 'Paid ' + MP.rupiah(result.data.amount) + ' · fee ' +
        MP.rupiah(result.data.fee) + ' · ledger ' + result.data.ledger_transaction_id);
      await loadConsumer();
    });

    submit($('#transfer-submit'), async function () {
      clearNote('transfer-result');
      var result = await MP.request('POST', '/v1/transfers', {
        role: 'user',
        body: {
          to: $('#transfer-to').value.trim(),
          amount: MP.toMinor($('#transfer-amount').value),
          currency: 'IDR',
        },
      });

      if (!result.ok) {
        note('transfer-result', 'warn', failure(result));
        return;
      }
      note('transfer-result', 'ok', 'Sent ' + MP.rupiah(result.data.amount) + '.');
      await loadConsumer();
    });

    submit($('#topup-submit'), async function () {
      clearNote('topup-result');
      var result = await MP.request('POST', '/v1/topups', {
        role: 'user',
        body: {
          source: 'bank_va',
          provider_code: 'bca',
          amount: MP.toMinor($('#topup-amount').value),
          currency: 'IDR',
        },
      });

      if (!result.ok) {
        note('topup-result', 'warn', failure(result));
        return;
      }
      note('topup-result', 'info', 'Top up is pending. The balance moves when the provider ' +
        'callback arrives, not now. Virtual account ' +
        (result.data.virtual_account || result.data.id) + '.');
      await loadConsumer();
    });

    submit($('#withdraw-submit'), async function () {
      clearNote('withdraw-result');
      var result = await MP.request('POST', '/v1/withdrawals', {
        role: 'user',
        body: {
          bank_code: 'BCA',
          account_number: '1234567890',
          account_name: 'Demo Consumer',
          amount: MP.toMinor($('#withdraw-amount').value),
          currency: 'IDR',
        },
      });

      if (!result.ok) {
        note('withdraw-result', 'warn', failure(result));
        return;
      }
      note('withdraw-result', 'info', 'Withdrawal is pending at the bank. ' +
        MP.rupiah(result.data.total_debited) + ' debited including ' +
        MP.rupiah(result.data.admin_fee) + ' fee.');
      await loadConsumer();
    });

    submit($('#bill-inquire'), async function () {
      clearNote('bill-result');
      var code = $('#bill-code').value;
      var result = await MP.request('POST', '/v1/billers/' + encodeURIComponent(code) + '/inquire', {
        role: 'user',
        body: { customer_ref: $('#bill-ref').value.trim() },
      });

      if (!result.ok) {
        note('bill-result', 'warn', failure(result));
        return;
      }

      var d = result.data;
      set('bill-inquiry',
        '<dt>Biller</dt><dd>' + MP.escape(d.biller_name) + '</dd>' +
        '<dt>Name</dt><dd>' + MP.escape(d.customer_name) + '</dd>' +
        '<dt>Period</dt><dd>' + MP.escape(d.period || '—') + '</dd>' +
        '<dt>Bill</dt><dd class="num">' + MP.rupiah(d.amount) + '</dd>' +
        '<dt>Admin fee</dt><dd class="num">' + MP.rupiah(d.admin_fee) + '</dd>' +
        '<dt>Total</dt><dd class="num">' + MP.rupiah(d.total_payable) + '</dd>');
      quotedBill = d;
      note('bill-result', 'ok', 'Quoted ' + MP.rupiah(d.total_payable) + '. Press Pay to settle it.');
    });

    submit($('#bill-pay'), async function () {
      clearNote('bill-result');
      if (!quotedBill) {
        note('bill-result', 'warn', 'Check the bill first; the amount comes from the inquiry.');
        return;
      }

      var result = await MP.request('POST', '/v1/bill-payments', {
        role: 'user',
        body: {
          biller_code: quotedBill.biller_code,
          customer_ref: quotedBill.customer_ref,
          amount: quotedBill.amount,
          currency: 'IDR',
        },
      });

      if (!result.ok) {
        note('bill-result', 'warn', failure(result));
        return;
      }
      note('bill-result', result.data.status === 'pending' ? 'info' : 'ok',
        'Bill payment is ' + result.data.status + '.');
      await loadConsumer();
    });

    submit($('#request-submit'), async function () {
      clearNote('request-result');
      var result = await MP.request('POST', '/v1/money-requests', {
        role: 'user',
        body: {
          payer_id: $('#request-from').value.trim(),
          amount: MP.toMinor($('#request-amount').value),
          currency: 'IDR',
          note: $('#request-note').value.trim(),
        },
      });

      if (!result.ok) {
        note('request-result', 'warn', failure(result));
        return;
      }
      note('request-result', 'ok', 'Asked for ' + MP.rupiah(result.data.amount) + '.');
    });

    document.addEventListener('click', async function (e) {
      var promo = e.target.closest('[data-promo]');
      if (promo && live.user) {
        e.preventDefault();
        e.stopPropagation();
        var applied = await MP.request('POST', '/v1/promos/apply', {
          role: 'user',
          body: { code: promo.dataset.promo, spend: 3200000 },
        });
        promo.textContent = applied.ok
          ? 'Applied · ' + MP.rupiah(applied.data.value)
          : 'Refused';
        return;
      }

      var redeem = e.target.closest('[data-redeem]');
      if (redeem && live.user) {
        e.preventDefault();
        e.stopPropagation();
        clearNote('points-result');
        var out = await MP.request('POST', '/v1/points/redeem', {
          role: 'user',
          body: { kind: 'balance', points: Number(redeem.dataset.redeem) },
        });
        if (!out.ok) {
          note('points-result', 'warn', failure(out));
          return;
        }
        note('points-result', 'ok', 'Redeemed ' + out.data.points + ' points for ' +
          MP.rupiah(out.data.value) + '.');
        await loadConsumer();
        return;
      }

      var decline = e.target.closest('[data-decline]');
      if (decline && live.user) {
        e.preventDefault();
        e.stopPropagation();
        await MP.request('POST', '/v1/money-requests/' + decline.dataset.decline + '/decline',
          { role: 'user' });
        await loadWallet();
        return;
      }

      var biller = e.target.closest('[data-biller]');
      if (biller && live.user) {
        var select = $('#bill-code');
        if (select) select.value = biller.dataset.biller;
      }

      var btn = e.target.closest('[data-revoke-device]');
      if (!btn || !live.user) return;
      e.preventDefault();
      e.stopPropagation();
      await MP.request('POST', '/v1/devices/' + btn.dataset.revokeDevice + '/revoke', { role: 'user' });
      await loadConsumer();
    }, true);
  }

  async function boot() {
    wireConnectBar();
    wireConsumerActions();

    if (MP.signedIn('user')) {
      markRole('user', true);
      await loadConsumer();
      $('#cons-signout').hidden = false;
    }
    if (MP.signedIn('merchant')) {
      markRole('merchant', true);
      $('#merch-clear').hidden = false;
      if (window.MPMerchant) await window.MPMerchant.load();
    }
    if (MP.signedIn('operator')) {
      markRole('operator', true);
      $('#ops-signout').hidden = false;
      if (window.MPOps) await window.MPOps.load();
    }
    status();
  }

  window.MPLive = {
    isLive: function (role) { return live[role]; },
    reload: loadConsumer,
    remember: remember,
    set: set,
    restore: restore,
    txRow: txRow,
    empty: empty,
    failure: failure,
    badge: badge,
  };

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
  else boot();
})();
