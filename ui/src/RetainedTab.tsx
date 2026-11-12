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
    const scroll = useRef({ x: 0, y: 0 });
    if (active && !visited) setVisited(true);
    useLayoutEffect(() => {
      if (!active) return;
      window.scrollTo(scroll.current.x, scroll.current.y);
      const remember = () => {
        scroll.current = { x: window.scrollX, y: window.scrollY };
      };
      window.addEventListener("scroll", remember, { passive: true });
      return () => {
        window.removeEventListener("scroll", remember);
        dismissActivePortalTooltip();
      };
    }, [active]);
    if (!visited && !active) return null;
    return (
      <div className="tab-workspace" data-app-tab={id} hidden={!active} inert={!active}>
        <TabActivity.Provider value={active}>{children}</TabActivity.Provider>
      </div>
    );
  },
  // Admit new props on reveal, rather than reconciling offscreen tables on every shell update.
  (previous, next) => previous.id === next.id && !previous.active && !next.active,
);
