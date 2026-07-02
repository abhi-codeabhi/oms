import { useState } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { useSession } from '../auth/SessionContext';
import { startOtp, verifyOtp } from '../lib/endpoints';
import { ApiError } from '../lib/api';
import { Field } from '../components/ui';
import { useToast } from '../components/Toast';

type Channel = 'email' | 'phone';
type Realm = 'platform' | 'tenant';

// OTP login. Two calm steps (Hick: one decision at a time). The single brass
// primary action per step. Dev hint surfaced so operators don't fish for the code.
export default function Login() {
  const { login } = useSession();
  const toast = useToast();
  const nav = useNavigate();
  const loc = useLocation();
  const from = (loc.state as { from?: string } | null)?.from || '/dashboard';

  const [step, setStep] = useState<'request' | 'verify'>('request');
  const [channel, setChannel] = useState<Channel>('email');
  const [realm, setRealm] = useState<Realm>('tenant');
  const [address, setAddress] = useState('');
  const [challengeId, setChallengeId] = useState('');
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const isDev = import.meta.env.DEV;

  async function onRequest(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    if (!address.trim()) return setError('Enter your ' + channel + '.');
    setBusy(true);
    try {
      const { challenge_id } = await startOtp(channel, address.trim(), realm);
      setChallengeId(challenge_id);
      setStep('verify');
      toast.info('Code sent — check your ' + channel);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not start sign-in.');
    } finally {
      setBusy(false);
    }
  }

  async function onVerify(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    if (code.trim().length < 4) return setError('Enter the code from your ' + channel + '.');
    setBusy(true);
    try {
      const { tokens } = await verifyOtp(challengeId, code.trim());
      await login(tokens);
      toast.ok('Signed in');
      nav(from, { replace: true });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'That code did not match.');
    } finally {
      setBusy(false);
    }
  }

  return (
    <div style={{ minHeight: '100vh', display: 'grid', placeItems: 'center', padding: 20 }}>
      <div className="rz-card pad rz-in" style={{ width: '100%', maxWidth: 420 }}>
        <div className="rz-brand" style={{ fontSize: 22, marginBottom: 6 }}>
          <span className="dot">R</span> Restorna
        </div>
        <div className="kicker">Control plane · Admin console</div>
        <h1 style={{ fontSize: 22, margin: '10px 0 4px' }}>
          {step === 'request' ? 'Sign in' : 'Enter your code'}
        </h1>
        <p className="sm muted" style={{ marginBottom: 20 }}>
          {step === 'request'
            ? 'We’ll send a one-time code to verify it’s you.'
            : `Sent to ${address}. It expires shortly.`}
        </p>

        {step === 'request' ? (
          <form onSubmit={onRequest}>
            <Field label="Sign in with">
              <div className="rz-seg" role="tablist" aria-label="Channel">
                <button type="button" className={channel === 'email' ? 'on' : ''} onClick={() => setChannel('email')}>
                  Email
                </button>
                <button type="button" className={channel === 'phone' ? 'on' : ''} onClick={() => setChannel('phone')}>
                  Phone
                </button>
              </div>
            </Field>

            <Field label={channel === 'email' ? 'Email address' : 'Phone number'} htmlFor="addr">
              <input
                id="addr"
                className="rz-input"
                type={channel === 'email' ? 'email' : 'tel'}
                inputMode={channel === 'email' ? 'email' : 'tel'}
                autoComplete={channel === 'email' ? 'email' : 'tel'}
                placeholder={channel === 'email' ? 'you@restaurant.com' : '+91 98765 43210'}
                value={address}
                onChange={(e) => setAddress(e.target.value)}
                autoFocus
              />
            </Field>

            <Field label="Realm" hint="Platform admins sign into the platform realm; owners & staff use tenant.">
              <div className="rz-seg" aria-label="Realm">
                <button type="button" className={realm === 'tenant' ? 'on' : ''} onClick={() => setRealm('tenant')}>
                  Tenant
                </button>
                <button type="button" className={realm === 'platform' ? 'on' : ''} onClick={() => setRealm('platform')}>
                  Platform
                </button>
              </div>
            </Field>

            {error && <div className="sm" style={{ color: 'var(--red)', marginBottom: 12 }}>{error}</div>}

            <button className="rz-cta block" type="submit" disabled={busy}>
              {busy ? 'Sending…' : 'Send code'}
            </button>
          </form>
        ) : (
          <form onSubmit={onVerify}>
            <Field label="6-digit code" htmlFor="code" hint={isDev ? 'Dev tip: with identity APP_ENV=dev the code is 123456.' : undefined}>
              <input
                id="code"
                className="rz-input rz-num"
                inputMode="numeric"
                autoComplete="one-time-code"
                pattern="[0-9]*"
                maxLength={8}
                placeholder="123456"
                value={code}
                onChange={(e) => setCode(e.target.value.replace(/[^0-9]/g, ''))}
                autoFocus
                style={{ letterSpacing: 4, fontSize: 18 }}
              />
            </Field>

            {error && <div className="sm" style={{ color: 'var(--red)', marginBottom: 12 }}>{error}</div>}

            <button className="rz-cta block" type="submit" disabled={busy}>
              {busy ? 'Verifying…' : 'Verify & continue'}
            </button>
            <button
              type="button"
              className="rz-ghost block"
              style={{ marginTop: 10 }}
              onClick={() => {
                setStep('request');
                setCode('');
                setError(null);
              }}
              disabled={busy}
            >
              Use a different {channel}
            </button>
          </form>
        )}
      </div>
    </div>
  );
}
