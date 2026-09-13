"use client";

import { useState } from "react";

import * as api from "@/lib/api";
import { clock, millis, plain, rupiah, shortId } from "@/lib/format";
import { useSession } from "@/lib/session";
import { useResource } from "@/lib/useResource";
import type {
  AccountView,
  AuditEntry,
  Engine,
  FloatView,
  Hit,
  LedgerView,
  Page,
  Queues,
  Reconciliation,
  Transition,
} from "@/lib/types";
import { Dashboard } from "@/components/Dashboard";
import { Badge, Card, Note, PageHead, SignedOut, Stat, Table } from "@/components/ui";

const SECTIONS = [
  { key: "search", label: "Search", icon: "i-search", group: "Support" },
  { key: "ledger", label: "Ledger", icon: "i-book", group: "Support" },
  { key: "account", label: "Account", icon: "i-user", group: "Support" },
  { key: "float", label: "Float & Liquidity", icon: "i-gauge", group: "System health" },
  { key: "engine", label: "Instant Payout Engine", icon: "i-zap", group: "System health" },
  { key: "recon", label: "Reconciliation", icon: "i-scale", group: "System health" },
  { key: "queues", label: "Jobs & Queues", icon: "i-activity", group: "System health" },
  { key: "audit", label: "Audit Log", icon: "i-file", group: "System health" },
];

export default function AdminPage() {
  const { live } = useSession();
  const [focus, setFocus] = useState<{ payment?: string; account?: string }>({});

  if (!live.operator) {
    return <Dashboard sections={SECTIONS}>{() => <SignedOut role="an operator" />}</Dashboard>;
  }

  return (
    <Dashboard sections={SECTIONS}>
      {(active) => {
        switch (active) {
          case "search":
            return <Search onOpen={setFocus} />;
          case "ledger":
            return <Ledger paymentID={focus.payment} />;
          case "account":
            return <Account accountID={focus.account} />;
          case "float":
            return <Float />;
          case "engine":
            return <EngineView />;
          case "recon":
            return <Recon />;
          case "queues":
            return <QueuesView />;
          case "audit":
            return <Audit />;
          default:
            return null;
        }
      }}
    </Dashboard>
  );
}

function Search({ onOpen }: { onOpen: (focus: { payment?: string; account?: string }) => void }) {
  const [query, setQuery] = useState("merch_demo");
  const [hits, setHits] = useState<Hit[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function run() {
    const result = await api.request<Page<Hit>>(
      "GET",
      `/internal/v1/search?q=${encodeURIComponent(query)}`,
      { role: "operator" },
    );
    if (!result.ok) {
      setError(api.describe(result));
      setHits(null);
      return;
    }
    setError(null);
    setHits(result.data.data);
  }

  return (
    <section className="screen active">
      <PageHead
        title="Search"
        subtitle="One box for users, merchants, payments or external references"
      />

      <Card>
        <div className="body">
          <div className="filters" style={{ margin: 0 }}>
            <input
              className="input mono"
              style={{ flex: 1, minWidth: 280 }}
              value={query}
              spellCheck={false}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && run()}
            />
            <button className="btn brand" onClick={run}>
              <svg>
                <use href="#i-search" />
              </svg>
              Search
            </button>
          </div>
        </div>
      </Card>

      {error ? <Note tone="warn">{error}</Note> : null}

      <Card title="Results" hint={hits ? `${hits.length} found` : undefined} className="mt4">
        <div className="queue body">
          {(hits ?? []).map((hit) => (
            <div className="qitem" key={`${hit.kind}-${hit.id}`}>
              <div className="av">
                <svg width="18" height="18">
                  <use href={`#${hit.kind === "user" ? "i-user" : hit.kind === "merchant" ? "i-store" : "i-card"}`} />
                </svg>
              </div>
              <div>
                <div className="t mono">
                  {hit.id}
                  {hit.amount !== undefined && hit.amount !== null ? ` · ${rupiah(hit.amount)}` : ""}
                </div>
                <div className="s">
                  {hit.kind} · {hit.label}
                  {hit.detail ? ` · ${hit.detail}` : ""}
                </div>
              </div>
              <div className="ops">
                <Badge status={hit.status} />
                {hit.kind === "payment" ? (
                  <button className="btn sm" onClick={() => onOpen({ payment: hit.id })}>
                    Ledger
                  </button>
                ) : null}
                {hit.kind === "user" ? (
                  <button
                    className="btn sm"
                    onClick={() => onOpen({ account: `acc_${hit.id}_user_wallet` })}
                  >
                    Wallet
                  </button>
                ) : null}
              </div>
            </div>
          ))}
          {hits && !hits.length ? (
            <div className="qitem">
              <div>
                <div className="t">Nothing matched</div>
                <div className="s">Try a payment id, a phone number or a merchant id</div>
              </div>
            </div>
          ) : null}
          {!hits ? (
            <div className="qitem">
              <div>
                <div className="t">Search for something</div>
                <div className="s">Then open its ledger or wallet from the result</div>
              </div>
            </div>
          ) : null}
        </div>
      </Card>
    </section>
  );
}

