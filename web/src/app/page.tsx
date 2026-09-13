"use client";

import { useCallback, useState } from "react";

import * as api from "@/lib/api";
import { clock, initials, plain, rupiah, toMinor } from "@/lib/format";
import { useSession } from "@/lib/session";
import { useResource } from "@/lib/useResource";
import type {
  Activity,
  Balance,
  Biller,
  Charge,
  Device,
  Inbox,
  Inquiry,
  MoneyRequest,
  Page,
  Points,
  Profile,
  Promo,
  Split,
} from "@/lib/types";
import { Badge, Note } from "@/components/ui";
import { Notes } from "@/components/Notes";
import type { NoteCard } from "@/components/Notes";

type Screen =
  | "home"
  | "pay"
  | "transfer"
  | "topup"
  | "withdraw"
  | "history"
  | "bills"
  | "promo"
  | "points"
  | "split"
  | "inbox"
  | "profile";

const TABS: { key: Screen; label: string; icon: string }[] = [
  { key: "home", label: "Home", icon: "i-home" },
  { key: "history", label: "History", icon: "i-clock" },
  { key: "inbox", label: "Inbox", icon: "i-inbox" },
  { key: "profile", label: "Account", icon: "i-user" },
];

export default function ConsumerPage() {
  const { live } = useSession();
  const [screen, setScreen] = useState<Screen>("home");
  const [version, setVersion] = useState(0);
  const refresh = useCallback(() => setVersion((n) => n + 1), []);

  return (
    <div className="role active">
      <div className="phonewrap">
        <div className="phone">
          <div className="statusbar">
            <span>Marspay</span>
            <span>{live.user ? "Live" : "Sample"}</span>
          </div>
          <div className="view">
            <Screens screen={screen} go={setScreen} live={live.user} version={version} refresh={refresh} />
          </div>
          <div className="tabbar">
            {TABS.map((tab) => (
              <button
                key={tab.key}
                aria-current={screen === tab.key ? "page" : undefined}
                onClick={() => setScreen(tab.key)}
              >
                <svg>
                  <use href={`#${tab.icon}`} />
                </svg>
                {tab.label}
              </button>
            ))}
          </div>
        </div>
        <Notes cards={NOTES[screen] ?? HOME_NOTES} />
      </div>
    </div>
  );
}

const HOME_NOTES: NoteCard[] = [
    {
      icon: "i-book",
      title: "Balance is not a column",
      body: (
        <>
          <p>
            The figure on the card is <code>SUM(amount_minor)</code> over that account&apos;s ledger
            entries, cached in Redis. There is no <code>users.balance</code> to drift.
          </p>
          <p>
            When the cache and the ledger disagree the ledger wins, and the screen says so rather
            than hiding it.
          </p>
        </>
      ),
    },
  {
    icon: "i-lock",
    title: "Held is a third state",
    body: (
      <p>
        Money on hold has left <code>available</code> but not the system — a withdrawal the bank
        has not finished. Two states would have to lie about one of those.
      </p>
    ),
  },
];

const NOTES: Partial<Record<Screen, NoteCard[]>> = {
  home: HOME_NOTES,
  pay: [
    {
      icon: "i-shield",
      title: "The payer never sets the price",
      body: (
        <p>
          With a payment link the amount is resolved from the link server-side. A client that sends
          its own amount against someone else&apos;s invoice is exactly the attack this prevents.
        </p>
      ),
    },
    {
      icon: "i-refresh",
      title: "Retrying is safe",
      body: (
        <p>
          Every money-moving request carries an <code>Idempotency-Key</code>. A replay returns the
          original response byte for byte instead of paying twice.
        </p>
      ),
    },
  ],
  transfer: [
    {
      icon: "i-alert",
      title: "Velocity runs on the hot path",
      body: (
        <p>
          Counters live in Redis so the check costs milliseconds. Ten new recipients inside five
          minutes freezes the account — VR-03, and it is enforced, not merely logged.
        </p>
      ),
    },
  ],
  topup: [
    {
      icon: "i-clock",
      title: "The callback moves the money",
      body: (
        <p>
          Pressing the button records intent. The balance changes when the provider callback
          arrives, which is why a lost callback shows up on the reconciliation screen rather than
          as silently missing money.
        </p>
      ),
    },
  ],
  withdraw: [
    {
      icon: "i-scale",
      title: "Failures reverse, they do not delete",
      body: (
        <p>
          A failed withdrawal posts new opposite entries. Nothing is ever removed from the ledger,
          so the history of what was attempted survives the outcome.
        </p>
      ),
    },
  ],
  inbox: [
    {
      icon: "i-info",
      title: "Derived, not stored",
      body: (
        <p>
          There is no notifications table. This feed is built from the activity, pending money
          requests and expiring points — and the response says so, rather than implying a
          subsystem that does not exist.
        </p>
      ),
    },
  ],
  profile: [
    {
      icon: "i-key",
      title: "A device can be signed out",
      body: (
        <p>
          Revoking a device revokes every session on it. Refresh tokens rotate, and replaying a
          retired one kills the whole chain, because a replay is indistinguishable from theft.
        </p>
      ),
    },
  ],
  split: [
    {
      icon: "i-users",
      title: "A split is not a transaction",
      body: (
        <p>
          It is N independent requests. If two people never pay, the others still stand — which is
          only true because nothing was posted to the ledger when the split was created.
        </p>
      ),
    },
  ],
};

