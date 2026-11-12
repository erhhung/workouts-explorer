import { fireEvent, render, screen } from "@testing-library/react";
import { useEffect } from "react";
import { expect, test, vi } from "vitest";
import { RetainedTab, useTabActive } from "./RetainedTab";

test("retains visited DOM and effects but excludes inactive controls from navigation", () => {
  const mounted = vi.fn(),
    disposed = vi.fn();
  function Content({ label }: { label: string }) {
    const active = useTabActive();
    useEffect(() => {
      mounted();
      return disposed;
    }, []);
    return (
      <button>
        {label} {active ? "active" : "inactive"}
      </button>
    );
  }
  const view = render(
    <RetainedTab id="/map" active={false}>
      <Content label="first" />
    </RetainedTab>,
  );
  expect(mounted).not.toHaveBeenCalled();
  view.rerender(
    <RetainedTab id="/map" active>
      <Content label="first" />
    </RetainedTab>,
  );
  const button = screen.getByRole("button");
  view.rerender(
    <RetainedTab id="/map" active={false}>
      <Content label="first" />
    </RetainedTab>,
  );
  expect(screen.queryByRole("button")).not.toBeInTheDocument();
  expect(button.closest("[data-app-tab]")).toHaveAttribute("hidden");
  expect(button.closest("[data-app-tab]")).toHaveAttribute("inert");
  expect(button).toHaveTextContent("inactive");
  view.rerender(
    <RetainedTab id="/map" active={false}>
      <Content label="latest" />
    </RetainedTab>,
  );
  expect(button).toHaveTextContent("first inactive");
  view.rerender(
    <RetainedTab id="/map" active>
      <Content label="latest" />
    </RetainedTab>,
  );
  expect(screen.getByRole("button")).toBe(button);
  expect(button).toHaveTextContent("latest active");
  expect(mounted).toHaveBeenCalledTimes(1);
  expect(disposed).not.toHaveBeenCalled();
  view.unmount();
  expect(disposed).toHaveBeenCalledTimes(1);
});

test("restores tab scroll without capturing another tab's position", () => {
  const scroll = vi.spyOn(window, "scrollTo");
  const view = render(
    <RetainedTab id="/" active>
      <p>Summary</p>
    </RetainedTab>,
  );
  vi.spyOn(window, "scrollY", "get").mockReturnValue(700);
  fireEvent.scroll(window);
  view.rerender(
    <RetainedTab id="/" active={false}>
      <p>Summary</p>
    </RetainedTab>,
  );
  vi.spyOn(window, "scrollY", "get").mockReturnValue(120);
  fireEvent.scroll(window);
  scroll.mockClear();
  view.rerender(
    <RetainedTab id="/" active>
      <p>Summary</p>
    </RetainedTab>,
  );
  expect(scroll).toHaveBeenLastCalledWith(0, 700);
});
