import { createBrowserRouter, RouterProvider } from "react-router";
import { QueryClientProvider } from "@tanstack/react-query";
import { AppShell } from "./components/layout/AppShell";
import { queryClient } from "./lib/queries";
import { useThemeSync } from "./lib/theme";
import { RouteError } from "./pages/RouteError";

const page = (load: () => Promise<{ default: React.ComponentType }>) => async () => ({ Component: (await load()).default });

const router = createBrowserRouter([
  {
    path: "/",
    element: <AppShell />,
    errorElement: <RouteError />,
    children: [
      { index: true, lazy: page(() => import("./pages/Overview")) },
      { path: "inbox", lazy: page(() => import("./pages/Inbox")) },
      { path: "chat", lazy: page(() => import("./pages/Chat")) },
      { path: "tasks", lazy: page(() => import("./pages/Tasks")) },
      { path: "tasks/:id", lazy: page(() => import("./pages/TaskDetail")) },
      { path: "agents", lazy: page(() => import("./pages/Agents")) },
      { path: "agents/:id", lazy: page(() => import("./pages/AgentDetail")) },
      { path: "vault", lazy: page(() => import("./pages/Vault")) },
      { path: "machines", lazy: page(() => import("./pages/Machines")) },
      { path: "machines/:id", lazy: page(() => import("./pages/MachineDetail")) },
      { path: "costs", lazy: page(() => import("./pages/Costs")) },
      { path: "settings", lazy: page(() => import("./pages/Settings")) },
      { path: "security", lazy: page(() => import("./pages/Security")) },
      { path: "*", lazy: page(() => import("./pages/NotFound")) },
    ],
  },
]);

export function App() {
  useThemeSync();
  return (
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  );
}