type ScreenProps = {
  screen: Screen;
  go: (screen: Screen) => void;
  live: boolean;
  version: number;
  refresh: () => void;
};

function Screens({ screen, go, live, version, refresh }: ScreenProps) {
  if (!live) {
    return (
      <div className="offline">
        Sign in as a consumer from the panel in the top right. Every number on these screens then
        comes from the ledger, and Pay, Transfer, Top Up and Withdraw move real money through the
        real velocity rules.
      </div>
    );
  }

  switch (screen) {
    case "home":
      return <Home go={go} version={version} />;
    case "pay":
      return <Pay go={go} refresh={refresh} />;
    case "transfer":
      return <Transfer refresh={refresh} />;
    case "topup":
      return <TopUp refresh={refresh} />;
    case "withdraw":
      return <Withdraw refresh={refresh} />;
    case "history":
      return <History version={version} />;
    case "bills":
      return <Bills refresh={refresh} />;
    case "promo":
      return <Promos />;
    case "points":
      return <PointsScreen version={version} />;
    case "split":
      return <SplitBill />;
    case "inbox":
      return <InboxScreen version={version} />;
    case "profile":
      return <ProfileScreen version={version} refresh={refresh} />;
  }
}

function Header({ title, back }: { title: string; back?: () => void }) {
  return (
    <div className="pheader">
      {back ? (
        <button className="back" onClick={back} aria-label="Back">
          <svg>
            <use href="#i-back" />
          </svg>
        </button>
      ) : null}
      <h3>{title}</h3>
    </div>
  );
}

function Home({ go, version }: { go: (s: Screen) => void; version: number }) {
  const balance = useResource<Balance>(`/v1/balance?v=${version}`, "user");
  const profile = useResource<Profile>("/v1/me", "user");
  const history = useResource<Page<Activity>>(`/v1/transactions?limit=6&v=${version}`, "user");
  const points = useResource<Points>(`/v1/points?v=${version}`, "user");

  return (
    <section className="pscreen active">
      <div className="balance">
        <div className="k">Available balance</div>
        <div className="v">{rupiah(balance.data?.available)}</div>
        <div className="meta">
          <span>On hold {rupiah(balance.data?.held)}</span>
          <span>·</span>
          <span>{profile.data ? `${profile.data.kyc_tier} tier` : "…"}</span>
        </div>
      </div>

      {balance.data && !balance.data.cache_agreed ? (
        <Note tone="warn">
          Redis disagrees with the ledger. The ledger figure is the one shown; the cache will be
          rebuilt from it.
        </Note>
      ) : null}

      <div className="quick">
        <button onClick={() => go("topup")}>
          <svg><use href="#i-plus" /></svg>Top Up
        </button>
        <button onClick={() => go("transfer")}>
          <svg><use href="#i-send" /></svg>Transfer
        </button>
        <button onClick={() => go("pay")}>
          <svg><use href="#i-qr" /></svg>Pay
        </button>
        <button onClick={() => go("withdraw")}>
          <svg><use href="#i-out" /></svg>Withdraw
        </button>
      </div>

      <button className="pointsbar" onClick={() => go("points")}>
        <svg><use href="#i-star" /></svg>
        <div>
          <div className="t">MarsPoin</div>
          <div className="s">{points.data?.balance.rate ?? "Loyalty points"}</div>
        </div>
        <div className="v">{points.data?.balance.points ?? 0}</div>
      </button>

      <div className="sect">
        <h4>Pay bills</h4>
        <a href="#" onClick={(e) => { e.preventDefault(); go("bills"); }}>All</a>
      </div>
      <div className="services">
        <button onClick={() => go("bills")}>
          <span className="ic"><svg><use href="#i-zap" /></svg></span>Electricity
        </button>
        <button onClick={() => go("bills")}>
          <span className="ic"><svg><use href="#i-phone" /></svg></span>Airtime
        </button>
        <button onClick={() => go("promo")}>
          <span className="ic"><svg><use href="#i-tag" /></svg></span>Offers
        </button>
        <button onClick={() => go("split")} className="gold">
          <span className="ic"><svg><use href="#i-split" /></svg></span>Split Bill
        </button>
      </div>

      <div className="sect">
        <h4>Recent transactions</h4>
        <a href="#" onClick={(e) => { e.preventDefault(); go("history"); }}>View all</a>
      </div>
      <ActivityList page={history.data} loading={history.loading} />
    </section>
  );
}

