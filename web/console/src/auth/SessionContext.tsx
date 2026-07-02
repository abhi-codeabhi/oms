// SessionContext — holds the token pair + the introspected `me`, persists to
// localStorage, wires the api layer's token source, and exposes login/logout.
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { configureAuth } from '../lib/api';
import { getMe, refresh as refreshTokens } from '../lib/endpoints';
import type { Me, TokenPair } from '../lib/types';

const LS_KEY = 'restorna.console.session.v1';

interface StoredSession {
  tokens: TokenPair;
}

interface SessionState {
  ready: boolean; // finished the initial hydrate + introspect
  tokens: TokenPair | null;
  me: Me | null;
}

interface SessionContextValue extends SessionState {
  isAuthed: boolean;
  login: (tokens: TokenPair) => Promise<void>;
  logout: () => void;
  reloadMe: () => Promise<void>;
}

const Ctx = createContext<SessionContextValue | null>(null);

function load(): StoredSession | null {
  try {
    const raw = localStorage.getItem(LS_KEY);
    return raw ? (JSON.parse(raw) as StoredSession) : null;
  } catch {
    return null;
  }
}
function save(s: StoredSession | null) {
  try {
    if (s) localStorage.setItem(LS_KEY, JSON.stringify(s));
    else localStorage.removeItem(LS_KEY);
  } catch {
    /* ignore quota / private mode */
  }
}

export function SessionProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<SessionState>({ ready: false, tokens: null, me: null });
  const tokensRef = useRef<TokenPair | null>(null);

  // Give the api layer a live token source + a 401 handler (attempt one refresh).
  useEffect(() => {
    configureAuth(
      () => tokensRef.current?.access_token ?? null,
      () => {
        // On hard 401, drop the session; a full re-login is the safe path.
        tokensRef.current = null;
        save(null);
        setState((s) => ({ ...s, tokens: null, me: null }));
      }
    );
  }, []);

  const setTokens = useCallback((t: TokenPair | null) => {
    tokensRef.current = t;
  }, []);

  const reloadMe = useCallback(async () => {
    if (!tokensRef.current) return;
    const me = await getMe();
    setState((s) => ({ ...s, me }));
  }, []);

  const login = useCallback(
    async (tokens: TokenPair) => {
      setTokens(tokens);
      save({ tokens });
      setState((s) => ({ ...s, tokens }));
      const me = await getMe();
      setState((s) => ({ ...s, tokens, me, ready: true }));
    },
    [setTokens]
  );

  const logout = useCallback(() => {
    setTokens(null);
    save(null);
    setState({ ready: true, tokens: null, me: null });
  }, [setTokens]);

  // Initial hydrate: restore tokens, try introspect, fall back to refresh once.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      const stored = load();
      if (!stored) {
        if (!cancelled) setState({ ready: true, tokens: null, me: null });
        return;
      }
      setTokens(stored.tokens);
      try {
        const me = await getMe();
        if (!cancelled) setState({ ready: true, tokens: stored.tokens, me });
      } catch {
        // access token likely expired — try a refresh, then re-introspect.
        try {
          const { tokens } = await refreshTokens(stored.tokens.refresh_token);
          setTokens(tokens);
          save({ tokens });
          const me = await getMe();
          if (!cancelled) setState({ ready: true, tokens, me });
        } catch {
          setTokens(null);
          save(null);
          if (!cancelled) setState({ ready: true, tokens: null, me: null });
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [setTokens]);

  const value = useMemo<SessionContextValue>(
    () => ({
      ...state,
      isAuthed: !!state.tokens && !!state.me?.active,
      login,
      logout,
      reloadMe,
    }),
    [state, login, logout, reloadMe]
  );

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useSession(): SessionContextValue {
  const v = useContext(Ctx);
  if (!v) throw new Error('useSession must be used within <SessionProvider>');
  return v;
}
