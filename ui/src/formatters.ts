type Locales = ConstructorParameters<typeof Intl.DateTimeFormat>[0];

const dates = new Map<string, Intl.DateTimeFormat>();
const numbers = new Map<string, Intl.NumberFormat>();

function formatterKey(locales: Locales, options: object) {
  const language =
    locales === undefined ? null : Array.isArray(locales) ? locales.map(String) : String(locales);
  return JSON.stringify([
    language,
    Object.entries(options)
      .filter(([, value]) => value !== undefined)
      .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)),
  ]);
}

function cached<T>(cache: Map<string, T>, key: string, maximum: number, create: () => T): T {
  const existing = cache.get(key);
  if (existing !== undefined) {
    cache.delete(key);
    cache.set(key, existing);
    return existing;
  }
  const value = create();
  cache.set(key, value);
  if (cache.size > maximum) cache.delete(cache.keys().next().value!);
  return value;
}

// Cache only formatter configuration, never dates, offsets or private row values.
// Each instant is still formatted independently, including across DST changes.
export function dateTimeFormatter(locales?: Locales, options: Intl.DateTimeFormatOptions = {}) {
  return cached(
    dates,
    formatterKey(locales, options),
    64,
    () => new Intl.DateTimeFormat(locales, options),
  );
}

export function numberFormatter(locales?: Locales, options: Intl.NumberFormatOptions = {}) {
  return cached(
    numbers,
    formatterKey(locales, options),
    16,
    () => new Intl.NumberFormat(locales, options),
  );
}

export function autoSyncIntervalLabel(seconds: number) {
  if (seconds === 86400) return "Once a day";
  if (seconds === 43200) return "Twice a day";
  const hours = seconds / 3600;
  return `Every ${numberFormatter(undefined, { maximumFractionDigits: 6 }).format(hours)} ${hours === 1 ? "hour" : "hours"}`;
}
