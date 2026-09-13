export type Role = "user" | "operator" | "merchant";

export type Failure = {
  ok: false;
  status: number;
  type: string;
  message: string;
  requestId?: string;
  retryAfter?: string;
  details?: Record<string, unknown> | null;
  offline?: boolean;
};

export type Success<T> = { ok: true; status: number; data: T };
export type Result<T> = Success<T> | Failure;

export type Tokens = {
  session_id: string;
  device_id: string;
  access_token: string;
  refresh_token: string;
  access_expires_at: string;
  refresh_expires_at: string;
  new_device: boolean;
};

const STORE = "marspay.ui";
const BASE_KEY = `${STORE}.base`;
const DEFAULT_BASE = "http://127.0.0.1:8080";

type Stored = {
  base?: string;
  user?: Tokens;
  operator?: Tokens;
  merchantKey?: string;
};

function read(): Stored {
  if (typeof window === "undefined") return {};
  try {
    return JSON.parse(sessionStorage.getItem(STORE) ?? "{}") as Stored;
  } catch {
    return {};
  }
}

function write(state: Stored) {
  if (typeof window === "undefined") return;
  sessionStorage.setItem(STORE, JSON.stringify(state));
}

export function baseURL(): string {
  if (typeof window === "undefined") return DEFAULT_BASE;
  return read().base ?? localStorage.getItem(BASE_KEY) ?? DEFAULT_BASE;
}

export function setBaseURL(url: string) {
  const trimmed = url.replace(/\/+$/, "");
  const state = read();
  state.base = trimmed;
  write(state);
  localStorage.setItem(BASE_KEY, trimmed);
}

export function credential(role: Role): string {
  const state = read();
  if (role === "merchant") return state.merchantKey ?? "";
  return state[role]?.access_token ?? "";
}

export function signedIn(role: Role): boolean {
  return credential(role) !== "";
}

export function setSession(role: Exclude<Role, "merchant">, tokens: Tokens) {
  const state = read();
  state[role] = tokens;
  write(state);
}

export function setMerchantKey(key: string) {
  const state = read();
  state.merchantKey = key;
  write(state);
}

export function forget(role: Role) {
  const state = read();
  if (role === "merchant") delete state.merchantKey;
  else delete state[role];
  write(state);
}

function idempotencyKey(path: string): string {
  const random = Math.random().toString(36).slice(2, 10);
  return `ui-${path.replace(/[^a-z]+/gi, "-")}-${Date.now()}-${random}`;
}

type Options = {
  role?: Role;
  body?: unknown;
  idem?: string;
};

async function send<T>(method: string, path: string, options: Options): Promise<Result<T>> {
  const headers: Record<string, string> = { Accept: "application/json" };
  const token = options.role ? credential(options.role) : "";

  if (token) headers.Authorization = `Bearer ${token}`;
  if (options.body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET") headers["Idempotency-Key"] = options.idem ?? idempotencyKey(path);

  let response: Response;
  try {
    response = await fetch(baseURL() + path, {
      method,
      headers,
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
    });
  } catch {
    return {
      ok: false,
      status: 0,
      type: "offline",
      offline: true,
      message: `The API is not reachable at ${baseURL()}.`,
    };
  }

  let payload: unknown = null;
  if (response.status !== 204) {
    const text = await response.text();
    if (text) {
      try {
        payload = JSON.parse(text);
      } catch {
        payload = { raw: text };
      }
    }
  }

  if (response.ok) return { ok: true, status: response.status, data: payload as T };

  const envelope = (payload as { error?: Record<string, unknown> } | null)?.error ?? {};
  return {
    ok: false,
    status: response.status,
    type: (envelope.type as string) ?? "error",
    message: (envelope.message as string) ?? `Request failed with status ${response.status}.`,
    requestId: (envelope.request_id as string) ?? response.headers.get("X-Request-Id") ?? undefined,
    retryAfter: response.headers.get("Retry-After") ?? undefined,
    details: (envelope.details as Record<string, unknown>) ?? null,
  };
}

export async function request<T>(
  method: string,
  path: string,
  options: Options = {},
): Promise<Result<T>> {
  const first = await send<T>(method, path, options);
  const refreshable = options.role && options.role !== "merchant";

  if (first.ok || first.status !== 401 || !refreshable) return first;

  const session = read()[options.role as Exclude<Role, "merchant">];
  if (!session?.refresh_token) return first;

  const rotated = await send<Tokens>("POST", "/v1/auth/refresh", {
    body: { refresh_token: session.refresh_token },
  });
  if (!rotated.ok) {
    forget(options.role!);
    return first;
  }

  setSession(options.role as Exclude<Role, "merchant">, rotated.data);
  return send<T>(method, path, options);
}

export async function signIn(
  role: Exclude<Role, "merchant">,
  phone: string,
  pin: string,
): Promise<{ ok: boolean; message?: string }> {
  const challenge = await request<{ id: string; code?: string }>("POST", "/v1/auth/otp", {
    body: { phone },
  });
  if (!challenge.ok) return { ok: false, message: describe(challenge) };

  if (!challenge.data.code) {
    return {
      ok: false,
      message:
        "The API did not return the code. Start it with MARSPAY_REVEAL_OTP=true, or use ./scripts/demo.sh.",
    };
  }

  const tokens = await request<Tokens>("POST", "/v1/auth/token", {
    body: {
      challenge_id: challenge.data.id,
      code: challenge.data.code,
      pin,
      platform: "web",
      model: "Marspay web",
    },
  });
  if (!tokens.ok) return { ok: false, message: describe(tokens) };

  setSession(role, tokens.data);
  return { ok: true };
}

export function describe(failure: Failure): string {
  if (failure.offline) return failure.message;
  return failure.requestId ? `${failure.message} (${failure.requestId})` : failure.message;
}
