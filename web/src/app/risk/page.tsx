"use client";

import { useState } from "react";

import * as api from "@/lib/api";
import { clock, percent, plain, rupiah, shortId } from "@/lib/format";
import { useSession } from "@/lib/session";
import { useResource } from "@/lib/useResource";
import type { Alert, AlertPage, Block, Dispute, MerchantScore, Page, Rule, Submission } from "@/lib/types";
import { Dashboard } from "@/components/Dashboard";
import { Badge, Card, Note, PageHead, SignedOut, Stat, Table } from "@/components/ui";

const SECTIONS = [
  { key: "kyc", label: "KYC Review", icon: "i-shield", group: "Queues" },
  { key: "disputes", label: "Disputes", icon: "i-scale", group: "Queues" },
  { key: "alerts", label: "Fraud Alerts", icon: "i-alert", group: "Monitoring" },
  { key: "rules", label: "Velocity Rules", icon: "i-activity", group: "Monitoring" },
  { key: "blocked", label: "Blocked Accounts", icon: "i-ban", group: "Monitoring" },
  { key: "merchant", label: "Merchant Risk", icon: "i-store", group: "Monitoring" },
];

export default function RiskPage() {
  const { live } = useSession();

  if (!live.operator) {
    return <Dashboard sections={SECTIONS}>{() => <SignedOut role="an operator" />}</Dashboard>;
  }

  return (
    <Dashboard sections={SECTIONS}>
      {(active) => {
        switch (active) {
          case "kyc":
            return <KYC />;
          case "disputes":
            return <Disputes />;
          case "alerts":
            return <Alerts />;
          case "rules":
            return <Rules />;
          case "blocked":
            return <Blocked />;
          case "merchant":
            return <MerchantRisk />;
          default:
            return null;
        }
      }}
    </Dashboard>
  );
}

function ReasonBar({
  value,
  onChange,
  placeholder,
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
}) {
  return (
    <div className="note info mt4">
      <div>
        Privileged actions need a written reason. It is stored in the audit log beside the actor
        and cannot be edited later.
        <input
          className="input mt3"
          style={{ width: "100%" }}
          placeholder={placeholder}
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      </div>
    </div>
  );
}

function useReasoned(reload: () => void) {
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function act(id: string, path: string, body: Record<string, unknown>) {
    if (!reason.trim()) {
      setError("A reason is required before this action is accepted.");
      return;
    }

    setBusy(id);
    setError(null);
    const result = await api.request("POST", path, {
      role: "operator",
      body: { ...body, reason: reason.trim() },
    });
    setBusy(null);

    if (!result.ok) {
      setError(api.describe(result));
      return;
    }
    setReason("");
    reload();
  }

  return { reason, setReason, busy, error, act };
}

function KYC() {
  const queue = useResource<Page<Submission>>("/internal/v1/kyc", "operator");
  const { reason, setReason, busy, error, act } = useReasoned(queue.reload);
  const rows = queue.data?.data ?? [];

  return (
    <section className="screen active">
      <PageHead title="KYC Review" subtitle="24 hour SLA; what the automated check could not decide" />

      <div className="grid g4">
        <Stat title="Waiting" value={rows.length} />
        <Stat title="Past SLA" value={rows.filter((r) => r.overdue).length} />
      </div>

      <ReasonBar value={reason} onChange={setReason} placeholder="Why this decision" />
      {error ? <Note tone="warn">{error}</Note> : null}

      <Card title="Queue" className="mt4">
        <div className="queue body">
          {rows.map((submission) => (
            <div className="qitem" key={submission.id}>
              <div className="av">{submission.user_id.slice(-2).toUpperCase()}</div>
              <div>
                <div className="t">{submission.user_id}</div>
                <div className="s">
                  Upgrade to {submission.target_tier}
                  {submission.match_score !== undefined && submission.match_score !== null
                    ? ` · selfie match ${submission.match_score}`
                    : ""}
                  {submission.overdue ? " · past SLA" : ""} · {clock(submission.submitted_at)}
                </div>
              </div>
              <div className="ops">
                {(["rejected", "resubmit", "approved"] as const).map((decision) => (
                  <button
                    key={decision}
                    className={`btn sm${decision === "approved" ? " brand" : decision === "rejected" ? " danger" : ""}`}
                    disabled={busy === submission.id}
                    onClick={() =>
                      act(submission.id, `/internal/v1/kyc/${submission.id}/review`, { decision })
                    }
                  >
                    {decision === "resubmit" ? "Ask again" : decision === "approved" ? "Approve" : "Decline"}
                  </button>
                ))}
              </div>
            </div>
          ))}
          {!queue.loading && !rows.length ? (
            <div className="qitem">
              <div>
                <div className="t">The queue is empty</div>
                <div className="s">Nothing is waiting for review</div>
              </div>
            </div>
          ) : null}
        </div>
      </Card>
    </section>
  );
}

