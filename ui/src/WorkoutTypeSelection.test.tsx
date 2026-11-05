import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import * as Dialog from "@radix-ui/react-dialog";
import { useState } from "react";
import { describe, expect, test, vi } from "vitest";
import { WorkoutTypeSelection } from "./WorkoutTypeSelection";
import { installFocusVisibility } from "./focusVisibility";

const workouts = [
  { id: "run-1", type: { id: "run", key: "outdoor-run", name: "Outdoor run" } },
  { id: "run-2", type: { id: "run", key: "outdoor-run", name: "Outdoor run" } },
  { id: "walk-1", type: { id: "walk", key: "outdoor-walk", name: "Outdoor walk" } },
];

function setup(initial?: string[]) {
  const onApply = vi.fn();
  const onToggle = vi.fn();
  function Harness() {
    const [ids, setIds] = useState(initial);
    const allSelected = ids === undefined || ids.length === workouts.length;
    return (
      <WorkoutTypeSelection
        workouts={workouts}
        selectedIds={ids}
        allSelected={allSelected}
        onToggleAll={() => {
          onToggle();
          setIds(allSelected ? [] : undefined);
        }}
        onApply={(next) => {
          onApply(next);
          setIds(next);
        }}
      />
    );
  }
  const view = render(<Harness />);
  const user = userEvent.setup();
  const checkbox = screen.getByRole("checkbox", { name: "Select all workout routes" });
  return { ...view, user, checkbox, onApply, onToggle };
}

function setupTooltipInteraction() {
  const view = setup();
  const disposeFocus = installFocusVisibility();
  const originalHitTest = Object.getOwnPropertyDescriptor(document, "elementFromPoint");
  let hit: Element | null = view.checkbox;
  Object.defineProperty(document, "elementFromPoint", { configurable: true, value: () => hit });
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
    this: HTMLElement,
  ) {
    return this.classList.contains("tooltip-content")
      ? new DOMRect(0, 0, 320, 80)
      : new DOMRect(40, 200, 16, 16);
  });
  return {
    ...view,
    pointAt(element: Element | null) {
      hit = element;
    },
    dispose() {
      disposeFocus();
      if (originalHitTest) Object.defineProperty(document, "elementFromPoint", originalHitTest);
      else Reflect.deleteProperty(document, "elementFromPoint");
    },
  };
}

