import * as Popover from "@radix-ui/react-popover";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { useEffect, useId, useRef, useState } from "react";
import type {
  Preferences,
  WorkoutColumn,
  WorkoutComparison,
  WorkoutList,
  WorkoutType,
} from "./api";
import { Tooltip } from "./Tooltip";
import { LONG_PRESS_DELAY } from "./interaction";
import { useTabActive } from "./RetainedTab";
import {
  checkboxColumns,
  columnFilterEnabled,
  numericFilterScale,
  WORKOUT_COLUMN_LABELS,
  type ColumnFilters,
  type NumericFilter,
} from "./workoutFilters";

const HEADER_HELP =
  "Click to sort or toggle sort direction; click\nand hold to apply custom filter (use space\nand down keys in keyboard mode)";
const COMPARISONS: Record<WorkoutComparison, string> = { gt: ">", gte: "≥", lt: "<", lte: "≤" };
const WEEKDAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

function ComparisonSelect({
  value,
  onChange,
  disabled,
  pace,
}: {
  value: WorkoutComparison;
  onChange: (value: WorkoutComparison) => void;
  disabled: boolean;
  pace: boolean;
}) {
  const choices: WorkoutComparison[] = pace
    ? ["lt", "lte", "gt", "gte"]
    : ["gt", "gte", "lt", "lte"];
  return (
    <DropdownMenu.Root modal={false}>
      <DropdownMenu.Trigger
        className="range-trigger workout-filter-operator"
        disabled={disabled}
        aria-label="Comparison operator"
      >
        <span>{COMPARISONS[value]}</span>
        <span aria-hidden="true">v</span>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          className="menu-content history-filter-menu workout-comparison-menu"
          align="start"
          sideOffset={4}
          collisionPadding={12}
        >
          <DropdownMenu.RadioGroup
            value={value}
            onValueChange={(next) => onChange(next as WorkoutComparison)}
          >
            {choices.map((operator) => (
              <DropdownMenu.RadioItem
                key={operator}
                value={operator}
                className="history-filter-option"
              >
                {COMPARISONS[operator]}
                <DropdownMenu.ItemIndicator aria-hidden="true">&#10003;</DropdownMenu.ItemIndicator>
              </DropdownMenu.RadioItem>
            ))}
          </DropdownMenu.RadioGroup>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

export function WorkoutColumnFilter({
  column,
  filters,
  extents,
  types,
  preferences,
  direction,
  open,
  onOpenChange,
  onSort,
  onApply,
  mobile = false,
  canRestoreFocus = true,
}: {
  column: WorkoutColumn;
  filters: ColumnFilters;
  extents?: WorkoutList["columnExtents"];
  types: WorkoutType[];
  preferences: Preferences;
  direction?: "asc" | "desc";
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSort: () => void;
  onApply: (filter: string[] | NumericFilter | undefined) => void;
  mobile?: boolean;
  canRestoreFocus?: boolean;
}) {
  const tabActive = useTabActive();
  const id = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const outside = useRef(false);
  const focusPolicy = useRef({ tabActive, canRestoreFocus });
  focusPolicy.current = { tabActive, canRestoreFocus };
  const suppressClick = useRef(false);
  const pressTimer = useRef<number | undefined>(undefined);
  const firstDay = preferences.firstWeekday === "sunday" ? 0 : 1;
  const options =
    column === "date"
      ? Array.from({ length: 7 }, (_, index) => ({
          id: String((index + firstDay) % 7),
          displayName: WEEKDAYS[(index + firstDay) % 7],
        }))
      : types;
  const selection = column === "date" || column === "type";
  const scale = selection ? undefined : numericFilterScale(column, extents, preferences.units);
  const [checked, setChecked] = useState<string[]>([]);
  const [numeric, setNumeric] = useState<NumericFilter>({
    enabled: false,
    operator: "gt",
    value: "0",
  });
  const current = useRef({ filters, options, scale });
  current.current = { filters, options, scale };
  const allChecked = options.every((item) => checked.includes(item.id));
  const title =
    column === "date"
      ? "Show days of week"
      : column === "type"
        ? "Show workout types"
        : `Show ${column === "calories" ? "calorie" : column === "heartRate" ? "heart rate" : column === "elevationGain" ? "elevation gain" : column} range`;
  function cancelPress() {
    window.clearTimeout(pressTimer.current);
    pressTimer.current = undefined;
  }
  function openPicker() {
    cancelPress();
    outside.current = false;
    const { filters: saved, options: available, scale: limits } = current.current;
    if (column === "date" || column === "type")
      setChecked(saved[column] ?? available.map((item) => item.id));
    else if (limits) {
      const filter = saved[column];
      const sliderValue = filter ? limits.toSlider(filter.value) : limits.defaultValue;
      const pace =
        column === "pace"
          ? {
              value: String(sliderValue),
              unit: preferences.units === "imperial" ? ("s/mi" as const) : ("s/km" as const),
            }
          : undefined;
      setNumeric(
        filter
          ? { ...filter, value: limits.toCanonical(sliderValue), pace }
          : {
              enabled: false,
              operator: column === "pace" ? "lt" : "gt",
              value: limits.toCanonical(limits.defaultValue),
              pace,
            },
      );
    }
    onOpenChange(true);
  }
  const openPickerRef = useRef(openPicker);
  openPickerRef.current = openPicker;
  useEffect(() => {
    const cancel = () => cancelPress();
    window.addEventListener("pointerup", cancel, true);
    window.addEventListener("pointercancel", cancel, true);
    window.addEventListener("blur", cancel);
    window.addEventListener("scroll", cancel, true);
    return () => {
      cancel();
      window.removeEventListener("pointerup", cancel, true);
      window.removeEventListener("pointercancel", cancel, true);
      window.removeEventListener("blur", cancel);
      window.removeEventListener("scroll", cancel, true);
    };
  }, []);
  useEffect(() => {
    if (!tabActive) cancelPress();
  }, [tabActive]);
  return (
    <Popover.Root open={open} onOpenChange={onOpenChange}>
      <Tooltip
        id={`${id}-help`}
        content={HEADER_HELP}
        portal
        multiline
        keyboardFocusOnly
        dismissOnPointerDown
        focusable={false}
        enabled={!open && tabActive}
        className="workout-column-help"
      >
        <Popover.Anchor asChild>
          <button
            ref={trigger}
            type="button"
            className="workout-column-trigger"
            aria-haspopup="dialog"
            aria-expanded={open}
            aria-controls={open ? `${id}-panel` : undefined}
            aria-describedby={open ? undefined : `${id}-help`}
            aria-label={`${WORKOUT_COLUMN_LABELS[column]}${mobile ? " column" : ""}`}
            aria-description={columnFilterEnabled(filters, column) ? "Filter enabled" : undefined}
            onPointerDown={(event) => {
              if (event.button !== 0) return;
              cancelPress();
              suppressClick.current = false;
              event.currentTarget.focus({ preventScroll: true });
              pressTimer.current = window.setTimeout(() => {
                suppressClick.current = true;
                openPickerRef.current();
              }, LONG_PRESS_DELAY);
            }}
            onPointerLeave={cancelPress}
            onContextMenu={(event) => {
              if (open || pressTimer.current !== undefined) event.preventDefault();
            }}
            onClick={() => {
              if (!suppressClick.current) onSort();
              suppressClick.current = false;
            }}
            onKeyDown={(event) => {
              if (event.altKey || event.ctrlKey || event.metaKey) return;
              if (event.key === " " || event.key === "ArrowDown") {
                event.preventDefault();
                suppressClick.current = false;
                if (!event.repeat) openPicker();
              } else if (event.key === "Tab" || event.key === "Escape" || event.key === "Enter") {
                cancelPress();
                suppressClick.current = false;
              }
            }}
          >
            {columnFilterEnabled(filters, column) && (
              <svg
                className="workout-filter-indicator"
                aria-hidden="true"
                focusable="false"
                viewBox="0 0 640 640"
                fill="currentColor"
              >
                <path d="M96 128C83.1 128 71.4 135.8 66.4 147.8C61.4 159.8 64.2 173.5 73.4 182.6L256 365.3L256 480C256 488.5 259.4 496.6 265.4 502.6L329.4 566.6C338.6 575.8 352.3 578.5 364.3 573.5C376.3 568.5 384 556.9 384 544L384 365.3L566.6 182.7C575.8 173.5 578.5 159.8 573.5 147.8C568.5 135.8 556.9 128 544 128L96 128z" />
              </svg>
            )}
            <span className="column-label">{WORKOUT_COLUMN_LABELS[column]}</span>
            {columnFilterEnabled(filters, column) && (
              <span className="visually-hidden"> (filtered)</span>
            )}
            <span className="sort-indicator" aria-hidden="true">
              {direction ? direction === "asc" ? <>&#9650;</> : <>&#9660;</> : <>&#9650; &#9660;</>}
            </span>
          </button>
        </Popover.Anchor>
      </Tooltip>
      <Popover.Portal>
        <Popover.Content
          id={`${id}-panel`}
          aria-labelledby={`${id}-heading`}
          className={`menu-content workout-types-menu workout-column-menu${selection ? "" : " workout-numeric-menu"}`}
          align="start"
          side="bottom"
          sideOffset={4}
          collisionPadding={12}
          onInteractOutside={() => {
            outside.current = true;
          }}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            if (
              !outside.current &&
              focusPolicy.current.tabActive &&
              focusPolicy.current.canRestoreFocus
            )
              trigger.current?.focus({ preventScroll: true });
          }}
        >
          <div className="workout-types-heading workout-column-filter-heading">
            <span id={`${id}-heading`}>{title}</span>
            <Tooltip
              content={
                selection
                  ? allChecked
                    ? "Deselect all"
                    : "Select all"
                  : numeric.enabled
                    ? "Disable filter"
                    : "Enable filter"
              }
              portal
              focusable={false}
              keyboardFocusOnly
              dismissOnPointerDown
              dismissOnPointerLeave
            >
              <button
                type="button"
                className="text-action"
                onClick={() => {
                  if (selection) setChecked(allChecked ? [] : options.map((item) => item.id));
                  else setNumeric((previous) => ({ ...previous, enabled: !previous.enabled }));
                }}
              >
                {selection ? (allChecked ? "None" : "All") : numeric.enabled ? "Disable" : "Enable"}
              </button>
            </Tooltip>
          </div>
          {selection ? (
            <div className="workout-types-options workout-filter-columns">
              {checkboxColumns(options).map((items, index) => (
                <div className="workout-filter-checkbox-column" key={index}>
                  {items.map((item) => (
                    <label className="workout-type-option" key={item.id}>
                      <input
                        type="checkbox"
                        checked={checked.includes(item.id)}
                        onChange={(event) => {
                          const selected = event.currentTarget.checked;
                          setChecked((previous) =>
                            selected
                              ? [...previous, item.id]
                              : previous.filter((id) => id !== item.id),
                          );
                        }}
                      />
                      <span>{item.displayName}</span>
                      <span className="workout-type-checkmark" aria-hidden="true">
                        {checked.includes(item.id) && <>&#10003;</>}
                      </span>
                    </label>
                  ))}
                </div>
              ))}
              {options.length === 0 && (
                <p className="workout-filter-no-types">No workout types in this range.</p>
              )}
            </div>
          ) : (
            scale && (
              <div className="workout-numeric-controls">
                <ComparisonSelect
                  value={numeric.operator}
                  pace={column === "pace"}
                  disabled={!numeric.enabled}
                  onChange={(operator) => setNumeric((previous) => ({ ...previous, operator }))}
                />
                <span className="workout-slider-bound">{scale.format(scale.min)}</span>
                <label className="workout-filter-slider">
                  <span>{scale.format(Number(numeric.value) * scale.factor)}</span>
                  <input
                    type="range"
                    min={scale.min}
                    max={scale.max}
                    step={scale.step}
                    value={Number(numeric.value) * scale.factor}
                    disabled={!numeric.enabled}
                    aria-label={`${WORKOUT_COLUMN_LABELS[column]} threshold`}
                    aria-valuetext={scale.format(Number(numeric.value) * scale.factor)}
                    onChange={(event) => {
                      const sliderValue = Number(event.currentTarget.value);
                      const value = scale.toCanonical(sliderValue);
                      const pace =
                        column === "pace"
                          ? {
                              value: String(sliderValue),
                              unit:
                                preferences.units === "imperial"
                                  ? ("s/mi" as const)
                                  : ("s/km" as const),
                            }
                          : undefined;
                      setNumeric((previous) => ({ ...previous, value, pace }));
                    }}
                  />
                </label>
                <span className="workout-slider-bound">{scale.format(scale.max)}</span>
              </div>
            )
          )}
          <div className="workout-types-actions">
            <button type="button" className="secondary" onClick={() => onOpenChange(false)}>
              Cancel
            </button>
            <button
              type="button"
              className="primary"
              onClick={() => {
                onApply(
                  selection
                    ? allChecked
                      ? undefined
                      : checked.filter((id) => options.some((item) => item.id === id))
                    : numeric,
                );
                onOpenChange(false);
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
