// endpoints.ts — one typed function per gateway BFF route. Keep these THIN; they
// mirror services/gateway/internal/bff/*.go 1:1 so the mapping is auditable.

import { api } from './api';
import type {
  AuthUser,
  Brand,
  EntitlementView,
  Me,
  OnboardingState,
  Outlet,
  Owner,
  Plan,
  RoleSlug,
  SettingValue,
  StaffMember,
  TokenPair,
} from './types';

// ---------- /api/auth/* (public except scoped-token) ----------
export const startOtp = (channel: 'email' | 'phone', address: string, realm: 'platform' | 'tenant') =>
  api.post<{ challenge_id: string }>('/api/auth/start-otp', { channel, address, realm }, { anonymous: true });

export const verifyOtp = (challenge_id: string, code: string) =>
  api.post<{ tokens: TokenPair; user: AuthUser }>('/api/auth/verify-otp', { challenge_id, code }, { anonymous: true });

export const refresh = (refresh_token: string) =>
  api.post<{ tokens: TokenPair }>('/api/auth/refresh', { refresh_token }, { anonymous: true });

// ---------- /api/me ----------
export const getMe = () => api.get<Me>('/api/me');

// ---------- /api/owner/onboarding/* ----------
export const onbStart = (b: {
  owner_name: string;
  contact_email: string;
  contact_phone: string;
  country: string;
  plan_id: string;
}) => api.post<{ state: OnboardingState }>('/api/owner/onboarding/start', b);

export const onbSubmitBrand = (b: {
  onboarding_id: string;
  brand_name: string;
  primary_color: string;
  logo_base64?: string;
  logo_content_type?: string;
}) => api.post<{ state: OnboardingState; brand_id: string }>('/api/owner/onboarding/submit-brand', b);

export const onbSubmitOutlet = (b: {
  onboarding_id: string;
  name: string;
  address: string;
  timezone: string;
  gstin: string;
}) => api.post<{ state: OnboardingState; restaurant_id: string }>('/api/owner/onboarding/submit-outlet', b);

export const onbInviteTeam = (
  onboarding_id: string,
  invites: { name: string; email: string; phone: string; role: RoleSlug }[]
) => api.post<{ state: OnboardingState }>('/api/owner/onboarding/invite-team', { onboarding_id, invites });

export const onbComplete = (onboarding_id: string) =>
  api.post<{ state: OnboardingState }>('/api/owner/onboarding/complete', { onboarding_id });

export const onbState = (onboarding_id: string) =>
  api.get<{ state: OnboardingState }>('/api/owner/onboarding/state', { onboarding_id });

// ---------- /api/owner/* reads ----------
export const ownerBrands = (owner_id?: string) =>
  api.get<{ brands: Brand[] }>('/api/owner/brands', { owner_id });

export const ownerOutlets = (brand_id: string) =>
  api.get<{ outlets: Outlet[] }>('/api/owner/outlets', { brand_id });

export const ownerEntitlement = (owner_id: string) =>
  api.get<EntitlementView>('/api/owner/entitlement', { owner_id });

export const ownerGetSettings = (namespace?: string, keys?: string[]) =>
  api.get<{ values: SettingValue[] }>('/api/owner/settings', {
    namespace,
    keys: keys && keys.length ? keys.join(',') : undefined,
  });

export const ownerSetSetting = (key: string, type: string, value: string) =>
  api.post<SettingValue>('/api/owner/settings', { key, type, value });

// ---------- /api/manager/* ----------
export const mgrListStaff = (restaurant_id: string) =>
  api.get<{ members: StaffMember[] }>('/api/manager/staff', { restaurant_id });

export const mgrAddStaff = (b: {
  restaurant_id: string;
  name: string;
  email: string;
  phone: string;
  role: RoleSlug;
}) => api.post<{ member: StaffMember }>('/api/manager/staff', b);

export const mgrDisableStaff = (staff_id: string, active = false) =>
  api.post<{ member: StaffMember }>('/api/manager/staff/disable', { staff_id, active });

export const mgrChangeRole = (staff_id: string, role: RoleSlug) =>
  api.post<{ member: StaffMember }>('/api/manager/staff/change-role', { staff_id, role });

export const mgrInviteStaff = (staff_id: string) =>
  api.post<{ invite_id: string }>('/api/manager/staff/invite', { staff_id });

export const mgrGetSettings = (namespace?: string, keys?: string[]) =>
  api.get<{ values: SettingValue[] }>('/api/manager/settings', {
    namespace,
    keys: keys && keys.length ? keys.join(',') : undefined,
  });

export const mgrSetSetting = (key: string, type: string, value: string) =>
  api.post<SettingValue>('/api/manager/settings', { key, type, value });

// ---------- /api/platform/* (platform_admin) ----------
export const platformGetOwner = (owner_id: string) =>
  api.get<{ owner: Owner | null }>('/api/platform/owner', { owner_id });

export const platformGetEntitlement = (owner_id: string) =>
  api.get<EntitlementView>('/api/platform/entitlement', { owner_id });

export const platformUpsertPlan = (plan: {
  id: string;
  name: string;
  quotas: Record<string, number>;
  features: Record<string, boolean>;
}) => api.post<{ plan: Plan }>('/api/platform/plan', plan);
