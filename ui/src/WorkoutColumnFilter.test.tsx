import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, test, vi } from "vitest";
import type { Preferences, WorkoutColumn, WorkoutType } from "./api";
import { WorkoutColumnFilter } from "./WorkoutColumnFilter";
import {
  checkboxColumns,
  numericFilterScale,
  workoutFilterParams,
  type ColumnFilters,
} from "./workoutFilters";

const preferences = { units: "imperial", firstWeekday: "monday" } as Preferences;
const types: WorkoutType[] = Array.from({ length: 19 }, (_, index) => ({
  id: String(index),
  key: String(index),
  displayName: `Workout type ${String(index + 1).padStart(2, "0")}`,
}));
const extents = {
  duration: "8001",
  distance: { value: "32", unit: "km" },
  pace: { value: "10", unit: "min/km" },
  calories: { value: "4050", unit: "kcal" },
  heartRate: { value: "167", unit: "count/min" },
  elevationGain: { value: "3300", unit: "m" },
};
function setup(column: WorkoutColumn = "date", prefs = preferences) {
  const onSort = vi.fn(),
    onApply = vi.fn();
  function Harness() {
    const [open, setOpen] = useState(false);
    const [filters, setFilters] = useState<ColumnFilters>({});
    return (
      <>
        <button>Outside</button>
        <WorkoutColumnFilter
          column={column}
          filters={filters}
          extents={extents}
          types={types}
          preferences={prefs}
          open={open}
          onOpenChange={setOpen}
          onSort={onSort}
          onApply={(next) => {
            onApply(next);
            setFilters((previous) => ({ ...previous, [column]: next }));
          }}
        />
      </>
    );
  }
  const view = render(<Harness />);
  const header = view.container.querySelector(".workout-column-trigger") as HTMLButtonElement;
  return { ...view, header, onSort, onApply, user: userEvent.setup() };
}

