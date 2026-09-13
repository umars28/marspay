"use client";

import { useCallback, useEffect, useState } from "react";

import * as api from "./api";
import type { Role } from "./api";

type State<T> = {
  data: T | null;
  error: string | null;
  loading: boolean;
  reload: () => void;
};

export function useResource<T>(path: string | null, role: Role, enabled = true): State<T> {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [tick, setTick] = useState(0);

  const reload = useCallback(() => setTick((n) => n + 1), []);

  useEffect(() => {
    if (!path || !enabled) return;

    let cancelled = false;
    const timer = setTimeout(() => setLoading(true), 0);

    void api.request<T>("GET", path, { role }).then((result) => {
      if (cancelled) return;
      setLoading(false);

      if (result.ok) {
        setData(result.data);
        setError(null);
        return;
      }
      setData(null);
      setError(api.describe(result));
    });

    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [path, role, enabled, tick]);

  return { data, error, loading, reload };
}
