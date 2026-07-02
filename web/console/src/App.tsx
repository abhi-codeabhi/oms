import { Navigate, Route, Routes } from 'react-router-dom';
import { useSession } from './auth/SessionContext';
import { RequireAuth, RequireRole, Splash } from './auth/guards';
import { isManagerOrOwner, isOwner, isPlatformAdmin } from './lib/roles';
import AppShell from './components/AppShell';
import Login from './screens/Login';
import Onboarding from './screens/Onboarding';
import Dashboard from './screens/Dashboard';
import Team from './screens/Team';
import Settings from './screens/Settings';
import Platform from './screens/Platform';

// Landing target depends on role: platform admins -> platform, everyone else -> dashboard.
function Home() {
  const { me } = useSession();
  if (isPlatformAdmin(me?.role)) return <Navigate to="/platform" replace />;
  return <Navigate to="/dashboard" replace />;
}

export default function App() {
  const { ready } = useSession();
  if (!ready) return <Splash />;

  return (
    <Routes>
      <Route path="/login" element={<Login />} />

      <Route
        element={
          <RequireAuth>
            <AppShell />
          </RequireAuth>
        }
      >
        <Route index element={<Home />} />
        <Route path="/dashboard" element={<RequireRole allow={(r) => isOwner(r) || isPlatformAdmin(r)}><Dashboard /></RequireRole>} />
        <Route path="/onboarding" element={<RequireRole allow={isOwner}><Onboarding /></RequireRole>} />
        <Route path="/team" element={<RequireRole allow={isManagerOrOwner}><Team /></RequireRole>} />
        <Route path="/settings" element={<RequireRole allow={isManagerOrOwner}><Settings /></RequireRole>} />
        <Route path="/platform" element={<RequireRole allow={isPlatformAdmin}><Platform /></RequireRole>} />
      </Route>

      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}
