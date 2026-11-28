import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { useEffect, useState } from "react";
import type { DateRangePreference, Preferences } from "./api";
import { formatDateOnly } from "./date";
import { useDateFormat } from "./DateFormat";

export function useRecentDateRanges(
  preferences: Preferences,
  onSaved?: (ranges: string[]) => void,
) {
  const [history, setHistory] = useState(preferences.recentCustomDateRanges);
  useEffect(() => {
    setHistory(preferences.recentCustomDateRanges);
  }, [preferences.recentCustomDateRanges]);
  return {
    history: history.slice(0, preferences.customDateRangeLimit),
    saved(ranges: string[]) {
      setHistory(ranges);
      onSaved?.(ranges);
    },
  };
}

export function RecentDateRangeItems({
  ranges,
  current,
  onSelect,
}: {
  ranges: string[];
  current: DateRangePreference;
  onSelect: (range: DateRangePreference) => void;
}) {
  const dateFormat = useDateFormat();
  return ranges.map((range) => {
    const parts = /^(\d{4}-\d{2}-\d{2})\/(\d{4}-\d{2}-\d{2})$/.exec(range);
    if (!parts) return null;
    const value: DateRangePreference = `${parts[1]}/${parts[2]}`;
    return (
      <DropdownMenu.Item key={value} onSelect={() => current !== value && onSelect(value)}>
        {formatDateOnly(parts[1], dateFormat)} to {formatDateOnly(parts[2], dateFormat)}
      </DropdownMenu.Item>
    );
  });
}
