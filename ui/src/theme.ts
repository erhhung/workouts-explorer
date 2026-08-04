export type Theme = "light" | "dark";

export const THEME_STORAGE_KEY = "workouts-explorer.theme";

export function applyTheme(theme: Theme) {
  const root = document.documentElement;
  root.dataset.theme = theme;
  root.style.colorScheme = theme;
  document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')?.setAttribute(
    "content",
    theme === "light" ? "#f3f0e7" : "#0b1514",
  );
  try {
    localStorage.setItem(THEME_STORAGE_KEY, theme);
  } catch {
    // Storage can be denied; the DOM theme remains usable for this page.
  }
}
