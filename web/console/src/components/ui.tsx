// Small presentational primitives used across screens. Kept dependency-free.
import type { ReactNode } from 'react';

export function Skeleton({ h = 16, w = '100%', style }: { h?: number; w?: number | string; style?: React.CSSProperties }) {
  return <div className="rz-skel" style={{ height: h, width: w, ...style }} aria-hidden />;
}

export function SkeletonCard({ lines = 3 }: { lines?: number }) {
  return (
    <div className="rz-card pad rz-stack" style={{ gap: 12 }}>
      <Skeleton h={14} w="40%" />
      {Array.from({ length: lines }).map((_, i) => (
        <Skeleton key={i} h={12} w={`${90 - i * 12}%`} />
      ))}
    </div>
  );
}

export function ErrorState({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="rz-card pad rz-in" role="alert" style={{ borderColor: '#E9CFCF', background: '#FCF6F5' }}>
      <div className="rz-row between">
        <div>
          <div style={{ fontWeight: 600, color: 'var(--red)' }}>Something went wrong</div>
          <div className="sm muted" style={{ marginTop: 4 }}>{message}</div>
        </div>
        {onRetry && (
          <button className="rz-ghost" onClick={onRetry} style={{ minWidth: 92 }}>
            Retry
          </button>
        )}
      </div>
    </div>
  );
}

export function EmptyState({ icon = '◇', title, hint }: { icon?: string; title: string; hint?: string }) {
  return (
    <div className="rz-empty rz-in">
      <span className="ic" aria-hidden>{icon}</span>
      <div style={{ fontWeight: 600, color: 'var(--ink)' }}>{title}</div>
      {hint && <div className="xs" style={{ marginTop: 4 }}>{hint}</div>}
    </div>
  );
}

export function Field({
  label,
  hint,
  children,
  htmlFor,
}: {
  label: string;
  hint?: string;
  children: ReactNode;
  htmlFor?: string;
}) {
  return (
    <div className="rz-field">
      <label htmlFor={htmlFor}>{label}</label>
      {children}
      {hint && <span className="hint">{hint}</span>}
    </div>
  );
}

export function PageHeader({ kicker, title, subtitle, action }: { kicker?: string; title: string; subtitle?: string; action?: ReactNode }) {
  return (
    <div className="rz-row between" style={{ marginBottom: 22, gap: 16, flexWrap: 'wrap' }}>
      <div>
        {kicker && <div className="kicker" style={{ marginBottom: 6 }}>{kicker}</div>}
        <h1 style={{ fontSize: 24 }}>{title}</h1>
        {subtitle && <div className="sm muted" style={{ marginTop: 6, maxWidth: 560 }}>{subtitle}</div>}
      </div>
      {action}
    </div>
  );
}

export function Meter({ used, limit }: { used: number; limit: number }) {
  const pct = limit > 0 ? Math.min(100, Math.round((used / limit) * 100)) : 0;
  const cls = pct >= 100 ? 'full' : pct >= 80 ? 'warn' : '';
  return (
    <div className={`rz-meter ${cls}`} aria-label={`${used} of ${limit} used`}>
      <i style={{ width: `${limit > 0 ? pct : 0}%` }} />
    </div>
  );
}
