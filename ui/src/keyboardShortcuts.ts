import { type RefObject, useLayoutEffect, useRef } from "react";

export type Shortcut =
  | { kind: "tab"; path: "/" | "/map" | "/stats" | "/data-sync" }
  | { kind: "page"; delta: number }
  | { kind: "map"; action: "routes" | "coverage" | "fit" }
  | { kind: "osm-regions"; action: "show-all" }
  | { kind: "search" }
  | { kind: "stats"; group: "week" | "month" | "year" };

type Registration = {
  kind: Shortcut["kind"];
  root: () => HTMLElement | null;
  handle: (shortcut: Shortcut) => boolean;
  defaultPager?: boolean;
};
type Registry = {
  entries: Set<Registration>;
  lastPager?: Registration;
  dispose: () => void;
};
const registries = new WeakMap<Document, Registry>();

export function shortcutForKey(event: KeyboardEvent): Shortcut | undefined {
  if (event.altKey || event.ctrlKey || event.metaKey || event.isComposing || event.keyCode === 229)
    return;
  const tabs = { "1": "/", "2": "/map", "3": "/stats", "4": "/data-sync" } as const;
  if (Object.hasOwn(tabs, event.key))
    return { kind: "tab", path: tabs[event.key as keyof typeof tabs] };
  if (event.key === "ArrowLeft" || event.key === "ArrowRight")
    return { kind: "page", delta: (event.key === "ArrowLeft" ? -1 : 1) * (event.shiftKey ? 5 : 1) };
  switch (event.key.toLowerCase()) {
    case "/":
      return { kind: "search" };
    case "a":
      return { kind: "osm-regions", action: "show-all" };
    case "c":
      return { kind: "map", action: "coverage" };
    case "r":
      return { kind: "map", action: "routes" };
    case "f":
      return { kind: "map", action: "fit" };
    case "m":
      return { kind: "stats", group: "month" };
    case "w":
      return { kind: "stats", group: "week" };
    case "y":
      return { kind: "stats", group: "year" };
  }
}

function visible(element: Element): boolean {
  if (
    !element.isConnected ||
    element.closest("[hidden], [inert], [aria-hidden='true'], .visually-hidden")
  )
    return false;
  if (["hidden", "collapse"].includes(getComputedStyle(element).visibility)) return false;
  for (let current: Element | null = element; current; current = current.parentElement)
    if (getComputedStyle(current).display === "none") return false;
  return true;
}

function textEntry(element: Element): boolean {
  if (
    element.matches(
      "textarea, [role='textbox'], [role='searchbox'], [contenteditable]:not([contenteditable='false'])",
    )
  )
    return true;
  return (
    element instanceof HTMLInputElement &&
    [
      "text",
      "search",
      "email",
      "url",
      "tel",
      "password",
      "number",
      "date",
      "time",
      "datetime-local",
      "month",
      "week",
    ].includes(element.type)
  );
}

function region(entry: Registration) {
  return (
    entry
      .root()
      ?.closest<HTMLElement>("[data-paging-region], [role='dialog'], [role='alertdialog']") ??
    entry.root()
  );
}
function deepest(entries: Registration[], target: Element | null) {
  return entries
    .filter((entry) => Boolean(target && region(entry)?.contains(target)))
    .sort((a, b) => {
      const first = region(a),
        second = region(b);
      return first !== second && first && second
        ? first.contains(second)
          ? 1
          : second.contains(first)
            ? -1
            : 0
        : 0;
    })[0];
}
function onScreen(entry: Registration) {
  const element = region(entry),
    view = element?.ownerDocument.defaultView;
  if (!element || !view) return false;
  const rect = element.getBoundingClientRect();
  return (
    (!rect.width && !rect.height) ||
    (rect.bottom > 0 &&
      rect.top < view.innerHeight &&
      rect.right > 0 &&
      rect.left < view.innerWidth)
  );
}

