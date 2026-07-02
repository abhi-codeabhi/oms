// types.ts — JSON shapes exactly as the gateway BFFs project them.
// Sourced from services/gateway/internal/bff/*.go (the *JSON helpers).

export type Role =
  | 'ROLE_PLATFORM_ADMIN'
  | 'ROLE_OWNER'
  | 'ROLE_BRAND_ADMIN'
  | 'ROLE_MANAGER'
  | 'ROLE_WAITER'
  | 'ROLE_KITCHEN'
  | 'ROLE_CASHIER'
  | 'ROLE_CUSTOMER'
  | 'ROLE_UNSPECIFIED';

/** snake-case role strings the BFFs accept in request bodies (parseRole). */
export type RoleSlug =
  | 'platform_admin'
  | 'owner'
  | 'brand_admin'
  | 'manager'
  | 'waiter'
  | 'kitchen'
  | 'cashier'
  | 'customer';

export interface TokenPair {
  access_token: string;
  refresh_token: string;
  expires_in: number;
}

export interface AuthUser {
  id: string;
  email: string;
  phone: string;
  display_name: string;
  realm: string;
  active: boolean;
}

export interface Scope {
  owner_id: string;
  brand_id: string;
  restaurant_id: string;
}

export interface Me {
  active: boolean;
  user_id: string;
  role: Role;
  scope?: Scope;
}

// --- onboarding ---
export type OnboardingStep =
  | 'STEP_UNSPECIFIED'
  | 'STEP_ACCOUNT'
  | 'STEP_PLAN'
  | 'STEP_BRAND'
  | 'STEP_OUTLET'
  | 'STEP_SETTINGS'
  | 'STEP_TEAM'
  | 'STEP_MENU'
  | 'STEP_GOLIVE';

export interface OnboardingState {
  id: string;
  owner_id: string;
  current: OnboardingStep;
  completed: OnboardingStep[];
  done: boolean;
}

// --- tenant ---
export interface Asset {
  id: string;
  url: string;
  content_type: string;
}
export interface Brand {
  id: string;
  owner_id: string;
  name: string;
  logo?: Asset | null;
  primary_color: string;
  created_at: string;
}
export interface Outlet {
  id: string;
  brand_id: string;
  owner_id: string;
  name: string;
  address: string;
  timezone: string;
  gstin: string;
  logo?: Asset | null;
  active: boolean;
  created_at: string;
}
export interface Owner {
  id: string;
  name: string;
  legal_name: string;
  country: string;
  created_at: string;
}

// --- entitlements ---
export interface Plan {
  id: string;
  name: string;
  quotas: Record<string, number>;
  features: Record<string, boolean>;
}
export interface Entitlement {
  owner_id: string;
  plan_id: string;
  quota_overrides: Record<string, number>;
  feature_overrides: Record<string, boolean>;
}
export interface EntitlementView {
  entitlement: Entitlement | null;
  effective_plan: Plan | null;
}

// --- staff ---
export interface StaffMember {
  id: string;
  owner_id: string;
  brand_id: string;
  restaurant_id: string;
  name: string;
  email: string;
  phone: string;
  role: Role;
  active: boolean;
  user_id: string;
}

// --- settings ---
export type ValueType =
  | 'VALUE_TYPE_UNSPECIFIED'
  | 'INT'
  | 'BOOL'
  | 'STRING'
  | 'DECIMAL'
  | 'JSON'
  | 'ENUM';
export interface SettingValue {
  key: string;
  source_scope: string;
  type?: ValueType;
  raw?: string;
}
