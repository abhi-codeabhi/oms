import { useState } from 'react';
import {
  platformGetEntitlement,
  platformGetOwner,
  platformUpsertPlan,
} from '../lib/endpoints';
import { ApiError } from '../lib/api';
import type { EntitlementView, Owner } from '../lib/types';
import { ErrorState, Field, PageHeader } from '../components/ui';
import { useToast } from '../components/Toast';

// Platform admin. Note the gateway "contract gap": there is no ListOwners/ListPlans
// RPC yet, so the owners index is a lookup-by-id, the plans editor upserts a plan by
// id (quotas + feature flags), and connectors is a marketplace placeholder.
type Tab = 'owners' | 'plans' | 'connectors';

export default function Platform() {
  const [tab, setTab] = useState<Tab>('owners');
  return (
    <>
      <PageHeader
        kicker="Control plane"
        title="Platform admin"
        subtitle="Look up owners, edit plan quotas and feature flags, and manage connectors."
      />
      <div className="rz-row" style={{ gap: 6, marginBottom: 18 }}>
        {(['owners', 'plans', 'connectors'] as Tab[]).map((t) => (
          <button key={t} className={`rz-chip ${tab === t ? 'on' : ''}`} onClick={() => setTab(t)}>
            {t === 'owners' ? 'Owners' : t === 'plans' ? 'Plans' : 'Connectors'}
          </button>
        ))}
      </div>
      {tab === 'owners' && <OwnersLookup />}
      {tab === 'plans' && <PlansEditor />}
      {tab === 'connectors' && <Connectors />}
    </>
  );
}

