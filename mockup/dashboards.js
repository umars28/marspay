(function () {
  function set(name, html) {
    MPLive.set(name, html);
  }

  function restore(names) {
    names.forEach(MPLive.restore);
  }

  function esc(v) {
    return MP.escape(v);
  }

  function cell(value, cls) {
    return '<td' + (cls ? ' class="' + cls + '"' : '') + '>' + value + '</td>';
  }

  function row(cells) {
    return '<tr>' + cells.join('') + '</tr>';
  }

  function noRows(columns, message) {
    return '<tr><td colspan="' + columns + '" class="muted">' + esc(message) + '</td></tr>';
  }

  function badge(status) {
    var good = ['succeeded', 'settled', 'active', 'delivered', 'approved', 'finished', 'resolved'];
    var bad = ['failed', 'dead_letter', 'rejected', 'blocked'];
    var kind = 'pend';
    if (good.indexOf(status) >= 0) kind = 'ok';
    if (bad.indexOf(status) >= 0) kind = 'bad';
    return '<span class="badge ' + kind + '">' + esc(MPLive.badge(status)) + '</span>';
  }

  function pct(bps) {
    return (bps / 100).toFixed(2).replace(/\.00$/, '') + '%';
  }

  function ms(value) {
    if (value === null || value === undefined) return '—';
    if (value >= 1000) return (value / 1000).toFixed(1) + 's';
    return value + ' ms';
  }

  function short(id) {
    return id && id.length > 18 ? id.slice(0, 18) + '…' : id || '';
  }

  var MERCHANT_SLOTS = [
    'm-overview-stat0', 'm-overview-stat1', 'm-overview-stat2', 'm-overview-stat3',
    'm-overview-rows', 'm-tx-rows', 'm-settlement-rows', 'm-keys-rows',
    'm-outlets-rows', 'm-outlets-rows1', 'm-webhooks-rows',
    'm-instant-stat0', 'm-instant-stat1', 'm-instant-stat2', 'm-instant-latency',
    'm-instant-headline', 'm-holdback-headline', 'm-holdback-rate', 'm-holdback-meter',
    'm-payout-stream',
  ];

  async function loadMerchant() {
    var payments = await MP.request('GET', '/v1/payments?limit=50', { role: 'merchant' });
    if (payments.ok) {
      var t = payments.data.totals;
      set('m-overview-stat0', MP.rupiah(t.gross));
      set('m-overview-stat1', String(t.count));
      set('m-overview-stat2', t.count
        ? (t.succeeded / t.count * 100).toFixed(1) + '%'
        : '—');
      set('m-overview-stat3', MP.rupiah(t.net));

      var rows = payments.data.data.map(function (p) {
        return row([
          cell('<span class="mono">' + esc(short(p.id)) + '</span>'),
          cell(esc(MP.clock(p.created_at))),
          cell(esc(p.method.toUpperCase())),
          cell(esc(p.outlet_id || '—')),
          cell(MP.plain(p.amount), 'r num'),
          cell(MP.plain(p.fee), 'r num'),
          cell(badge(p.status)),
        ]);
      });
      set('m-overview-rows', rows.length ? rows.slice(0, 8).join('') : noRows(7, 'No payments yet'));

      var wide = payments.data.data.map(function (p) {
        return row([
          cell('<span class="mono">' + esc(short(p.id)) + '</span>'),
          cell(esc(MP.clock(p.created_at))),
          cell(esc(p.method.toUpperCase())),
          cell(esc(p.outlet_id || '—')),
          cell(MP.plain(p.amount), 'r num'),
          cell(MP.plain(p.fee), 'r num'),
          cell(MP.plain(p.net), 'r num'),
          cell(badge(p.status)),
          cell(esc(p.ledger_transaction_id ? 'posted' : '—')),
          cell(''),
        ]);
      });
      set('m-tx-rows', wide.length ? wide.join('') : noRows(10, 'No payments yet'));
    }

    var payouts = await MP.request('GET', '/v1/payouts?limit=50', { role: 'merchant' });
    if (payouts.ok) {
      var s = payouts.data.summary;
      set('m-instant-stat0', MP.rupiah(s.net));
      set('m-instant-stat1', MP.rupiah(s.holdback));
      set('m-instant-stat2', String(s.failed));
      set('m-instant-latency', ms(s.median_latency_ms) + ' / ' + ms(s.p95_latency_ms));
      set('m-instant-headline', ms(s.p95_latency_ms) +
        '<span>p95 payment → funds in account</span>');

      var stream = payouts.data.data.slice(0, 8).map(function (p) {
        var slow = p.status === 'sending';
        var landed = p.latency_ms === null || p.latency_ms === undefined
          ? esc(p.status.replace(/_/g, ' '))
          : 'landed in ' + ms(p.latency_ms);
        return '<div class="liverow">' +
          '<span class="pulse' + (slow ? ' slow' : '') + '"></span>' +
          '<span class="who mono">' + esc(short(p.payment_id || p.id)) + '</span>' +
          '<span class="num">' + MP.rupiah(p.gross) + '</span>' +
          '<span class="num muted">− ' + MP.rupiah(p.holdback) + '</span>' +
          '<span class="lat">' + landed + ' · ' +
          badge(p.status) + '</span></div>';
      });
      set('m-payout-stream', stream.length ? stream.join('') :
        '<div class="liverow"><span class="who">No payouts yet</span></div>');
    }

    var config = await MP.request('GET', '/v1/payouts/config', { role: 'merchant' });
    if (config.ok) {
      var bps = config.data.holdback_bps;
      set('m-holdback-headline', pct(bps) + '<span>your current holdback</span>');
      set('m-holdback-rate', pct(bps));
      var span = config.data.holdback_ceiling_bps - config.data.holdback_floor_bps;
      var width = span > 0 ? ((bps - config.data.holdback_floor_bps) / span * 100) : 0;
      set('m-holdback-meter', '<i style="width:' + Math.max(2, Math.min(100, width)) + '%"></i>');
    }

    var settlements = await MP.request('GET', '/v1/settlements?limit=25', { role: 'merchant' });
    if (settlements.ok) {
      var batches = settlements.data.data.map(function (b) {
        return row([
          cell('<span class="mono">' + esc(short(b.id)) + '</span>'),
          cell(esc(MP.clock(b.period_start) + ' → ' + MP.clock(b.period_end))),
          cell(String(b.payout_count), 'r num'),
          cell(MP.plain(b.gross), 'r num'),
          cell(MP.plain(b.fee), 'r num'),
          cell(MP.plain(b.net), 'r num'),
          cell(badge(b.status)),
        ]);
      });
      set('m-settlement-rows', batches.length ? batches.join('') :
        noRows(7, 'No settlement batches; every payout went instant'));
    }

    var keys = await MP.request('GET', '/v1/api-keys', { role: 'merchant' });
    if (keys.ok) {
      var keyRows = keys.data.data.map(function (k) {
        return row([
          cell(esc(k.name)),
          cell('<span class="mono">' + esc(k.prefix) + '</span>'),
          cell(esc(k.mode)),
          cell(esc(MP.clock(k.created_at))),
          cell(esc(k.last_used_at ? MP.clock(k.last_used_at) : 'never')),
          cell(esc((k.scopes || []).join(', '))),
          cell(k.revoked_at ? badge('revoked') : badge('active')),
        ]);
      });
      set('m-keys-rows', keyRows.length ? keyRows.join('') : noRows(7, 'No API keys'));
    }

    var outlets = await MP.request('GET', '/v1/outlets', { role: 'merchant' });
    if (outlets.ok) {
      var outletRows = outlets.data.data.map(function (o) {
        return row([
          cell(esc(o.name)),
          cell('<span class="mono">' + esc(o.nmid || '—') + '</span>'),
          cell(String(o.staff_count), 'r num'),
          cell('—', 'r num'),
          cell('—', 'r num'),
          cell('—'),
          cell(badge(o.status)),
        ]);
      });
      set('m-outlets-rows', outletRows.length ? outletRows.join('') :
        noRows(7, 'No outlets yet'));
    }

    var staff = await MP.request('GET', '/v1/staff', { role: 'merchant' });
    if (staff.ok) {
      var staffRows = staff.data.data.map(function (p) {
        return row([
          cell(esc(p.full_name)),
          cell(esc(p.outlet_id || 'all outlets')),
          cell(esc(p.role)),
          cell(esc(p.last_seen_at ? MP.clock(p.last_seen_at) : 'never')),
          cell('—', 'r num'),
          cell(p.revoked_at ? badge('revoked') : badge('active')),
        ]);
      });
      set('m-outlets-rows1', staffRows.length ? staffRows.join('') : noRows(6, 'No staff'));
    }

    var deliveries = await MP.request('GET', '/v1/webhook-deliveries?limit=25', { role: 'merchant' });
    if (deliveries.ok) {
      var list = deliveries.data.data.map(function (d) {
        return row([
          cell('<span class="mono">' + esc(short(d.event_id)) + '</span>'),
          cell(esc(d.event_type)),
          cell(esc(MP.clock(d.created_at))),
          cell(String(d.attempt), 'r num'),
          cell(ms(d.latency_ms), 'r num'),
          cell(esc(d.response_code === null || d.response_code === undefined ? '—' : d.response_code)),
          cell(badge(d.status)),
          cell(''),
        ]);
      });
      set('m-webhooks-rows', list.length ? list.join('') :
        noRows(8, 'No deliveries; nothing has been sent to this endpoint yet'));
    }
  }

  var OPS_SLOTS = [
    'a-search-results', 'a-search-hint', 'a-float-rows', 'a-float-headline',
    'a-float-used', 'a-float-meter', 'a-instant-rows', 'a-instant-rows1',
    'a-instant-stat0', 'a-instant-stat1', 'a-instant-stat2',
    'a-recon-rows', 'a-recon-stat0', 'a-recon-stat1', 'a-recon-stat2', 'a-recon-stat3',
    'a-audit-rows', 'a-txdetail-rows',
    'r-alerts-rows', 'r-rules-rows', 'r-dispute-rows', 'r-blocked-rows', 'r-merchant-rows',
    'r-dispute-stat0', 'r-dispute-stat1', 'r-dispute-stat2', 'r-dispute-stat3',
    'r-merchant-stat0', 'r-merchant-stat1', 'r-merchant-stat2', 'r-merchant-stat3',
  ];

  async function loadOps() {
    var float = await MP.request('GET', '/internal/v1/float', { role: 'operator' });
    if (float.ok) {
      var pos = float.data.position;
      var used = pos.utilisation_bps / 100;
      set('a-float-headline', MP.rupiah(pos.outstanding) +
        '<span>used of a limit of ' + MP.rupiah(pos.limit) + '</span>');
      set('a-float-used', used.toFixed(1) + '% · ' + MP.rupiah(pos.outstanding) +
        ' of ' + MP.rupiah(pos.limit));
      set('a-float-meter', '<i style="width:' + Math.min(100, used).toFixed(1) + '%"></i>');

      var exposure = float.data.top_exposure.map(function (e) {
        return row([
          cell(esc(e.display_name)),
          cell(MP.plain(e.outstanding), 'r num'),
          cell('—', 'r num'),
          cell('—', 'r num'),
          cell(String(e.payouts), 'r num'),
          cell(badge(pos.instant_enabled ? 'active' : 'degraded')),
          cell(''),
        ]);
      });
      set('a-float-rows', exposure.length ? exposure.join('') :
        noRows(7, 'Nothing is outstanding; no instant payout has been sent'));
    }

    var engine = await MP.request('GET', '/internal/v1/payouts/engine', { role: 'operator' });
    if (engine.ok) {
      var e = engine.data;
      set('a-instant-stat0', String(e.instant));
      set('a-instant-stat1', ms(e.p95_latency_ms));
      set('a-instant-stat2', String(e.failed));

      var rails = e.rails.map(function (r) {
        var share = e.instant ? (r.sent / e.instant * 100).toFixed(0) + '%' : '—';
        return row([
          cell(esc(r.rail)),
          cell(share, 'r num'),
          cell(ms(r.p95_latency_ms), 'r num'),
          cell(r.sent ? (r.settled / r.sent * 100).toFixed(1) + '%' : '—', 'r num'),
          cell(badge(r.failed > 0 ? 'degraded' : 'active')),
        ]);
      });
      set('a-instant-rows', rails.length ? rails.join('') :
        noRows(5, 'No payout has chosen a rail yet'));

      var stuck = e.needing_attention.map(function (p) {
        return row([
          cell('<span class="mono">' + esc(short(p.id)) + '</span>'),
          cell(esc(p.merchant_id)),
          cell(MP.plain(p.net), 'r num'),
          cell(esc(p.failure_reason || p.status.replace(/_/g, ' '))),
          cell('1', 'r num'),
        ]);
      });
      set('a-instant-rows1', stuck.length ? stuck.join('') :
        noRows(5, 'Nothing is stuck'));
    }

    var recon = await MP.request('GET', '/internal/v1/reconciliation?limit=25', { role: 'operator' });
    if (recon.ok) {
      var latest = recon.data.runs[0];
      if (latest) {
        set('a-recon-stat0', String(latest.rows_compared));
        set('a-recon-stat1', String(latest.rows_matched));
        set('a-recon-stat2', String(latest.discrepancies));
        set('a-recon-stat3', MP.rupiah(latest.delta));
      }

      var open = recon.data.open.map(function (d) {
        return row([
          cell('<span class="mono">' + esc(d.external_ref) + '</span>'),
          cell(esc(d.run_id)),
          cell(d.internal === null || d.internal === undefined ? '—' : MP.plain(d.internal), 'r num'),
          cell(d.provider === null || d.provider === undefined ? '—' : MP.plain(d.provider), 'r num'),
          cell(MP.plain(d.delta), 'r num'),
          cell(esc(d.suspected_cause || 'unknown')),
          cell(''),
        ]);
      });
      set('a-recon-rows', open.length ? open.join('') :
        noRows(7, 'Every row matched; nothing is open'));
    }

    var audit = await MP.request('GET', '/internal/v1/audit?limit=40', { role: 'operator' });
    if (audit.ok) {
      var entries = audit.data.data.map(function (a) {
        return row([
          cell(esc(MP.clock(a.created_at))),
          cell(esc(a.actor)),
          cell(esc(a.action)),
          cell('<span class="mono">' + esc(a.object_type + ' ' + short(a.object_id)) + '</span>'),
          cell(esc(a.before || a.after ? 'recorded' : '—')),
          cell(esc(a.reason || '—')),
          cell(esc(a.ip || '—')),
        ]);
      });
      set('a-audit-rows', entries.length ? entries.join('') :
        noRows(7, 'The audit log is empty'));
    }

    var alerts = await MP.request('GET', '/internal/v1/velocity/alerts?limit=40', { role: 'operator' });
    if (alerts.ok) {
      var rows = alerts.data.data.map(function (a) {
        return row([
          cell(esc(MP.clock(a.created_at))),
          cell(esc(a.subject_name || a.subject_id)),
          cell('<span class="mono">' + esc(a.rule_code) + '</span>'),
          cell(esc(a.detail)),
          cell(a.observed === null || a.observed === undefined ? '—' : MP.plain(a.observed), 'r num'),
          cell(badge(a.severity === 'high' ? 'failed' : 'pending')),
          cell(esc(a.auto_action || 'none')),
          cell(''),
        ]);
      });
      set('r-alerts-rows', rows.length ? rows.join('') :
        noRows(8, 'No rule has tripped'));
    }

    var rules = await MP.request('GET', '/internal/v1/velocity/rules', { role: 'operator' });
    if (rules.ok) {
      var ruleRows = rules.data.data.map(function (r) {
        var window = r.window_seconds >= 3600
          ? (r.window_seconds / 3600) + ' h'
          : (r.window_seconds / 60) + ' min';
        return row([
          cell('<span class="mono">' + esc(r.code) + '</span>'),
          cell(esc(r.description)),
          cell(esc(window), 'r num'),
          cell(String(r.threshold), 'r num'),
          cell(esc(r.action.replace(/_/g, ' '))),
          cell(String(r.trips_7d), 'r num'),
          cell('—', 'r num'),
          cell(badge(r.mode === 'active' ? 'active' : 'pending')),
        ]);
      });
      set('r-rules-rows', ruleRows.length ? ruleRows.join('') : noRows(8, 'No rules loaded'));
    }

    var disputes = await MP.request('GET', '/internal/v1/disputes', { role: 'operator' });
    if (disputes.ok) {
      var open = disputes.data.data;
      set('r-dispute-stat0', String(open.length));
      set('r-dispute-stat1', String(open.filter(function (d) { return d.overdue; }).length));
      set('r-dispute-stat2', MP.rupiah(open.reduce(function (a, d) { return a + d.amount; }, 0)));
      set('r-dispute-stat3', MP.rupiah(open.reduce(function (a, d) { return a + d.platform_loss; }, 0)));

      var disputeRows = open.map(function (d) {
        return row([
          cell('<span class="mono">' + esc(short(d.id)) + '</span>'),
          cell('<span class="mono">' + esc(short(d.payment_id)) + '</span>'),
          cell(esc(d.user_id)),
          cell(esc(d.merchant_id)),
          cell(MP.plain(d.amount), 'r num'),
          cell(esc(d.reason)),
          cell(d.covered_by_holdback >= d.amount ? 'fully' :
            (d.covered_by_holdback > 0 ? 'partly' : 'no')),
          cell(badge(d.status)),
          cell(''),
        ]);
      });
      set('r-dispute-rows', disputeRows.length ? disputeRows.join('') :
        noRows(9, 'No disputes are open'));
    }

    var blocks = await MP.request('GET', '/internal/v1/blocks', { role: 'operator' });
    if (blocks.ok) {
      var blockRows = blocks.data.data.map(function (b) {
        return row([
          cell(esc(b.subject_type + ' ' + b.subject_id)),
          cell(esc(MP.clock(b.created_at))),
          cell(esc(b.blocked_by)),
          cell(esc(b.reason)),
          cell(MP.plain(b.balance_held), 'r num'),
          cell(esc(b.appeal_status || '—')),
          cell(''),
        ]);
      });
      set('r-blocked-rows', blockRows.length ? blockRows.join('') :
        noRows(7, 'Nothing is blocked'));
    }

    var score = await MP.request('GET', '/internal/v1/merchants/merch_demo/score', { role: 'operator' });
    if (score.ok && score.data.current) {
      var c = score.data.current;
      set('r-merchant-stat0', String(c.score));
      set('r-merchant-stat1', pct(c.holdback_bps));
      set('r-merchant-stat2', esc(c.mode));
      set('r-merchant-stat3', String((score.data.history || []).length));

      set('r-merchant-rows', row([
        cell(esc(c.merchant_id)),
        cell(String(c.score), 'r num'),
        cell('—', 'r num'),
        cell(esc(String(c.components && c.components.refund_rate)), 'r num'),
        cell(esc(String(c.components && c.components.dispute_rate)), 'r num'),
        cell(pct(c.holdback_bps), 'r num'),
        cell(esc(c.mode)),
        cell(''),
      ]));
    }
  }

  async function search() {
    var query = document.querySelector('#ops-search').value;
    var result = await MP.request('GET',
      '/internal/v1/search?q=' + encodeURIComponent(query), { role: 'operator' });

    if (!result.ok) {
      set('a-search-hint', esc(MPLive.failure(result)));
      return;
    }

    var hits = result.data.data;
    set('a-search-hint', hits.length + (hits.length === 1 ? ' result' : ' results'));

    var items = hits.map(function (h) {
      var icon = h.kind === 'user' ? 'i-user' : h.kind === 'merchant' ? 'i-store' : 'i-card';
      var amount = h.amount === null || h.amount === undefined ? '' : ' · ' + MP.rupiah(h.amount);
      return '<div class="qitem">' +
        '<div class="av"><svg width="18" height="18"><use href="#' + icon + '"/></svg></div>' +
        '<div><div class="t mono">' + esc(h.id) + amount + '</div>' +
        '<div class="s">' + esc(h.kind + ' · ' + h.label + (h.detail ? ' · ' + h.detail : '')) +
        '</div></div>' +
        '<div class="ops">' + badge(h.status) +
        (h.kind === 'payment'
          ? '<button class="btn sm" data-ledger="' + esc(h.id) + '">Ledger</button>'
          : '') +
        '</div></div>';
    });
    set('a-search-results', items.length ? items.join('') :
      '<div class="qitem"><div><div class="t">Nothing matched</div>' +
      '<div class="s">Try a payment id, a phone number or a merchant id</div></div></div>');
  }

  async function openLedger(paymentID) {
    var result = await MP.request('GET',
      '/internal/v1/payments/' + encodeURIComponent(paymentID) + '/ledger', { role: 'operator' });
    if (!result.ok) return;

    var entries = result.data.entries.map(function (e) {
      return row([
        cell('<span class="mono">' + esc(short(e.id)) + '</span>'),
        cell('<span class="mono">' + esc(e.account_id) + '</span>'),
        cell(esc(e.owner_type)),
        cell(e.amount < 0 ? MP.plain(-e.amount) : '', 'r num'),
        cell(e.amount > 0 ? MP.plain(e.amount) : '', 'r num'),
        cell(esc(MP.clock(e.created_at))),
      ]);
    });
    entries.push(row([
      cell('<strong>Sum</strong>'),
      cell(''), cell(''), cell(''),
      cell('<strong>' + MP.plain(result.data.sum) + '</strong>', 'r num'),
      cell(result.data.balanced ? badge('succeeded') : badge('failed')),
    ]));
    set('a-txdetail-rows', entries.join(''));

    var admin = document.querySelector('#role-admin');
    if (admin) {
      admin.querySelectorAll('.screen').forEach(function (s) {
        s.classList.toggle('active', s.dataset.screen === 'a-txdetail');
      });
    }
  }

  document.addEventListener('click', function (e) {
    var go = e.target.closest('#ops-search-go');
    if (go && MPLive.isLive('operator')) {
      e.preventDefault();
      search();
      return;
    }

    var ledger = e.target.closest('[data-ledger]');
    if (ledger && MPLive.isLive('operator')) {
      e.preventDefault();
      openLedger(ledger.dataset.ledger);
    }
  }, true);

  window.MPMerchant = {
    load: loadMerchant,
    reset: function () { restore(MERCHANT_SLOTS); },
  };

  window.MPOps = {
    load: loadOps,
    reset: function () { restore(OPS_SLOTS); },
  };
})();
