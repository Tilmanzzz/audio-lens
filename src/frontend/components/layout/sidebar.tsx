import Link from "next/link";
import { House,Landmark, AudioLines } from "lucide-react";

export function Sidebar() {
  return (
    <aside className="fixed left-0 top-0 h-screen w-[210px] border-r border-border bg-background-card text-foreground shadow-[0_0_0_1px_var(--border)]">
      <div className="flex h-full flex-col px-5 py-5">
        <div className="mb-10 flex items-center gap-3">
          <AudioLines size={40} color="var(--accent)" aria-hidden="true" />
          <h3 className="text-xl font-semibold text-accent">Media Lens</h3>
        </div>

        <nav className="flex flex-col gap-2">
          <Link
            href="/"
            className="flex items-center gap-3 rounded-xl px-3 py-2 text-lg transition hover:bg-background-raised hover:text-accent"
          >
            <House size={22} aria-hidden="true" />
            <span>Home</span>
          </Link>
          <Link
            href="/gesamt"
            className="flex items-center gap-3 rounded-xl px-3 py-2 text-lg transition hover:bg-background-raised hover:text-accent"
          >
            <Landmark size={22} aria-hidden="true" />
            <span>Library</span>
          </Link>
        </nav>

        <div className="mt-auto pt-4 text-xs text-foreground-subtle">
          Media Lens
        </div>
      </div>
    </aside>
  );
}