function Ledger({ paymentID }: { paymentID?: string }) {
  const view = useResource<LedgerView>(
    paymentID ? `/internal/v1/payments/${encodeURIComponent(paymentID)}/ledger` : null,
    "operator",
  );
  const transitions = useResource<Page<Transition>>(
    paymentID ? `/internal/v1/transitions?id=${encodeURIComponent(paymentID)}` : null,
    "operator",
  );

  if (!paymentID) {
    return (
      <section className="screen active">
        <PageHead title="Ledger" subtitle="Open a payment from Search to see the entries behind it" />
      </section>
    );
  }

  return (
    <section className="screen active">
      <PageHead title={paymentID} subtitle={view.data ? `${view.data.kind} · ${view.data.transaction_id}` : ""} />

      {view.data ? (
        <Note tone={view.data.balanced ? "ok" : "warn"}>
          The entries sum to <strong>{view.data.sum}</strong>.{" "}
          {view.data.balanced
            ? "The database would have refused this transaction otherwise."
            : "This should be impossible; the deferred trigger rejects unbalanced commits."}
        </Note>
      ) : null}

      <Card className="mt4">
        <Table
          head={["Entry", "Account", "Owner", "#Debit", "#Credit", "Time"]}
          loading={view.loading}
          error={view.error}
        >
          {(view.data?.entries ?? []).map((entry) => (
            <tr key={entry.id}>
              <td className="mono">{shortId(entry.id)}</td>
              <td className="mono">{entry.account_id}</td>
              <td>{entry.owner_type}</td>
              <td className="r num">{entry.amount < 0 ? plain(-entry.amount) : ""}</td>
              <td className="r num">{entry.amount > 0 ? plain(entry.amount) : ""}</td>
              <td>{clock(entry.created_at)}</td>
            </tr>
          ))}
        </Table>
      </Card>

      <Card title="How it got here" className="mt4">
        <Table
          head={["When", "From", "To", "Actor", "Reason"]}
          loading={transitions.loading}
          empty="No recorded transitions for this operation"
        >
          {(transitions.data?.data ?? []).map((step) => (
            <tr key={step.id}>
              <td>{clock(step.created_at)}</td>
              <td>{step.from_status || "—"}</td>
              <td>
                <Badge status={step.to_status} />
              </td>
              <td>{step.actor}</td>
              <td>{step.reason || "—"}</td>
            </tr>
          ))}
        </Table>
      </Card>
    </section>
  );
}

