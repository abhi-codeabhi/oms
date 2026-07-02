import { useEffect, useMemo, useState } from 'react';
import { useSession } from '../auth/SessionContext';
import { isOwner } from '../lib/roles';
import {
  mgrGetSettings,
  mgrSetSetting,
  ownerGetSettings,
  ownerSetSetting,
} from '../lib/endpoints';
import { useAsync } from '../lib/useAsync';
import { ApiError } from '../lib/api';
import type { SettingValue } from '../lib/types';
import {
  NAMESPACE_LABELS,
  SETTING_DEFS,
  groupByNamespace,
  type SettingDef,
} from '../lib/settingsDefs';
import { ErrorState, Field, PageHeader, SkeletonCard } from '../components/ui';
import { useToast } from '../components/Toast';

// Settings: business config grouped by namespace with the DEFINITION metadata (title,
// description, type, validation, editable_by). Effective values come from the settings
// BFF; each row writes a single override via SetOverride. Owners use /api/owner/settings,
// managers use /api/manager/settings (scope resolved from the token in both cases).
export default function Settings() {
  const { me } = useSession();
  const toast = useToast();
  const owner = isOwner(me?.role) || me?.role === 'ROLE_PLATFORM_ADMIN';

  const get = owner ? ownerGetSettings : mgrGetSettings;
  const set = owner ? ownerSetSetting : mgrSetSetting;

  const grouped = useMemo(() => groupByNamespace(SETTING_DEFS), []);
  const namespaces = Object.keys(grouped);
  const [ns, setNs] = useState(namespaces[0]);

  // Effective values for the visible namespace.
  const valuesQ = useAsync<{ values: SettingValue[] }>(() => get(ns), [ns, owner]);
  const effective = useMemo(() => {
    const map = new Map<string, SettingValue>();
    for (const v of valuesQ.data?.values ?? []) map.set(v.key, v);
    return map;
  }, [valuesQ.data]);

  return (
    <>
      <PageHeader
        kicker="Configuration"
        title="Settings"
        subtitle="Tune your business rules without code — tax, service charge, currency, ordering and floor timings. Changes take effect immediately."
      />

      <div className="rz-row" style={{ gap: 6, flexWrap: 'wrap', marginBottom: 18 }}>
        {namespaces.map((n) => (
          <button key={n} className={`rz-chip ${n === ns ? 'on' : ''}`} onClick={() => setNs(n)}>
            {NAMESPACE_LABELS[n] || n}
          </button>
        ))}
      </div>

      {valuesQ.loading ? (
        <SkeletonCard lines={5} />
      ) : valuesQ.error ? (
        <ErrorState message={valuesQ.error} onRetry={valuesQ.reload} />
      ) : (
        <div className="rz-grid" style={{ gap: 14 }}>
          {grouped[ns].map((def) => (
            <SettingRow
              key={def.key}
              def={def}
              effective={effective.get(def.key)}
              canEdit={owner || def.editable_by === 'manager'}
              onSave={async (raw) => {
                try {
                  await set(def.key, def.type, raw);
                  toast.ok(`${def.title} saved`);
                  valuesQ.reload();
                } catch (err) {
                  toast.err(err instanceof ApiError ? err.message : 'Could not save.');
                  throw err;
                }
              }}
            />
          ))}
        </div>
      )}
    </>
  );
}

function SettingRow({
  def,
  effective,
  canEdit,
  onSave,
}: {
  def: SettingDef;
  effective?: SettingValue;
  canEdit: boolean;
  onSave: (raw: string) => Promise<void>;
}) {
  const current = effective?.raw ?? def.default;
  const [value, setValue] = useState(current);
  const [busy, setBusy] = useState(false);
  useEffect(() => setValue(current), [current]);

  const dirty = value !== current;
  const source = effective?.source_scope?.replace('SCOPE_', '').toLowerCase() || 'default';
  const isOverride = effective && source !== 'default' && !!effective.raw;

  async function save() {
    setBusy(true);
    try {
      await onSave(value);
    } catch {
      /* toast handled upstream */
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="rz-card pad">
      <div className="rz-row between" style={{ alignItems: 'flex-start', gap: 16, flexWrap: 'wrap' }}>
        <div style={{ minWidth: 220, flex: 1 }}>
          <div className="rz-row" style={{ gap: 8 }}>
            <strong>{def.title}</strong>
            <span className="rz-tag off">{def.type.toLowerCase()}</span>
            {isOverride ? <span className="rz-tag live">override · {source}</span> : <span className="rz-tag">default</span>}
          </div>
          <div className="sm muted" style={{ marginTop: 6 }}>{def.description}</div>
          <div className="xs muted" style={{ marginTop: 6 }}>
            <code>{def.key}</code>
            {def.validation && <> · {def.validation}</>}
            <> · editable by {def.editable_by}</>
          </div>
        </div>

        <div style={{ width: 260, flexShrink: 0 }}>
          <Field label="Value">
            {def.type === 'BOOL' ? (
              <div className="rz-seg">
                <button type="button" className={value === 'true' ? 'on' : ''} disabled={!canEdit} onClick={() => setValue('true')}>On</button>
                <button type="button" className={value === 'false' ? 'on' : ''} disabled={!canEdit} onClick={() => setValue('false')}>Off</button>
              </div>
            ) : def.type === 'ENUM' ? (
              <select className="rz-select" value={value} disabled={!canEdit} onChange={(e) => setValue(e.target.value)}>
                {(def.enum_options ?? []).map((o) => <option key={o} value={o}>{o}</option>)}
              </select>
            ) : def.type === 'JSON' ? (
              <textarea className="rz-textarea rz-num" value={value} disabled={!canEdit} onChange={(e) => setValue(e.target.value)} />
            ) : (
              <input
                className="rz-input rz-num"
                inputMode={def.type === 'INT' || def.type === 'DECIMAL' ? 'decimal' : 'text'}
                value={value}
                disabled={!canEdit}
                onChange={(e) => setValue(e.target.value)}
              />
            )}
          </Field>
          <button className="rz-cta block" disabled={!canEdit || !dirty || busy} onClick={save}>
            {busy ? 'Saving…' : dirty ? 'Save' : 'Saved'}
          </button>
          {!canEdit && <div className="xs muted" style={{ marginTop: 6 }}>Editable by {def.editable_by} only.</div>}
        </div>
      </div>
    </div>
  );
}
