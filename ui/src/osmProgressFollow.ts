import { type RefObject, useLayoutEffect } from "react";
import { DIALOG_PIXEL_ALIGNMENT_EVENT } from "./dialogPixelAlignment";

function progressScrollTarget(viewport: HTMLElement, current: number, total?: number) {
  const start = viewport.scrollTop;
  let target = start;
  if (current <= 20) {
    target = 0;
  } else if (total !== undefined && current >= total) {
    return Math.max(0, viewport.scrollHeight - viewport.clientHeight);
  } else {
    const first = viewport.querySelector<HTMLElement>(`[data-task-order="${current - 19}"]`);
    const header = viewport.querySelector("thead");
    if (!first || !header) return start;
    const top = viewport.getBoundingClientRect().top + viewport.clientTop;
    target = Math.max(
      0,
      start + first.getBoundingClientRect().top - top - header.getBoundingClientRect().height,
    );
  }
  // A short/mobile viewport may fit fewer than twenty rows.
  const row = viewport.querySelector<HTMLElement>(`[data-task-order="${current}"]`);
  if (row && viewport.clientHeight > 0) {
    const bottom =
      viewport.getBoundingClientRect().top + viewport.clientTop + viewport.clientHeight;
    target = Math.max(target, start + row.getBoundingClientRect().bottom - bottom);
  }
  return Math.max(0, Math.min(target, viewport.scrollHeight - viewport.clientHeight));
}

export function followOSMProgress(viewport: HTMLElement, current: number, total?: number) {
  viewport.scrollTop = progressScrollTarget(viewport, current, total);
}

export function osmProgressScrollFraction(elapsed: number) {
  const time = Math.max(0, Math.min(elapsed, 1000));
  // Integrate a trapezoidal velocity profile: 250ms up, 450ms cruise, 300ms down.
  if (time < 250) return (time * time) / (500 * 725);
  if (time < 700) return (time - 125) / 725;
  const tail = time - 700;
  return (575 + tail - (tail * tail) / 600) / 725;
}

export function animateOSMProgressScroll(viewport: HTMLElement, target: number) {
  const start = viewport.scrollTop;
  const started = performance.now();
  let active = true;
  let frame: number;
  const update = (now: number) => {
    if (!active) return;
    const elapsed = now - started;
    viewport.scrollTop = start + (target - start) * osmProgressScrollFraction(elapsed);
    if (elapsed < 1000) frame = requestAnimationFrame(update);
  };
  frame = requestAnimationFrame(update);
  return () => {
    active = false;
    cancelAnimationFrame(frame);
  };
}

export function useOSMProgressFollow(
  scroll: RefObject<HTMLDivElement | null>,
  jobId: string | undefined,
  current: number,
  total: number | undefined,
  state: string | undefined,
) {
  useLayoutEffect(() => {
    if (!jobId) return;
    let active = true,
      frame: number | undefined,
      waitingForAnimation = false;
    let observed: HTMLDivElement | null = null,
      dialog: HTMLElement | null = null,
      sizeSignature = "";
    let cancelScroll: (() => void) | undefined;
    let destination: number | undefined;
    let manuallyInterrupted = false;
    const motion = window.matchMedia?.("(prefers-reduced-motion: reduce)");
    const stopScroll = () => {
      cancelScroll?.();
      cancelScroll = undefined;
      destination = undefined;
    };
    const interrupt = (event: Event) => {
      if (
        event instanceof KeyboardEvent &&
        !["ArrowUp", "ArrowDown", "Home", "End", "PageUp", "PageDown", " "].includes(event.key)
      )
        return;
      manuallyInterrupted = true;
      stopScroll();
    };
    const detachInput = () => {
      for (const event of ["wheel", "touchstart", "pointerdown", "keydown"])
        observed?.removeEventListener(event, interrupt);
    };
    const reduceMotion = () => {
      if (!motion?.matches || !observed || destination === undefined) return;
      const target = destination;
      stopScroll();
      observed.scrollTop = target;
    };
    const sizes = () => {
      const table = observed?.querySelector("table");
      return `${observed?.clientWidth}:${observed?.clientHeight}:${table?.getBoundingClientRect().width}:${table?.getBoundingClientRect().height}`;
    };
    const update = () => {
      if (!active || manuallyInterrupted) return;
      const viewport = scroll.current;
      if (!viewport) {
        schedule();
        return;
      }
      if (viewport !== observed) {
        stopScroll();
        detachInput();
        observer?.disconnect();
        dialog?.removeEventListener(DIALOG_PIXEL_ALIGNMENT_EVENT, schedule);
        observed = viewport;
        for (const event of ["wheel", "touchstart", "pointerdown", "keydown"])
          observed.addEventListener(event, interrupt, { passive: true });
        dialog = viewport.closest<HTMLElement>('[role="dialog"]');
        sizeSignature = sizes();
        observer?.observe(viewport);
        const table = viewport.querySelector("table");
        if (table) observer?.observe(table);
        dialog?.addEventListener(DIALOG_PIXEL_ALIGNMENT_EVENT, schedule);
      }
      const animations = dialog
        ?.getAnimations?.()
        .filter((animation) => animation.playState === "running");
      if (animations?.length) {
        if (!waitingForAnimation) {
          waitingForAnimation = true;
          void Promise.all(animations.map((animation) => animation.finished.catch(() => {}))).then(
            () => {
              waitingForAnimation = false;
              schedule();
            },
          );
        }
        return;
      }
      const target = progressScrollTarget(viewport, current, total);
      if (current <= 20 || motion?.matches || Math.abs(target - viewport.scrollTop) < 0.5) {
        stopScroll();
        viewport.scrollTop = target;
      } else if (destination === undefined || Math.abs(destination - target) >= 0.5) {
        stopScroll();
        destination = target;
        cancelScroll = animateOSMProgressScroll(viewport, target);
      }
    };
    const schedule = () => {
      if (!active || frame !== undefined) return;
      frame = requestAnimationFrame(() => {
        frame = undefined;
        update();
      });
    };
    const observer =
      typeof ResizeObserver === "undefined"
        ? undefined
        : new ResizeObserver(() => {
            const next = sizes();
            if (next !== sizeSignature) {
              sizeSignature = next;
              schedule();
            }
          });
    window.addEventListener("resize", schedule);
    motion?.addEventListener("change", reduceMotion);
    document.fonts?.addEventListener("loadingdone", schedule);
    void document.fonts?.ready.then(schedule);
    schedule();
    return () => {
      active = false;
      if (frame !== undefined) cancelAnimationFrame(frame);
      stopScroll();
      detachInput();
      motion?.removeEventListener("change", reduceMotion);
      observer?.disconnect();
      dialog?.removeEventListener(DIALOG_PIXEL_ALIGNMENT_EVENT, schedule);
      window.removeEventListener("resize", schedule);
      document.fonts?.removeEventListener("loadingdone", schedule);
    };
  }, [scroll, jobId, current, total, state]);
}
