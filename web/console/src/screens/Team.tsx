import { useState } from 'react';
import { useSession } from '../auth/SessionContext';
import {
  mgrAddStaff,
  mgrChangeRole,
  mgrDisableStaff,
  mgrInviteStaff,
  mgrListStaff,
  ownerBrands,
  ownerEntitlement,
  ownerOutlets,
} from '../lib/endpoints';
import { useAsync } from '../lib/useAsync';
import { ApiError } from '../lib/api';
import type { Brand, EntitlementView, Outlet, RoleSlug, StaffMember } from '../lib/types';
import { ASSIGNABLE_STAFF_ROLES, ROLE_LABELS } from '../lib/roles';
import { EmptyState, ErrorState, Field, Meter, PageHeader, SkeletonCard } from '../components/ui';
import { useToast } from '../components/Toast';

// Team: staff list + add / disable / change-role via the manager BFF, plus the plan's
// staff quota so the operator sees limits before hitting a 429/quota error.
export default function Team() {
  const { me } = useSession();
  const toast = useToast();
  const ownerId = me?.scope?.owner_id || '';
  const scopedRestaurant = me?.scope?.restaurant_id || '';

  // Owners pick an outlet; managers are already restaurant-scoped by their token.
  const brandsQ = useAsync<{ brands: Brand[] }>(() => ownerBrands(ownerId || undefined), [ownerId]);
  const brands = brandsQ.data?.brands ?? [];
  const [brandId, setBrandId] = useState('');
  const activeBrand = brandId || brands[0]?.id || '';

  const outletsQ = useAsync<{ outlets: Outlet[] }>(
    () => (activeBrand ? ownerOutlets(activeBrand) : Promise.resolve({ outlets: [] as Outlet[] })),
    [activeBrand]
  );
  const outlets = outletsQ.data?.outlets ?? [];
  const [restaurantId, setRestaurantId] = useState('');
  const activeRestaurant = scopedRestaurant || restaurantId || outlets[0]?.id || '';

  const entQ = useAsync<EntitlementView>(
    () => (ownerId ? ownerEntitlement(ownerId) : Promise.resolve({ entitlement: null, effective_plan: null })),
    [ownerId]
  );
  const staffLimit =
    entQ.data?.effective_plan?.quotas?.['staff'] ??
    entQ.data?.effective_plan?.quotas?.['seats'] ??
    entQ.data?.effective_plan?.quotas?.['max_staff'];

  const staffQ = useAsync<{ members: StaffMember[] }>(
    () => (activeRestaurant ? mgrListStaff(activeRestaurant) : Promise.resolve({ members: [] as StaffMember[] })),
    [activeRestaurant]
  );
  const members = staffQ.data?.members ?? [];
  const activeCount = members.filter((m) => m.active).length;

  // Add-staff form
  const [showAdd, setShowAdd] = useState(false);
  const [form, setForm] = useState({ name: '', email: '', phone: '', role: 'waiter' as RoleSlug });
  const [busy, setBusy] = useState(false);

  async function add(e: React.FormEvent) {
    e.preventDefault();
    if (!activeRestaurant) return toast.err('Pick an outlet first.');
    if (!form.name.trim()) return toast.err('Name is required.');
    setBusy(true);
    try {
      await mgrAddStaff({ restaurant_id: activeRestaurant, ...form, name: form.name.trim() });
      toast.ok(`${form.name.trim()} added`);
      setForm({ name: '', email: '', phone: '', role: 'waiter' });
      setShowAdd(false);
      staffQ.reload();
    } catch (err) {
      toast.err(err instanceof ApiError ? err.message : 'Could not add staff.');
    } finally {
      setBusy(false);
    }
  }

  async function toggle(m: StaffMember) {
    try {
      await mgrDisableStaff(m.id, !m.active);
      toast.ok(m.active ? `${m.name} disabled` : `${m.name} re-enabled`);
      staffQ.reload();
    } catch (err) {
      toast.err(err instanceof ApiError ? err.message : 'Could not update.');
    }
  }

  async function changeRole(m: StaffMember, role: RoleSlug) {
    try {
      await mgrChangeRole(m.id, role);
      toast.ok(`${m.name} is now ${role}`);
      staffQ.reload();
    } catch (err) {
      toast.err(err instanceof ApiError ? err.message : 'Could not change role.');
    }
  }

  async function invite(m: StaffMember) {
    try {
      await mgrInviteStaff(m.id);
      toast.ok(`Invite sent to ${m.name}`);
    } catch (err) {
      toast.err(err instanceof ApiError ? err.message : 'Could not send invite.');
    }
  }

  const atLimit = typeof staffLimit === 'number' && staffLimit > 0 && activeCount >= staffLimit;

  return (
    <>
      <PageHeader
        kicker="People"
        title="Team"
        subtitle="Add staff, set roles, and enable or disable access. Roles gate what each person sees."
        action={
          <button className="rz-cta" disabled={atLimit || !activeRestaurant} onClick={() => setShowAdd((v) => !v)}>
            {showAdd ? 'Close' : '+ Add staff'}
          </button>
        }
      />

      {/* Scope pickers (owners only; managers are token-scoped) */}
      {!scopedRestaurant && (
        <div className="rz-card pad" style={{ marginBottom: 16 }}>
          <div className="rz-grid cols-2" style={{ marginBottom: 0 }}>
            <Field label="Brand">
              <select className="rz-select" value={activeBrand} onChange={(e) => { setBrandId(e.target.value); setRestaurantId(''); }}>
                {brands.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
              </select>
            </Field>
            <Field label="Outlet">
              <select className="rz-select" value={activeRestaurant} onChange={(e) => setRestaurantId(e.target.value)}>
                {outlets.map((o) => <option key={o.id} value={o.id}>{o.name}</option>)}
              </select>
            </Field>
          </div>
        </div>
      )}

      {/* Seat usage */}
      <div className="rz-card pad" style={{ marginBottom: 16 }}>
        <div className="rz-row between" style={{ marginBottom: 8 }}>
          <span className="sm" style={{ fontWeight: 600 }}>Staff seats</span>
          <span className="sm muted rz-num">
            {activeCount}{typeof staffLimit === 'number' && staffLimit > 0 ? ` / ${staffLimit}` : ' · unlimited'}
          </span>
        </div>
        {typeof staffLimit === 'number' && staffLimit > 0 && <Meter used={activeCount} limit={staffLimit} />}
        {atLimit && <div className="sm" style={{ color: 'var(--amber)', marginTop: 8 }}>Seat limit reached — disable someone or upgrade the plan to add more.</div>}
      </div>

      {showAdd && (
        <form onSubmit={add} className="rz-card pad rz-in" style={{ marginBottom: 16 }}>
          <h3 style={{ fontSize: 16, marginBottom: 12 }}>Add a team member</h3>
          <div className="rz-grid cols-2" style={{ marginBottom: 0 }}>
            <Field label="Name"><input className="rz-input" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} autoFocus /></Field>
            <Field label="Role">
              <select className="rz-select" value={form.role} onChange={(e) => setForm({ ...form, role: e.target.value as RoleSlug })}>
                {ASSIGNABLE_STAFF_ROLES.map((r) => <option key={r.slug} value={r.slug}>{r.label}</option>)}
              </select>
            </Field>
          </div>
          <div className="rz-grid cols-2">
            <Field label="Email"><input className="rz-input" type="email" value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} /></Field>
            <Field label="Phone"><input className="rz-input" type="tel" value={form.phone} onChange={(e) => setForm({ ...form, phone: e.target.value })} /></Field>
          </div>
          <button className="rz-cta" type="submit" disabled={busy}>{busy ? 'Adding…' : 'Add member'}</button>
        </form>
      )}

      <div className="rz-card" style={{ overflow: 'hidden' }}>
        {staffQ.loading ? (
          <div className="pad"><SkeletonCard lines={4} /></div>
        ) : staffQ.error ? (
          <div className="pad"><ErrorState message={staffQ.error} onRetry={staffQ.reload} /></div>
        ) : members.length === 0 ? (
          <div className="pad"><EmptyState icon="☺" title="No staff yet" hint="Add your first team member to get started." /></div>
        ) : (
          <div style={{ overflowX: 'auto' }}>
            <table className="rz-table">
              <thead>
                <tr><th>Name</th><th>Contact</th><th>Role</th><th>Status</th><th style={{ textAlign: 'right' }}>Actions</th></tr>
              </thead>
              <tbody>
                {members.map((m) => (
                  <tr key={m.id}>
                    <td><strong>{m.name}</strong></td>
                    <td className="sm muted">{m.email || m.phone || '—'}</td>
                    <td>
                      <select
                        className="rz-select"
                        value={roleSlugFor(m.role)}
                        onChange={(e) => changeRole(m, e.target.value as RoleSlug)}
                        style={{ padding: '6px 8px', maxWidth: 130, fontSize: 12.5 }}
                        aria-label={`Role for ${m.name}`}
                      >
                        {ASSIGNABLE_STAFF_ROLES.map((r) => <option key={r.slug} value={r.slug}>{r.label}</option>)}
                      </select>
                    </td>
                    <td><span className={`rz-tag ${m.active ? 'live' : 'off'}`}>{m.active ? 'Active' : 'Disabled'}</span></td>
                    <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                      {!m.user_id && (
                        <button className="rz-ghost" style={{ minHeight: 32, padding: '4px 10px', marginRight: 6 }} onClick={() => invite(m)}>Invite</button>
                      )}
                      <button className={`rz-ghost ${m.active ? 'rz-danger' : ''}`} style={{ minHeight: 32, padding: '4px 10px' }} onClick={() => toggle(m)}>
                        {m.active ? 'Disable' : 'Enable'}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      <p className="xs muted" style={{ marginTop: 10 }}>Roles: {ASSIGNABLE_STAFF_ROLES.map((r) => ROLE_LABELS[('ROLE_' + r.slug.toUpperCase()) as keyof typeof ROLE_LABELS]).join(' · ')}</p>
    </>
  );
}

function roleSlugFor(role: StaffMember['role']): RoleSlug {
  const s = role.replace('ROLE_', '').toLowerCase();
  return (ASSIGNABLE_STAFF_ROLES.find((r) => r.slug === s)?.slug ?? 'waiter') as RoleSlug;
}
