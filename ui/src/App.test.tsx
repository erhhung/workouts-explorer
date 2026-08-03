import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { App } from "./App";

test("loads safe runtime configuration from the same origin", async () => {
  const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(JSON.stringify({ productName: "Workouts Explorer", pollingIntervalSeconds: 30, mapFitPaddingPixels: 48 })),
  );
  render(<QueryClientProvider client={new QueryClient()}><App /></QueryClientProvider>);
  expect(await screen.findByText(/UI and API are connected/)).toBeInTheDocument();
  expect(fetchMock).toHaveBeenCalledWith("/api/config");
  fetchMock.mockRestore();
});
