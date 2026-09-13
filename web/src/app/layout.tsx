import type { Metadata } from "next";

import "./marspay.css";
import "./shell.css";
import { Shell } from "@/components/Shell";

export const metadata: Metadata = {
  title: "Marspay",
  description: "A digital wallet built to be explained, not just demoed.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>
        <Shell>{children}</Shell>
      </body>
    </html>
  );
}
