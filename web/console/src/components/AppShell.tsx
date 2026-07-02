import { NavLink, Outlet } from 'react-router-dom';
import { useSession } from '../auth/SessionContext';
import { ROLE_LABELS } from '../lib/roles';
import { isManagerOrOwner, isOwner, isPlatformAdmin } from '../lib/roles';

export default function AppShell() {
  const { me, logout } = useSession();
  const role = me?.role;

  const links: { to: string; label: string; show: boolean }[] = [
    { to: '/dashboard', label: 'Dashboard', show: isOwner(role) || isPlatformAdmin(role) },
    { to: '/onboarding', label: 'Onboarding', show: isOwner(role) },
    { to: '/team', label: 'Team', show: isManagerOrOwner(role) },
    { to: '/settings', label: 'Settings', show: isManagerOrOwner(role) },
    { to: '/platform', label: 'Platform', show: isPlatformAdmin(role) },
  ];

  return (
    <div className="rz-shell">
      <header className="rz-top">
        <div className="rz-wrap">
          <div className="rz-brand">
            <span className="dot">R</span>
            <span>Restorna</span>
            <span className="rz-tag" style={{ marginLeft: 4 }}>Console</span>
          </div>
          <nav className="rz-nav" aria-label="Primary">
            {links
              .filter((l) => l.show)
              .map((l) => (
                <NavLink key={l.to} to={l.to} className={({ isActive }) => (isActive ? 'active' : '')}>
                  {l.label}
                </NavLink>
              ))}
          </nav>
          <div className="rz-spacer" />
          <div className="rz-row" style={{ gap: 10 }}>
            {role && <span className="rz-tag off">{ROLE_LABELS[role]}</span>}
            <button className="rz-ghost" onClick={logout} style={{ minHeight: 36, padding: '6px 12px' }}>
              Sign out
            </button>
          </div>
        </div>
      </header>
      <main className="rz-main">
        <div className="rz-wrap">
          <Outlet />
        </div>
      </main>
    </div>
  );
}