function Disputes() {
  const disputes = useResource<Page<Dispute>>("/internal/v1/disputes", "operator");
  const { reason, setReason, busy, error, act } = useReasoned(disputes.reload);
  const rows = disputes.data?.data ?? [];

  return (
    <section className="screen active">
      <PageHead title="Disputes" subtitle="Who absorbs the loss is decided here" />

      <div className="grid g4">
        <Stat title="Open" value={rows.length} />
        <Stat title="Past SLA" value={rows.filter((d) => d.overdue).length} />
        <Stat title="Value" value={rupiah(rows.reduce((a, d) => a + d.amount, 0))} />
        <Stat title="Platform loss" value={rupiah(rows.reduce((a, d) => a + d.platform_loss, 0))} />
      </div>

      <ReasonBar value={reason} onChange={setReason} placeholder="Why this outcome" />
      {error ? <Note tone="warn">{error}</Note> : null}

      <Card className="mt4">
        <Table
          head={["ID", "Payment", "User", "Merchant", "#Amount", "Reason", "Covered", "Status", ""]}
          loading={disputes.loading}
          error={disputes.error}
          empty="No disputes are open"
        >
          {rows.map((dispute) => (
            <tr key={dispute.id}>
              <td className="mono">{shortId(dispute.id)}</td>
              <td className="mono">{shortId(dispute.payment_id)}</td>
              <td>{dispute.user_id}</td>
              <td>{dispute.merchant_id}</td>
              <td className="r num">{plain(dispute.amount)}</td>
              <td>{dispute.reason}</td>
              <td>
                {dispute.covered_by_holdback >= dispute.amount
                  ? "fully"
                  : dispute.covered_by_holdback > 0
                    ? "partly"
                    : "no"}
              </td>
              <td>
                <Badge status={dispute.status} />
              </td>
              <td>
                <button
                  className="btn sm brand"
                  disabled={busy === dispute.id}
                  onClick={() =>
                    act(dispute.id, `/internal/v1/disputes/${dispute.id}/resolve`, {
                      outcome: "resolved_user",
                    })
                  }
                >
                  Refund user
                </button>{" "}
                <button
                  className="btn sm"
                  disabled={busy === dispute.id}
                  onClick={() =>
                    act(dispute.id, `/internal/v1/disputes/${dispute.id}/resolve`, {
                      outcome: "resolved_merchant",
                    })
                  }
                >
                  For merchant
                </button>
              </td>
            </tr>
          ))}
        </Table>
      </Card>
    </section>
  );
}

function Alerts() {
  const alerts = useResource<AlertPage>("/internal/v1/velocity/alerts?limit=40", "operator");

  return (
    <section className="screen active">
      <PageHead
        title="Fraud Alerts"
        subtitle="Raised by velocity rules; high severity freezes the account automatically"
      />

      <div className="grid g4">
        <Stat title="Unreviewed" value={alerts.data?.open ?? 0} />
        <Stat title="High severity" value={alerts.data?.high_severity ?? 0} />
        <Stat title="Last 24 hours" value={alerts.data?.last_24h ?? 0} />
      </div>

      <Card className="mt4">
        <Table
          head={["Time", "Account", "Rule", "What was detected", "#Value", "Severity", "Automatic action"]}
          loading={alerts.loading}
          error={alerts.error}
          empty="No rule has tripped"
        >
          {(alerts.data?.data ?? []).map((alert: Alert) => (
            <tr key={alert.id}>
              <td>{clock(alert.created_at)}</td>
              <td>{alert.subject_name || alert.subject_id}</td>
              <td className="mono">{alert.rule_code}</td>
              <td>{alert.detail}</td>
              <td className="r num">
                {alert.observed === undefined || alert.observed === null ? "—" : plain(alert.observed)}
              </td>
              <td>
                <Badge status={alert.severity === "high" ? "failed" : "pending"} />
              </td>
              <td>{alert.auto_action || "none"}</td>
            </tr>
          ))}
        </Table>
      </Card>
    </section>
  );
}

