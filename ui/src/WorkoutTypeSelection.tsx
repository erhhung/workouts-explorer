import * as Popover from "@radix-ui/react-popover";
import { useEffect, useId, useMemo, useRef, useState } from "react";
import type { MapSelectionWorkout } from "./api";
import { Tooltip } from "./Tooltip";

const CHECKBOX_HELP =
  "Toggle checkboxes to show or hide all routes;\nclick and hold to filter routes by workout types\n(use space and down keys in keyboard mode)";

export function WorkoutTypeSelection({
  workouts,
  selectedIds,
  allSelected,
  onToggleAll,
  onApply,
}: {
  workouts: Array<Pick<MapSelectionWorkout, "id" | "type">>;
  selectedIds?: string[];
  allSelected: boolean;
  onToggleAll: () => void;
  onApply: (ids: string[]) => void;
}) {
  const headingId = useId();
  const helpId = `${headingId}-help`;
  const checkbox = useRef<HTMLInputElement>(null);
  const outsideInteraction = useRef(false);
  const anchor = useRef({
    contextElement: undefined as Element | undefined,
    getBoundingClientRect() {
      const control = checkbox.current?.getBoundingClientRect() ?? new DOMRect();
      const fieldset = checkbox.current?.closest("fieldset");
      const frame = fieldset?.getBoundingClientRect() ?? control;
      const list = fieldset?.querySelector(".map-filter-options")?.getBoundingClientRect();
      return new DOMRect(frame.left, list?.top ?? control.bottom, frame.width, 0);
    },
  });
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState(new Set<string>());
  const pressTimer = useRef<number | undefined>(undefined);
  const suppressClick = useRef(false);
  const current = useRef({ workouts, selectedIds });
  current.current = { workouts, selectedIds };
  const types = useMemo(() => {
    const grouped = new Map<string, string>();
    for (const workout of workouts) {
      grouped.set(workout.type.id, workout.type.name);
    }
    return [...grouped].sort((a, b) => a[1].localeCompare(b[1]));
  }, [workouts]);
  const membership = workouts.map((workout) => `${workout.id}:${workout.type.id}`).join(",");

  function cancelPress() {
    window.clearTimeout(pressTimer.current);
    pressTimer.current = undefined;
  }

  function openPicker() {
    const { workouts: latestWorkouts, selectedIds: latestIds } = current.current;
    const selected = new Set(latestIds ?? latestWorkouts.map((workout) => workout.id));
    const checked = new Set(latestWorkouts.map((workout) => workout.type.id));
    for (const workout of latestWorkouts) {
      if (!selected.has(workout.id)) checked.delete(workout.type.id);
    }
    setDraft(checked);
    outsideInteraction.current = false;
    setOpen(true);
  }

  useEffect(() => {
    cancelPress();
    setOpen(false);
  }, [membership]);

  useEffect(() => {
    window.addEventListener("pointerup", cancelPress, true);
    window.addEventListener("pointercancel", cancelPress, true);
    window.addEventListener("blur", cancelPress);
    window.addEventListener("scroll", cancelPress, true);
    return () => {
      cancelPress();
      window.removeEventListener("pointerup", cancelPress, true);
      window.removeEventListener("pointercancel", cancelPress, true);
      window.removeEventListener("blur", cancelPress);
      window.removeEventListener("scroll", cancelPress, true);
    };
  }, []);

  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Anchor virtualRef={anchor} />
      <label className="map-route-toggle">
        <Tooltip
          id={helpId}
          content={CHECKBOX_HELP}
          className="map-route-toggle-help"
          portal
          multiline
          offsetY={-2}
          keyboardFocusOnly
          dismissOnPointerDown
          focusable={false}
          enabled={!open}
        >
          <input
            ref={(node) => {
              checkbox.current = node;
              anchor.current.contextElement = node?.closest("fieldset") ?? node ?? undefined;
            }}
            type="checkbox"
            aria-label="Select all workout routes"
            aria-haspopup="dialog"
            aria-expanded={open}
            aria-controls={open ? `${headingId}-panel` : undefined}
            aria-describedby={open ? undefined : helpId}
            checked={allSelected}
            disabled={!workouts.length}
            onPointerDown={(event) => {
              if (event.button !== 0 || !workouts.length) return;
              cancelPress();
              suppressClick.current = false;
              event.currentTarget.focus({ preventScroll: true });
              pressTimer.current = window.setTimeout(() => {
                pressTimer.current = undefined;
                suppressClick.current = true;
                openPicker();
              }, 500);
            }}
            onPointerLeave={cancelPress}
            onChange={() => {
              if (!suppressClick.current) onToggleAll();
            }}
            onKeyDown={(event) => {
              if (event.key === " " || event.key === "Enter") {
                event.preventDefault();
                cancelPress();
                suppressClick.current = false;
                if (!event.repeat) onToggleAll();
              } else if (event.key === "ArrowDown") {
                event.preventDefault();
                cancelPress();
                openPicker();
              } else if (event.key === "Tab" || event.key === "Escape") {
                cancelPress();
              }
            }}
          />
        </Tooltip>
      </label>
      <Popover.Portal
        container={checkbox.current?.closest<HTMLElement>('[role="dialog"]') ?? undefined}
      >
        <Popover.Content
          id={`${headingId}-panel`}
          className="menu-content workout-types-menu"
          side="bottom"
          align="start"
          sideOffset={0}
          avoidCollisions={false}
          collisionPadding={12}
          aria-labelledby={headingId}
          onInteractOutside={() => {
            outsideInteraction.current = true;
          }}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            if (!outsideInteraction.current) checkbox.current?.focus({ preventScroll: true });
          }}
        >
          <div id={headingId} className="workout-types-heading">
            Show workout types
          </div>
          <div className="workout-types-options">
            {types.map(([id, name]) => (
              <label key={id} className="workout-type-option">
                <input
                  type="checkbox"
                  checked={draft.has(id)}
                  onChange={(event) => {
                    const checked = event.currentTarget.checked;
                    setDraft((previous) => {
                      const next = new Set(previous);
                      if (checked) next.add(id);
                      else next.delete(id);
                      return next;
                    });
                  }}
                />
                <span>{name}</span>
                <span className="workout-type-checkmark" aria-hidden="true">
                  {draft.has(id) && <>&#10003;</>}
                </span>
              </label>
            ))}
          </div>
          <div className="workout-types-actions">
            <button type="button" className="secondary" onClick={() => setOpen(false)}>
              Cancel
            </button>
            <button
              type="button"
              className="primary"
              onClick={() => {
                onApply(
                  workouts
                    .filter((workout) => draft.has(workout.type.id))
                    .map((workout) => workout.id),
                );
                setOpen(false);
              }}
            >
              Apply
            </button>
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
