import { BrowserRouter, Routes, Route, Navigate } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Toaster } from "sonner";
import { AuthProvider } from "@/contexts/AuthContext";
import { TooltipProvider } from "@/components/ui/tooltip";
import { AppShell } from "@/components/layout/AppShell";

// Auth pages
import { LoginPage } from "@/pages/auth/LoginPage";
import { OAuthCallback } from "@/pages/auth/OAuthCallback";

// Project pages
import { ProjectListPage } from "@/pages/projects/ProjectListPage";
import { CreateProjectPage } from "@/pages/projects/CreateProjectPage";

// Dashboard
import { OverviewPage } from "@/pages/dashboard/OverviewPage";

// Keys
import { KeysPage } from "@/pages/keys/KeysPage";

// OAuth Grants
import { OAuthGrantsPage } from "@/pages/oauth-grants/OAuthGrantsPage";

// Members
import { MembersPage } from "@/pages/members/MembersPage";

// Requests & Usage
import { RequestsPage } from "@/pages/requests/RequestsPage";
import { UsagePage } from "@/pages/usage/UsagePage";

// Traces
import { TracesPage } from "@/pages/traces/TracesPage";

// Settings
import { UserSettingsPage } from "@/pages/settings/UserSettingsPage";
import { ProjectSettingsPage } from "@/pages/settings/ProjectSettingsPage";

// Admin
import { UsersPage } from "@/pages/admin/UsersPage";
import { AdminProjectsPage } from "@/pages/admin/ProjectsPage";

// Subscription
import { SubscriptionPage } from "@/pages/subscriptions/SubscriptionPage";

// Extra usage
import { ExtraUsagePage } from "@/pages/extra-usage/ExtraUsagePage";

// Admin Plans
import { PlansPage } from "@/pages/admin/PlansPage";

// Admin Models
import { ModelsPage } from "@/pages/admin/ModelsPage";

// Admin Routes
import { RoutesPage } from "@/pages/admin/RoutesPage";

// Admin Requests
import { AdminRequestsPage } from "@/pages/admin/RequestsPage";

// Admin Upstreams & Routing
import { UpstreamsPage } from "@/pages/admin/UpstreamsPage";
import { UpstreamGroupsPage } from "@/pages/admin/UpstreamGroupsPage";
import { RoutingHealthPage } from "@/pages/admin/RoutingHealthPage";

// Admin OAuth Clients
import { OAuthClientsPage } from "@/pages/admin/OAuthClientsPage";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      retry: 1,
    },
  },
});

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <AuthProvider>
        <TooltipProvider>
          <Toaster position="top-center" richColors closeButton />
          <BrowserRouter>
            <Routes>
              {/* Public routes */}
              <Route path="/login" element={<LoginPage />} />
              <Route path="/auth/callback/:provider" element={<OAuthCallback />} />

              {/* Authenticated routes */}
              <Route element={<AppShell />}>
                <Route index element={<Navigate to="/projects" replace />} />
                <Route path="projects" element={<ProjectListPage />} />
                <Route path="projects/new" element={<CreateProjectPage />} />
                <Route path="projects/:projectId" element={<OverviewPage />} />
                <Route path="projects/:projectId/keys" element={<KeysPage />} />
                <Route path="projects/:projectId/oauth-grants" element={<OAuthGrantsPage />} />
                <Route path="projects/:projectId/members" element={<MembersPage />} />
                <Route path="projects/:projectId/requests" element={<RequestsPage />} />
                <Route path="projects/:projectId/traces" element={<TracesPage />} />
                <Route path="projects/:projectId/usage" element={<UsagePage />} />
                <Route path="projects/:projectId/subscription" element={<SubscriptionPage />} />
                <Route path="projects/:projectId/extra-usage" element={<ExtraUsagePage />} />
                <Route path="projects/:projectId/settings" element={<ProjectSettingsPage />} />
                <Route path="settings" element={<UserSettingsPage />} />
                <Route path="admin/users" element={<UsersPage />} />
                <Route path="admin/projects" element={<AdminProjectsPage />} />
                <Route path="admin/plans" element={<PlansPage />} />
                <Route path="admin/models" element={<ModelsPage />} />
                <Route path="admin/requests" element={<AdminRequestsPage />} />
                <Route path="admin/routes" element={<RoutesPage />} />
                <Route path="admin/upstreams" element={<UpstreamsPage />} />
                <Route path="admin/upstream-groups" element={<UpstreamGroupsPage />} />
                <Route path="admin/routing-health" element={<RoutingHealthPage />} />
                <Route path="admin/oauth-clients" element={<OAuthClientsPage />} />
              </Route>
            </Routes>
          </BrowserRouter>
        </TooltipProvider>
      </AuthProvider>
    </QueryClientProvider>
  );
}
