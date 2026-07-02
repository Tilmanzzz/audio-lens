import { fetchEpisodes } from "@/lib/api";
import { InfoCard } from "@/components/ui/card";
import { SearchBar } from "@/components/layout/searchbar";


export default async function HomePage({
  searchParams,
}: {
  searchParams: Promise<{ q?: string; cursor?: string }>;
}) {
  const { q, cursor } = await searchParams;
  const query = q ?? "";

  const { items, total } = await fetchEpisodes({
    q: query || undefined,
    cursor: cursor || undefined,
  });


  return (
    <div className="mx-auto max-w-7xl">
      <header className="mb-8">
        <h1>Library</h1>
        <p className="mt-2 text-sm text-foreground-subtle">
          {total} {query ? `Ergebnisse für „${query}"` : "Episodes in Database"}
        </p>
      </header>

      <section>
        <div className="grid gap-6 grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
          {items.length > 0 ? (
            items.map((item) => <InfoCard key={item.id} {...item} />)
          ) : (
            <p className="text-sm text-foreground-subtle col-span-full">No Podcasts found.</p>
          )}
        </div>
      </section>
    </div>
  );
}