function OwnersLookup() {
  const [ownerId, setOwnerId] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [owner, setOwner] = useState<Owner | null>(null);
  const [ent, setEnt] = useState<EntitlementView | null>(null);

  async function lookup(e: React.FormEvent) {
    e.preventDefault();
    if (!ownerId.trim()) return;
    setLoading(true);
    setError(null);
    setOwner(null);
    setEnt(null);
    try {
      const [o, en] = await Promise.all([
        platformGetOwner(ownerId.trim()),
        platformGetEntitlement(ownerId.trim()).catch(() => null),
      ]);
      setOwner(o.owner);
      setEnt(en);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Lookup failed.');
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="rz-grid" style={{ gap: 16 }}>
      <form onSubmit={lookup} className="rz-card pad">
        <p className="sm muted" style={{ marginBottom: 12 }}>
          The M1 contracts expose per-owner reads (no list RPC yet). Enter an owner id to inspect it.
        </p>
        <div className="rz-row" style={{ gap: 10, alignItems: 'flex-end' }}>
          <div style={{ flex: 1 }}>
            <Field label="Owner ID">
              <input className="rz-input rz-num" value={ownerId} onChange={(e) => setOwnerId(e.target.value)} placeholder="own_…" autoFocus />
            </Field>
          </div>
          <button className="rz-cta" type="submit" disabled={loading} style={{ marginBottom: 14 }}>
            {loading ? 'Looking…' : 'Look up'}
          </button>
        </div>
      </form>

      {error && <ErrorState message={error} />}

      {owner && (
        <div className="rz-card pad rz-in">
          <h3 style={{ fontSize: 16, marginBottom: 12 }}>{owner.name}</h3>
          <div className="rz-grid cols-2">
            <KV k="Legal name" v={owner.legal_name || '—'} />
            <KV k="Country" v={owner.country || '—'} />
            <KV k="Owner ID" v={owner.id} mono />
            <KV k="Created" v={owner.created_at || '—'} />
          </div>
          {ent && (
            <div style={{ marginTop: 16 }}>
              <div className="kicker" style={{ marginBottom: 8 }}>Entitlement</div>
              <div className="rz-row" style={{ gap: 8, flexWrap: 'wrap' }}>
                <span className="rz-tag">plan: {ent.effective_plan?.name || ent.entitlement?.plan_id || '—'}</span>
                {Object.entries(ent.effective_plan?.quotas ?? {}).map(([k, v]) => (
                  <span key={k} className="rz-tag off rz-num">{k}: {v}</span>
                ))}
                {Object.entries(ent.effective_plan?.features ?? {}).map(([k, on]) => (
                  <span key={k} className={`rz-tag ${on ? 'live' : 'off'}`}>{k}</span>
                ))}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function KV({ k, v, mono }: { k: string; v: string; mono?: boolean }) {
  return (
    <div style={{ marginBottom: 8 }}>
      <div className="xs muted">{k}</div>
      <div className={mono ? 'rz-num sm' : 'sm'}>{v}</div>
    </div>
  );
}

interface KVPair {
  key: string;
  value: string;
}

function PlansEditor() {
  const toast = useToast();
  const [id, setId] = useState('growth');
  const [name, setName] = useState('Growth');
  const [quotas, setQuotas] = useState<KVPair[]>([
    { key: 'brands', value: '3' },
    { key: 'outlets', value: '10' },
    { key: 'staff', value: '50' },
  ]);
  const [features, setFeatures] = useState<{ key: string; on: boolean }[]>([
    { key: 'connectors', on: false },
    { key: 'analytics', on: true },
  ]);
  const [busy, setBusy] = useState(false);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    if (!id.trim()) return toast.err('Plan id required.');
    setBusy(true);
    try {
      const q: Record<string, number> = {};
      for (const kv of quotas) {
        if (kv.key.trim()) q[kv.key.trim()] = Number(kv.value) || 0;
      }
      const f: Record<string, boolean> = {};
      for (const ft of features) {
        if (ft.key.trim()) f[ft.key.trim()] = ft.on;
      }
      const { plan } = await platformUpsertPlan({ id: id.trim(), name: name.trim() || id.trim(), quotas: q, features: f });
      toast.ok(`Plan “${plan.name}” saved`);
    } catch (err) {
      toast.err(err instanceof ApiError ? err.message : 'Could not save plan.');
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={save} className="rz-card pad">
      <div className="rz-grid cols-2" style={{ marginBottom: 0 }}>
        <Field label="Plan ID" hint="Stable key, e.g. free / growth / scale.">
          <input className="rz-input rz-num" value={id} onChange={(e) => setId(e.target.value)} />
        </Field>
        <Field label="Display name">
          <input className="rz-input" value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
      </div>

      <h4 style={{ fontSize: 13, margin: '10px 0' }}>Quotas</h4>
      {quotas.map((kv, i) => (
        <div key={i} className="rz-row" style={{ gap: 8, marginBottom: 8 }}>
          <input className="rz-input" placeholder="key" value={kv.key} onChange={(e) => setQuotas((a) => a.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)))} />
          <input className="rz-input rz-num" type="number" placeholder="limit" value={kv.value} onChange={(e) => setQuotas((a) => a.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))} style={{ maxWidth: 120 }} />
          <button type="button" className="rz-ghost rz-danger" style={{ minHeight: 40 }} onClick={() => setQuotas((a) => a.filter((_, j) => j !== i))}>×</button>
        </div>
      ))}
      <button type="button" className="rz-ghost" style={{ marginBottom: 16 }} onClick={() => setQuotas((a) => [...a, { key: '', value: '0' }])}>+ Add quota</button>

      <h4 style={{ fontSize: 13, margin: '10px 0' }}>Feature flags</h4>
      {features.map((ft, i) => (
        <div key={i} className="rz-row between" style={{ gap: 8, marginBottom: 8 }}>
          <input className="rz-input" placeholder="feature key" value={ft.key} onChange={(e) => setFeatures((a) => a.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)))} />
          <div className="rz-seg" style={{ width: 130, flexShrink: 0 }}>
            <button type="button" className={ft.on ? 'on' : ''} onClick={() => setFeatures((a) => a.map((x, j) => (j === i ? { ...x, on: true } : x)))}>On</button>
            <button type="button" className={!ft.on ? 'on' : ''} onClick={() => setFeatures((a) => a.map((x, j) => (j === i ? { ...x, on: false } : x)))}>Off</button>
          </div>
          <button type="button" className="rz-ghost rz-danger" style={{ minHeight: 40 }} onClick={() => setFeatures((a) => a.filter((_, j) => j !== i))}>×</button>
        </div>
      ))}
      <button type="button" className="rz-ghost" style={{ marginBottom: 18 }} onClick={() => setFeatures((a) => [...a, { key: '', on: false }])}>+ Add feature</button>

      <button className="rz-cta block" type="submit" disabled={busy}>{busy ? 'Saving…' : 'Save plan'}</button>
    </form>
  );
}

const CONNECTORS = [
  { name: 'Petpooja POS', kind: 'POS', status: 'available' },
  { name: 'Zomato', kind: 'Aggregator', status: 'available' },
  { name: 'Swiggy', kind: 'Aggregator', status: 'available' },
  { name: 'Razorpay', kind: 'Payments', status: 'available' },
  { name: 'WhatsApp Business', kind: 'Messaging', status: 'soon' },
  { name: 'Tally', kind: 'Accounting', status: 'soon' },
];

function Connectors() {
  const toast = useToast();
  return (
    <div className="rz-card pad">
      <p className="sm muted" style={{ marginBottom: 16 }}>
        Connector marketplace (placeholder). Wire these to the M3 integration plane when its BFF routes land.
      </p>
      <div className="rz-grid cols-3">
        {CONNECTORS.map((c) => (
          <div key={c.name} className="rz-card pad" style={{ background: 'var(--s1)' }}>
            <div className="rz-row between" style={{ marginBottom: 8 }}>
              <strong className="sm">{c.name}</strong>
              <span className={`rz-tag ${c.status === 'available' ? 'live' : 'warn'}`}>{c.status === 'available' ? 'Available' : 'Soon'}</span>
            </div>
            <div className="xs muted" style={{ marginBottom: 12 }}>{c.kind}</div>
            <button
              className="rz-ghost block"
              disabled={c.status !== 'available'}
              onClick={() => toast.info(`${c.name} connect flow not wired yet`)}
            >
              {c.status === 'available' ? 'Connect' : 'Coming soon'}
            </button>
          </div>
        ))}
      </div>
    </div>
  );
}