function Account({ accountID }: { accountID?: string }) {
  const view = useResource<AccountView>(
    accountID ? `/internal/v1/accounts/${encodeURIComponent(accountID)}` : null,
    "operator",
  );

  if (!accountID) {
    return (
      <section className="screen active">
        <PageHead title="Account" subtitle="Open a wallet from Search to see its ledger" />
      </section>
    );
  }

  return (
    <section className="screen active">
      <PageHead
        title={accountID}
        subtitle={view.data ? `${view.data.owner_type} · ${view.data.account_type}` : ""}
      />

      <div className="grid g4">
        <Stat title="Ledger balance" value={rupiah(view.data?.balance)} />
        <Stat title="Entries shown" value={view.data?.entries.length ?? 0} />
        <Stat title="Owner" value={view.data?.owner_id ?? view.data?.owner_type ?? "—"} />
      </div>

      <Card className="mt4">
        <Table
          head={["Entry", "Time", "#Amount", "Direction"]}
          loading={view.loading}
          error={view.error}
          empty="No entries on this account"
        >
          {(view.data?.entries ?? []).map((entry) => (
            <tr key={entry.id}>
              <td className="mono">{shortId(entry.id)}</td>
              <td>{clock(entry.created_at)}</td>
              <td className="r num">{plain(entry.amount)}</td>
              <td>
                <Badge status={entry.amount < 0 ? "pending" : "succeeded"} />
              </td>
            </tr>
          ))}
        </Table>
      </Card>
    </section>
  );
}

function Float() {
  const view = useResource<FloatView>("/internal/v1/float", "operator");
  const position = view.data?.position;
  const used = position ? position.utilisation_bps / 100 : 0;

  return (
    <section className="screen active">
      <PageHead
        title="Float &amp; Liquidity"
        subtitle="The direct consequence of instant payout: we pay merchants before the funds arrive"
      />

      <div className="keyband">
        <div className="txt">
          <h3>Current float position</h3>
          <p>
            Our own money, lent to merchants while we wait for funds from the switch and the banks.
            Above 85% utilisation instant payout degrades to batch automatically — it degrades, it
            does not stop.
          </p>
        </div>
        <div className="big">
          {rupiah(position?.outstanding)}
          <span>used of a limit of {rupiah(position?.limit)}</span>
        </div>
      </div>

      <Card title="Float utilisation" hint="automatic threshold at 85%" className="mt4">
        <div className="body">
          <div className="meterrow">
            <span>Used</span>
            <span className="n">
              {used.toFixed(1)}% · {rupiah(position?.outstanding)} of {rupiah(position?.limit)}
            </span>
          </div>
          <div className={`meter ${used > 85 ? "bad" : "ok"}`}>
            <i style={{ width: `${Math.min(100, used).toFixed(1)}%` }} />
          </div>
          <div className="grid g4 mt5">
            <div>
              <div className="small muted">Headroom</div>
              <div className="num strong">{rupiah(position?.headroom)}</div>
            </div>
            <div>
              <div className="small muted">Instant payout</div>
              <div className="num strong">{position?.instant_enabled ? "enabled" : "degraded"}</div>
            </div>
          </div>
        </div>
      </Card>

      <Card title="Exposure per merchant" hint="paid out, not yet collected" className="mt4">
        <Table
          head={["Merchant", "#Exposure", "#Payouts", "Status"]}
          loading={view.loading}
          error={view.error}
          empty="Nothing is outstanding; no instant payout has been sent"
        >
          {(view.data?.top_exposure ?? []).map((row) => (
            <tr key={row.merchant_id}>
              <td>{row.display_name}</td>
              <td className="r num">{plain(row.outstanding)}</td>
              <td className="r num">{row.payouts}</td>
              <td>
                <Badge status={position?.instant_enabled ? "active" : "pending"} />
              </td>
            </tr>
          ))}
        </Table>
      </Card>
    </section>
  );
}

