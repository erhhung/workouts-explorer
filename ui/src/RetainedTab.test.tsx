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
  const owner = view.container.querySelector<HTMLElement>("[data-app-scroll]")!;
  owner.scrollTop = 700;
  fireEvent.scroll(owner);
  view.rerender(
    <RetainedTab id="/" active={false}>
      <p>Summary</p>
    </RetainedTab>,
  );
  owner.scrollTop = 0;
  fireEvent.scroll(window);
  scroll.mockClear();
  view.rerender(
    <RetainedTab id="/" active>
      <p>Summary</p>
    </RetainedTab>,
  );
  expect(owner.scrollTop).toBe(700);
  expect(scroll).not.toHaveBeenCalled();
});

test("restores independent Stats content and sidebar scroll owners", () => {
  function Content() {
    return (
      <>
        <aside data-app-scroll>Controls</aside>
        <main data-app-scroll>Report</main>
      </>
    );
  }
  const view = render(
    <RetainedTab id="/stats" active>
      <Content />
    </RetainedTab>,
  );
  const [sidebar, report] = [...view.container.querySelectorAll<HTMLElement>("[data-app-scroll]")];
  sidebar.scrollTop = 80;
  report.scrollTop = 900;
  fireEvent.scroll(sidebar);
  fireEvent.scroll(report);
  view.rerender(
    <RetainedTab id="/stats" active={false}>
      <Content />
    </RetainedTab>,
  );
  sidebar.scrollTop = 0;
  report.scrollTop = 0;
  view.rerender(
    <RetainedTab id="/stats" active>
      <Content />
    </RetainedTab>,
  );
  expect(sidebar.scrollTop).toBe(80);
  expect(report.scrollTop).toBe(900);
});
