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

test("only Tab initiates keyboard navigation; arrows and shortcuts preserve the current mode", () => {
  const dispose = installFocusVisibility();
  const button = document.createElement("button");
  document.body.append(button);
  try {
    for (const key of [
      "1",
      "2",
      "3",
      "4",
      "c",
      "r",
      "f",
      "m",
      "y",
      "ArrowDown",
      "ArrowUp",
      "ArrowLeft",
      "ArrowRight",
      "Home",
      "End",
      "Enter",
      " ",
    ]) {
      fireEvent.keyDown(button, { key });
      expect(document.documentElement.dataset.inputModality).toBe("pointer");
    }
    fireEvent.keyDown(button, { key: "ArrowRight", shiftKey: true });
    expect(document.documentElement.dataset.inputModality).toBe("pointer");
    fireEvent.keyDown(button, { key: "Tab", ctrlKey: true });
    expect(document.documentElement.dataset.inputModality).toBe("pointer");
    fireEvent.keyDown(button, { key: "Tab", shiftKey: true });
    expect(document.documentElement.dataset.inputModality).toBe("keyboard");
    fireEvent.keyDown(button, { key: "ArrowRight" });
    expect(document.documentElement.dataset.inputModality).toBe("keyboard");
    fireEvent.pointerDown(button);
    fireEvent.keyDown(button, { key: "4" });
    expect(document.documentElement.dataset.inputModality).toBe("pointer");
  } finally {
    dispose();
    button.remove();
  }
});
