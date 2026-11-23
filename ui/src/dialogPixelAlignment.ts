export const DIALOG_PIXEL_ALIGNMENT_EVENT = "dialogpixelalignment";

export function installDialogPixelAlignment(dialog: HTMLElement) {
  let active = true;
  let scheduled: number | undefined;
  let waitingForAnimation = false;
  const properties = [
    "--dialog-pixel-offset-x",
    "--dialog-pixel-offset-y",
    "--dialog-pixel-left",
    "--dialog-pixel-top",
  ] as const;
  const update = () => {
    if (!active) return;
    const animations = dialog.getAnimations?.().filter((item) => item.playState === "running");
    if (animations?.length) {
      if (!waitingForAnimation) {
        waitingForAnimation = true;
        void Promise.all(animations.map((item) => item.finished.catch(() => {}))).then(() => {
          waitingForAnimation = false;
          schedule();
        });
      }
      return;
    }
    const bounds = dialog.getBoundingClientRect();
    const scale = window.devicePixelRatio || 1;
    let changed = false;
    const centered = dialog.classList.contains("dialog-content");
    const edges = centered
      ? [
          ((document.documentElement.clientWidth || window.innerWidth) - bounds.width) / 2,
          ((document.documentElement.clientHeight || window.innerHeight) - bounds.height) / 2,
        ]
      : [bounds.left, bounds.top];
    for (const [index, edge] of edges.entries()) {
      const name = properties[index + (centered ? 2 : 0)];
      const previous = centered ? 0 : parseFloat(dialog.style.getPropertyValue(name)) || 0;
      const unsnapped = edge - previous;
      const snapped = Math.round(unsnapped * scale) / scale;
      const next = `${centered ? snapped : snapped - unsnapped}px`;
      if (dialog.style.getPropertyValue(name) !== next) {
        dialog.style.setProperty(name, next);
        changed = true;
      }
    }
    // A compensating translate can leave a fractionally rasterized compositor layer
    // even when its DOM bounds are integral. Center settled modals with layout instead.
    if (centered) dialog.setAttribute("data-dialog-pixel-positioned", "");
    // Child table frames must measure their residual offset after the dialog moves.
    if (changed) dialog.dispatchEvent(new Event(DIALOG_PIXEL_ALIGNMENT_EVENT));
  };
  const schedule = () => {
    if (!active || scheduled !== undefined) return;
    scheduled = requestAnimationFrame(() => {
      scheduled = undefined;
      update();
    });
  };
  let resolution: MediaQueryList | undefined;
  const watchResolution = () => {
    resolution?.removeEventListener?.("change", resolutionChanged);
    resolution = window.matchMedia?.(`(resolution: ${window.devicePixelRatio || 1}dppx)`);
    resolution?.addEventListener?.("change", resolutionChanged);
  };
  const resolutionChanged = () => {
    watchResolution();
    schedule();
  };
  const observer = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(schedule);
  observer?.observe(dialog);
  window.addEventListener("resize", schedule);
  document.fonts?.addEventListener("loadingdone", schedule);
  void document.fonts?.ready.then(schedule);
  watchResolution();
  update();
  return () => {
    active = false;
    if (scheduled !== undefined) cancelAnimationFrame(scheduled);
    observer?.disconnect();
    resolution?.removeEventListener?.("change", resolutionChanged);
    window.removeEventListener("resize", schedule);
    document.fonts?.removeEventListener("loadingdone", schedule);
    for (const name of properties) dialog.style.removeProperty(name);
    dialog.removeAttribute("data-dialog-pixel-positioned");
  };
}
