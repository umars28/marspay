"use client";

import { useState } from "react";
import type { ReactNode } from "react";

export type Section = { key: string; label: string; icon: string; group?: string };

export function Dashboard({
  sections,
  children,
}: {
  sections: Section[];
  children: (active: string) => ReactNode;
}) {
  const [active, setActive] = useState(sections[0]?.key ?? "");
  const groups = new Map<string, Section[]>();

  for (const section of sections) {
    const name = section.group ?? "";
    groups.set(name, [...(groups.get(name) ?? []), section]);
  }

  return (
    <div className="role active">
      <div className="shell">
        <aside className="sidebar">
          {[...groups.entries()].map(([name, items]) => (
            <div className="navgroup" key={name || "main"}>
              {name ? <div className="label">{name}</div> : null}
              <nav>
                {items.map((item) => (
                  <button
                    key={item.key}
                    aria-current={active === item.key ? "page" : undefined}
                    onClick={() => setActive(item.key)}
                  >
                    <svg>
                      <use href={`#${item.icon}`} />
                    </svg>
                    {item.label}
                  </button>
                ))}
              </nav>
            </div>
          ))}
        </aside>
        <div className="main">{children(active)}</div>
      </div>
    </div>
  );
}
