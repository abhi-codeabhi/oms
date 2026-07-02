// settingsDefs.ts — client-side setting DEFINITION metadata.
//
// The settings BFF only exposes GetEffective / SetOverride (no ListDefinitions RPC
// on the gateway yet — see settings.proto `Definition` and the gateway README
// "contract gap" note). To render a grouped, typed, validated editor we carry the
// definition catalogue here, matching the proto examples. When the BFF gains a
// ListDefinitions passthrough, swap this for a fetch — the shape already matches
// proto `Definition`.

import type { ValueType } from './types';

export interface SettingDef {
  key: string; // dotted, first segment = namespace
  title: string;
  description: string;
  type: ValueType;
  default: string;
  enum_options?: string[];
  validation?: string; // e.g. "min:0,max:100"
  editable_by: 'platform_admin' | 'owner' | 'manager';
}

export const NAMESPACE_LABELS: Record<string, string> = {
  billing: 'Billing & tax',
  ordering: 'Ordering',
  floor: 'Floor & service',
  brand: 'Brand theme',
};

// Mirrors the examples in settings.proto's service doc comment.
export const SETTING_DEFS: SettingDef[] = [
  {
    key: 'billing.gst_pct',
    title: 'GST %',
    description: 'Goods & services tax applied to the bill subtotal.',
    type: 'DECIMAL',
    default: '5',
    validation: 'min:0,max:100',
    editable_by: 'owner',
  },
  {
    key: 'billing.service_charge_pct',
    title: 'Service charge %',
    description: 'Optional service charge added before tax. 0 to disable.',
    type: 'DECIMAL',
    default: '0',
    validation: 'min:0,max:100',
    editable_by: 'owner',
  },
  {
    key: 'billing.currency',
    title: 'Currency',
    description: 'ISO currency the outlet bills in.',
    type: 'ENUM',
    default: 'INR',
    enum_options: ['INR', 'USD', 'AED', 'GBP', 'EUR', 'SGD'],
    editable_by: 'owner',
  },
  {
    key: 'billing.rounding',
    title: 'Bill rounding',
    description: 'How the grand total is rounded on the printed bill.',
    type: 'ENUM',
    default: 'nearest_1',
    enum_options: ['none', 'nearest_1', 'nearest_5', 'round_up'],
    editable_by: 'owner',
  },
  {
    key: 'ordering.require_prepay',
    title: 'Require prepayment',
    description: 'Guests must pay before the kitchen accepts the order.',
    type: 'BOOL',
    default: 'false',
    editable_by: 'owner',
  },
  {
    key: 'ordering.modes',
    title: 'Ordering modes',
    description: 'Enabled service modes (JSON array), e.g. ["dine_in","takeaway"].',
    type: 'JSON',
    default: '["dine_in"]',
    editable_by: 'owner',
  },
  {
    key: 'floor.nudge.greet_secs',
    title: 'Greet nudge (secs)',
    description: 'Seconds a seated table waits before a greet nudge fires to the floor.',
    type: 'INT',
    default: '30',
    validation: 'min:0,max:600',
    editable_by: 'manager',
  },
  {
    key: 'floor.call.cooldown_secs',
    title: 'Call cooldown (secs)',
    description: 'Rate limit between guest "call waiter" presses.',
    type: 'INT',
    default: '60',
    validation: 'min:0,max:900',
    editable_by: 'manager',
  },
  {
    key: 'brand.theme.accent',
    title: 'Accent colour',
    description: 'Brand accent used across guest surfaces.',
    type: 'STRING',
    default: '#9E7C46',
    editable_by: 'owner',
  },
];

export function namespaceOf(key: string): string {
  return key.split('.')[0];
}

export function groupByNamespace(defs: SettingDef[]): Record<string, SettingDef[]> {
  const out: Record<string, SettingDef[]> = {};
  for (const d of defs) {
    const ns = namespaceOf(d.key);
    (out[ns] ||= []).push(d);
  }
  return out;
}
