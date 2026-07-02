import type { Role, RoleSlug } from './types';

export const ROLE_LABELS: Record<Role, string> = {
  ROLE_PLATFORM_ADMIN: 'Platform admin',
  ROLE_OWNER: 'Owner',
  ROLE_BRAND_ADMIN: 'Brand admin',
  ROLE_MANAGER: 'Manager',
  ROLE_WAITER: 'Waiter',
  ROLE_KITCHEN: 'Kitchen',
  ROLE_CASHIER: 'Cashier',
  ROLE_CUSTOMER: 'Customer',
  ROLE_UNSPECIFIED: 'Unknown',
};

/** Roles a manager/owner can assign to staff (staff-level roles only). */
export const ASSIGNABLE_STAFF_ROLES: { slug: RoleSlug; label: string }[] = [
  { slug: 'manager', label: 'Manager' },
  { slug: 'waiter', label: 'Waiter' },
  { slug: 'kitchen', label: 'Kitchen' },
  { slug: 'cashier', label: 'Cashier' },
];

export function roleToSlug(role: Role): RoleSlug | undefined {
  const map: Partial<Record<Role, RoleSlug>> = {
    ROLE_PLATFORM_ADMIN: 'platform_admin',
    ROLE_OWNER: 'owner',
    ROLE_BRAND_ADMIN: 'brand_admin',
    ROLE_MANAGER: 'manager',
    ROLE_WAITER: 'waiter',
    ROLE_KITCHEN: 'kitchen',
    ROLE_CASHIER: 'cashier',
    ROLE_CUSTOMER: 'customer',
  };
  return map[role];
}

export function isPlatformAdmin(role?: Role) {
  return role === 'ROLE_PLATFORM_ADMIN';
}
export function isOwner(role?: Role) {
  return role === 'ROLE_OWNER' || role === 'ROLE_BRAND_ADMIN';
}
export function isManagerOrOwner(role?: Role) {
  return role === 'ROLE_MANAGER' || isOwner(role) || role === 'ROLE_PLATFORM_ADMIN';
}