function registryFor(document: Document) {
  const prior = registries.get(document);
  if (prior) return prior;
  const registry: Registry = { entries: new Set(), dispose: () => undefined };
  let searchKeyHeld = "";
  const keyup = (event: KeyboardEvent) => {
    if (event.key === "/" || event.code === searchKeyHeld) searchKeyHeld = "";
  };
  const interacted = (event: Event) => {
    if (!(event.target instanceof Element)) return;
    const pagers = [...registry.entries].filter(
      (entry) => entry.kind === "page" && entry.root() && visible(entry.root()!),
    );
    const owner = deepest(pagers, event.target);
    if (owner) registry.lastPager = owner;
  };
  const keydown = (event: KeyboardEvent) => {
    if (event.defaultPrevented) return;
    const shortcut = shortcutForKey(event);
    if (!shortcut) return;
    if (shortcut.kind === "search") {
      if (event.repeat && searchKeyHeld) {
        event.preventDefault();
        event.stopPropagation();
        return;
      }
      if (!event.repeat) searchKeyHeld = "";
    }
    const target = event.target instanceof Element ? event.target : document.activeElement;
    if (
      target?.closest(
        "input:not([type='checkbox']):not([type='radio']), textarea, select, [role='combobox'], [role='slider'], [role='spinbutton'], [contenteditable]:not([contenteditable='false'])",
      )
    )
      return;
    if (
      shortcut.kind === "page" &&
      target?.closest("input[type='radio'], [role='radiogroup'], [role='tablist'], [role='tree']")
    )
      return;
    if ([...document.querySelectorAll("[role='menu'], [role='listbox']")].some(visible)) return;
    const modal = [
      ...document.querySelectorAll<HTMLElement>("[role='dialog'], [role='alertdialog']"),
    ]
      .filter(visible)
      .at(-1);
    const area = modal ?? document.querySelector(".shell") ?? document.body;
    if (shortcut.kind === "search") {
      const searches = [
        ...area.querySelectorAll<HTMLElement>('input[type="search"], [role="searchbox"]'),
      ].filter(
        (element) =>
          visible(element) &&
          !element.matches(":disabled, [readonly]") &&
          !element.closest('[aria-disabled="true"]') &&
          element.getAttribute("contenteditable") !== "false",
      );
      const focusedRegion = target?.closest("[data-paging-region]");
      const search = searches.find((element) => focusedRegion?.contains(element)) ?? searches[0];
      if (!search) return;
      event.preventDefault();
      event.stopPropagation();
      searchKeyHeld = event.code || "/";
      search.focus();
      return;
    }
    if (
      !(modal && shortcut.kind === "page") &&
      [
        ...area.querySelectorAll(
          "input, textarea, [role='textbox'], [role='searchbox'], [contenteditable]:not([contenteditable='false'])",
        ),
      ].some(
        (element) =>
          textEntry(element) &&
          visible(element) &&
          !element.hasAttribute("data-unfocused-shortcuts"),
      )
    )
      return;
    if (modal && shortcut.kind !== "page") return;
    const candidates = [...registry.entries].filter((entry) => {
      const root = entry.root();
      return (
        entry.kind === shortcut.kind && root && visible(root) && (!modal || modal.contains(root))
      );
    });
    let chosen: Registration | undefined;
    if (shortcut.kind === "page") {
      chosen =
        deepest(candidates, target) ??
        (registry.lastPager &&
        candidates.includes(registry.lastPager) &&
        onScreen(registry.lastPager)
          ? registry.lastPager
          : undefined) ??
        candidates.find((entry) => entry.defaultPager) ??
        candidates[0];
    } else chosen = candidates[0];
    if (!chosen) return;
    if (event.repeat && shortcut.kind !== "page") {
      event.preventDefault();
      event.stopPropagation();
      return;
    }
    if (chosen.handle(shortcut)) {
      if (shortcut.kind === "page") registry.lastPager = chosen;
      event.preventDefault();
      event.stopPropagation();
    }
  };
  document.addEventListener("keydown", keydown, true);
  document.addEventListener("keyup", keyup, true);
  document.addEventListener("focusin", interacted, true);
  document.addEventListener("pointerdown", interacted, true);
  registry.dispose = () => {
    document.removeEventListener("keydown", keydown, true);
    document.removeEventListener("keyup", keyup, true);
    document.removeEventListener("focusin", interacted, true);
    document.removeEventListener("pointerdown", interacted, true);
    registries.delete(document);
  };
  registries.set(document, registry);
  return registry;
}

export function useKeyboardShortcut(
  kind: Shortcut["kind"],
  root: RefObject<HTMLElement | null>,
  handle: (shortcut: Shortcut) => boolean,
  defaultPager = false,
) {
  const current = useRef(handle);
  useLayoutEffect(() => {
    current.current = handle;
  });
  useLayoutEffect(() => {
    const registry = registryFor(root.current?.ownerDocument ?? document);
    const entry: Registration = {
      kind,
      root: () => root.current,
      handle: (shortcut) => current.current(shortcut),
      defaultPager,
    };
    registry.entries.add(entry);
    return () => {
      registry.entries.delete(entry);
      if (registry.lastPager === entry) registry.lastPager = undefined;
      if (!registry.entries.size) registry.dispose();
    };
  }, [kind, root, defaultPager]);
}
