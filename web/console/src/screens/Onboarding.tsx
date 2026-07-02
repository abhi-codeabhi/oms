import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { ApiError, fileToBase64 } from '../lib/api';
import {
  onbComplete,
  onbInviteTeam,
  onbStart,
  onbSubmitBrand,
  onbSubmitOutlet,
} from '../lib/endpoints';
import type { OnboardingState, RoleSlug } from '../lib/types';
import { Field, PageHeader } from '../components/ui';
import { useToast } from '../components/Toast';

// The onboarding wizard: platform onboards a client owner -> brand -> outlet -> team
// -> go-live. Maps STEP_* from onboarding.proto. Psychology:
//  - Zeigarnik: an always-visible progress bar + stepper (open loop pulls to finish).
//  - Hick: exactly one form + one brass primary per step.
//  - Von Restorff: the live/complete states use the one distinct green.
//  - Peak-end: a celebratory go-live moment ends the flow on a high.

type StepId = 'account' | 'plan' | 'brand' | 'outlet' | 'team' | 'golive';
const STEP_ORDER: StepId[] = ['account', 'plan', 'brand', 'outlet', 'team', 'golive'];
const STEP_LABEL: Record<StepId, string> = {
  account: 'Account',
  plan: 'Plan',
  brand: 'Brand',
  outlet: 'First outlet',
  team: 'Invite team',
  golive: 'Go live',
};

const PLANS = [
  { id: 'free', name: 'Free', blurb: '1 outlet · core ordering' },
  { id: 'growth', name: 'Growth', blurb: 'Multi-outlet · team & settings' },
  { id: 'scale', name: 'Scale', blurb: 'Connectors · quotas raised' },
];

interface Invite {
  name: string;
  email: string;
  phone: string;
  role: RoleSlug;
}

