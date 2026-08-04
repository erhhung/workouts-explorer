import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, vi } from "vitest";

Object.defineProperty(Element.prototype, "hasPointerCapture", { configurable: true, value: () => false });
Object.defineProperty(Element.prototype, "setPointerCapture", { configurable: true, value: () => undefined });
Object.defineProperty(Element.prototype, "releasePointerCapture", { configurable: true, value: () => undefined });
Object.defineProperty(Element.prototype, "scrollIntoView", { configurable: true, value: () => undefined });

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  history.replaceState({}, "", "/");
  localStorage.clear();
  document.documentElement.dataset.theme = "dark";
  document.documentElement.style.colorScheme = "dark";
});