function EngineView() {
  const engine = useResource<Engine>("/internal/v1/payouts/engine", "operator");
  const data = engine.data;

  return (
    <section className="screen active">
      <PageHead title="Instant Payout Engine" subtitle={data ? `window ${data.window}` : ""} />

      <div className="grid g4">
        <Stat title="Instant payouts" value={data?.instant ?? 0} />
        <Stat title="p95 latency" value={millis(data?.p95_latency_ms)} detail={`p99 ${millis(data?.p99_latency_ms)}`} />
        <Stat title="Degraded to batch" value={data?.degraded_to_batch ?? 0} />
        <Stat title="Failed" value={data?.failed ?? 0} detail={`${data?.sending ?? 0} in flight`} />
      </div>

      <Card title="Rail health" className="mt4">
        <Table
          head={["Rail", "#Sent", "#Settled", "#Failed", "p95", "Status"]}
          loading={engine.loading}
          error={engine.error}
          empty="No payout has chosen a rail yet"
        >
          {(data?.rails ?? []).map((rail) => (
            <tr key={rail.rail}>
              <td>{rail.rail}</td>
              <td className="r num">{rail.sent}</td>
              <td className="r num">{rail.settled}</td>
              <td className="r num">{rail.failed}</td>
              <td>{millis(rail.p95_latency_ms)}</td>
              <td>
                <Badge status={rail.failed > 0 ? "pending" : "active"} />
              </td>
            </tr>
          ))}
        </Table>
      </Card>

      <Card title="Needing attention" hint="sending or failed" className="mt4">
        <Table
          head={["Payout", "Merchant", "#Net", "Cause", "Status"]}
          loading={engine.loading}
          empty="Nothing is stuck"
        >
          {(data?.needing_attention ?? []).map((payout) => (
            <tr key={payout.id}>
              <td className="mono">{shortId(payout.id)}</td>
              <td>{payout.merchant_id}</td>
              <td className="r num">{plain(payout.net)}</td>
              <td>{payout.status.replace(/_/g, " ")}</td>
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

function Recon() {
  const recon = useResource<Reconciliation>("/internal/v1/reconciliation?limit=25", "operator");
  const latest = recon.data?.runs[0];

  return (
    <section className="screen active">
      <PageHead
        title="Reconciliation"
        subtitle="Our ledger against what the provider says happened"
      />

      <div className="grid g4">
        <Stat title="Rows compared" value={latest?.rows_compared ?? 0} />
        <Stat title="Matched" value={latest?.rows_matched ?? 0} />
        <Stat title="Differences" value={latest?.discrepancies ?? 0} />
        <Stat title="Delta" value={rupiah(latest?.delta)} />
      </div>

      <Card title="Open differences" className="mt4">
        <Table
          head={["Reference", "Run", "#Internal", "#Provider", "#Delta", "Suspected cause"]}
          loading={recon.loading}
          error={recon.error}
          empty="Every row matched; nothing is open"
        >
          {(recon.data?.open ?? []).map((row) => (
            <tr key={row.id}>
              <td className="mono">{row.external_ref}</td>
              <td className="mono">{shortId(row.run_id)}</td>
              <td className="r num">{row.internal === undefined || row.internal === null ? "—" : plain(row.internal)}</td>
              <td className="r num">{row.provider === undefined || row.provider === null ? "—" : plain(row.provider)}</td>
              <td className="r num">{plain(row.delta)}</td>
              <td>{row.suspected_cause || "unknown"}</td>
            </tr>
          ))}
        </Table>
      </Card>

      <Card title="Runs" className="mt4">
        <Table head={["Date", "Provider", "#Compared", "#Differences", "Status"]} loading={recon.loading}>
          {(recon.data?.runs ?? []).map((run) => (
            <tr key={run.id}>
              <td>{run.business_date?.slice(0, 10)}</td>
              <td>{run.provider_code}</td>
              <td className="r num">{run.rows_compared}</td>
              <td className="r num">{run.discrepancies}</td>
              <td>
                <Badge status={run.status} />
              </td>
            </tr>
          ))}
        </Table>
      </Card>
    </section>
  );
}

function QueuesView() {
  const queues = useResource<Queues>("/internal/v1/queues", "operator");
  const transitions = useResource<Page<Transition>>("/internal/v1/transitions?limit=25", "operator");

  const list = queues.data?.queues ?? [];
  const jobs = queues.data?.jobs ?? [];

  return (
    <section className="screen active">
      <PageHead
        title="Jobs &amp; Queues"
        subtitle="The queues this system actually has, not the ones a diagram wishes for"
      />

      <div className="grid g4">
        <Stat title="Deepest queue" value={list.reduce((a, q) => Math.max(a, q.waiting), 0)} />
        <Stat title="Failed or dead lettered" value={list.reduce((a, q) => a + q.failed, 0)} />
        <Stat title="Scheduled work due" value={jobs.reduce((a, j) => a + j.pending, 0)} />
        <Stat
          title="Oldest thing waiting"
          value={millis(
            Math.round(list.reduce((a, q) => Math.max(a, q.lag_seconds ?? 0), 0) * 1000) || null,
          )}
        />
      </div>

      <Card title="Queues" className="mt4">
        <Table
          head={["Queue", "Kind", "What it carries", "#Waiting", "#Done", "Status"]}
          loading={queues.loading}
          error={queues.error}
        >
          {list.map((queue) => (
            <tr key={queue.name}>
              <td className="mono">{queue.name}</td>
              <td>{queue.kind}</td>
              <td>{queue.detail}</td>
              <td className="r num">{queue.waiting}</td>
              <td className="r num">{queue.done}</td>
              <td>
                <Badge status={queue.failed > 0 ? "failed" : queue.waiting > 0 ? "pending" : "active"} />
              </td>
            </tr>
          ))}
        </Table>
      </Card>

      <Card title="Scheduled work" className="mt4">
        <Table head={["Job", "Schedule", "Last run", "Outcome", "#Pending"]} loading={queues.loading}>
          {jobs.map((job) => (
            <tr key={job.name}>
              <td>{job.name}</td>
              <td>{job.schedule}</td>
              <td>{job.last_run ? clock(job.last_run) : "never"}</td>
              <td>{job.outcome}</td>
              <td className="r num">{job.pending}</td>
            </tr>
          ))}
        </Table>
      </Card>

      <Card title="Recent state changes" hint="every status change, with who and why" className="mt4">
        <Table
          head={["When", "Operation", "From", "To", "Actor", "Reason"]}
          loading={transitions.loading}
          empty="Nothing has changed state yet"
        >
          {(transitions.data?.data ?? []).map((step) => (
            <tr key={step.id}>
              <td>{clock(step.created_at)}</td>
              <td className="mono">
                {step.operation_kind} {shortId(step.operation_id)}
              </td>
              <td>{step.from_status || "—"}</td>
              <td>
                <Badge status={step.to_status} />
              </td>
              <td>{step.actor}</td>
              <td>{step.reason || "—"}</td>
            </tr>
          ))}
        </Table>
      </Card>
    </section>
  );
}

function Audit() {
  const audit = useResource<Page<AuditEntry>>("/internal/v1/audit?limit=40", "operator");

  return (
    <section className="screen active">
      <PageHead title="Audit Log" subtitle="Append-only; readable by everyone, writable by no one" />

      <Card className="mt4">
        <Table
          head={["Time", "Actor", "Action", "Object", "Reason", "IP"]}
          loading={audit.loading}
          error={audit.error}
          empty="The audit log is empty"
        >
          {(audit.data?.data ?? []).map((entry) => (
            <tr key={entry.id}>
              <td>{clock(entry.created_at)}</td>
              <td>{entry.actor}</td>
              <td>{entry.action}</td>
              <td className="mono">
                {entry.object_type} {shortId(entry.object_id)}
              </td>
              <td>{entry.reason || "—"}</td>
              <td>{entry.ip || "—"}</td>
            </tr>
          ))}
        </Table>
      </Card>
    </section>
  );
}
