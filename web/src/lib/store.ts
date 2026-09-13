import * as api from "./api";
import type { Role } from "./api";

export type Live = { user: boolean; operator: boolean; merchant: boolean };

const OFFLINE: Live = { user: false, operator: false, merchant: false };

let snapshot: Live = OFFLINE;
const listeners = new Set<() => void>();

function announce() {
  for (const listener of listeners) listener();
}

export function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function getSnapshot(): Live {
  return snapshot;
}

export function getServerSnapshot(): Live {
  return OFFLINE;
}

export function sync() {
  const next: Live = {
    user: api.signedIn("user"),
    operator: api.signedIn("operator"),
    merchant: api.signedIn("merchant"),
  };

  if (
    next.user === snapshot.user &&
    next.operator === snapshot.operator &&
    next.merchant === snapshot.merchant
  ) {
    return;
  }

  snapshot = next;
  announce();
}

export async function signIn(
  role: Exclude<Role, "merchant">,
  phone: string,
  pin: string,
): Promise<string | null> {
  const result = await api.signIn(role, phone, pin);
  sync();
  return result.ok ? null : (result.message ?? "Sign in failed.");
}

export function signOut(role: Role) {
  if (role !== "merchant") void api.request("POST", "/v1/auth/logout", { role });
  api.forget(role);
  sync();
}

export async function applyMerchantKey(key: string): Promise<string | null> {
  if (!key.trim()) return "Paste the API key that ./scripts/demo.sh printed.";

  api.setMerchantKey(key.trim());
  const probe = await api.request("GET", "/v1/api-keys", { role: "merchant" });
  if (!probe.ok) {
    api.forget("merchant");
    sync();
    return api.describe(probe);
  }

  sync();
  return null;
}
