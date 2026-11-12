import { type ReactNode, useLayoutEffect, useRef } from "react";

export function FittedAggregateValue({
  children,
  className,
  "aria-describedby": describedBy,
}: {
  children: ReactNode;
  className?: string;
  "aria-describedby"?: string;
}) {
  const ref = useRef<HTMLElement>(null);
  useLayoutEffect(() => {
    const element = ref.current!;
    let active = true;
    const fit = () => {
      if (!active) return;
      element.style.removeProperty("font-size");
      const available = element.getBoundingClientRect().width;
      const maximum = parseFloat(getComputedStyle(element).fontSize);
      const range = document.createRange();
      if (
        !available ||
        !Number.isFinite(maximum) ||
        typeof range.getBoundingClientRect !== "function"
      )
        return;
      range.selectNodeContents(element);
      if (range.getBoundingClientRect().width <= available) return;
      let lower = 0,
        upper = maximum;
      // Measure shaped text, including the current font and letter spacing, before paint.
      for (let iteration = 0; iteration < 12; iteration++) {
        const candidate = (lower + upper) / 2;
        element.style.fontSize = `${candidate}px`;
        if (range.getBoundingClientRect().width <= available) lower = candidate;
        else upper = candidate;
      }
      element.style.fontSize = `${Math.floor(lower * 100) / 100}px`;
    };
    fit();
    let lastWidth = element.getBoundingClientRect().width;
    const observer =
      typeof ResizeObserver === "undefined"
        ? undefined
        : new ResizeObserver(() => {
            const width = element.getBoundingClientRect().width;
            if (width !== lastWidth) {
              lastWidth = width;
              fit();
            }
          });
    observer?.observe(element);
    window.addEventListener("resize", fit);
    document.fonts?.addEventListener("loadingdone", fit);
    void document.fonts?.ready.then(fit);
    return () => {
      active = false;
      observer?.disconnect();
      window.removeEventListener("resize", fit);
      document.fonts?.removeEventListener("loadingdone", fit);
    };
  }, [children]);
  return (
    <strong
      ref={ref}
      className={`aggregate-fit-value${className ? ` ${className}` : ""}`}
      aria-describedby={describedBy}
    >
      {children}
    </strong>
  );
}
