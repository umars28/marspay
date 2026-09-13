"use client";

import { useState } from "react";

import * as api from "@/lib/api";
import { clock, millis, percent, plain, rupiah, shortId, toMinor } from "@/lib/format";
import { useSession } from "@/lib/session";
import { useResource } from "@/lib/useResource";
import type {
  ApiKey,
  Charge,
  Delivery,
  Hour,
  Outlet,
  Page,
  PaymentPage,
  PayoutConfig,
  PayoutPage,
  Settlement,
  Staff,
} from "@/lib/types";
import { Dashboard } from "@/components/Dashboard";
import { Badge, Card, Note, PageHead, SignedOut, Stat, Table } from "@/components/ui";

const SECTIONS = [
  { key: "overview", label: "Overview", icon: "i-trend", group: "Money in" },
  { key: "payments", label: "Transactions", icon: "i-list", group: "Money in" },
  { key: "charges", label: "Payment Link", icon: "i-link", group: "Money in" },
  { key: "payouts", label: "Instant Payout", icon: "i-zap", group: "Payout" },
  { key: "settlements", label: "Settlements", icon: "i-file", group: "Payout" },
  { key: "outlets", label: "Outlets & Staff", icon: "i-layers", group: "Setup" },
  { key: "keys", label: "API Keys", icon: "i-key", group: "Setup" },
  { key: "webhooks", label: "Webhooks", icon: "i-activity", group: "Setup" },
];

export default function MerchantPage() {
  const { live } = useSession();
  const [version, setVersion] = useState(0);
  const refresh = () => setVersion((n) => n + 1);

  if (!live.merchant) {
    return (
      <Dashboard sections={SECTIONS}>
        {() => <SignedOut role="a merchant, by pasting the API key" />}
      </Dashboard>
    );
  }

  return (
    <Dashboard sections={SECTIONS}>
      {(active) => {
        switch (active) {
          case "overview":
            return <Overview version={version} />;
          case "payments":
            return <Payments version={version} refresh={refresh} />;
          case "charges":
            return <Charges version={version} refresh={refresh} />;
          case "payouts":
            return <Payouts version={version} />;
          case "settlements":
            return <Settlements version={version} />;
          case "outlets":
            return <Outlets version={version} refresh={refresh} />;
          case "keys":
            return <Keys version={version} refresh={refresh} />;
          case "webhooks":
            return <Webhooks version={version} />;
          default:
            return null;
        }
      }}
    </Dashboard>
  );
}

function Overview({ version }: { version: number }) {
  const payments = useResource<PaymentPage>(`/v1/payments?limit=10&v=${version}`, "merchant");
  const hourly = useResource<Page<Hour>>(`/v1/volume?v=${version}`, "merchant");
  const config = useResource<PayoutConfig>(`/v1/payouts/config?v=${version}`, "merchant");

  const totals = payments.data?.totals;
  const buckets = hourly.data?.data ?? [];
  const peak = buckets.reduce((a, h) => Math.max(a, h.volume), 0);

  return (
    <section className="screen active">
      <PageHead title="Overview" subtitle="Everything here is read from the ledger" />

      <div className="grid g4">
        <Stat title="Volume" value={rupiah(totals?.gross)} detail={`${totals?.count ?? 0} payments`} />
        <Stat title="Succeeded" value={totals?.succeeded ?? 0} detail={`${totals?.failed ?? 0} failed`} />
        <Stat
          title="Success rate"
          value={totals?.count ? `${((totals.succeeded / totals.count) * 100).toFixed(1)}%` : "—"}
        />
        <Stat title="Net after fees" value={rupiah(totals?.net)} detail={rupiah(totals?.fees) + " in fees"} />
      </div>

      {config.data ? (
        <div className="keyband mt4">
          <div className="txt">
            <h3>Instant Payout is on</h3>
            <p>
              Money reaches your bank within seconds of the customer paying. A holdback is kept
              briefly against dispute risk and released automatically.
            </p>
          </div>
          <div className="big">
            {percent(config.data.holdback_bps)}
            <span>your current holdback</span>
          </div>
        </div>
      ) : null}

      <Card title="Volume per hour" hint="last 24 hours" className="mt4">
        <div className="body">
          <div className="bars">
            {buckets.map((bucket) => (
              <div
                key={bucket.hour}
                style={{ height: `${peak > 0 ? Math.max(2, (bucket.volume / peak) * 100) : 2}%` }}
                title={`${bucket.hour}:00 · ${rupiah(bucket.volume)}`}
              />
            ))}
          </div>
          <div className="axisrow">
            <span>00:00</span>
            <span>06:00</span>
            <span>12:00</span>
            <span>18:00</span>
            <span>23:00</span>
          </div>
        </div>
      </Card>

      <Card title="Recent transactions" className="mt4">
        <Table
          head={["ID", "Time", "Method", "#Amount", "#Fee", "Status"]}
          loading={payments.loading}
          error={payments.error}
          empty="No payments yet"
        >
          {(payments.data?.data ?? []).map((payment) => (
            <tr key={payment.id}>
              <td className="mono">{shortId(payment.id)}</td>
              <td>{clock(payment.created_at)}</td>
              <td>{payment.method.toUpperCase()}</td>
              <td className="r num">{plain(payment.amount)}</td>
              <td className="r num">{plain(payment.fee)}</td>
              <td>
                <Badge status={payment.status} />
              </td>
            </tr>
          ))}
        </Table>
      </Card>
    </section>
  );
}

