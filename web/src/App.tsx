import { lazy, Suspense } from "react";
import { createBrowserRouter, RouterProvider } from "react-router";
import { QueryClientProvider } from "@tanstack/react-query";
import { AppShell } from "./components/layout/AppShell";
import { queryClient } from "./lib/queryClient";
import { useThemeSync } from "./lib/theme";
import { useSettings } from "./lib/settings";
import { useEvents } from "./lib/store";
import { RouteError } from "./pages/RouteError";
import { PAGES } from "./app/registry";

const page = (load: () => Promise<{ default: React.ComponentType }>) => async () => ({ Component: (await load()).default });

const router = createBrowserRouter([
  {
    path: "/",
    element: <AppShell />,
    errorElement: <RouteError />,
    hydrateFallbackElement: <div className="h-dvh bg-bg" />,
    children: Object.values(PAGES).map((p) => (p.path === "" ? { index: true, lazy: page(p.load) } : { path: p.path, lazy: page(p.load) })),
  },
]);

const Connect = lazy(() => import("./pages/Connect"));

/** First run (no token) or a rejected token: show the Connect screen instead of a wall of errors. */
function Gate({ children }: { children: React.ReactNode }) {
  const mock = useSettings((s) => s.mock);
  const token = useSettings((s) => s.token);
  const unauthorized = useEvents((s) => s.connection.status === "unauthorized");
  if (!mock && (!token || unauthorized)) {
    return (
      <Suspense fallback={<div className="h-dvh bg-bg" />}>
        <Connect />
      </Suspense>
    );
  }
  return <>{children}</>;
}

export function App() {
  useThemeSync();
  return (
    <QueryClientProvider client={queryClient}>
      <Gate>
        <RouterProvider router={router} />
      </Gate>
    </QueryClientProvider>
  );
}
