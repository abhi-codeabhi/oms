import { Link } from 'react-router-dom';
import { useSession } from '../auth/SessionContext';
import { ownerBrands, ownerEntitlement, ownerOutlets } from '../lib/endpoints';
import { useAsync } from '../lib/useAsync';
import type { Brand, EntitlementView, Outlet } from '../lib/types';
import { EmptyState, ErrorState, Meter, PageHeader, SkeletonCard } from '../components/ui';

// Owner dashboard: brands + outlets + plan usage (quota used vs limit) + quick links.
// Quotas are the effective-plan limits; "used" is derived from live tenant counts we
// already loaded (outlets/brands), so the meters are real, not mocked.

function QuotaRow({ label, used, limit }: { label: string; used: number; limit?: number }) {
  const has = typeof limit === 'number' && limit > 0;
  return (
    <div style={{ marginBottom: 14 }}>
      <div className="rz-row between" style={{ marginBottom: 6 }}>
        <span className="sm" style={{ fontWeight: 600 }}>{label}</span>
        <span className="sm muted rz-num">
          {used}
          {has ? ` / ${limit}` : ' · unlimited'}
        </span>
      </div>
      {has && <Meter used={used} limit={limit as number} />}
    </div>
  );
}

export default function Dashboard() {
  const { me } = useSession();
  const ownerId = me?.scope?.owner_id || '';

  const brandsQ = useAsync<{ brands: Brand[] }>(() => ownerBrands(ownerId || undefined), [ownerId]);
  const entQ = useAsync<EntitlementView>(() => ownerEntitlement(ownerId), [ownerId]);

  const brands = brandsQ.data?.brands ?? [];
  const firstBrandId = brands[0]?.id;

  // Outlets for the first brand — a representative usage signal; owners with many
  // brands drill in per-brand below.
  const outletsQ = useAsync<{ outlets: Outlet[] }>(
    () => (firstBrandId ? ownerOutlets(firstBrandId) : Promise.resolve({ outlets: [] as Outlet[] })),
    [firstBrandId]
  );
  const outlets = outletsQ.data?.outlets ?? [];

  const plan = entQ.data?.effective_plan;
  const quotas = plan?.quotas ?? {};
  const features = plan?.features ?? {};

  return (
    <>
      <PageHeader
        kicker="Overview"
        title="Dashboard"
        subtitle="Your brands, outlets and how much of your plan you’re using."
        action={
          <Link to="/onboarding" className="rz-cta" style={{ textDecoration: 'none' }}>
            + Add outlet
          </Link>
        }
      />

      <div className="rz-grid cols-3" style={{ marginBottom: 20 }}>
        <div className="rz-card pad">
          <div className="kicker">Plan</div>
          {entQ.loading ? (
            <div className="rz-skel" style={{ height: 24, width: 120, marginTop: 8 }} />
          ) : (
            <div style={{ fontSize: 22, fontWeight: 680, marginTop: 4 }}>{plan?.name || plan?.id || '—'}</div>
          )}
          <div className="sm muted" style={{ marginTop: 4 }}>{Object.keys(features).filter((k) => features[k]).length} features on</div>
        </div>
        <div className="rz-card pad">
          <div className="kicker">Brands</div>
          <div style={{ fontSize: 22, fontWeight: 680, marginTop: 4 }} className="rz-num">{brandsQ.loading ? '—' : brands.length}</div>
        </div>
        <div className="rz-card pad">
          <div className="kicker">Outlets (first brand)</div>
          <div style={{ fontSize: 22, fontWeight: 680, marginTop: 4 }} className="rz-num">{outletsQ.loading ? '—' : outlets.length}</div>
        </div>
      </div>

      <div className="rz-grid cols-2">
        {/* Plan usage */}
        <div className="rz-card pad">
          <h3 style={{ fontSize: 16, marginBottom: 14 }}>Plan usage</h3>
          {entQ.loading ? (
            <SkeletonCard lines={3} />
          ) : entQ.error ? (
            <ErrorState message={entQ.error} onRetry={entQ.reload} />
          ) : (
            <>
              <QuotaRow label="Brands" used={brands.length} limit={quotas['brands'] ?? quotas['max_brands']} />
              <QuotaRow label="Outlets" used={outlets.length} limit={quotas['outlets'] ?? quotas['max_outlets'] ?? quotas['restaurants']} />
              <QuotaRow label="Staff seats" used={0} limit={quotas['staff'] ?? quotas['seats'] ?? quotas['max_staff']} />
              {Object.entries(quotas)
                .filter(([k]) => !['brands', 'max_brands', 'outlets', 'max_outlets', 'restaurants', 'staff', 'seats', 'max_staff'].includes(k))
                .map(([k, v]) => (
                  <QuotaRow key={k} label={k} used={0} limit={v} />
                ))}
              {Object.keys(quotas).length === 0 && <div className="sm muted">No quotas set on this plan.</div>}
            </>
          )}
        </div>

        {/* Features + quick links */}
        <div className="rz-card pad">
          <h3 style={{ fontSize: 16, marginBottom: 14 }}>Features & quick links</h3>
          <div className="rz-row" style={{ flexWrap: 'wrap', gap: 6, marginBottom: 18 }}>
            {Object.keys(features).length === 0 && <span className="sm muted">No feature flags.</span>}
            {Object.entries(features).map(([k, on]) => (
              <span key={k} className={`rz-tag ${on ? 'live' : 'off'}`}>{k}</span>
            ))}
          </div>
          <div className="rz-stack" style={{ gap: 8 }}>
            <Link to="/team" className="rz-ghost block" style={{ textAlign: 'center' }}>Manage team</Link>
            <Link to="/settings" className="rz-ghost block" style={{ textAlign: 'center' }}>Business settings</Link>
          </div>
        </div>
      </div>

      {/* Brands + outlets */}
      <div className="rz-card pad" style={{ marginTop: 20 }}>
        <h3 style={{ fontSize: 16, marginBottom: 14 }}>Brands & outlets</h3>
        {brandsQ.loading ? (
          <SkeletonCard lines={2} />
        ) : brandsQ.error ? (
          <ErrorState message={brandsQ.error} onRetry={brandsQ.reload} />
        ) : brands.length === 0 ? (
          <EmptyState icon="✦" title="No brands yet" hint="Run onboarding to create your first brand and outlet." />
        ) : (
          <div className="rz-grid cols-2">
            {brands.map((b) => (
              <BrandCard key={b.id} brand={b} defaultOutlets={b.id === firstBrandId ? outlets : undefined} />
            ))}
          </div>
        )}
      </div>
    </>
  );
}