export default function Onboarding() {
  const toast = useToast();
  const nav = useNavigate();

  const [stepIdx, setStepIdx] = useState(0);
  const step = STEP_ORDER[stepIdx];
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [state, setState] = useState<OnboardingState | null>(null);

  // Account
  const [ownerName, setOwnerName] = useState('');
  const [contactEmail, setContactEmail] = useState('');
  const [contactPhone, setContactPhone] = useState('');
  const [country, setCountry] = useState('IN');
  // Plan
  const [planId, setPlanId] = useState('free');
  // Brand
  const [brandName, setBrandName] = useState('');
  const [accent, setAccent] = useState('#9E7C46');
  const [logoFile, setLogoFile] = useState<File | null>(null);
  const [logoPreview, setLogoPreview] = useState<string | null>(null);
  // Outlet
  const [outletName, setOutletName] = useState('');
  const [address, setAddress] = useState('');
  const [timezone, setTimezone] = useState('Asia/Kolkata');
  const [gstin, setGstin] = useState('');
  // Team
  const [invites, setInvites] = useState<Invite[]>([{ name: '', email: '', phone: '', role: 'manager' }]);

  const progressPct = Math.round((stepIdx / (STEP_ORDER.length - 1)) * 100);
  const onbId = state?.id;

  const go = (delta: number) => {
    setError(null);
    setStepIdx((i) => Math.min(STEP_ORDER.length - 1, Math.max(0, i + delta)));
  };

  function handle(err: unknown) {
    setError(err instanceof ApiError ? err.message : (err as Error)?.message || 'Something went wrong.');
  }

  async function submitAccount(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    if (!ownerName.trim()) return setError('A business name is required.');
    setBusy(true);
    try {
      const { state: st } = await onbStart({
        owner_name: ownerName.trim(),
        contact_email: contactEmail.trim(),
        contact_phone: contactPhone.trim(),
        country,
        plan_id: planId,
      });
      setState(st);
      go(1);
    } catch (err) {
      handle(err);
    } finally {
      setBusy(false);
    }
  }

  // Plan step re-uses start's plan_id (already sent). It's a confirmation step; if the
  // owner changed plan after starting, we re-start with the new plan.
  async function submitPlan() {
    setError(null);
    setBusy(true);
    try {
      // Re-start is idempotent per owner scope; ensures the chosen plan is applied.
      const { state: st } = await onbStart({
        owner_name: ownerName.trim(),
        contact_email: contactEmail.trim(),
        contact_phone: contactPhone.trim(),
        country,
        plan_id: planId,
      });
      setState(st);
      go(1);
    } catch (err) {
      handle(err);
    } finally {
      setBusy(false);
    }
  }

  async function onPickLogo(file: File | null) {
    setLogoFile(file);
    if (logoPreview) URL.revokeObjectURL(logoPreview);
    setLogoPreview(file ? URL.createObjectURL(file) : null);
  }

  async function submitBrand(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    if (!onbId) return setError('Onboarding not started.');
    if (!brandName.trim()) return setError('Give your brand a name.');
    setBusy(true);
    try {
      let logo_base64: string | undefined;
      let logo_content_type: string | undefined;
      if (logoFile) {
        const { base64, contentType } = await fileToBase64(logoFile);
        logo_base64 = base64;
        logo_content_type = contentType;
      }
      const { state: st } = await onbSubmitBrand({
        onboarding_id: onbId,
        brand_name: brandName.trim(),
        primary_color: accent,
        logo_base64,
        logo_content_type,
      });
      setState(st);
      go(1);
    } catch (err) {
      handle(err);
    } finally {
      setBusy(false);
    }
  }

  async function submitOutlet(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    if (!onbId) return setError('Onboarding not started.');
    if (!outletName.trim()) return setError('Name your first outlet.');
    setBusy(true);
    try {
      const { state: st } = await onbSubmitOutlet({
        onboarding_id: onbId,
        name: outletName.trim(),
        address: address.trim(),
        timezone,
        gstin: gstin.trim(),
      });
      setState(st);
      go(1);
    } catch (err) {
      handle(err);
    } finally {
      setBusy(false);
    }
  }

  async function submitTeamThenGolive(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    if (!onbId) return setError('Onboarding not started.');
    setBusy(true);
    try {
      const filled = invites.filter((i) => i.name.trim() && (i.email.trim() || i.phone.trim()));
      if (filled.length) {
        const { state: st } = await onbInviteTeam(onbId, filled);
        setState(st);
      }
      go(1); // -> go-live step; completion fires there
    } catch (err) {
      handle(err);
    } finally {
      setBusy(false);
    }
  }

  async function complete() {
    setError(null);
    if (!onbId) return setError('Onboarding not started.');
    setBusy(true);
    try {
      const { state: st } = await onbComplete(onbId);
      setState(st);
      toast.ok('You’re live 🎉');
    } catch (err) {
      handle(err);
    } finally {
      setBusy(false);
    }
  }

  const done = state?.done;

  const stepper = useMemo(
    () => (
      <>
        <div className="rz-steps" aria-hidden>
          {STEP_ORDER.map((s, i) => (
            <div
              key={s}
              className={`rz-step ${i < stepIdx ? 'done' : ''} ${i === stepIdx ? 'active' : ''}`}
            >
              <span className="n">{i < stepIdx ? '✓' : i + 1}</span>
              <span>{STEP_LABEL[s]}</span>
              {i < STEP_ORDER.length - 1 && <span className="bar" />}
            </div>
          ))}
        </div>
        <div className="rz-progress" role="progressbar" aria-valuenow={progressPct} aria-valuemin={0} aria-valuemax={100}>
          <i style={{ width: `${progressPct}%` }} />
        </div>
      </>
    ),
    [stepIdx, progressPct]
  );

  return (
    <>
      <PageHeader
        kicker="Onboarding"
        title="Let’s get your restaurant live"
        subtitle="Six calm steps. Your progress is saved as you go — you can leave and return."
      />

      <div className="rz-card pad" style={{ maxWidth: 640, margin: '0 auto' }}>
        {stepper}

        {error && (
          <div className="sm rz-in" style={{ color: 'var(--red)', marginBottom: 14 }}>
            {error}
          </div>
        )}

        {step === 'account' && (
          <form onSubmit={submitAccount} className="rz-in">
            <h3 style={{ fontSize: 17, marginBottom: 12 }}>Your business</h3>
            <Field label="Business / owner name" htmlFor="ownerName">
              <input id="ownerName" className="rz-input" value={ownerName} onChange={(e) => setOwnerName(e.target.value)} placeholder="Spice Route Hospitality" autoFocus />
            </Field>
            <div className="rz-grid cols-2">
              <Field label="Contact email" htmlFor="cemail">
                <input id="cemail" className="rz-input" type="email" value={contactEmail} onChange={(e) => setContactEmail(e.target.value)} placeholder="owner@spiceroute.com" />
              </Field>
              <Field label="Contact phone" htmlFor="cphone">
                <input id="cphone" className="rz-input" type="tel" value={contactPhone} onChange={(e) => setContactPhone(e.target.value)} placeholder="+91 98765 43210" />
              </Field>
            </div>
            <Field label="Country" htmlFor="country">
              <select id="country" className="rz-select" value={country} onChange={(e) => setCountry(e.target.value)}>
                <option value="IN">India</option>
                <option value="AE">United Arab Emirates</option>
                <option value="GB">United Kingdom</option>
                <option value="US">United States</option>
                <option value="SG">Singapore</option>
              </select>
            </Field>
            <button className="rz-cta block" type="submit" disabled={busy}>
              {busy ? 'Starting…' : 'Continue'}
            </button>
          </form>
        )}

        {step === 'plan' && (
          <div className="rz-in">
            <h3 style={{ fontSize: 17, marginBottom: 12 }}>Choose a plan</h3>
            <div className="rz-grid" style={{ gap: 10, marginBottom: 18 }}>
              {PLANS.map((p) => (
                <button
                  key={p.id}
                  type="button"
                  className="rz-card rz-tap pad"
                  onClick={() => setPlanId(p.id)}
                  style={{
                    textAlign: 'left',
                    padding: 16,
                    borderColor: planId === p.id ? 'var(--g)' : 'var(--border)',
                    background: planId === p.id ? 'var(--gs)' : 'var(--surface)',
                  }}
                >
                  <div className="rz-row between">
                    <strong>{p.name}</strong>
                    {planId === p.id && <span className="rz-tag live">Selected</span>}
                  </div>
                  <div className="sm muted" style={{ marginTop: 4 }}>{p.blurb}</div>
                </button>
              ))}
            </div>
            <div className="rz-row" style={{ gap: 10 }}>
              <button className="rz-ghost" onClick={() => go(-1)} disabled={busy}>Back</button>
              <button className="rz-cta" style={{ flex: 1 }} onClick={submitPlan} disabled={busy}>
                {busy ? 'Applying…' : 'Continue with ' + PLANS.find((p) => p.id === planId)?.name}
              </button>
            </div>
          </div>
        )}

        {step === 'brand' && (
          <form onSubmit={submitBrand} className="rz-in">
            <h3 style={{ fontSize: 17, marginBottom: 12 }}>Your first brand</h3>
            <Field label="Brand name" htmlFor="brandName">
              <input id="brandName" className="rz-input" value={brandName} onChange={(e) => setBrandName(e.target.value)} placeholder="Spice Route" autoFocus />
            </Field>
            <Field label="Logo" hint="PNG or SVG, square works best. Sent base64 to submit-brand.">
              <div className="rz-row" style={{ gap: 14 }}>
                <div
                  aria-hidden
                  style={{
                    width: 64, height: 64, borderRadius: 14, border: '1px solid var(--border2)',
                    background: logoPreview ? `center/cover no-repeat url(${logoPreview})` : accent,
                    display: 'grid', placeItems: 'center', color: '#fff', fontWeight: 700, flexShrink: 0,
                  }}
                >
                  {!logoPreview && (brandName.trim()[0]?.toUpperCase() || 'R')}
                </div>
                <label className="rz-ghost" style={{ cursor: 'pointer', display: 'inline-flex', alignItems: 'center' }}>
                  {logoFile ? 'Replace logo' : 'Upload logo'}
                  <input
                    type="file"
                    accept="image/png,image/jpeg,image/svg+xml,image/webp"
                    style={{ display: 'none' }}
                    onChange={(e) => onPickLogo(e.target.files?.[0] || null)}
                  />
                </label>
              </div>
            </Field>
            <Field label="Accent colour" hint="One brass-family accent keeps the guest surface calm.">
              <div className="rz-row" style={{ gap: 12 }}>
                <input type="color" value={accent} onChange={(e) => setAccent(e.target.value)} style={{ width: 46, height: 40, borderRadius: 10, border: '1px solid var(--border2)', background: 'none' }} />
                <input className="rz-input rz-num" value={accent} onChange={(e) => setAccent(e.target.value)} style={{ maxWidth: 140 }} />
              </div>
            </Field>
            <div className="rz-row" style={{ gap: 10 }}>
              <button type="button" className="rz-ghost" onClick={() => go(-1)} disabled={busy}>Back</button>
              <button className="rz-cta" style={{ flex: 1 }} type="submit" disabled={busy}>
                {busy ? 'Saving brand…' : 'Continue'}
              </button>
            </div>
          </form>
        )}

        {step === 'outlet' && (
          <form onSubmit={submitOutlet} className="rz-in">
            <h3 style={{ fontSize: 17, marginBottom: 12 }}>Your first outlet</h3>
            <Field label="Outlet name" htmlFor="outletName">
              <input id="outletName" className="rz-input" value={outletName} onChange={(e) => setOutletName(e.target.value)} placeholder="Spice Route — Bandra" autoFocus />
            </Field>
            <Field label="Address" htmlFor="addr2">
              <textarea id="addr2" className="rz-textarea" value={address} onChange={(e) => setAddress(e.target.value)} placeholder="Street, area, city, pincode" />
            </Field>
            <div className="rz-grid cols-2">
              <Field label="Timezone" htmlFor="tz">
                <select id="tz" className="rz-select" value={timezone} onChange={(e) => setTimezone(e.target.value)}>
                  <option value="Asia/Kolkata">Asia/Kolkata</option>
                  <option value="Asia/Dubai">Asia/Dubai</option>
                  <option value="Europe/London">Europe/London</option>
                  <option value="America/New_York">America/New_York</option>
                  <option value="Asia/Singapore">Asia/Singapore</option>
                </select>
              </Field>
              <Field label="GSTIN" hint="Tax registration (optional)." htmlFor="gstin">
                <input id="gstin" className="rz-input rz-num" value={gstin} onChange={(e) => setGstin(e.target.value)} placeholder="27ABCDE1234F1Z5" />
              </Field>
            </div>
            <div className="rz-row" style={{ gap: 10 }}>
              <button type="button" className="rz-ghost" onClick={() => go(-1)} disabled={busy}>Back</button>
              <button className="rz-cta" style={{ flex: 1 }} type="submit" disabled={busy}>
                {busy ? 'Saving outlet…' : 'Continue'}
              </button>
            </div>
          </form>
        )}

        {step === 'team' && (
          <form onSubmit={submitTeamThenGolive} className="rz-in">
            <h3 style={{ fontSize: 17, marginBottom: 4 }}>Invite your team</h3>
            <p className="sm muted" style={{ marginBottom: 14 }}>Optional — you can add staff later from the Team screen.</p>
            {invites.map((iv, idx) => (
              <div key={idx} className="rz-card pad" style={{ marginBottom: 10, background: 'var(--s1)' }}>
                <div className="rz-grid cols-2" style={{ marginBottom: 0 }}>
                  <Field label="Name">
                    <input className="rz-input" value={iv.name} onChange={(e) => setInvites((a) => a.map((x, i) => (i === idx ? { ...x, name: e.target.value } : x)))} placeholder="Asha" />
                  </Field>
                  <Field label="Role">
                    <select className="rz-select" value={iv.role} onChange={(e) => setInvites((a) => a.map((x, i) => (i === idx ? { ...x, role: e.target.value as RoleSlug } : x)))}>
                      <option value="manager">Manager</option>
                      <option value="waiter">Waiter</option>
                      <option value="kitchen">Kitchen</option>
                      <option value="cashier">Cashier</option>
                    </select>
                  </Field>
                </div>
                <div className="rz-grid cols-2">
                  <Field label="Email">
                    <input className="rz-input" type="email" value={iv.email} onChange={(e) => setInvites((a) => a.map((x, i) => (i === idx ? { ...x, email: e.target.value } : x)))} placeholder="asha@spiceroute.com" />
                  </Field>
                  <Field label="Phone">
                    <input className="rz-input" type="tel" value={iv.phone} onChange={(e) => setInvites((a) => a.map((x, i) => (i === idx ? { ...x, phone: e.target.value } : x)))} placeholder="+91…" />
                  </Field>
                </div>
                {invites.length > 1 && (
                  <button type="button" className="rz-ghost rz-danger" style={{ minHeight: 34, padding: '4px 10px' }} onClick={() => setInvites((a) => a.filter((_, i) => i !== idx))}>
                    Remove
                  </button>
                )}
              </div>
            ))}
            <button type="button" className="rz-ghost block" style={{ marginBottom: 16 }} onClick={() => setInvites((a) => [...a, { name: '', email: '', phone: '', role: 'waiter' }])}>
              + Add another teammate
            </button>
            <div className="rz-row" style={{ gap: 10 }}>
              <button type="button" className="rz-ghost" onClick={() => go(-1)} disabled={busy}>Back</button>
              <button className="rz-cta" style={{ flex: 1 }} type="submit" disabled={busy}>
                {busy ? 'Inviting…' : 'Review & go live'}
              </button>
            </div>
          </form>
        )}

        {step === 'golive' && (
          <div className="rz-in">
            {!done ? (
              <>
                <h3 style={{ fontSize: 17, marginBottom: 6 }}>Ready to go live</h3>
                <p className="sm muted" style={{ marginBottom: 16 }}>
                  We’ll generate your QR codes and switch your outlet on. This is the last step.
                </p>
                <ul className="rz-stack" style={{ gap: 8, marginBottom: 20, listStyle: 'none', padding: 0 }}>
                  <li className="rz-row" style={{ gap: 8 }}><span style={{ color: 'var(--green)' }}>✓</span> Brand <strong>{brandName || '—'}</strong></li>
                  <li className="rz-row" style={{ gap: 8 }}><span style={{ color: 'var(--green)' }}>✓</span> Outlet <strong>{outletName || '—'}</strong></li>
                  <li className="rz-row" style={{ gap: 8 }}><span style={{ color: 'var(--green)' }}>✓</span> Plan <strong>{PLANS.find((p) => p.id === planId)?.name}</strong></li>
                </ul>
                <div className="rz-row" style={{ gap: 10 }}>
                  <button type="button" className="rz-ghost" onClick={() => go(-1)} disabled={busy}>Back</button>
                  <button className="rz-cta" style={{ flex: 1 }} onClick={complete} disabled={busy}>
                    {busy ? 'Going live…' : '🚀 Go live'}
                  </button>
                </div>
              </>
            ) : (
              <div className="rz-celebrate rz-in">
                <div className="ring"><span>✓</span></div>
                <h2 style={{ fontSize: 22 }}>You’re live!</h2>
                <p className="sm muted" style={{ marginTop: 8, maxWidth: 380, marginInline: 'auto' }}>
                  <strong>{brandName}</strong> at <strong>{outletName}</strong> is switched on and ready to take orders.
                </p>
                <div className="rz-row" style={{ gap: 10, justifyContent: 'center', marginTop: 22 }}>
                  <button className="rz-cta" onClick={() => nav('/dashboard')}>Open dashboard</button>
                  <button className="rz-ghost" onClick={() => nav('/team')}>Add more team</button>
                </div>
              </div>
            )}
          </div>
        )}
      </div>
    </>
  );
}
