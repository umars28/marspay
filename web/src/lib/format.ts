export function rupiah(minor: number | null | undefined): string {
  if (minor === null || minor === undefined) return "—";
  const negative = minor < 0;
  const whole = Math.abs(Math.trunc(minor / 100));
  return `${negative ? "−" : ""}Rp ${group(whole)}`;
}

export function plain(minor: number | null | undefined): string {
  if (minor === null || minor === undefined) return "—";
  const negative = minor < 0;
  return `${negative ? "−" : ""}${group(Math.abs(Math.trunc(minor / 100)))}`;
}

function group(value: number): string {
  return String(value).replace(/\B(?=(\d{3})+(?!\d))/g, ",");
}

export function toMinor(input: string): number {
  const digits = String(input).replace(/[^\d]/g, "");
  return digits ? parseInt(digits, 10) * 100 : 0;
}

export function clock(iso: string | null | undefined): string {
  if (!iso) return "";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";

  const time = `${String(date.getHours()).padStart(2, "0")}:${String(date.getMinutes()).padStart(2, "0")}`;
  if (date.toDateString() === new Date().toDateString()) return time;

  return `${date.toLocaleDateString("en-GB", { day: "2-digit", month: "short" })} · ${time}`;
}

export function millis(value: number | null | undefined): string {
  if (value === null || value === undefined) return "—";
  return value >= 1000 ? `${(value / 1000).toFixed(1)}s` : `${value} ms`;
}

export function percent(bps: number): string {
  return `${(bps / 100).toFixed(2).replace(/\.00$/, "")}%`;
}

export function shortId(id: string | null | undefined): string {
  if (!id) return "";
  return id.length > 18 ? `${id.slice(0, 18)}…` : id;
}

export function initials(name: string): string {
  return name
    .split(/\s+/)
    .map((word) => word.charAt(0))
    .join("")
    .slice(0, 2)
    .toUpperCase();
}

const GOOD = ["succeeded", "settled", "active", "delivered", "approved", "finished", "resolved", "paid"];
const BAD = ["failed", "dead_letter", "rejected", "blocked", "expired", "cancelled"];

export function badgeTone(status: string): string {
  if (GOOD.includes(status)) return "ok";
  if (BAD.includes(status)) return "bad";
  return "pend";
}

export function label(status: string): string {
  const map: Record<string, string> = {
    succeeded: "Succeeded",
    pending: "Pending",
    failed: "Failed",
    sending: "Sending",
    settled: "Settled",
    degraded_to_batch: "Batch",
    dead_letter: "Dead letter",
  };
  return map[status] ?? status.replace(/_/g, " ");
}
