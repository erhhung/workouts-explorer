import { fireEvent } from "@testing-library/react";
import { expect, test } from "vitest";
import { installFocusVisibility } from "./focusVisibility";

test("shows keyboard focus after Tab, suppresses it after pointer use, and ignores text editing", () => {
  const dispose = installFocusVisibility();
  const root = document.documentElement;
  const input = document.createElement("input");
  document.body.append(input);
  try {
    expect(root.dataset.inputModality).toBe("pointer");
    fireEvent.keyDown(document, { key: "Tab" });
    expect(root.dataset.inputModality).toBe("keyboard");
    fireEvent.pointerDown(input);
    expect(root.dataset.inputModality).toBe("pointer");
    for (const key of ["a", "ArrowLeft", "Home"]) fireEvent.keyDown(input, { key });
    expect(root.dataset.inputModality).toBe("pointer");
    fireEvent.keyDown(input, { key: "Tab", shiftKey: true });
    expect(root.dataset.inputModality).toBe("keyboard");
  } finally {
    dispose();
    input.remove();
  }
  expect(root.dataset.inputModality).toBeUndefined();
  fireEvent.keyDown(document, { key: "Tab" });
  expect(root.dataset.inputModality).toBeUndefined();
});

test("arrow-key navigation of buttons enables keyboard focus without treating shortcuts as navigation", () => {
  const dispose = installFocusVisibility();
  const button = document.createElement("button");
  document.body.append(button);
  try {
    fireEvent.keyDown(button, { key: "ArrowDown", metaKey: true });
    expect(document.documentElement.dataset.inputModality).toBe("pointer");
    fireEvent.keyDown(button, { key: "ArrowDown" });
    expect(document.documentElement.dataset.inputModality).toBe("keyboard");
  } finally {
    dispose();
    button.remove();
  }
});
