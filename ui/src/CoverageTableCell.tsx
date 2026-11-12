import { useEffect, useRef, useState } from "react";
import { Tooltip } from "./Tooltip";

export function CoverageCellValue({ text }: { text: string }) {
  const ref = useRef<HTMLSpanElement>(null);
  const [truncated, setTruncated] = useState(false);
  useEffect(() => {
    const element = ref.current!;
    let active = true;
    const measure = () => {
      if (active)
        setTruncated(element.clientWidth > 0 && element.scrollWidth > element.clientWidth);
    };
    measure();
    const observer =
      typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(measure);
    observer?.observe(element);
    window.addEventListener("resize", measure);
    document.fonts?.addEventListener("loadingdone", measure);
    void document.fonts?.ready.then(measure);
    return () => {
      active = false;
      observer?.disconnect();
      window.removeEventListener("resize", measure);
      document.fonts?.removeEventListener("loadingdone", measure);
    };
  }, [text]);
  return (
    <Tooltip
      content={text}
      className="coverage-cell-tooltip"
      enabled={truncated}
      focusable={false}
      portal
      singleLine
    >
      <span ref={ref} className="coverage-cell-value">
        {text}
      </span>
    </Tooltip>
  );
}
