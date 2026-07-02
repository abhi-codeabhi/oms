import type { ReactNode } from 'react';
import { Navigate, useLocation } from 'react-router-dom';
import { useSession } from './SessionContext';
import type { Role } from '../lib/types';

/** Full-screen splash while the session hydrates (introspect/refresh in flight). */
export function Splash() {
  return (
    <div style={{ minHeight: '100vh', display: 'grid', placeItems: 'center' }}>
      <div className="rz-stack" style={{ alignItems: 'center', gap: 12 }}>
        <div className="rz-brand" style={{ fontSize: 20 }}>
          <span className="dot">R</span> Restorna
        </div>
        <div className="rz-skel" style={{ width: 120, height: 6 }} />
      </div>
    </div>
  );
}

/** Requires an authenticated + active session; else bounce to /login. */
export function RequireAuth({ children }: { children: ReactNode }) {
  const { ready, isAuthed } = useSession();
  const loc = useLocation();
  if (!ready) return <Splash />;
  if (!isAuthed) return <Navigate to="/login" replace state={{ from: loc.pathname }} />;
  return <>{children}</>;
}

/** Requires the session role to be one of `allow`; else show a calm forbidden card. */
export function RequireRole({ allow, children }: { allow: (r: Role) => boolean; children: ReactNode }) {
  const { me } = useSession();
  if (!me || !allow(me.role)) {
    return (
      <div className="rz-card pad rz-in" style={{ maxWidth: 520, margin: '40px auto', textAlign: 'center' }}>
        <div className="kicker" style={{ marginBottom: 8 }}>403</div>
        <h2 style={{ fontSize: 20 }}>Not available for your role</h2>
        <p className="sm muted" style={{ marginTop: 8 }}>
          This surface is gated by <code>/api/me</code>. Ask an owner or platform admin for access.
        </p>
      </div>
    );
  }
  return <>{children}</>;
}