function Payments({ version, refresh }: { version: number; refresh: () => void }) {
  const payments = useResource<PaymentPage>(`/v1/payments?limit=50&v=${version}`, "merchant");
  const [busy, setBusy] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);

  return (
    <section className="screen active">
      <PageHead title="Transactions" subtitle="Every payment this merchant has taken" />
      {note ? <Note tone="warn">{note}</Note> : null}

      <Card className="mt4">
        <Table
          head={["ID", "Time", "Method", "#Amount", "#Fee", "#Net", "Status", "Ledger", ""]}
          loading={payments.loading}
          error={payments.error}
          empty="No payments yet"
        >
          {(payments.data?.data ?? []).map((payment) => (
            <tr key={payment.id}>
              <td className="mono">{shortId(payment.id)}</td>
              <td>{clock(payment.created_at)}</td>
              <td>{payment.method.toUpperCase()}</td>
              <td className="r num">{plain(payment.amount)}</td>
              <td className="r num">{plain(payment.fee)}</td>
              <td className="r num">{plain(payment.net)}</td>
              <td>
                <Badge status={payment.status} />
              </td>
              <td className="mono">{payment.ledger_transaction_id ? "posted" : "—"}</td>
              <td>
                {payment.status === "succeeded" ? (
                  <button
                    className="btn sm"
                    disabled={busy === payment.id}
                    onClick={async () => {
                      setBusy(payment.id);
                      setNote(null);
                      const result = await api.request("POST", "/v1/refunds", {
                        role: "merchant",
                        body: {
                          payment_id: payment.id,
                          reason: "refunded from the dashboard",
                        },
                      });
                      setBusy(null);
                      if (!result.ok) setNote(api.describe(result));
                      else refresh();
                    }}
                  >
                    Refund
                  </button>
                ) : null}
              </td>
            </tr>
          ))}
        </Table>
      </Card>
    </section>
  );
}