function ActivityList({ page, loading }: { page: Page<Activity> | null; loading: boolean }) {
  if (loading) return <div className="loadrow">Loading…</div>;
  if (!page?.data.length) return <div className="loadrow">No transactions yet.</div>;

  return (
    <div className="txlist">
      {page.data.map((tx) => {
        const incoming = tx.direction === "in";
        return (
          <div className="txrow" key={tx.id}>
            <div className={`ic ${incoming ? "in" : "out"}`}>
              <svg><use href={`#${iconFor(tx.kind, incoming)}`} /></svg>
            </div>
            <div>
              <div className="t">{tx.counterpart || tx.kind.replace(/_/g, " ")}</div>
              <div className="s">
                {tx.kind.replace(/_/g, " ")} · {clock(tx.created_at)}
              </div>
            </div>
            <div className={`amt${incoming ? " in" : ""}`}>
              {incoming ? "+" : "−"}
              {plain(Math.abs(tx.amount))}
              <span className="st">{tx.status}</span>
            </div>
          </div>
        );
      })}
    </div>
  );
}

function iconFor(kind: string, incoming: boolean): string {
  if (kind === "payment") return "i-store";
  if (kind === "transfer") return "i-send";
  if (kind === "topup") return "i-in";
  if (kind === "withdrawal") return "i-out";
  if (kind === "bill_payment") return "i-file";
  return incoming ? "i-in" : "i-out";
}

function useAction() {
  const [note, setNote] = useState<{ tone: "info" | "ok" | "warn"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);

  async function run<T>(
    call: () => Promise<api.Result<T>>,
    onSuccess: (data: T) => { tone: "info" | "ok"; text: string },
  ) {
    setBusy(true);
    const result = await call();
    setBusy(false);

    if (!result.ok) {
      setNote({ tone: "warn", text: api.describe(result) });
      return null;
    }
    setNote(onSuccess(result.data));
    return result.data;
  }

  return { note, busy, run, setNote };
}

