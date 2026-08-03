import { useQuery } from "@tanstack/react-query";

export interface PublicConfig {
  productName: string;
  pollingIntervalSeconds: number;
  mapFitPaddingPixels: number;
}

async function loadConfig(): Promise<PublicConfig> {
  const response = await fetch("/api/config");
  if (!response.ok) throw new Error("Runtime configuration is unavailable");
  return response.json() as Promise<PublicConfig>;
}

export function App() {
  const config = useQuery({ queryKey: ["public-config"], queryFn: loadConfig });

  return (
    <main>
      <div className="orbit" aria-hidden="true" />
      <section className="panel">
        <p className="eyebrow">Executable skeleton</p>
        <h1>{config.data?.productName ?? "Workouts Explorer"}</h1>
        {config.isPending && <p role="status">Loading runtime configuration...</p>}
        {config.isError && <p role="alert">The API is not ready. Start it and refresh this page.</p>}
        {config.isSuccess && (
          <p role="status" className="ready">
            UI and API are connected. Product workflows begin in Milestone 2.
          </p>
        )}
        <a href="/swagger">Explore the API contract</a>
      </section>
    </main>
  );
}
