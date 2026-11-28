import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import {
  AdminOSMStorage,
  storageGiB,
  storageTicks,
  storageMeasurementTime,
  type StorageUsage,
} from "./AdminOSMStorage";
import { RetainedTab } from "./RetainedTab";

const GiB = 2 ** 30;
const snapshot: StorageUsage = {
  configured: true,
  checkedAt: "2026-11-27T00:00:00Z",
  databases: ["osm"],
  nodes: [
    {
      pod: "postgresql-postgresql-0",
      namespace: "postgresql",
      role: "primary",
      pvcs: ["data-postgresql-postgresql-0"],
      capacityBytes: 50 * GiB,
      usedBytes: 36.4 * GiB,
      availableBytes: 13.6 * GiB,
      applicationBytes: 20 * GiB,
      sampledAt: "2026-11-27T00:00:00Z",
    },
    {
      pod: "postgresql-postgresql-1",
      namespace: "postgresql",
      role: "replica",
      pvcs: ["data-postgresql-postgresql-1"],
      capacityBytes: 50 * GiB,
      usedBytes: 35 * GiB,
      availableBytes: 15 * GiB,
      applicationBytes: 19 * GiB,
      sampledAt: "2026-11-27T00:00:00Z",
    },
  ],
};
function setup(value = snapshot) {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } }),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  const view = (active = true) => (
    <QueryClientProvider client={client}>
      <RetainedTab id="storage" active={active}>
        <AdminOSMStorage polling={30} />
      </RetainedTab>
    </QueryClientProvider>
  );
  const rendered = render(view());
  return { client, ...rendered, view };
}
describe("administrator OSM storage", () => {
  test("uses title-case content, real pod titles and Consistency-style default disclosures", async () => {
    setup();
    await screen.findByRole("heading", { name: "Storage Usage" });
    const primary = await screen.findByRole("button", {
      name: "Read/write primary: postgresql-postgresql-0",
    });
    const replica = screen.getByRole("button", {
      name: "Read-only replica: postgresql-postgresql-1",
    });
    expect(primary).toHaveAttribute("aria-expanded", "true");
    expect(replica).toHaveAttribute("aria-expanded", "false");
    expect(primary).toHaveClass("consistency-disclosure");
    expect(screen.getByRole("img", { name: /36.4 GiB used of 50 GiB/ })).toBeInTheDocument();
    expect(
      screen.getByRole("img", { name: /Workouts Explorer 54.9%; Other applications 45.1%/ }),
    ).toBeInTheDocument();
    fireEvent.click(replica);
    expect(replica).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("img", { name: /35 GiB used of 50 GiB/ })).toBeInTheDocument();
  });
  test("preserves manual disclosure choices through refresh and tab retention", async () => {
    const { client, view, rerender } = setup();
    const primary = await screen.findByRole("button", { name: /Read\/write primary:/ });
    fireEvent.click(primary);
    expect(primary).toHaveAttribute("aria-expanded", "false");
    act(() => {
      client.setQueryData(["admin-osm-storage-usage"], {
        ...snapshot,
        checkedAt: "2026-11-27T00:01:00Z",
      });
    });
    expect(primary).toHaveAttribute("aria-expanded", "false");
    rerender(view(false));
    rerender(view(true));
    expect(primary).toHaveAttribute("aria-expanded", "false");
  });
  test("refreshes primary titles and untouched default expansion after failover", async () => {
    const { client } = setup();
    await screen.findByRole("button", { name: "Read/write primary: postgresql-postgresql-0" });
    act(() => {
      client.setQueryData(["admin-osm-storage-usage"], {
        ...snapshot,
        nodes: [
          { ...snapshot.nodes[1], role: "primary" },
          { ...snapshot.nodes[0], role: "replica" },
        ],
      });
    });
    expect(
      await screen.findByRole("button", { name: "Read/write primary: postgresql-postgresql-1" }),
    ).toHaveAttribute("aria-expanded", "true");
    expect(
      screen.getByRole("button", { name: "Read-only replica: postgresql-postgresql-0" }),
    ).toHaveAttribute("aria-expanded", "false");
  });
  test("keeps accepted charts on failed refresh with the exact available-measurements notice", async () => {
    const { client } = setup();
    await screen.findByRole("img", { name: /36.4 GiB used of 50 GiB/ });
    vi.mocked(globalThis.fetch).mockRejectedValueOnce(new Error("metrics unavailable"));
    await act(async () => {
      await client.invalidateQueries({ queryKey: ["admin-osm-storage-usage"] });
    });
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Storage usage could not be refreshed. Showing the last available measurements.",
    );
    expect(screen.getByRole("img", { name: /36.4 GiB used of 50 GiB/ })).toBeInTheDocument();
  });
  test("uses an explicit local measurement date and time", () => {
    expect(storageMeasurementTime(new Date(2026, 10, 27, 11, 54, 55).toISOString())).toBe(
      "11/27/2026 at 11:54:55 AM",
    );
    expect(storageGiB(53619982336)).toBe("49.9 GiB");
    expect(storageTicks(53619982336)).not.toContain(50);
  });
  test("labels the requested PVC size without changing usable-filesystem percentages", async () => {
    const usable = 53619982336;
    setup({
      ...snapshot,
      nodes: [
        {
          ...snapshot.nodes[0],
          capacityBytes: usable,
          requestedCapacityBytes: 50 * GiB,
          usedBytes: 16.8 * GiB,
          availableBytes: 33.1 * GiB,
          applicationBytes: 12 * GiB,
        },
      ],
    });
    const chart = await screen.findByRole("img", {
      name: /16.8 GiB used of 49.9 GiB usable; 33.1 GiB available; requested PVC size 50 GiB/,
    });
    expect(chart.querySelector(".osm-storage-capacity")).toHaveTextContent("50 GiB");
    expect(chart.querySelector(".osm-storage-bar-fill")).toHaveStyle({
      width: `${((16.8 * GiB) / usable) * 100}%`,
    });
    expect(chart.querySelector(".osm-storage-available")).toHaveTextContent(
      "33.1 GiB available (66.3%)",
    );
    expect(
      chart.querySelector(".osm-storage-available > span.osm-storage-foreground"),
    ).toHaveTextContent("33.1 GiB");
    expect(
      chart.querySelector(".osm-storage-available > strong.osm-storage-foreground"),
    ).toHaveTextContent("66.3%");
    expect(chart.querySelector(".osm-storage-bar-value")).toHaveTextContent("16.8 GiB (33.6%)");
    const ticks = chart.querySelectorAll<HTMLElement>(".osm-storage-axis span");
    expect([...ticks].map((tick) => tick.textContent)).toEqual([
      "0 GiB",
      "5 GiB",
      "10 GiB",
      "15 GiB",
      "20 GiB",
      "25 GiB",
      "30 GiB",
      "35 GiB",
      "40 GiB",
      "45 GiB",
      "50 GiB",
    ]);
    expect(ticks[2].style.left).toBe("20%");
    expect(ticks[10].style.left).toBe("100%");
  });
  test("does not force an end tick when requested capacity is not an automatic tick interval", async () => {
    setup({
      ...snapshot,
      nodes: [
        { ...snapshot.nodes[0], capacityBytes: 53619982336, requestedCapacityBytes: 63 * GiB },
      ],
    });
    const chart = await screen.findByRole("img", { name: /requested PVC size 63 GiB/ });
    expect(chart.querySelector(".osm-storage-capacity")).toHaveTextContent("63 GiB");
    const ticks = chart.querySelectorAll<HTMLElement>(".osm-storage-axis span");
    expect(ticks[ticks.length - 1]).toHaveTextContent("60 GiB");
    expect(parseFloat(ticks[ticks.length - 1].style.left)).toBeCloseTo((60 / 63) * 100);
    expect([...ticks].some((tick) => tick.textContent === "63 GiB")).toBe(false);
  });
  test.each([null, undefined])(
    "falls back to measured capacity without a requested PVC size (%s)",
    async (requestedCapacityBytes) => {
      setup({
        ...snapshot,
        nodes: [
          { ...snapshot.nodes[0], pvcs: [], capacityBytes: 53619982336, requestedCapacityBytes },
        ],
      });
      const chart = await screen.findByRole("img", { name: /36.4 GiB used of 49.9 GiB usable/ });
      expect(chart.querySelector(".osm-storage-capacity")).toHaveTextContent("49.9 GiB");
    },
  );
  test("does not invent an app share when database measurements are unavailable", async () => {
    setup({
      ...snapshot,
      nodes: [
        {
          ...snapshot.nodes[0],
          applicationBytes: null,
          issue: "Per-database storage measurements are unavailable.",
        },
      ],
    });
    expect(
      await screen.findByText("Per-database storage measurements are unavailable."),
    ).toBeInTheDocument();
    expect(screen.getByRole("img", { name: /36.4 GiB used/ })).toBeInTheDocument();
    expect(screen.queryByRole("img", { name: /Workouts Explorer/ })).toBeNull();
  });
  test("formats capacity without artificial decimals and uses whole GiB ticks", () => {
    expect(storageGiB(50 * GiB)).toBe("50 GiB");
    expect(storageGiB(36.4 * GiB)).toBe("36.4 GiB");
    expect(storageTicks(50 * GiB)).toEqual([0, 5, 10, 15, 20, 25, 30, 35, 40, 45, 50]);
    expect(storageTicks(0.5 * GiB)).toEqual([0]);
    expect(storageTicks(64 * GiB).every(Number.isInteger)).toBe(true);
  });
});