function Pay({ go, refresh }: { go: (s: Screen) => void; refresh: () => void }) {
  const [merchant, setMerchant] = useState("merch_demo");
  const [amount, setAmount] = useState("32,000");
  const [chargeID, setChargeID] = useState("");
  const [charge, setCharge] = useState<Charge | null>(null);
  const { note, busy, run, setNote } = useAction();

  async function lookup(id: string) {
    if (!id.trim()) {
      setCharge(null);
      return;
    }

    const result = await api.request<Charge>("GET", `/v1/charges/${encodeURIComponent(id.trim())}`, {
      role: "user",
    });
    if (!result.ok) {
      setCharge(null);
      setNote({ tone: "warn", text: api.describe(result) });
      return;
    }

    setCharge(result.data);
    setAmount(plain(result.data.amount));
    setNote({
      tone: result.data.expired ? "warn" : "info",
      text: result.data.expired
        ? "This link has expired."
        : `Link for ${rupiah(result.data.amount)}, expires ${clock(result.data.expires_at)}.`,
    });
  }

  return (
    <section className="pscreen active">
      <Header title="Pay" back={() => go("home")} />

      <div className="qrbox">
        <div className="frame" />
        <div className="cap">{charge ? charge.description : "Point at the merchant QRIS code"}</div>
      </div>

      <label className="field mt4">
        <span className="lbl">Merchant</span>
        <input
          className="input"
          value={merchant}
          spellCheck={false}
          onChange={(e) => setMerchant(e.target.value)}
        />
      </label>

      <label className="field mt4">
        <span className="lbl">Or a payment link</span>
        <input
          className="input mono"
          placeholder="chg_… from the merchant"
          value={chargeID}
          spellCheck={false}
          onChange={(e) => setChargeID(e.target.value)}
          onBlur={(e) => lookup(e.target.value)}
        />
        <span className="help">
          With a link the amount comes from the link, and the merchant field is ignored.
        </span>
      </label>

      <label className="field mt4">
        <span className="lbl">Amount</span>
        <input
          className="input num"
          inputMode="numeric"
          value={amount}
          disabled={Boolean(charge)}
          onChange={(e) => setAmount(e.target.value)}
        />
      </label>

      {note ? <Note tone={note.tone}>{note.text}</Note> : null}

      <button
        className="btn brand block mt4"
        disabled={busy}
        onClick={async () => {
          const body = chargeID.trim()
            ? { charge_id: chargeID.trim() }
            : {
                merchant_id: merchant.trim(),
                method: "qris",
                amount: toMinor(amount),
                currency: "IDR",
              };

          const paid = await run(
            () => api.request<{ amount: number; fee: number; ledger_transaction_id: string }>(
              "POST",
              "/v1/payments",
              { role: "user", body },
            ),
            (data) => ({
              tone: "ok",
              text: `Paid ${rupiah(data.amount)} · fee ${rupiah(data.fee)} · ledger ${data.ledger_transaction_id}`,
            }),
          );

          if (paid) {
            setChargeID("");
            setCharge(null);
            refresh();
          }
        }}
      >
        Pay now
      </button>
    </section>
  );
}

function Transfer({ refresh }: { refresh: () => void }) {
  const [to, setTo] = useState("081200000002");
  const [amount, setAmount] = useState("150,000");
  const [noteText, setNoteText] = useState("");
  const { note, busy, run } = useAction();
  const balance = useResource<Balance>("/v1/balance", "user");

  return (
    <section className="pscreen active">
      <Header title="Transfer" />

      <label className="field mt4">
        <span className="lbl">Recipient</span>
        <input className="input" value={to} spellCheck={false} onChange={(e) => setTo(e.target.value)} />
        <span className="help">Sending from a balance of {rupiah(balance.data?.available)}</span>
      </label>

      <label className="field mt4">
        <span className="lbl">Amount</span>
        <input
          className="input num"
          inputMode="numeric"
          value={amount}
          onChange={(e) => setAmount(e.target.value)}
        />
      </label>

      <label className="field mt4">
        <span className="lbl">Note (optional)</span>
        <input className="input" value={noteText} onChange={(e) => setNoteText(e.target.value)} />
      </label>

      <Note tone="info">
        A first-time recipient passes a <strong>velocity check</strong> before the money moves.
        Ten new recipients inside five minutes freezes the account.
      </Note>

      {note ? <Note tone={note.tone}>{note.text}</Note> : null}

      <button
        className="btn brand block mt4"
        disabled={busy}
        onClick={async () => {
          const sent = await run(
            () =>
              api.request<{ amount: number }>("POST", "/v1/transfers", {
                role: "user",
                body: { to: to.trim(), amount: toMinor(amount), currency: "IDR", note: noteText },
              }),
            (data) => ({ tone: "ok", text: `Sent ${rupiah(data.amount)}.` }),
          );
          if (sent) refresh();
        }}
      >
        Send
      </button>
    </section>
  );
}