function Charges({ version, refresh }: { version: number; refresh: () => void }) {
  const charges = useResource<Page<Charge>>(`/v1/charges?limit=25&v=${version}`, "merchant");
  const [description, setDescription] = useState("");
  const [amount, setAmount] = useState("");
  const [note, setNote] = useState<{ tone: "ok" | "warn"; text: string } | null>(null);

  return (
    <section className="screen active">
      <PageHead
        title="Payment Link"
        subtitle="Single-use invoices with an expiry"
        actions={
          <>
            <input
              className="input"
              placeholder="What is this for"
              style={{ minWidth: 180 }}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
            <input
              className="input num"
              placeholder="Amount"
              style={{ maxWidth: 130 }}
              value={amount}
              onChange={(e) => setAmount(e.target.value)}
            />
            <button
              className="btn primary"
              onClick={async () => {
                if (!description.trim() || !toMinor(amount)) {
                  setNote({ tone: "warn", text: "A description and an amount are both required." });
                  return;
                }

                const result = await api.request<Charge>("POST", "/v1/charges", {
                  role: "merchant",
                  body: {
                    description: description.trim(),
                    amount: toMinor(amount),
                    currency: "IDR",
                    expires_in: "24h",
                  },
                });

                if (!result.ok) {
                  setNote({ tone: "warn", text: api.describe(result) });
                  return;
                }
                setNote({ tone: "ok", text: `Created ${result.data.id}. Give that id to the payer.` });
                setDescription("");
                setAmount("");
                refresh();
              }}
            >
              Create new
            </button>
          </>
        }
      />

      {note ? <Note tone={note.tone}>{note.text}</Note> : null}

      <Card title="Links" className="mt4">
        <Table
          head={["Reference", "For", "#Amount", "Expires", "Status", ""]}
          loading={charges.loading}
          error={charges.error}
          empty="No payment links; create one above"
        >
          {(charges.data?.data ?? []).map((charge) => (
            <tr key={charge.id}>
              <td className="mono">{charge.reference}</td>
              <td>{charge.description}</td>
              <td className="r num">{plain(charge.amount)}</td>
              <td>{clock(charge.expires_at)}</td>
              <td>
                <Badge status={charge.expired ? "expired" : charge.status} />
              </td>
              <td>
                {charge.status === "open" ? (
                  <button
                    className="btn sm"
                    onClick={async () => {
                      await api.request("POST", `/v1/charges/${charge.id}/cancel`, {
                        role: "merchant",
                      });
                      refresh();
                    }}
                  >
                    Cancel
                  </button>
                ) : charge.payment_id ? (
                  <span className="mono small">{shortId(charge.payment_id)}</span>
                ) : null}
              </td>
            </tr>
          ))}
        </Table>
      </Card>
    </section>
  );
}

function Payouts({ version }: { version: number }) {
  const payouts = useResource<PayoutPage>(`/v1/payouts?limit=50&v=${version}`, "merchant");
  const config = useResource<PayoutConfig>(`/v1/payouts/config?v=${version}`, "merchant");
  const summary = payouts.data?.summary;

  return (
    <section className="screen active">
      <PageHead
        title="Instant Payout"
        subtitle="Every payment is paid out on its own instead of waiting for a nightly batch"
      />

      <div className="grid g4">
        <Stat title="Paid out" value={rupiah(summary?.net)} detail={`${summary?.count ?? 0} payouts`} />
        <Stat
          title="p50 / p95 latency"
          value={`${millis(summary?.median_latency_ms)} / ${millis(summary?.p95_latency_ms)}`}
        />
        <Stat title="Held back" value={rupiah(summary?.holdback)} detail="released over 24 hours" />
        <Stat title="Failed at the bank" value={summary?.failed ?? 0} />
      </div>

      {config.data ? (
        <Card title="Your holdback" hint="recomputed every 5 minutes" className="mt4">
          <div className="body">
            <div className="meterrow">
              <span>Holdback rate</span>
              <span className="n">{percent(config.data.holdback_bps)}</span>
            </div>
            <div className="meter ok">
              <i
                style={{
                  width: `${Math.max(
                    2,
                    Math.min(
                      100,
                      ((config.data.holdback_bps - config.data.holdback_floor_bps) /
                        (config.data.holdback_ceiling_bps - config.data.holdback_floor_bps)) *
                        100,
                    ),
                  )}%`,
                }}
              />
            </div>
            <p className="small muted mt3">{config.data.formula}</p>
            <dl className="kv mt3">
              {Object.entries(config.data.components).map(([factor, value]) => (
                <div key={factor} style={{ display: "contents" }}>
                  <dt>{factor.replace(/_/g, " ")}</dt>
                  <dd className="num">{value}</dd>
                </div>
              ))}
            </dl>
          </div>
        </Card>
      ) : null}

      <Card title="Payout stream" className="mt4">
        <Table
          head={["Payout", "Payment", "#Gross", "#Holdback", "#Net", "Rail", "Latency", "Status"]}
          loading={payouts.loading}
          error={payouts.error}
          empty="No payouts yet"
        >
          {(payouts.data?.data ?? []).map((payout) => (
            <tr key={payout.id}>
              <td className="mono">{shortId(payout.id)}</td>
              <td className="mono">{shortId(payout.payment_id)}</td>
              <td className="r num">{plain(payout.gross)}</td>
              <td className="r num">{plain(payout.holdback)}</td>
              <td className="r num">{plain(payout.net)}</td>
              <td>{payout.rail || "—"}</td>
              <td>{millis(payout.latency_ms)}</td>
              <td>
                <Badge status={payout.status} />
              </td>
            </tr>
          ))}
        </Table>
      </Card>
    </section>
  );
}

