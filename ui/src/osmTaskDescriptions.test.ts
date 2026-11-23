import { describe, expect, test } from "vitest";
import { osmTaskDescriptions } from "./osmTaskDescriptions";

describe("OSM task description contract", () => {
  test("reads all 58 descriptions from the document's work/completion column", () => {
    expect(Object.keys(osmTaskDescriptions)).toHaveLength(58);
    expect(osmTaskDescriptions["clip-replacements"]).toContain(
      "Split sections where they cross area boundaries",
    );
    expect(osmTaskDescriptions["identity-edges/orientation-4"]).toBe(
      "Find sections connected at their ending points.",
    );
    expect(osmTaskDescriptions["validate/finalize"]).toContain(
      "make the region available for coverage processing",
    );
    expect(osmTaskDescriptions["download"]).toContain(
      "Download the region's map file and check that it arrived intact",
    );
    expect(osmTaskDescriptions["prepare-source"]).toContain(
      "Check the required tools and prepare a workspace",
    );
    const sentences = new Intl.Segmenter("en", { granularity: "sentence" });
    for (const [name, description] of Object.entries(osmTaskDescriptions)) {
      expect(Array.from(sentences.segment(description)).length, name).toBeLessThanOrEqual(2);
      expect(description, name).not.toMatch(
        /\b(UUIDs?|keysets?|cursors?|GiST|canonical|partitions?|materialize|provenance|PBF|HTTP|SHA-256|graph)\b/i,
      );
    }
  });
});