function TopUp({ refresh }: { refresh: () => void }) {
  const [amount, setAmount] = useState("500,000");
  const { note, busy, run } = useAction();

  return (
    <section className="pscreen active">
      <Header title="Top Up" />

      <label className="field mt4">
        <span className="lbl">Amount</span>
        <input
          className="input num"
          inputMode="numeric"
          value={amount}
          onChange={(e) => setAmount(e.target.value)}
        />
        <span className="help">Minimum Rp 10,000 per transaction</span>
      </label>

      <Note tone="warn">
        Your balance moves when the <strong>provider callback</strong> arrives, not when you press
        this button. Until then the top up sits in <strong>pending</strong>.
      </Note>

      {note ? <Note tone={note.tone}>{note.text}</Note> : null}

      <button
        className="btn brand block mt4"
        disabled={busy}
        onClick={async () => {
          const created = await run(
            () =>
              api.request<{ virtual_account: string; status: string }>("POST", "/v1/topups", {
                role: "user",
                body: {
                  source: "bank_va",
                  provider_code: "bca",
                  amount: toMinor(amount),
                  currency: "IDR",
                },
              }),
            (data) => ({
              tone: "info",
              text: `Pending. Pay to virtual account ${data.virtual_account}.`,
            }),
          );
          if (created) refresh();
        }}
      >
        Continue
      </button>
    </section>
  );
}

function Withdraw({ refresh }: { refresh: () => void }) {
  const [amount, setAmount] = useState("100,000");
  const { note, busy, run } = useAction();

  return (
    <section className="pscreen active">
      <Header title="Withdraw" />

      <label className="field mt4">
        <span className="lbl">Amount</span>
        <input
          className="input num"
          inputMode="numeric"
          value={amount}
          onChange={(e) => setAmount(e.target.value)}
        />
        <span className="help">To BCA ····7890 · an admin fee applies</span>
      </label>

      <Note tone="warn">
        Withdrawals are <strong>asynchronous and can fail</strong> at the bank. A failure returns
        the money through new ledger entries, never by deleting the old ones.
      </Note>

      {note ? <Note tone={note.tone}>{note.text}</Note> : null}

      <button
        className="btn brand block mt4"
        disabled={busy}
        onClick={async () => {
          const sent = await run(
            () =>
              api.request<{ total_debited: number; admin_fee: number }>("POST", "/v1/withdrawals", {
                role: "user",
                body: {
                  bank_code: "BCA",
                  account_number: "1234567890",
                  account_name: "Demo Consumer",
                  amount: toMinor(amount),
                  currency: "IDR",
                },
              }),
            (data) => ({
              tone: "info",
              text: `Pending at the bank. ${rupiah(data.total_debited)} debited including ${rupiah(data.admin_fee)} fee.`,
            }),
          );
          if (sent) refresh();
        }}
      >
        Withdraw
      </button>
    </section>
  );
}

function History({ version }: { version: number }) {
  const history = useResource<Page<Activity>>(`/v1/transactions?limit=40&v=${version}`, "user");

  return (
    <section className="pscreen active">
      <Header title="History" />
      {history.error ? <Note tone="warn">{history.error}</Note> : null}
      <ActivityList page={history.data} loading={history.loading} />
    </section>
  );
}

