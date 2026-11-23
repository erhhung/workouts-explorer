import { describe, expect, test, vi } from "vitest";
import { followOSMProgress } from "./osmProgressFollow";

function viewport(total = 58) {
  const root = document.createElement("div");
  root.innerHTML = `<table><thead><tr><th>Task</th></tr></thead><tbody>${Array.from({ length: total }, (_, i) => `<tr data-task-order="${i + 1}"><td>Task ${i + 1}</td></tr>`).join("")}</tbody></table>`;
  const header = 40,
    row = 35,
    height = header + 20 * row,
    scrollHeight = header + total * row;
  let position = Math.max(0, scrollHeight - height);
  Object.defineProperties(root, {
    clientTop: { value: 1 },
    scrollHeight: { value: scrollHeight },
    scrollTop: {
      get: () => position,
      set: (value: number) => {
        position = Math.max(0, Math.min(value, scrollHeight - height));
      },
    },
  });
  vi.spyOn(root, "getBoundingClientRect").mockReturnValue({ top: 100 } as DOMRect);
  vi.spyOn(root.querySelector("thead")!, "getBoundingClientRect").mockReturnValue({
    height: header,
  } as DOMRect);
  for (const element of root.querySelectorAll<HTMLElement>("tbody tr")) {
    vi.spyOn(element, "getBoundingClientRect").mockImplementation(
      () =>
        ({
          top: 101 + header + (Number(element.dataset.taskOrder) - 1) * row - position,
        }) as DOMRect,
    );
  }
  return root;
}

describe("OSM progress table following", () => {
  test.each([0, 1, 10, 20])("step %s starts the visible table at the top", (step) => {
    const root = viewport();
    followOSMProgress(root, step, 58);
    expect(root.scrollTop).toBe(0);
  });
  test.each([
    [21, 35],
    [23, 105],
    [57, 1295],
  ])("step %s aligns row n-19 below the sticky header", (step, top) => {
    const root = viewport();
    followOSMProgress(root, step, 58);
    expect(root.scrollTop).toBe(top);
  });
  test.each([57, 58])(
    "the final step of a %s-step history reaches the absolute bottom",
    (total) => {
      const root = viewport(total);
      root.scrollTop = 0;
      followOSMProgress(root, total, total);
      expect(root.scrollTop).toBe((total - 20) * 35);
    },
  );
});
