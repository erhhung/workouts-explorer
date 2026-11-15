import {
  createContext,
  memo,
  useContext,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { dismissActivePortalTooltip } from "./Tooltip";
import {
  captureContentScroll,
  restoreContentScroll,
  type ContentScrollPosition,
} from "./contentScroll";

const TabActivity = createContext(true);

export function useTabActive() {
  return useContext(TabActivity);
}

export const RetainedTab = memo(
  function RetainedTab({
    id,
    active,
    children,
  }: {
    id: string;
    active: boolean;
    children: ReactNode;
  }) {
    const [visited, setVisited] = useState(active);
    const root = useRef<HTMLDivElement>(null);
    const scroll = useRef(new Map<HTMLElement, ContentScrollPosition>());
    if (active && !visited) setVisited(true);
    useLayoutEffect(() => {
      const element = root.current;
      if (!active || !element) return;
      for (const position of scroll.current.values()) restoreContentScroll(position);
      const remember = (event: Event) => {
        const owner = event.target;
        if (
          element.hidden ||
          !(owner instanceof HTMLElement) ||
          !owner.matches("[data-app-scroll]")
        )
          return;
        scroll.current.set(owner, captureContentScroll(owner));
      };
      element.addEventListener("scroll", remember, { passive: true, capture: true });
      return () => {
        element.removeEventListener("scroll", remember, true);
        dismissActivePortalTooltip();
      };
    }, [active]);
    if (!visited && !active) return null;
    return (
      <div
        ref={root}
        className="tab-workspace"
        data-app-tab={id}
        data-app-scroll={id !== "/map" && id !== "/stats" ? "" : undefined}
        hidden={!active}
        inert={!active}
      >
        <TabActivity.Provider value={active}>{children}</TabActivity.Provider>
      </div>
    );
  },
  // Admit new props on reveal, rather than reconciling offscreen tables on every shell update.
  (previous, next) => previous.id === next.id && !previous.active && !next.active,
);