function Bills({ refresh }: { refresh: () => void }) {
  const billers = useResource<Page<Biller> | Biller[]>("/v1/billers", "user");
  const [code, setCode] = useState("PLN_POSTPAID");
  const [ref, setRef] = useState("512201884471");
  const [quote, setQuote] = useState<Inquiry | null>(null);
  const { note, busy, run, setNote } = useAction();

  const list: Biller[] = Array.isArray(billers.data)
    ? billers.data
    : (billers.data?.data ?? []);

  return (
    <section className="pscreen active">
      <Header title="Pay Bills" />

      <label className="field mt4">
        <span className="lbl">Biller</span>
        <select className="input" value={code} onChange={(e) => setCode(e.target.value)}>
          {list.map((biller) => (
            <option key={biller.code} value={biller.code}>
              {biller.name}
            </option>
          ))}
        </select>
      </label>

      <label className="field mt4">
        <span className="lbl">Customer ID</span>
        <input className="input mono" value={ref} onChange={(e) => setRef(e.target.value)} />
      </label>

      <button
        className="btn block mt3"
        disabled={busy}
        onClick={async () => {
          const result = await api.request<Inquiry>(
            "POST",
            `/v1/billers/${encodeURIComponent(code)}/inquire`,
            { role: "user", body: { customer_ref: ref.trim() } },
          );
          if (!result.ok) {
            setQuote(null);
            setNote({ tone: "warn", text: api.describe(result) });
            return;
          }
          setQuote(result.data);
          setNote({
            tone: "ok",
            text: `Quoted ${rupiah(result.data.total_payable)}. Press Pay to settle it.`,
          });
        }}
      >
        Check the bill
      </button>

      {quote ? (
        <div className="card mt4">
          <div className="body">
            <dl className="kv" style={{ gridTemplateColumns: "110px minmax(0,1fr)" }}>
              <dt>Biller</dt>
              <dd>{quote.biller_name}</dd>
              <dt>Name</dt>
              <dd>{quote.customer_name}</dd>
              <dt>Period</dt>
              <dd>{quote.period || "—"}</dd>
              <dt>Bill</dt>
              <dd className="num">{rupiah(quote.amount)}</dd>
              <dt>Admin fee</dt>
              <dd className="num">{rupiah(quote.admin_fee)}</dd>
              <dt>Total</dt>
              <dd className="num">{rupiah(quote.total_payable)}</dd>
            </dl>
          </div>
        </div>
      ) : null}

      {note ? <Note tone={note.tone}>{note.text}</Note> : null}

      <button
        className="btn brand block mt4"
        disabled={busy || !quote}
        onClick={async () => {
          if (!quote) return;
          const paid = await run(
            () =>
              api.request<{ status: string }>("POST", "/v1/bill-payments", {
                role: "user",
                body: {
                  biller_code: quote.biller_code,
                  customer_ref: quote.customer_ref,
                  amount: quote.amount,
                  currency: "IDR",
                },
              }),
            (data) => ({ tone: "ok", text: `Bill payment is ${data.status}.` }),
          );
          if (paid) {
            setQuote(null);
            refresh();
          }
        }}
      >
        Pay
      </button>
    </section>
  );
}

function Promos() {
  const promos = useResource<Page<Promo>>("/v1/promos", "user");
  const [applied, setApplied] = useState<Record<string, string>>({});

  return (
    <section className="pscreen active">
      <Header title="Offers" />
      {promos.error ? <Note tone="warn">{promos.error}</Note> : null}

      <div className="queue">
        {(promos.data?.data ?? []).map((promo) => (
          <div className="qitem" key={promo.id}>
            <div className="av" style={{ background: "var(--brand-soft)", color: "var(--brand)" }}>
              {promo.value_bps ? `${promo.value_bps / 100}%` : rupiah(promo.value)}
            </div>
            <div>
              <div className="t">{promo.name}</div>
              <div className="s">
                {promo.max_benefit ? `max ${rupiah(promo.max_benefit)} · ` : ""}
                min spend {rupiah(promo.min_spend)} · {promo.remaining_quota} left
              </div>
            </div>
            <button
              className="btn sm brand"
              onClick={async () => {
                const result = await api.request<{ value: number }>("POST", "/v1/promos/apply", {
                  role: "user",
                  body: { code: promo.code, spend: 3_200_000 },
                });
                setApplied((current) => ({
                  ...current,
                  [promo.id]: result.ok ? `Applied · ${rupiah(result.data.value)}` : "Refused",
                }));
              }}
            >
              {applied[promo.id] ?? "Apply"}
            </button>
          </div>
        ))}
        {!promos.loading && !(promos.data?.data ?? []).length ? (
          <div className="qitem">
            <div>
              <div className="t">No offers right now</div>
            </div>
          </div>
        ) : null}
      </div>
    </section>
  );
}