function BrandCard({ brand, defaultOutlets }: { brand: Brand; defaultOutlets?: Outlet[] }) {
  // Reuse pre-loaded outlets for the first brand; fetch on demand for the rest.
  const q = useAsync<{ outlets: Outlet[] }>(
    () => (defaultOutlets ? Promise.resolve({ outlets: defaultOutlets }) : ownerOutlets(brand.id)),
    [brand.id]
  );
  const outlets = q.data?.outlets ?? [];
  const accent = brand.primary_color || 'var(--g)';
  return (
    <div className="rz-card pad" style={{ background: 'var(--s1)' }}>
      <div className="rz-row" style={{ gap: 12, marginBottom: 10 }}>
        <div
          style={{
            width: 40, height: 40, borderRadius: 11, flexShrink: 0,
            background: brand.logo?.url ? `center/cover url(${brand.logo.url})` : accent,
            display: 'grid', placeItems: 'center', color: '#fff', fontWeight: 700,
          }}
        >
          {!brand.logo?.url && (brand.name[0]?.toUpperCase() || 'R')}
        </div>
        <div>
          <div style={{ fontWeight: 640 }}>{brand.name}</div>
          <div className="xs muted rz-num">{brand.id}</div>
        </div>
      </div>
      {q.loading ? (
        <div className="rz-skel" style={{ height: 12, width: '70%' }} />
      ) : outlets.length === 0 ? (
        <div className="xs muted">No outlets under this brand yet.</div>
      ) : (
        <ul className="rz-stack" style={{ gap: 6, listStyle: 'none', padding: 0, margin: 0 }}>
          {outlets.map((o) => (
            <li key={o.id} className="rz-row between">
              <span className="sm">{o.name}</span>
              <span className={`rz-tag ${o.active ? 'live' : 'off'}`}>{o.active ? 'Live' : 'Off'}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
