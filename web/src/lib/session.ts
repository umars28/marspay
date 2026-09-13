"use client";

import { useSyncExternalStore } from "react";

import * as store from "./store";

export function useSession() {
  const live = useSyncExternalStore(store.subscribe, store.getSnapshot, store.getServerSnapshot);

  return {
    live,
    signIn: store.signIn,
    signOut: store.signOut,
    applyMerchantKey: store.applyMerchantKey,
  };
}
