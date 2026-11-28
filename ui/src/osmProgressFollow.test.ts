import { describe, expect, test, vi } from "vitest";
import { act, fireEvent, renderHook } from "@testing-library/react";
import {
  animateOSMProgressScroll,
  followOSMProgress,
  osmProgressScrollFraction,
  useOSMProgressFollow,
} from "./osmProgressFollow";

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
    clientHeight: { value: height },
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
          bottom: 101 + header + Number(element.dataset.taskOrder) * row - position,
        }) as DOMRect,
    );
  }
  return root;
}

function animationClock() {
  let now = 0,
    next = 0;
  const frames = new Map<number, FrameRequestCallback>();
  vi.spyOn(performance, "now").mockImplementation(() => now);
  vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
    frames.set(++next, callback);
    return next;
  });
  vi.spyOn(window, "cancelAnimationFrame").mockImplementation((id) => {
    frames.delete(id);
  });
  return {
    frames,
    tick(time: number) {
      now = time;
      const ready = [...frames.values()];
      frames.clear();
      act(() => {
        for (const callback of ready) callback(now);
      });
    },
  };
}

describe("OSM progress table following", () => {
  test("integrates the requested acceleration, cruise and deceleration into one second", () => {
    const clock = animationClock();
    const root = viewport();
    root.scrollTop = 0;
    const cancel = animateOSMProgressScroll(root, 725);
    for (const [time, position] of [
      [0, 0],
      [125, 31.25],
      [250, 125],
      [475, 350],
      [700, 575],
      [850, 687.5],
      [1000, 725],
    ]) {
      clock.tick(time);
      expect(root.scrollTop).toBeCloseTo(position, 6);
    }
    expect(clock.frames.size).toBe(0);
    expect(osmProgressScrollFraction(-1)).toBe(0);
    expect(osmProgressScrollFraction(2000)).toBe(1);
    cancel();
  });

  test("manual scrolling interrupts automatic motion and stable polls do not restart it", () => {
    const clock = animationClock();
    const root = viewport();
    root.scrollTop = 0;
    const scroll = { current: root as HTMLDivElement };
    const hook = renderHook(
      ({ current }) => useOSMProgressFollow(scroll, "job", current, 58, "processing"),
      { initialProps: { current: 21 } },
    );
    clock.tick(0);
    clock.tick(250);
    const partial = root.scrollTop;
    expect(partial).toBeGreaterThan(0);
    expect(partial).toBeLessThan(35);
    fireEvent.wheel(root);
    root.scrollTop = 200;
    hook.rerender({ current: 21 });
    clock.tick(700);
    expect(root.scrollTop).toBe(200);
    hook.rerender({ current: 22 });
    clock.tick(710);
    clock.tick(1710);
    expect(root.scrollTop).toBe(70);
    hook.unmount();
  });

  test("reduced motion follows immediately and unmount cancels unfinished scrolling", () => {
    const clock = animationClock();
    vi.spyOn(window, "matchMedia").mockReturnValue({
      matches: true,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    } as unknown as MediaQueryList);
    const root = viewport();
    root.scrollTop = 0;
    const hook = renderHook(() =>
      useOSMProgressFollow({ current: root as HTMLDivElement }, "job", 58, 58, "succeeded"),
    );
    clock.tick(0);
    expect(root.scrollTop).toBe(1330);
    expect(clock.frames.size).toBe(0);
    hook.unmount();
    vi.mocked(window.matchMedia).mockReturnValue({
      matches: false,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    } as unknown as MediaQueryList);
    root.scrollTop = 0;
    const moving = renderHook(() =>
      useOSMProgressFollow({ current: root as HTMLDivElement }, "job", 58, 58, "processing"),
    );
    clock.tick(10);
    clock.tick(260);
    const partial = root.scrollTop;
    moving.unmount();
    clock.tick(1010);
    expect(root.scrollTop).toBe(partial);
    expect(clock.frames.size).toBe(0);
  });

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
