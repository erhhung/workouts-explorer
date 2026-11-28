import { afterEach, describe, expect, it, vi } from "vitest";
import { DIALOG_PIXEL_ALIGNMENT_EVENT, installDialogPixelAlignment } from "./dialogPixelAlignment";

afterEach(() => vi.unstubAllGlobals());

function surface() {
  const dialog = document.createElement("div");
  document.body.append(dialog);
  const frames = new Map<number, FrameRequestCallback>();
  let sequence = 0;
  let resize: ResizeObserverCallback | undefined;
  const disconnect = vi.fn();
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    frames.set(++sequence, callback);
    return sequence;
  });
  vi.stubGlobal("cancelAnimationFrame", (id: number) => frames.delete(id));
  vi.stubGlobal(
    "ResizeObserver",
    class {
      constructor(callback: ResizeObserverCallback) {
        resize = callback;
      }
      observe() {}
      disconnect = disconnect;
    },
  );
  let left = 160.25;
  let top = 96.5;
  vi.spyOn(dialog, "getBoundingClientRect").mockImplementation(
    () =>
      ({
        left: left + (parseFloat(dialog.style.getPropertyValue("--dialog-pixel-offset-x")) || 0),
        top: top + (parseFloat(dialog.style.getPropertyValue("--dialog-pixel-offset-y")) || 0),
      }) as DOMRect,
  );
  const flush = () => {
    const pending = [...frames.values()];
    frames.clear();
    for (const callback of pending) callback(0);
  };
  return {
    dialog,
    frames,
    disconnect,
    flush,
    resize(x: number, y: number, flushFrame = true) {
      left = x;
      top = y;
      resize?.([], {} as ResizeObserver);
      if (flushFrame) flush();
    },
  };
}

describe("shared dialog pixel alignment", () => {
  it("centers resized content in the resize delivery instead of a later animation frame", () => {
    vi.stubGlobal("devicePixelRatio", 1);
    vi.stubGlobal("innerWidth", 1200);
    vi.stubGlobal("innerHeight", 900);
    const { dialog, resize, frames } = surface();
    dialog.className = "dialog-content";
    let height = 300;
    vi.mocked(dialog.getBoundingClientRect).mockImplementation(
      () => ({ width: 600, height }) as DOMRect,
    );
    const dispose = installDialogPixelAlignment(dialog);
    expect(dialog.style.getPropertyValue("--dialog-pixel-top")).toBe("300px");
    height = 700;
    resize(0, 0, false);
    expect(dialog.style.getPropertyValue("--dialog-pixel-top")).toBe("100px");
    expect(frames.size).toBe(0);
    height = 300;
    resize(0, 0, false);
    expect(dialog.style.getPropertyValue("--dialog-pixel-top")).toBe("300px");
    dispose();
    dialog.remove();
  });

  it("centers settled modals with layout coordinates rather than another compositor translation", () => {
    vi.stubGlobal("devicePixelRatio", 1);
    vi.stubGlobal("innerWidth", 1440);
    vi.stubGlobal("innerHeight", 1604);
    const { dialog, flush } = surface();
    dialog.className = "dialog-content";
    vi.mocked(dialog.getBoundingClientRect).mockReturnValue({
      width: 1120,
      height: 1207,
    } as DOMRect);
    const dispose = installDialogPixelAlignment(dialog);
    expect(dialog).toHaveAttribute("data-dialog-pixel-positioned");
    expect(dialog.style.getPropertyValue("--dialog-pixel-left")).toBe("160px");
    expect(dialog.style.getPropertyValue("--dialog-pixel-top")).toBe("199px");
    expect(dialog.style.getPropertyValue("--dialog-pixel-offset-y")).toBe("");
    vi.stubGlobal("innerWidth", 1441);
    vi.stubGlobal("innerHeight", 1605);
    window.dispatchEvent(new Event("resize"));
    flush();
    expect(dialog.style.getPropertyValue("--dialog-pixel-left")).toBe("161px");
    expect(dialog.style.getPropertyValue("--dialog-pixel-top")).toBe("199px");
    dispose();
    expect(dialog).not.toHaveAttribute("data-dialog-pixel-positioned");
    expect(dialog.style.length).toBe(0);
    dialog.remove();
  });

  it("remains stable across repeated measurements, content resizing and a display-scale change", () => {
    vi.stubGlobal("devicePixelRatio", 1);
    const { dialog, flush, resize } = surface();
    const notified: { left: number; top: number }[] = [];
    dialog.addEventListener(DIALOG_PIXEL_ALIGNMENT_EVENT, () => {
      const { left, top } = dialog.getBoundingClientRect();
      notified.push({ left, top });
    });
    const dispose = installDialogPixelAlignment(dialog);
    expect(notified).toEqual([{ left: 160, top: 97 }]);
    for (let i = 0; i < 5; i++) {
      window.dispatchEvent(new Event("resize"));
      flush();
    }
    expect(notified).toHaveLength(1);
    resize(160.75, 99.25);
    expect(notified.at(-1)).toEqual({ left: 161, top: 99 });
    vi.stubGlobal("devicePixelRatio", 2);
    window.dispatchEvent(new Event("resize"));
    flush();
    expect(notified.at(-1)).toEqual({ left: 161, top: 99.5 });
    dispose();
    dialog.remove();
  });

  it("waits for the entry animation and cancels observation and pending work on close", async () => {
    const { dialog, frames, flush, disconnect } = surface();
    let finish!: () => void;
    const animation = {
      playState: "running",
      finished: new Promise<void>((resolve) => (finish = resolve)),
    };
    Object.defineProperty(dialog, "getAnimations", { value: () => [animation] });
    const notified = vi.fn();
    dialog.addEventListener(DIALOG_PIXEL_ALIGNMENT_EVENT, notified);
    const dispose = installDialogPixelAlignment(dialog);
    expect(notified).not.toHaveBeenCalled();
    animation.playState = "finished";
    finish();
    await vi.waitFor(() => expect(frames.size).toBe(1));
    flush();
    expect(notified).toHaveBeenCalledOnce();
    window.dispatchEvent(new Event("resize"));
    expect(frames.size).toBe(1);
    dispose();
    expect(disconnect).toHaveBeenCalledOnce();
    expect(frames.size).toBe(0);
    expect(dialog.style.length).toBe(0);
    window.dispatchEvent(new Event("resize"));
    expect(frames.size).toBe(0);
    dialog.remove();
  });
});
