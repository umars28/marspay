"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

import { useEffect } from "react";

import * as store from "@/lib/store";
import { ConnectBar } from "./ConnectBar";
import { Icons } from "./Icons";

const ROLES = [
  { href: "/", label: "Consumer" },
  { href: "/merchant", label: "Merchant" },
  { href: "/admin", label: "Admin / Ops" },
  { href: "/risk", label: "Risk / Compliance" },
];

export function Shell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();

  useEffect(() => {
    store.sync();
  }, []);

  return (
    <>
      <Icons />
      <ConnectBar
        tabs={
          <nav className="rolebar" aria-label="Select role">
            {ROLES.map((role) => (
              <Link
                key={role.href}
                href={role.href}
                aria-current={pathname === role.href ? "page" : undefined}
              >
                {role.label}
              </Link>
            ))}
          </nav>
        }
      />
      {children}
    </>
  );
}
