import { afterEach, describe, expect, test, vi } from "vitest";
import { api, type CoverageDiagnosticLabels, type CoverageDiagnosticLabelsPatch, type CoverageDiagnosticRun, type PublicConfig } from "./api";

afterEach(() => vi.restoreAllMocks());

describe("coverage diagnostic API contract", () => {
  test("models the disabled public feature default and diagnostic labels", () => {
    const config = { features: { coverageMatcherDiagnostics: false } } as Pick<PublicConfig, "features">;
    const patch: CoverageDiagnosticLabelsPatch = { overall: "correct", segments: [{ portionOrdinal: 3, label: "expected" }] };
    const labels: CoverageDiagnosticLabels = { overall: null, segments: patch.segments };
    expect(config.features.coverageMatcherDiagnostics).toBe(false);
    expect(labels.segments).toEqual([{ portionOrdinal: 3, label: "expected" }]);
  });

  test("posts JSON diagnostics and patches complete labels with CSRF", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ id: "D".repeat(32) }), { status: 201, headers: { "Content-Type": "application/json" } }));
    await api<CoverageDiagnosticRun>(`/api/workouts/${"A".repeat(32)}/coverage-diagnostic-runs`, { method: "POST", body: "{}" }, "csrf-diagnostic");
    let init = fetchMock.mock.calls[0][1]!;
    expect(init.credentials).toBe("same-origin");
    expect(new Headers(init.headers).get("Content-Type")).toBe("application/json");
    expect(new Headers(init.headers).get("X-CSRF-Token")).toBe("csrf-diagnostic");

    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ overall: "correct", segments: [] }), { headers: { "Content-Type": "application/json" } }));
    const body: CoverageDiagnosticLabelsPatch = { overall: "correct", segments: [] };
    await api<CoverageDiagnosticLabels>(`/api/coverage-diagnostic-runs/${"D".repeat(32)}/labels`, { method: "PATCH", body: JSON.stringify(body) }, "csrf-diagnostic");
    init = fetchMock.mock.calls[1][1]!;
    expect(init.body).toBe(JSON.stringify(body));
    expect(new Headers(init.headers).get("X-CSRF-Token")).toBe("csrf-diagnostic");
  });
});