function Settlements({ version }: { version: number }) {
  const settlements = useResource<Page<Settlement>>(`/v1/settlements?v=${version}`, "merchant");
  const batches = settlements.data?.data ?? [];

  return (
    <section className="screen active">
      <PageHead title="Settlements" subtitle="Batches for whatever did not go instant" />

      <div className="grid g4">
        <Stat title="Batches" value={batches.length} />
        <Stat title="Settled" value={rupiah(batches.reduce((a, b) => a + b.net, 0))} />
      </div>

      <Card className="mt4">
        <Table
          head={["Batch", "Period", "#Payouts", "#Gross", "#Fee", "#Net", "Status"]}
          loading={settlements.loading}
          error={settlements.error}
          empty="No settlement batches; every payout went instant"
        >
          {batches.map((batch) => (
            <tr key={batch.id}>
              <td className="mono">{shortId(batch.id)}</td>
              <td>
                {clock(batch.period_start)} → {clock(batch.period_end)}
              </td>
              <td className="r num">{batch.payout_count}</td>
              <td className="r num">{plain(batch.gross)}</td>
              <td className="r num">{plain(batch.fee)}</td>
              <td className="r num">{plain(batch.net)}</td>
              <td>
                <Badge status={batch.status} />
              </td>
            </tr>
          ))}
        </Table>
      </Card>
    </section>
  );
}

function Outlets({ version, refresh }: { version: number; refresh: () => void }) {
  const outlets = useResource<Page<Outlet>>(`/v1/outlets?v=${version}`, "merchant");
  const staff = useResource<Page<Staff>>(`/v1/staff?v=${version}`, "merchant");

  return (
    <section className="screen active">
      <PageHead
        title="Outlets &amp; Staff"
        actions={
          <button
            className="btn primary"
            onClick={async () => {
              await api.request("POST", "/v1/outlets", {
                role: "merchant",
                body: { name: `Outlet ${Math.floor(Math.random() * 900 + 100)}` },
              });
              refresh();
            }}
          >
            <svg>
              <use href="#i-plus" />
            </svg>
            Add outlet
          </button>
        }
      />

      <Card title="Outlets" className="mt4">
        <Table
          head={["Outlet", "NMID", "#Staff", "Status"]}
          loading={outlets.loading}
          error={outlets.error}
          empty="No outlets yet"
        >
          {(outlets.data?.data ?? []).map((outlet) => (
            <tr key={outlet.id}>
              <td>{outlet.name}</td>
              <td className="mono">{outlet.nmid || "—"}</td>
              <td className="r num">{outlet.staff_count}</td>
              <td>
                <Badge status={outlet.status} />
              </td>
            </tr>
          ))}
        </Table>
      </Card>

      <Card title="Staff" className="mt4">
        <Table
          head={["Name", "Outlet", "Role", "Last active", ""]}
          loading={staff.loading}
          error={staff.error}
          empty="No staff"
        >
          {(staff.data?.data ?? []).map((person) => (
            <tr key={person.id}>
              <td>{person.full_name}</td>
              <td>{person.outlet_id || "all outlets"}</td>
              <td>{person.role}</td>
              <td>{person.last_seen_at ? clock(person.last_seen_at) : "never"}</td>
              <td>
                <Badge status={person.revoked_at ? "revoked" : "active"} />
              </td>
            </tr>
          ))}
        </Table>
      </Card>
    </section>
  );
}

