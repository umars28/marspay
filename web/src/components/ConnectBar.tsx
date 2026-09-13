"use client";

import { useState } from "react";
import type { ReactNode } from "react";

import * as api from "@/lib/api";
import { useSession } from "@/lib/session";

type Note = { text: string; tone: "" | "ok" | "bad" };

const QUIET: Note = { text: "", tone: "" };

export function ConnectBar({ tabs }: { tabs: ReactNode }) {
  const { live, signIn, signOut, applyMerchantKey } = useSession();
  const [open, setOpen] = useState(false);
  const [base, setBase] = useState(() => api.baseURL());

  const [consumer, setConsumer] = useState({ phone: "081200000001", pin: "294715" });
  const [operator, setOperator] = useState({ phone: "081200000009", pin: "294715" });
  const [merchantKey, setMerchantKey] = useState("");

  const [apiNote, setApiNote] = useState<Note>({
    text: "Run ./scripts/demo.sh, then connect.",
    tone: "",
  });
  const [consumerNote, setConsumerNote] = useState<Note>(QUIET);
  const [operatorNote, setOperatorNote] = useState<Note>(QUIET);
  const [merchantNote, setMerchantNote] = useState<Note>({
    text: "A real dashboard would hold a session, not a raw API key.",
    tone: "",
  });

  const connected = live.user || live.operator || live.merchant;
  const parts = [
    live.user && "consumer",
    live.merchant && "merchant",
    live.operator && "operator",
  ].filter(Boolean);

  async function check() {
    const health = await api.request("GET", "/healthz");
    setApiNote(
      health.ok
        ? { text: "Reachable.", tone: "ok" }
        : { text: api.describe(health), tone: "bad" },
    );
  }

  return (
    <>
      <header className="topbar">
        <div className="brandmark">
          <span className="dot">M</span> Marspay
        </div>
        {tabs}
        <div className="spacer" />
        <button className="env" aria-expanded={open} onClick={() => setOpen(!open)}>
          <span className={`statusdot${connected ? " on" : ""}`} />
          {connected ? `Live · ${parts.join(", ")}` : "Sample data"}
        </button>
      </header>

      <div className="connectbar" hidden={!open}>
        <div className="cbhead">
          Connect this interface to a running API. Nothing here leaves your machine.
        </div>

        <div className="cbrow">
          <label htmlFor="api-base">API</label>
          <input
            id="api-base"
            className="wide"
            value={base}
            spellCheck={false}
            onChange={(e) => setBase(e.target.value)}
            onBlur={(e) => api.setBaseURL(e.target.value)}
          />
          <button className="btn sm" onClick={check}>
            Check
          </button>
          <span className={`cbnote ${apiNote.tone}`}>{apiNote.text}</span>
        </div>

        <div className="cbrow">
          <label>Consumer</label>
          <input
            value={consumer.phone}
            onChange={(e) => setConsumer({ ...consumer, phone: e.target.value })}
          />
          <input
            type="password"
            value={consumer.pin}
            onChange={(e) => setConsumer({ ...consumer, pin: e.target.value })}
          />
          {live.user ? (
            <button
              className="btn sm"
              onClick={() => {
                signOut("user");
                setConsumerNote({ text: "Signed out. Showing sample data again.", tone: "" });
              }}
            >
              Sign out
            </button>
          ) : (
            <button
              className="btn sm primary"
              onClick={async () => {
                setConsumerNote({ text: "Signing in…", tone: "" });
                const failed = await signIn("user", consumer.phone, consumer.pin);
                setConsumerNote(
                  failed
                    ? { text: failed, tone: "bad" }
                    : { text: "Signed in. The consumer screens are live.", tone: "ok" },
                );
              }}
            >
              Sign in
            </button>
          )}
          <span className={`cbnote ${consumerNote.tone}`}>{consumerNote.text}</span>
        </div>

        <div className="cbrow">
          <label>Operator</label>
          <input
            value={operator.phone}
            onChange={(e) => setOperator({ ...operator, phone: e.target.value })}
          />
          <input
            type="password"
            value={operator.pin}
            onChange={(e) => setOperator({ ...operator, pin: e.target.value })}
          />
          {live.operator ? (
            <button
              className="btn sm"
              onClick={() => {
                signOut("operator");
                setOperatorNote({ text: "Signed out.", tone: "" });
              }}
            >
              Sign out
            </button>
          ) : (
            <button
              className="btn sm primary"
              onClick={async () => {
                setOperatorNote({ text: "Signing in…", tone: "" });
                const failed = await signIn("operator", operator.phone, operator.pin);
                setOperatorNote(
                  failed
                    ? { text: failed, tone: "bad" }
                    : { text: "Signed in as an operator.", tone: "ok" },
                );
              }}
            >
              Sign in
            </button>
          )}
          <span className={`cbnote ${operatorNote.tone}`}>{operatorNote.text}</span>
        </div>

        <div className="cbrow">
          <label>Merchant</label>
          <input
            className="wide"
            type="password"
            placeholder="mp_test_…"
            value={merchantKey}
            spellCheck={false}
            onChange={(e) => setMerchantKey(e.target.value)}
          />
          {live.merchant ? (
            <button
              className="btn sm"
              onClick={() => {
                signOut("merchant");
                setMerchantNote({
                  text: "A real dashboard would hold a session, not a raw API key.",
                  tone: "",
                });
              }}
            >
              Clear
            </button>
          ) : (
            <button
              className="btn sm primary"
              onClick={async () => {
                const failed = await applyMerchantKey(merchantKey);
                setMerchantNote(
                  failed
                    ? { text: failed, tone: "bad" }
                    : { text: "Key accepted. The merchant screens are live.", tone: "ok" },
                );
              }}
            >
              Use key
            </button>
          )}
          <span className={`cbnote ${merchantNote.tone}`}>{merchantNote.text}</span>
        </div>
      </div>
    </>
  );
}
