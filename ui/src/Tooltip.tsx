import {
  type FocusEvent,
  type KeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { dateTimeFormatter } from "./formatters";

let activePortalTooltip: { id: string; close: () => void } | undefined;

export function dismissActivePortalTooltip() {
  activePortalTooltip?.close();
}

export function Tooltip({
  id,
  content,
  children,
  className = "",
  focusable = true,
  label,
  portal = false,
  enabled = true,
  singleLine = false,
  multiline = false,
  offsetY = 0,
  keyboardFocusOnly = false,
  dismissOnPointerDown = false,
  dismissOnPointerLeave = false,
  followPointer = false,
}: {
  id?: string;
  content: string;
  children: ReactNode;
  className?: string;
  focusable?: boolean;
  label?: string;
  portal?: boolean;
  enabled?: boolean;
  singleLine?: boolean;
  multiline?: boolean;
  offsetY?: number;
  keyboardFocusOnly?: boolean;
  dismissOnPointerDown?: boolean;
  dismissOnPointerLeave?: boolean;
  followPointer?: boolean;
}) {
  const generatedId = useId();
  const tooltipId = id ?? generatedId;
  const triggerRef = useRef<HTMLSpanElement>(null);
  const bubbleRef = useRef<HTMLSpanElement>(null);
  const interaction = useRef<{
    hovered: boolean;
    focused: boolean;
    focusedElement?: EventTarget | null;
    pointerDismissed: boolean;
    pointer?: { x: number; y: number };
  }>({ hovered: false, focused: false, pointerDismissed: false });
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState({ top: 0, left: 0 });
  const pointerFrame = useRef<number | undefined>(undefined);

  function close() {
    if (pointerFrame.current !== undefined) cancelAnimationFrame(pointerFrame.current);
    pointerFrame.current = undefined;
    if (activePortalTooltip?.id === tooltipId) activePortalTooltip = undefined;
    setOpen(false);
  }

  function rememberPointer(event: { clientX: number; clientY: number }) {
    if (Number.isFinite(event.clientX) && Number.isFinite(event.clientY))
      interaction.current.pointer = { x: event.clientX, y: event.clientY };
  }

  function pointerInside() {
    const pointer = interaction.current.pointer;
    if (!pointer) return interaction.current.hovered;
    const hit = document.elementFromPoint(pointer.x, pointer.y);
    return Boolean(hit && triggerRef.current?.contains(hit));
  }

  function acceptsFocus(target: EventTarget | null) {
    if (!keyboardFocusOnly) return true;
    const modality = document.documentElement.dataset.inputModality;
    if (modality) return modality === "keyboard";
    return target instanceof Element && target.matches(":focus-visible");
  }

  function place(activate = false) {
    if (!enabled || interaction.current.pointerDismissed) return;
    if (!activate && activePortalTooltip?.id !== tooltipId) {
      setOpen(false);
      return;
    }
    if (!interaction.current.hovered && !interaction.current.focused) {
      close();
      return;
    }
    const trigger = triggerRef.current;
    if (!trigger || !bubbleRef.current) return;
    const anchor = trigger.getBoundingClientRect();
    const bubble = bubbleRef.current.getBoundingClientRect();
    let left = 0,
      top = 0,
      right = window.innerWidth,
      bottom = window.innerHeight;
    // A portal must not outlive its anchor's visible portion in a scrolling list.
    for (let parent = trigger.parentElement; parent; parent = parent.parentElement) {
      const style = getComputedStyle(parent);
      const clipX = /auto|scroll|hidden|clip/.test(style.overflowX || style.overflow);
      const clipY = /auto|scroll|hidden|clip/.test(style.overflowY || style.overflow);
      if (!clipX && !clipY) continue;
      const bounds = parent.getBoundingClientRect();
      if (clipX) {
        left = Math.max(left, bounds.left);
        right = Math.min(right, bounds.right);
      }
      if (clipY) {
        top = Math.max(top, bounds.top);
        bottom = Math.min(bottom, bounds.bottom);
      }
    }
    if (
      anchor.right <= left ||
      anchor.left >= right ||
      anchor.bottom <= top ||
      anchor.top >= bottom
    ) {
      close();
      return;
    }
    if (activePortalTooltip?.id !== tooltipId) activePortalTooltip?.close();
    activePortalTooltip = { id: tooltipId, close };
    const pointer =
      followPointer && interaction.current.hovered ? interaction.current.pointer : undefined;
    const rootStyle = pointer ? getComputedStyle(document.documentElement) : undefined;
    const gapValue = rootStyle?.getPropertyValue("--cursor-tooltip-gap").trim();
    const gap = pointer
      ? (parseFloat(gapValue ?? "") || 0.65) *
        (gapValue?.endsWith("px") ? 1 : parseFloat(rootStyle?.fontSize ?? "") || 16)
      : 6;
    const center = pointer?.x ?? anchor.left + anchor.width / 2;
    const above = (pointer?.y ?? anchor.top) - bubble.height - gap;
    const below = (pointer?.y ?? anchor.bottom) + gap;
    setPosition({
      left: Math.max(8, Math.min(center - bubble.width / 2, window.innerWidth - bubble.width - 8)),
      top: Math.max(
        8,
        Math.min(above >= 8 ? above : below, window.innerHeight - bubble.height - 8),
      ),
    });
    setOpen(true);
  }
  const latestPlace = useRef(place);
  latestPlace.current = place;

  useEffect(() => {
    if (
      interaction.current.focused &&
      (!acceptsFocus(document.activeElement) ||
        (interaction.current.focusedElement instanceof Node &&
          !triggerRef.current?.contains(interaction.current.focusedElement)))
    ) {
      interaction.current.focused = false;
      if (!interaction.current.hovered) close();
    }
    if (!enabled) close();
    else if (portal && (interaction.current.hovered || interaction.current.focused)) {
      if (interaction.current.hovered) interaction.current.hovered = pointerInside();
      if (!activePortalTooltip || activePortalTooltip.id === tooltipId) place(!activePortalTooltip);
    }
  }, [
    enabled,
    portal,
    content,
    singleLine,
    multiline,
    tooltipId,
    keyboardFocusOnly,
    followPointer,
  ]);

  useEffect(
    () => () => {
      if (activePortalTooltip?.id === tooltipId) activePortalTooltip = undefined;
      if (pointerFrame.current !== undefined) cancelAnimationFrame(pointerFrame.current);
    },
    [tooltipId],
  );

  useEffect(() => {
    if (!portal || !enabled || !open) return;
    function escape(event: globalThis.KeyboardEvent) {
      if (event.key === "Escape") {
        interaction.current.focused = false;
        interaction.current.hovered = false;
        interaction.current.pointerDismissed = true;
        close();
      }
    }
    function follow() {
      // Wheel scrolling can move disabled controls without a reliable pointerleave.
      if (interaction.current.hovered) interaction.current.hovered = pointerInside();
      place();
    }
    function pointerDown(event: globalThis.PointerEvent) {
      interaction.current.focused = false;
      if (!(event.target instanceof Node && triggerRef.current?.contains(event.target)))
        interaction.current.hovered = false;
      if (!interaction.current.hovered) {
        interaction.current.pointerDismissed = true;
        close();
      }
    }
    function blur() {
      interaction.current.hovered = false;
      interaction.current.focused = false;
      close();
    }
    window.addEventListener("scroll", follow, true);
    window.addEventListener("resize", follow);
    window.addEventListener("keydown", escape);
    window.addEventListener("pointerdown", pointerDown, true);
    window.addEventListener("blur", blur);
    return () => {
      window.removeEventListener("scroll", follow, true);
      window.removeEventListener("resize", follow);
      window.removeEventListener("keydown", escape);
      window.removeEventListener("pointerdown", pointerDown, true);
      window.removeEventListener("blur", blur);
    };
  }, [portal, enabled, open, keyboardFocusOnly, followPointer]);

  const bubble = (
    <span
      ref={portal ? bubbleRef : undefined}
      className={`tooltip-content${portal ? " tooltip-content--portal" : ""}${singleLine ? " tooltip-content--single-line" : ""}${multiline ? " tooltip-content--multiline" : ""}${portal && open ? " is-open" : ""}`}
      style={portal ? { ...position, top: position.top + offsetY } : undefined}
      id={tooltipId}
      role="tooltip"
    >
      {content}
    </span>
  );
  return (
    <span className={`tooltip ${className}`.trim()}>
      <span
        className="tooltip-trigger"
        tabIndex={focusable && enabled ? 0 : undefined}
        aria-describedby={enabled ? tooltipId : undefined}
        aria-label={label}
        ref={portal ? triggerRef : undefined}
        {...(portal
          ? {
              onPointerEnter: (event: ReactPointerEvent<HTMLSpanElement>) => {
                rememberPointer(event);
                interaction.current.hovered = true;
                interaction.current.pointerDismissed =
                  dismissOnPointerDown && Boolean(event.buttons);
                place(true);
              },
              onPointerDownCapture: () => {
                if (!dismissOnPointerDown) return;
                // Cancel the CSS reveal delay and stay dismissed for this hover.
                interaction.current.pointerDismissed = true;
                interaction.current.focused = false;
                close();
              },
              onPointerMove: (event: ReactPointerEvent<HTMLSpanElement>) => {
                rememberPointer(event);
                if (
                  !followPointer ||
                  !interaction.current.hovered ||
                  activePortalTooltip?.id !== tooltipId ||
                  pointerFrame.current !== undefined
                )
                  return;
                pointerFrame.current = requestAnimationFrame(() => {
                  pointerFrame.current = undefined;
                  latestPlace.current();
                });
              },
              onPointerLeave: () => {
                interaction.current.hovered = false;
                if (dismissOnPointerLeave) interaction.current.focused = false;
                if (!interaction.current.focused) close();
                else if (followPointer) place();
              },
              onFocus: (event: FocusEvent<HTMLSpanElement>) => {
                interaction.current.focused = acceptsFocus(event.target);
                interaction.current.focusedElement = event.target;
                if (interaction.current.focused) interaction.current.pointerDismissed = false;
                if (interaction.current.focused || interaction.current.hovered) place(true);
                else close();
              },
              onBlur: (event: FocusEvent<HTMLSpanElement>) => {
                if (event.currentTarget.contains(event.relatedTarget)) return;
                interaction.current.focused = false;
                if (!interaction.current.hovered) close();
              },
            }
          : {})}
      >
        {children}
      </span>
      {enabled && (portal ? createPortal(bubble, document.body) : bubble)}
    </span>
  );
}

export function InfoTip({ content, label }: { content: string; label: string }) {
  const tooltipId = useId();
  const containerRef = useRef<HTMLSpanElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const [open, setOpen] = useState(false);
  const [dismissed, setDismissed] = useState(false);

  useEffect(() => {
    if (!open) return;
    function outside(event: PointerEvent) {
      if (!containerRef.current?.contains(event.target as Node)) {
        setOpen(false);
        setDismissed(true);
      }
    }
    document.addEventListener("pointerdown", outside);
    return () => document.removeEventListener("pointerdown", outside);
  }, [open]);

  function escape(event: KeyboardEvent<HTMLButtonElement>) {
    if (event.key !== "Escape" || !open) return;
    event.preventDefault();
    setOpen(false);
    setDismissed(true);
    triggerRef.current?.focus();
  }

  function blur(event: FocusEvent<HTMLSpanElement>) {
    if (event.relatedTarget instanceof Node && event.currentTarget.contains(event.relatedTarget))
      return;
    setOpen(false);
    setDismissed(false);
  }

  return (
    <span
      ref={containerRef}
      className={`tooltip info-tip${open ? " is-open" : ""}${dismissed ? " is-dismissed" : ""}`}
      onBlur={blur}
    >
      <button
        ref={triggerRef}
        type="button"
        className="tooltip-trigger"
        aria-label={label}
        aria-describedby={tooltipId}
        aria-expanded={open}
        onClick={() => {
          setOpen((value) => !value);
          setDismissed(open);
        }}
        onKeyDown={escape}
      >
        <span aria-hidden="true">?</span>
      </button>
      <span className="tooltip-content" id={tooltipId} role="tooltip">
        {content}
      </span>
    </span>
  );
}

export function offsetLabel(offset: number, compact = false) {
  const sign = offset < 0 ? "-" : "+";
  const absolute = Math.abs(offset);
  const hours = Math.floor(absolute / 60);
  const minutes = absolute % 60;
  return `UTC${sign}${hours}${compact && minutes === 0 ? "" : `:${String(minutes).padStart(2, "0")}`}`;
}

export function zoneOffsetAt(timeZone: string, instant: Date) {
  try {
    const name = dateTimeFormatter("en-US", { timeZone, timeZoneName: "longOffset" })
      .formatToParts(instant)
      .find((part) => part.type === "timeZoneName")?.value;
    const match = /^(?:GMT|UTC)(?:([+-])(\d{1,2})(?::?(\d{2}))?)?$/.exec(name ?? "");
    if (!match) return null;
    if (!match[1]) return 0;
    const minutes = Number(match[2]) * 60 + Number(match[3] ?? 0);
    return match[1] === "-" ? -minutes : minutes;
  } catch {
    return null;
  }
}

export function instantZone(timeZone: string, instant: Date) {
  const offset = zoneOffsetAt(timeZone, instant);
  if (offset == null) return { label: "TZ unavailable", title: `${timeZone} (offset unavailable)` };
  return {
    label: zoneAbbreviation(timeZone, instant),
    title: `${timeZone} (${offsetLabel(offset)})`,
  };
}

export function zoneAbbreviation(timeZone: string, instant: Date) {
  return (
    dateTimeFormatter("en-US", { timeZone, timeZoneName: "short" })
      .formatToParts(instant)
      .find((part) => part.type === "timeZoneName")?.value ?? timeZone
  );
}

export function ZoneBadge({
  label,
  title,
  focusable = true,
}: {
  label: string;
  title: string;
  focusable?: boolean;
}) {
  return (
    <Tooltip content={title} className="timezone-badge" focusable={focusable} label={title}>
      <span className="date-badge-text">{label}</span>
    </Tooltip>
  );
}
