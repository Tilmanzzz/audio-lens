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

  const { items} = await fetchEpisodes({
    q: query || undefined,
    cursor: cursor || undefined,
    limit: 12,
  });


  return (
    <div className="mx-auto max-w-7xl">
      <header className="mb-8">
        <div className="mt-3">
          <SearchBar placeholder="Suche..." />
        </div>
        <h1 className="mt-10 text-3xl font-bold text-foreground">Discover Podcasts</h1>
        <p className="mt-2 text-sm text-foreground-subtle">
          {"Our newest Episodes"}
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