function Rules() {
  const rules = useResource<Page<Rule>>("/internal/v1/velocity/rules", "operator");

  return (
    <section className="screen active">
      <PageHead
        title="Velocity Rules"
        subtitle="Monitor mode watches and records; active mode blocks"
      />

      <Card className="mt4">
        <Table
          head={["Code", "Condition", "Window", "#Threshold", "Action", "#Trips 7d", "Mode"]}
          loading={rules.loading}
          error={rules.error}
          empty="No rules loaded"
        >
          {(rules.data?.data ?? []).map((rule) => (
            <tr key={rule.code}>
              <td className="mono">{rule.code}</td>
              <td>{rule.description}</td>
              <td>
                {rule.window_seconds >= 3600
                  ? `${rule.window_seconds / 3600} h`
                  : `${rule.window_seconds / 60} min`}
              </td>
              <td className="r num">{rule.threshold}</td>
              <td>{rule.action.replace(/_/g, " ")}</td>
              <td className="r num">{rule.trips_7d}</td>
              <td>
                <Badge status={rule.mode === "active" ? "active" : "pending"} />
              </td>
            </tr>
          ))}
        </Table>
      </Card>
    </section>
  );
}

function Blocked() {
  const blocks = useResource<Page<Block>>("/internal/v1/blocks", "operator");
  const { reason, setReason, busy, error, act } = useReasoned(blocks.reload);

  return (
    <section className="screen active">
      <PageHead title="Blocked Accounts" subtitle="Every block and unblock carries a written reason" />

      <ReasonBar value={reason} onChange={setReason} placeholder="Why this account is being unblocked" />
      {error ? <Note tone="warn">{error}</Note> : null}

      <Card className="mt4">
        <Table
          head={["Account", "Blocked", "By", "Reason", "#Balance held", "Appeal", ""]}
          loading={blocks.loading}
          error={blocks.error}
          empty="Nothing is blocked"
        >
          {(blocks.data?.data ?? []).map((block) => (
            <tr key={block.id}>
              <td className="mono">
                {block.subject_type} {block.subject_id}
              </td>
              <td>{clock(block.created_at)}</td>
              <td>{block.blocked_by}</td>
              <td>{block.reason}</td>
              <td className="r num">{plain(block.balance_held)}</td>
              <td>{block.appeal_status || "—"}</td>
              <td>
                {block.lifted_at ? null : (
                  <button
                    className="btn sm brand"
                    disabled={busy === block.id}
                    onClick={() =>
                      act(
                        block.id,
                        `/internal/v1/blocks/${block.subject_type}/${block.subject_id}/unblock`,
                        {},
                      )
                    }
                  >
                    Unblock
                  </button>
                )}
              </td>
            </tr>
          ))}
        </Table>
      </Card>
    </section>
  );
}

function MerchantRisk() {
  const [merchantID, setMerchantID] = useState("merch_demo");
  const score = useResource<MerchantScore>(
    `/internal/v1/merchants/${encodeURIComponent(merchantID)}/score`,
    "operator",
  );
  const current = score.data?.current;

  return (
    <section className="screen active">
      <PageHead
        title="Merchant Risk"
        subtitle="A merchant is entitled to know why their money is being held"
        actions={
          <input
            className="input mono"
            value={merchantID}
            spellCheck={false}
            onChange={(e) => setMerchantID(e.target.value)}
          />
        }
      />

      {score.error ? <Note tone="warn">{score.error}</Note> : null}

      <div className="grid g4">
        <Stat title="Score" value={current?.score ?? "—"} />
        <Stat title="Holdback" value={current ? percent(current.holdback_bps) : "—"} />
        <Stat title="Payout mode" value={current?.mode ?? "—"} />
        <Stat title="Recorded changes" value={score.data?.history.length ?? 0} />
      </div>

      {current ? (
        <Card title="What the score is made of" className="mt4">
          <div className="body">
            <dl className="kv">
              {Object.entries(current.components).map(([factor, value]) => (
                <div key={factor} style={{ display: "contents" }}>
                  <dt>{factor.replace(/_/g, " ")}</dt>
                  <dd className="num">{value}</dd>
                </div>
              ))}
            </dl>
          </div>
        </Card>
      ) : null}

      <Card title="History" hint="written only when the score changes" className="mt4">
        <Table head={["When", "#Score", "#Holdback"]} loading={score.loading} empty="No history yet">
          {(score.data?.history ?? []).map((point, i) => (
            <tr key={i}>
              <td>{clock(point.computed_at)}</td>
              <td className="r num">{point.score}</td>
              <td className="r num">{percent(point.holdback_bps)}</td>
            </tr>
          ))}
        </Table>
      </Card>
    </section>
  );
}
