"use client";

import type { ReactNode } from "react";

import { badgeTone, label } from "@/lib/format";

export function Badge({ status }: { status: string }) {
  return <span className={`badge ${badgeTone(status)}`}>{label(status)}</span>;
}

export function Stat({
  title,
  value,
  detail,
}: {
  title: string;
  value: ReactNode;
  detail?: ReactNode;
}) {
  return (
    <div className="stat">
      <div className="k">{title}</div>
      <div className="v">{value}</div>
      {detail ? <div className="d">{detail}</div> : null}
    </div>
  );
}

export function Card({
  title,
  hint,
  actions,
  children,
  className,
}: {
  title?: string;
  hint?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={`card${className ? " " + className : ""}`}>
      {title ? (
        <header>
          <h3>{title}</h3>
          {hint ? <span className="hint">{hint}</span> : null}
          {actions ? <div className="actions">{actions}</div> : null}
        </header>
      ) : null}
      {children}
    </div>
  );
}

export function Table({
  head,
  children,
  empty,
  loading,
  error,
}: {
  head: ReactNode[];
  children: ReactNode;
  empty?: string;
  loading?: boolean;
  error?: string | null;
}) {
  const columns = head.length;
  const rows = Array.isArray(children) ? children : [children];
  const blank = rows.flat().filter(Boolean).length === 0;

  return (
    <div className="tablewrap">
      <table>
        <thead>
          <tr>
            {head.map((cell, i) => (
              <th key={i} className={typeof cell === "string" && cell.startsWith("#") ? "r" : ""}>
                {typeof cell === "string" && cell.startsWith("#") ? cell.slice(1) : cell}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {error ? (
            <tr>
              <td colSpan={columns} className="muted">
                {error}
              </td>
            </tr>
          ) : loading ? (
            <tr>
              <td colSpan={columns} className="muted">
                Loading…
              </td>
            </tr>
          ) : blank ? (
            <tr>
              <td colSpan={columns} className="muted">
                {empty ?? "Nothing to show"}
              </td>
            </tr>
          ) : (
            children
          )}
        </tbody>
      </table>
    </div>
  );
}

export function Note({
  tone = "info",
  children,
}: {
  tone?: "info" | "ok" | "warn";
  children: ReactNode;
}) {
  return (
    <div className={`note ${tone} mt4`}>
      <div>{children}</div>
    </div>
  );
}

export function PageHead({
  title,
  subtitle,
  actions,
}: {
  title: string;
  subtitle?: string;
  actions?: ReactNode;
}) {
  return (
    <div className="pagehead">
      <div>
        <h2>{title}</h2>
        {subtitle ? <p>{subtitle}</p> : null}
      </div>
      {actions ? <div className="actions">{actions}</div> : null}
    </div>
  );
}

export function SignedOut({ role }: { role: string }) {
  return (
    <div className="offline">
      Sign in as {role} from the panel in the top right to see live data here.
    </div>
  );
}