describe("Workout column filters", () => {
  test.each(["date", "duration"] as const)(
    "%s link help dismisses on pointer exit despite popup autofocus",
    async (column) => {
      const previous = document.documentElement.dataset.inputModality;
      document.documentElement.dataset.inputModality = "pointer";
      try {
        vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue(
          new DOMRect(100, 100, 120, 30),
        );
        const { header, user } = setup(column);
        fireEvent.keyDown(header, { key: "ArrowDown" });
        const dialog = screen.getByRole("dialog");
        const link = within(dialog).getByRole("button", {
          name: column === "date" ? "None" : "Enable",
        });
        await waitFor(() => expect(link).toHaveFocus());
        const wrapper = link.closest(".tooltip")!.querySelector(".tooltip-trigger")!;
        const help = document.getElementById(wrapper.getAttribute("aria-describedby")!)!;
        expect(help).not.toHaveClass("is-open");
        await user.hover(link);
        expect(help).toHaveClass("is-open");
        await user.unhover(link);
        expect(link).toHaveFocus();
        expect(help).not.toHaveClass("is-open");
        await user.click(link);
        expect(link).toHaveTextContent(column === "date" ? "All" : "Disable");
        await user.unhover(link);
        await user.hover(link);
        expect(help).toHaveClass("is-open");
        await user.unhover(link);
        expect(help).not.toHaveClass("is-open");
        document.documentElement.dataset.inputModality = "keyboard";
        fireEvent.focus(link);
        expect(help).toHaveClass("is-open");
        await user.hover(link);
        await user.unhover(link);
        expect(link).toHaveFocus();
        expect(help).not.toHaveClass("is-open");
      } finally {
        if (previous === undefined) delete document.documentElement.dataset.inputModality;
        else document.documentElement.dataset.inputModality = previous;
      }
    },
  );

  test("holds for the shared 500ms and suppresses sort; a short click and Enter still sort", async () => {
    const { header, onSort, user } = setup();
    await user.click(header);
    expect(onSort).toHaveBeenCalledTimes(1);
    vi.useFakeTimers();
    fireEvent(header, new MouseEvent("pointerdown", { bubbles: true, button: 0 }));
    act(() => vi.advanceTimersByTime(499));
    expect(screen.queryByRole("dialog")).toBeNull();
    act(() => vi.advanceTimersByTime(1));
    expect(screen.getByRole("dialog", { name: "Show days of week" })).toBeInTheDocument();
    fireEvent(header, new MouseEvent("pointerup", { bubbles: true, button: 0 }));
    fireEvent.click(header);
    expect(onSort).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    header.focus();
    await user.keyboard("{Enter}");
    expect(onSort).toHaveBeenCalledTimes(2);
  });

  test.each(["pointerleave", "pointercancel", "blur", "scroll"])(
    "cancels an unfinished press on %s",
    (event) => {
      const { header } = setup();
      vi.useFakeTimers();
      fireEvent(header, new MouseEvent("pointerdown", { bubbles: true, button: 0 }));
      act(() => vi.advanceTimersByTime(250));
      if (event === "pointerleave") fireEvent.pointerLeave(header);
      else fireEvent(window, new Event(event, { bubbles: true }));
      act(() => vi.advanceTimersByTime(500));
      expect(screen.queryByRole("dialog")).toBeNull();
    },
  );

  test.each(["monday", "sunday"] as const)(
    "orders weekdays from %s and commits only Apply",
    async (firstWeekday) => {
      const { header, user, onApply, onSort } = setup("date", { ...preferences, firstWeekday });
      header.focus();
      await user.keyboard(" ");
      const dialog = screen.getByRole("dialog", { name: "Show days of week" });
      expect(within(dialog).getAllByRole("checkbox")[0]).toHaveAccessibleName(
        firstWeekday === "monday" ? "Monday" : "Sunday",
      );
      expect(within(dialog).getAllByRole("checkbox")).toHaveLength(7);
      await user.click(within(dialog).getByRole("button", { name: "None" }));
      expect(onApply).not.toHaveBeenCalled();
      await user.click(screen.getByRole("button", { name: "Outside" }));
      expect(onApply).not.toHaveBeenCalled();
      header.focus();
      await user.keyboard("{ArrowDown}");
      expect(
        screen.getAllByRole("checkbox").every((item) => (item as HTMLInputElement).checked),
      ).toBe(true);
      await user.click(screen.getByRole("checkbox", { name: "Tuesday" }));
      await user.click(screen.getByRole("button", { name: "Apply" }));
      expect(onApply).toHaveBeenCalledTimes(1);
      expect(onApply.mock.calls[0][0]).not.toContain("2");
      expect(header.querySelector(".workout-filter-indicator")).toBeInTheDocument();
      header.focus();
      await user.keyboard("{ArrowDown}");
      expect(screen.getByRole("checkbox", { name: "Tuesday" })).not.toBeChecked();
      await user.click(screen.getByRole("button", { name: "All" }));
      await user.click(screen.getByRole("button", { name: "Apply" }));
      expect(onApply).toHaveBeenLastCalledWith(undefined);
      expect(header.querySelector(".workout-filter-indicator")).toBeNull();
      expect(onSort).not.toHaveBeenCalled();
    },
  );

  test("distributes type choices into 7,7,5 and keeps the complete unfiltered list", async () => {
    const { header, user } = setup("type");
    header.focus();
    await user.keyboard("{ArrowDown}");
    const columns = document.querySelectorAll(".workout-filter-checkbox-column");
    expect([...columns].map((column) => column.querySelectorAll("input").length)).toEqual([
      7, 7, 5,
    ]);
    expect(screen.getAllByRole("checkbox")).toHaveLength(19);
    await user.click(screen.getByRole("button", { name: "None" }));
    await user.click(screen.getByRole("checkbox", { name: "Workout type 19" }));
    await user.click(screen.getByRole("button", { name: "Apply" }));
    header.focus();
    await user.keyboard("{ArrowDown}");
    expect(screen.getAllByRole("checkbox")).toHaveLength(19);
    expect(screen.getByRole("checkbox", { name: "Workout type 19" })).toBeChecked();
  });

  test.each([
    ["duration", "0", "8100", "900", "0m", [">", "≥", "<", "≤"]],
    ["distance", "0", "20", "0.5", "0 mi", [">", "≥", "<", "≤"]],
    ["pace", "60", "990", "30", "16m 30s", ["<", "≤", ">", "≥"]],
    ["calories", "0", "4100", "100", "0 kcal", [">", "≥", "<", "≤"]],
    ["heartRate", "0", "170", "10", "0 bpm", [">", "≥", "<", "≤"]],
    ["elevationGain", "0", "10900", "100", "0 ft", [">", "≥", "<", "≤"]],
  ] as const)(
    "%s uses its required range, step, default and operator order",
    async (column, min, max, step, defaultValue, operators) => {
      const { header, user, onApply } = setup(column);
      header.focus();
      await user.keyboard("{ArrowDown}");
      const slider = screen.getByRole("slider");
      expect(slider).toHaveAttribute("min", min);
      expect(slider).toHaveAttribute("max", max);
      expect(slider).toHaveAttribute("step", step);
      expect(slider).toHaveAttribute("aria-valuetext", defaultValue);
      expect(slider).toBeDisabled();
      await user.click(screen.getByRole("button", { name: "Enable" }));
      expect(slider).toBeEnabled();
      await user.click(screen.getByRole("button", { name: "Comparison operator" }));
      expect(
        screen.getAllByRole("menuitemradio").map((item) => item.textContent?.replace("\u2713", "")),
      ).toEqual(operators);
      await user.click(screen.getByRole("menuitemradio", { name: operators[1] }));
      fireEvent.change(slider, { target: { value: column === "pace" ? "600" : step } });
      expect(onApply).not.toHaveBeenCalled();
      await user.click(screen.getByRole("button", { name: "Apply" }));
      expect(onApply).toHaveBeenCalledTimes(1);
      expect(onApply.mock.calls[0][0].operator).toBe(column === "pace" ? "lte" : "gte");
      if (column === "pace")
        expect(workoutFilterParams({ pace: onApply.mock.calls[0][0] })).toEqual({
          pace: { operator: "lte", value: "600", unit: "s/mi" },
        });
      expect(header.querySelector(".workout-filter-indicator")).toBeInTheDocument();
      header.focus();
      await user.keyboard("{ArrowDown}");
      await user.click(screen.getByRole("button", { name: "Disable" }));
      await user.keyboard("{Escape}");
      expect(onApply).toHaveBeenCalledTimes(1);
      expect(header.querySelector(".workout-filter-indicator")).toBeInTheDocument();
    },
  );

  test("uses exact physical conversions, metric steps and only the specified caps", () => {
    expect(numericFilterScale("duration", { ...extents, duration: "200000" }, "imperial").max).toBe(
      86400,
    );
    expect(
      numericFilterScale(
        "pace",
        { ...extents, pace: { value: "1000", unit: "min/km" } },
        "imperial",
      ).max,
    ).toBe(3600);
    expect(numericFilterScale("distance", extents, "imperial").toCanonical(0.5)).toBe("0.804672");
    expect(numericFilterScale("distance", extents, "metric").max).toBe(32);
    expect(numericFilterScale("distance", extents, "metric").step).toBe(0.5);
    expect(numericFilterScale("elevationGain", extents, "imperial").toCanonical(100)).toBe("30.48");
    expect(numericFilterScale("elevationGain", extents, "metric").step).toBe(25);
    expect(numericFilterScale("elevationGain", extents, "metric").max).toBe(3300);
    expect(checkboxColumns(Array.from({ length: 12 })).map((items) => items.length)).toEqual([
      6, 6,
    ]);
    expect(checkboxColumns(Array.from({ length: 16 })).map((items) => items.length)).toEqual([
      8, 8,
    ]);
  });
});