function PointsScreen({ version }: { version: number }) {
  const points = useResource<Points>(`/v1/points?v=${version}`, "user");
  const { note, busy, run } = useAction();

  return (
    <section className="pscreen active">
      <Header title="MarsPoin" />

      <div
        className="balance"
        style={{ background: "linear-gradient(145deg,#A16207 0%,#C98A1E 100%)" }}
      >
        <div className="k">Total points</div>
        <div className="v">{points.data?.balance.points ?? 0}</div>
        <div className="meta">
          <span>{points.data?.balance.expiring_soon ?? 0} expiring soon</span>
          <span>·</span>
          <span>{points.data?.balance.rate ?? ""}</span>
        </div>
      </div>

      {note ? <Note tone={note.tone}>{note.text}</Note> : null}

      <div className="sect">
        <h4>Redeem</h4>
      </div>
      <div className="txlist">
        <div className="txrow">
          <div className="ic out">
            <svg><use href="#i-wallet" /></svg>
          </div>
          <div>
            <div className="t">Convert to balance</div>
            <div className="s">10,000 points</div>
          </div>
          <div className="amt">
            <button
              className="btn sm"
              disabled={busy}
              onClick={() =>
                run(
                  () =>
                    api.request<{ points: number; value: number }>("POST", "/v1/points/redeem", {
                      role: "user",
                      body: { kind: "balance", points: 10000 },
                    }),
                  (data) => ({
                    tone: "ok",
                    text: `Redeemed ${data.points} points for ${rupiah(data.value)}.`,
                  }),
                )
              }
            >
              Redeem
            </button>
          </div>
        </div>
      </div>

      <div className="sect">
        <h4>Points history</h4>
      </div>
      <div className="txlist">
        {(points.data?.history ?? []).map((entry) => (
          <div className="txrow" key={entry.id}>
            <div className={`ic ${entry.amount > 0 ? "in" : "out"}`}>
              <svg><use href={`#${entry.amount > 0 ? "i-in" : "i-out"}`} /></svg>
            </div>
            <div>
              <div className="t">{entry.kind}</div>
              <div className="s">{entry.source ?? ""}</div>
            </div>
            <div className={`amt${entry.amount > 0 ? " in" : ""}`}>
              {entry.amount > 0 ? "+" : ""}
              {entry.amount}
            </div>
          </div>
        ))}
        {!(points.data?.history ?? []).length ? (
          <div className="loadrow">No points movement yet.</div>
        ) : null}
      </div>
    </section>
  );
}

function SplitBill() {
  const [title, setTitle] = useState("Dinner at Sate Khas Senayan");
  const [total, setTotal] = useState("480,000");
  const [payers, setPayers] = useState("081200000002,081200000003");
  const [split, setSplit] = useState<Split | null>(null);
  const { note, busy, run } = useAction();

  return (
    <section className="pscreen active">
      <Header title="Split Bill" />

      <div className="card mt3">
        <div className="body">
          <label className="field">
            <span className="lbl">What is being split</span>
            <input className="input" value={title} onChange={(e) => setTitle(e.target.value)} />
          </label>
          <label className="field mt3">
            <span className="lbl">Total</span>
            <input
              className="input num"
              inputMode="numeric"
              value={total}
              onChange={(e) => setTotal(e.target.value)}
            />
          </label>
          <label className="field mt3">
            <span className="lbl">Split with</span>
            <input className="input" value={payers} onChange={(e) => setPayers(e.target.value)} />
          </label>

          {note ? <Note tone={note.tone}>{note.text}</Note> : null}

          <button
            className="btn brand block mt3"
            disabled={busy}
            onClick={async () => {
              const created = await run(
                () =>
                  api.request<Split>("POST", "/v1/bill-splits", {
                    role: "user",
                    body: {
                      title: title.trim(),
                      total: toMinor(total),
                      currency: "IDR",
                      payers: payers.split(",").map((p) => p.trim()).filter(Boolean),
                    },
                  }),
                (data) => ({
                  tone: "ok",
                  text: `Split ${rupiah(data.total)} across ${data.participants} people.`,
                }),
              );
              if (created) setSplit(created);
            }}
          >
            Create the split
          </button>
        </div>
      </div>

      <Note tone="info">
        A bill split is not a transaction. It is <strong>N independent requests</strong> — if two
        people never pay, the others still stand.
      </Note>

      <div className="sect">
        <h4>Who owes what</h4>
      </div>
      <div className="queue">
        {(split?.requests ?? []).map((request) => (
          <div className="qitem" key={request.id}>
            <div className="av">{initials(request.payer_id.slice(-2))}</div>
            <div>
              <div className="t">{request.payer_id}</div>
              <div className="s num">{rupiah(request.amount)}</div>
            </div>
            <Badge status={request.status} />
          </div>
        ))}
        {!split ? (
          <div className="qitem">
            <div>
              <div className="t">Nothing split yet</div>
            </div>
          </div>
        ) : null}
      </div>
    </section>
  );
}

