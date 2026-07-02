import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from 'react';

type ToastKind = 'ok' | 'err' | 'info';
interface ToastState {
  msg: string;
  kind: ToastKind;
  show: boolean;
}

interface ToastApi {
  ok: (msg: string) => void;
  err: (msg: string) => void;
  info: (msg: string) => void;
}

const Ctx = createContext<ToastApi | null>(null);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<ToastState>({ msg: '', kind: 'info', show: false });
  const timer = useRef<number | undefined>(undefined);

  const push = useCallback((msg: string, kind: ToastKind) => {
    window.clearTimeout(timer.current);
    setState({ msg, kind, show: true });
    timer.current = window.setTimeout(() => setState((s) => ({ ...s, show: false })), 2600);
  }, []);

  const api: ToastApi = {
    ok: (m) => push(m, 'ok'),
    err: (m) => push(m, 'err'),
    info: (m) => push(m, 'info'),
  };

  return (
    <Ctx.Provider value={api}>
      {children}
      <div
        role="status"
        aria-live="polite"
        className={`rz-toast ${state.show ? 'show' : ''} ${state.kind === 'ok' ? 'ok' : state.kind === 'err' ? 'err' : ''}`}
      >
        {state.msg}
      </div>
    </Ctx.Provider>
  );
}

export function useToast(): ToastApi {
  const v = useContext(Ctx);
  if (!v) throw new Error('useToast must be used within <ToastProvider>');
  return v;
}