function Keys({ version, refresh }: { version: number; refresh: () => void }) {
  const keys = useResource<Page<ApiKey>>(`/v1/api-keys?v=${version}`, "merchant");
  const [created, setCreated] = useState<string | null>(null);

  return (
    <section className="screen active">
      <PageHead
        title="API Keys"
        subtitle="The secret is shown once and never again"
        actions={
          <button
            className="btn primary"
            onClick={async () => {
              const result = await api.request<ApiKey>("POST", "/v1/api-keys", {
                role: "merchant",
                body: {
                  name: `dashboard ${new Date().toISOString().slice(0, 10)}`,
                  mode: "test",
                  scopes: ["read"],
                },
              });
              if (result.ok) {
                setCreated(result.data.secret ?? null);
                refresh();
              }
            }}
          >
            <svg>
              <use href="#i-plus" />
            </svg>
            Create key
          </button>
        }
      />

      {created ? (
        <Note tone="ok">
          <strong className="mono">{created}</strong> — copy it now. Only a SHA-256 digest is
          stored, so this string cannot be shown again.
        </Note>
      ) : null}

      <Card className="mt4">
        <Table
          head={["Name", "Prefix", "Mode", "Created", "Last used", "Scopes", "Status"]}
          loading={keys.loading}
          error={keys.error}
          empty="No API keys"
        >
          {(keys.data?.data ?? []).map((key) => (
            <tr key={key.id}>
              <td>{key.name}</td>
              <td className="mono">{key.prefix}</td>
              <td>{key.mode}</td>
              <td>{clock(key.created_at)}</td>
              <td>{key.last_used_at ? clock(key.last_used_at) : "never"}</td>
              <td>{key.scopes.join(", ")}</td>
              <td>
                <Badge status={key.revoked_at ? "revoked" : "active"} />
              </td>
            </tr>
          ))}
        </Table>
      </Card>
    </section>
  );
}

function Webhooks({ version }: { version: number }) {
  const deliveries = useResource<Page<Delivery>>(
    `/v1/webhook-deliveries?limit=25&v=${version}`,
    "merchant",
  );

  const all = deliveries.data?.data ?? [];
  const delivered = all.filter((d) => d.status === "delivered");
  const latencies = all
    .map((d) => d.latency_ms)
    .filter((v): v is number => v !== null && v !== undefined)
    .sort((a, b) => a - b);

  return (
    <section className="screen active">
      <PageHead title="Webhooks" subtitle="At-least-once delivery, nine attempts, then a dead letter" />

      <div className="grid g4">
        <Stat
          title="Delivered"
          value={all.length ? `${((delivered.length / all.length) * 100).toFixed(1)}%` : "—"}
        />
        <Stat
          title="Waiting to retry"
          value={all.filter((d) => d.status === "pending" || d.status === "retrying").length}
        />
        <Stat title="Dead lettered" value={all.filter((d) => d.status === "dead_letter").length} />
        <Stat
          title="p95 endpoint response"
          value={latencies.length ? millis(latencies[Math.floor(latencies.length * 0.95)]) : "—"}
        />
      </div>

      <Card title="Deliveries" className="mt4">
        <Table
          head={["Event", "Type", "Time", "#Attempt", "Latency", "Response", "Status"]}
          loading={deliveries.loading}
          error={deliveries.error}
          empty="Nothing has been sent to this endpoint yet"
        >
          {all.map((delivery) => (
            <tr key={delivery.id}>
              <td className="mono">{shortId(delivery.event_id)}</td>
              <td>{delivery.event_type}</td>
              <td>{clock(delivery.created_at)}</td>
              <td className="r num">{delivery.attempt}</td>
              <td>{millis(delivery.latency_ms)}</td>
              <td>{delivery.response_code ?? "—"}</td>
              <td>
                <Badge status={delivery.status} />
              </td>
            </tr>
          ))}
        </Table>
      </Card>
    </section>
  );
}