function InboxScreen({ version }: { version: number }) {
  const inbox = useResource<Inbox>(`/v1/notifications?v=${version}`, "user");
  const requests = useResource<Page<MoneyRequest>>("/v1/money-requests", "user");

  return (
    <section className="pscreen active">
      <Header title="Notifications" />
      {inbox.data ? <div className="loadrow">{inbox.data.source}</div> : null}

      <div className="txlist">
        {(inbox.data?.data ?? []).map((item) => (
          <div className="txrow" key={item.id}>
            <div className={`ic${item.unread ? " in" : ""}`}>
              <svg><use href="#i-inbox" /></svg>
            </div>
            <div>
              <div className="t">{item.title}</div>
              <div className="s">
                {item.detail} · {clock(item.created_at)}
              </div>
            </div>
            <div className="amt">
              {item.unread ? <span className="badge info flat">New</span> : null}
            </div>
          </div>
        ))}
        {!inbox.loading && !(inbox.data?.data ?? []).length ? (
          <div className="loadrow">Nothing here yet.</div>
        ) : null}
      </div>

      <div className="sect">
        <h4>Incoming requests</h4>
      </div>
      <div className="queue">
        {(requests.data?.data ?? []).map((request) => (
          <div className="qitem" key={request.id}>
            <div className="av">{initials(request.requester_name ?? request.requester_id)}</div>
            <div>
              <div className="t">{request.requester_name ?? request.requester_id} is requesting</div>
              <div className="s num">
                {rupiah(request.amount)}
                {request.note ? ` · ${request.note}` : ""}
              </div>
            </div>
            <div className="ops">
              <button
                className="btn sm"
                onClick={async () => {
                  await api.request("POST", `/v1/money-requests/${request.id}/decline`, {
                    role: "user",
                  });
                  requests.reload();
                }}
              >
                Decline
              </button>
            </div>
          </div>
        ))}
        {!(requests.data?.data ?? []).length ? (
          <div className="qitem">
            <div>
              <div className="t">Nobody is asking you for money</div>
            </div>
          </div>
        ) : null}
      </div>
    </section>
  );
}

function ProfileScreen({ version, refresh }: { version: number; refresh: () => void }) {
  const profile = useResource<Profile>(`/v1/me?v=${version}`, "user");
  const devices = useResource<Page<Device>>(`/v1/devices?v=${version}`, "user");

  return (
    <section className="pscreen active">
      <Header title="Account" />

      <div className="qitem mt3">
        <div className="av">{initials(profile.data?.name ?? "")}</div>
        <div>
          <div className="t">{profile.data?.name ?? "…"}</div>
          <div className="s">{profile.data?.phone ?? ""}</div>
        </div>
        <span className="badge gold">{profile.data?.kyc_tier ?? ""}</span>
      </div>

      <div className="sect">
        <h4>Tier &amp; limits</h4>
      </div>
      <div className="card">
        <div className="body">
          <dl className="kv">
            <dt>Current tier</dt>
            <dd>{profile.data?.kyc_tier ?? "…"}</dd>
            <dt>Maximum balance</dt>
            <dd className="num">{rupiah(profile.data?.limits.max_balance)}</dd>
            <dt>Transactions / month</dt>
            <dd className="num">{rupiah(profile.data?.limits.max_monthly)}</dd>
            <dt>Used this month</dt>
            <dd className="num">{rupiah(profile.data?.limits.used_this_month)}</dd>
          </dl>
        </div>
      </div>

      <div className="sect">
        <h4>Devices</h4>
      </div>
      <div className="txlist">
        {(devices.data?.data ?? []).map((device) => (
          <div className="txrow" key={device.id}>
            <div className="ic">
              <svg><use href="#i-activity" /></svg>
            </div>
            <div>
              <div className="t">
                {device.model || device.platform}
                {device.current ? " · this device" : ""}
              </div>
              <div className="s">
                {device.revoked_at ? "Revoked" : `Last seen ${clock(device.last_seen_at)}`}
              </div>
            </div>
            <div className="amt">
              {device.revoked_at || device.current ? null : (
                <button
                  className="btn sm"
                  onClick={async () => {
                    await api.request("POST", `/v1/devices/${device.id}/revoke`, { role: "user" });
                    devices.reload();
                    refresh();
                  }}
                >
                  Revoke
                </button>
              )}
            </div>
          </div>
        ))}
        {!(devices.data?.data ?? []).length ? <div className="loadrow">No devices.</div> : null}
      </div>
    </section>
  );
}
