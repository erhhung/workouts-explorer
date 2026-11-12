import type { DateRangeEnum, DateRangePreference, Preferences } from "./api";
import { DATE_RANGE_SHORTCUTS } from "./DateRangePicker";

export interface MapWorkoutTarget {
  workoutId: string;
  localStartDate?: string | null;
}

export function shortcutDates(
  shortcut: DateRangeEnum,
  preferences: Pick<Preferences, "timezone" | "firstWeekday">,
  now = new Date(),
): { startDate: string; endDate: string } | undefined {
  if (shortcut === "allTime") return undefined;
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: preferences.timezone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(now);
  const part = (type: Intl.DateTimeFormatPartTypes) =>
    parts.find((value) => value.type === type)!.value;
  const today = `${part("year").padStart(4, "0")}-${part("month")}-${part("day")}`;
  const start = new Date(`${today}T00:00:00Z`);
  const end = new Date(start);
  switch (shortcut) {
    case "thisWeek":
    case "lastWeek": {
      const first = preferences.firstWeekday === "sunday" ? 0 : 1;
      const delta = (7 + start.getUTCDay() - first) % 7;
      start.setUTCDate(start.getUTCDate() - delta - (shortcut === "lastWeek" ? 7 : 0));
      end.setTime(start.getTime());
      end.setUTCDate(end.getUTCDate() + 6);
      break;
    }
    case "last7Days":
      start.setUTCDate(start.getUTCDate() - 6);
      break;
    case "last30Days":
      start.setUTCDate(start.getUTCDate() - 29);
      break;
    case "thisMonth":
      start.setUTCDate(1);
      end.setUTCMonth(end.getUTCMonth() + 1, 0);
      break;
    case "lastMonth":
      start.setUTCMonth(start.getUTCMonth() - 1, 1);
      end.setUTCDate(0);
      break;
    case "thisYear":
      start.setUTCMonth(0, 1);
      end.setUTCMonth(11, 31);
      break;
    case "lastYear":
      start.setUTCFullYear(start.getUTCFullYear() - 1, 0, 1);
      end.setUTCFullYear(end.getUTCFullYear() - 1, 11, 31);
      break;
  }
  return { startDate: start.toISOString().slice(0, 10), endDate: end.toISOString().slice(0, 10) };
}

export function rangeForWorkoutNavigation(
  current: DateRangePreference,
  localStartDate: string | null | undefined,
  preferences: Pick<Preferences, "timezone" | "firstWeekday">,
  now = new Date(),
): DateRangePreference {
  if (current === "allTime") return current;
  if (!localStartDate || !/^\d{4}-\d{2}-\d{2}$/.test(localStartDate)) return "allTime";
  const explicit = /^(\d{4}-\d{2}-\d{2})\/(\d{4}-\d{2}-\d{2})$/.exec(current);
  const contains = (range: { startDate: string; endDate: string } | undefined) =>
    !range || (localStartDate >= range.startDate && localStartDate <= range.endDate);
  if (
    contains(
      explicit
        ? { startDate: explicit[1], endDate: explicit[2] }
        : shortcutDates(current as DateRangeEnum, preferences, now),
    )
  )
    return current;
  return DATE_RANGE_SHORTCUTS.find(([shortcut]) =>
    contains(shortcutDates(shortcut, preferences, now)),
  )![0];
}
