import * as Primitive from "@radix-ui/react-dialog";
import { useCallback, type ComponentPropsWithRef } from "react";
import { installDialogPixelAlignment } from "./dialogPixelAlignment";

export * from "@radix-ui/react-dialog";

export function Content({ ref, ...props }: ComponentPropsWithRef<typeof Primitive.Content>) {
  const attach = useCallback(
    (node: HTMLDivElement | null) => {
      if (!node) return;
      const refCleanup = typeof ref === "function" ? ref(node) : undefined;
      if (ref && typeof ref !== "function") ref.current = node;
      const dispose = installDialogPixelAlignment(node);
      return () => {
        dispose();
        if (typeof refCleanup === "function") refCleanup();
        else if (typeof ref === "function") ref(null);
        else if (ref) ref.current = null;
      };
    },
    [ref],
  );
  return <Primitive.Content {...props} ref={attach} data-dialog-pixel-aligned="" />;
}