describe("workout type selection", () => {
  test.each([false, true])(
    "a pointer press cancels delayed help until a fresh hover (held=%s)",
    (held) => {
      const { checkbox, onToggle, dispose } = setupTooltipInteraction();
      try {
        vi.useFakeTimers();
        fireEvent.pointerEnter(checkbox);
        expect(screen.getByRole("tooltip")).toHaveClass("is-open");
        act(() => vi.advanceTimersByTime(600));
        fireEvent(
          checkbox,
          new MouseEvent("pointerdown", { bubbles: true, button: 0, buttons: 1 }),
        );
        if (!held) {
          fireEvent(checkbox, new MouseEvent("pointerup", { bubbles: true, button: 0 }));
          fireEvent.click(checkbox);
          expect(onToggle).toHaveBeenCalledTimes(1);
        }
        // Cross the 750ms hover delay before the 500ms hold can open the picker.
        act(() => vi.advanceTimersByTime(175));
        expect(screen.getByRole("tooltip")).not.toHaveClass("is-open");
        expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
        fireEvent.pointerMove(checkbox);
        fireEvent.resize(window);
        expect(screen.getByRole("tooltip")).not.toHaveClass("is-open");
        if (held) {
          act(() => vi.advanceTimersByTime(325));
          expect(screen.getByRole("dialog", { name: "Show workout types" })).toBeInTheDocument();
          expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
          expect(onToggle).not.toHaveBeenCalled();
        } else {
          act(() => vi.advanceTimersByTime(1000));
          expect(screen.getByRole("tooltip")).not.toHaveClass("is-open");
          fireEvent.pointerLeave(checkbox);
          fireEvent.pointerEnter(checkbox);
          expect(screen.getByRole("tooltip")).toHaveClass("is-open");
        }
      } finally {
        dispose();
      }
    },
  );

  test("re-entering with the mouse held does not restart delayed help", () => {
    const { checkbox, dispose } = setupTooltipInteraction();
    try {
      vi.useFakeTimers();
      fireEvent.pointerEnter(checkbox);
      fireEvent(checkbox, new MouseEvent("pointerdown", { bubbles: true, button: 0, buttons: 1 }));
      fireEvent.pointerLeave(checkbox);
      fireEvent(checkbox, new MouseEvent("pointerover", { bubbles: true, buttons: 1 }));
      act(() => vi.advanceTimersByTime(1000));
      expect(screen.getByRole("tooltip")).not.toHaveClass("is-open");
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      fireEvent(checkbox, new MouseEvent("pointerup", { bubbles: true, button: 0 }));
      act(() => vi.advanceTimersByTime(1000));
      expect(screen.getByRole("tooltip")).not.toHaveClass("is-open");
      fireEvent.pointerLeave(checkbox);
      fireEvent.pointerEnter(checkbox);
      expect(screen.getByRole("tooltip")).toHaveClass("is-open");
    } finally {
      dispose();
    }
  });

  test.each(["Apply", "Cancel"])(
    "mouse %s restores checkbox focus without reopening its tooltip",
    async (action) => {
      const { user, checkbox, pointAt, dispose } = setupTooltipInteraction();
      try {
        await user.hover(checkbox);
        expect(screen.getByRole("tooltip")).toHaveClass("is-open");
        await user.pointer({ target: checkbox, keys: "[MouseLeft>]" });
        await screen.findByRole("dialog", { name: "Show workout types" });
        await user.pointer({ target: checkbox, keys: "[/MouseLeft]" });
        expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
        await user.click(screen.getByRole("checkbox", { name: "Outdoor walk" }));
        const button = screen.getByRole("button", { name: action });
        pointAt(button);
        await user.click(button);
        await waitFor(() => expect(checkbox).toHaveFocus());
        await act(async () => {
          await new Promise((resolve) => setTimeout(resolve, 0));
        });
        expect(screen.getByRole("tooltip")).not.toHaveClass("is-open");
        pointAt(document.body);
        await user.hover(document.body);
        expect(screen.getByRole("tooltip")).not.toHaveClass("is-open");
        pointAt(checkbox);
        await user.hover(checkbox);
        expect(screen.getByRole("tooltip")).toHaveClass("is-open");
        await user.unhover(checkbox);
        expect(screen.getByRole("tooltip")).not.toHaveClass("is-open");
      } finally {
        dispose();
      }
    },
  );

  test("keyboard Apply restores checkbox focus and its keyboard help", async () => {
    const { user, checkbox, dispose } = setupTooltipInteraction();
    try {
      await user.tab();
      expect(checkbox).toHaveFocus();
      expect(screen.getByRole("tooltip")).toHaveClass("is-open");
      await user.keyboard("{ArrowDown}");
      await screen.findByRole("dialog", { name: "Show workout types" });
      await user.tab({ shift: true });
      expect(screen.getByRole("button", { name: "Apply" })).toHaveFocus();
      await user.keyboard("{Enter}");
      await waitFor(() => expect(checkbox).toHaveFocus());
      await waitFor(() => expect(screen.getByRole("tooltip")).toHaveClass("is-open"));
    } finally {
      dispose();
    }
  });

  test("keeps its panel inside the enclosing mobile dialog's accessible subtree", async () => {
    render(
      <Dialog.Root open>
        <Dialog.Portal>
          <Dialog.Content>
            <Dialog.Title>Map controls</Dialog.Title>
            <Dialog.Description>Filter the visible routes.</Dialog.Description>
            <WorkoutTypeSelection
              workouts={workouts}
              allSelected
              onToggleAll={vi.fn()}
              onApply={vi.fn()}
            />
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>,
    );
    const parent = screen.getByRole("dialog", { name: "Map controls" });
    screen.getByRole("checkbox", { name: "Select all workout routes" }).focus();
    await userEvent.keyboard("{ArrowDown}");
    expect(parent).toContainElement(screen.getByRole("dialog", { name: "Show workout types" }));
  });

  test.each([{ initial: undefined }, { initial: [] as string[] }])(
    "opens at exactly 500ms without toggling ($initial)",
    ({ initial }) => {
      const { checkbox, onToggle } = setup(initial);
      vi.useFakeTimers();
      fireEvent(checkbox, new MouseEvent("pointerdown", { bubbles: true, button: 0 }));
      act(() => vi.advanceTimersByTime(499));
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      act(() => vi.advanceTimersByTime(1));
      expect(screen.getByRole("dialog", { name: "Show workout types" })).toBeInTheDocument();
      expect(
        screen
          .getAllByRole("checkbox")
          .filter((item) => item !== checkbox)
          .map((item) => item.closest("label")?.textContent?.replace("\u2713", "")),
      ).toEqual(["Outdoor run", "Outdoor walk"]);
      for (const item of screen.getAllByRole("checkbox").filter((item) => item !== checkbox)) {
        expect(item).toHaveProperty("checked", initial === undefined);
      }
      fireEvent(checkbox, new MouseEvent("pointerup", { bubbles: true, button: 0 }));
      fireEvent.click(checkbox);
      expect(onToggle).not.toHaveBeenCalled();
      expect(checkbox).toHaveProperty("checked", initial === undefined);
    },
  );

  test("a short click still toggles all, and a subsequent hold does not", async () => {
    const { user, checkbox, onToggle } = setup();
    await user.click(checkbox);
    expect(checkbox).not.toBeChecked();
    expect(onToggle).toHaveBeenCalledTimes(1);
    vi.useFakeTimers();
    act(() => vi.advanceTimersByTime(2000));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    fireEvent(checkbox, new MouseEvent("pointerdown", { bubbles: true, button: 0 }));
    act(() => vi.advanceTimersByTime(500));
    fireEvent(checkbox, new MouseEvent("pointerup", { bubbles: true, button: 0 }));
    fireEvent.click(checkbox);
    expect(onToggle).toHaveBeenCalledTimes(1);
  });

  test("keeps a pointer hold active across programmatic focus restoration", () => {
    const { checkbox, onToggle } = setup();
    vi.useFakeTimers();
    fireEvent(checkbox, new MouseEvent("pointerdown", { bubbles: true, button: 0 }));
    act(() => vi.advanceTimersByTime(250));
    fireEvent.blur(checkbox);
    act(() => vi.advanceTimersByTime(250));
    expect(screen.getByRole("dialog", { name: "Show workout types" })).toBeInTheDocument();
    expect(onToggle).not.toHaveBeenCalled();
  });

  test.each(["Tab", "Escape"])("cancels an unfinished hold on %s", (key) => {
    const { checkbox } = setup();
    vi.useFakeTimers();
    fireEvent(checkbox, new MouseEvent("pointerdown", { bubbles: true, button: 0 }));
    act(() => vi.advanceTimersByTime(250));
    fireEvent.keyDown(checkbox, { key });
    act(() => vi.advanceTimersByTime(250));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  test("partial types start unchecked; drafts apply atomically to every route of each type", async () => {
    const { user, checkbox, onApply, onToggle } = setup(["run-1", "walk-1"]);
    checkbox.focus();
    await user.keyboard("{ArrowDown}");
    const run = screen.getByRole("checkbox", { name: "Outdoor run" });
    const walk = screen.getByRole("checkbox", { name: "Outdoor walk" });
    expect(run).not.toBeChecked();
    expect(walk).toBeChecked();
    await user.click(run);
    await user.click(walk);
    expect(onApply).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Apply" }));
    expect(onApply).toHaveBeenCalledExactlyOnceWith(["run-1", "run-2"]);
    expect(onToggle).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  test("Cancel, outside click and Escape discard drafts; keyboard Space still toggles all", async () => {
    const { user, checkbox, onApply, onToggle } = setup();
    for (const dismiss of ["cancel", "outside", "escape"]) {
      checkbox.focus();
      await user.keyboard("{ArrowDown}");
      const run = screen.getByRole("checkbox", { name: "Outdoor run" });
      expect(run).toBeChecked();
      await user.click(run);
      if (dismiss === "cancel") await user.click(screen.getByRole("button", { name: "Cancel" }));
      else if (dismiss === "outside") await user.click(document.body);
      else await user.keyboard("{Escape}");
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(onApply).not.toHaveBeenCalled();
      expect(checkbox).toBeChecked();
    }
    checkbox.focus();
    await user.keyboard(" ");
    expect(onToggle).toHaveBeenCalledTimes(1);
    expect(checkbox).not.toBeChecked();
  });

  test.each(["leave", "cancel", "blur", "unmount"])("cancels a pending hold on %s", (reason) => {
    const { checkbox, unmount } = setup();
    vi.useFakeTimers();
    fireEvent(checkbox, new MouseEvent("pointerdown", { bubbles: true, button: 0 }));
    act(() => vi.advanceTimersByTime(250));
    if (reason === "leave") fireEvent.pointerLeave(checkbox);
    else if (reason === "cancel") fireEvent.pointerCancel(window);
    else if (reason === "blur") fireEvent.blur(window);
    else unmount();
    act(() => vi.advanceTimersByTime(1000));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  test("describes the checkbox with the exact three-line styled help and hides it while filtering", async () => {
    const { user, checkbox } = setup();
    const help = screen.getByRole("tooltip");
    expect(checkbox).not.toHaveAttribute("title");
    expect(checkbox).toHaveAttribute("aria-describedby", help.id);
    expect(help).toHaveClass("tooltip-content--portal", "tooltip-content--multiline");
    expect(help.textContent).toBe(
      "Toggle checkboxes to show or hide all routes;\nclick and hold to filter routes by workout types\n(use space and down keys in keyboard mode)",
    );
    expect(checkbox.closest(".tooltip-trigger")).not.toHaveAttribute("tabindex");
    checkbox.focus();
    await user.keyboard("{ArrowDown}");
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
    expect(checkbox).not.toHaveAttribute("aria-describedby");
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.getByRole("tooltip").id).toBe(help.id);
    expect(checkbox).toHaveAttribute("aria-describedby", help.id);
  });
});
