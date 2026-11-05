import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App } from "./App";
import { installFocusVisibility } from "./focusVisibility";
import "./styles.css";

const queryClient = new QueryClient();
const disposeFocusVisibility = installFocusVisibility();
import.meta.hot?.dispose(disposeFocusVisibility);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </StrictMode>,
);
