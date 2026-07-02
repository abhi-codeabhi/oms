-- +goose Up
-- +goose StatementBegin

-- Starter definition set. In production services self-register their own
-- definitions on boot via RegisterDefinitions; seeding here gives a working
-- catalog out of the box and is idempotent (ON CONFLICT DO NOTHING).
--
-- type:  1=INT 2=BOOL 3=STRING 4=DECIMAL 5=JSON 6=ENUM
-- scope: 1=OWNER 2=BRAND 3=RESTAURANT

INSERT INTO definitions
    (key, title, description, type, default_type, default_raw, max_scope,
     enum_options, validation, editable_by, feature_gated)
VALUES
-- billing -------------------------------------------------------------------
('billing.gst_pct',            'GST %',              'GST percentage applied to bills.',
 1, 1, '5',   3, '{}',                'min:0,max:28',  'owner',          FALSE),
('billing.service_charge_pct', 'Service charge %',   'Service charge percentage added to bills.',
 1, 1, '0',   3, '{}',                'min:0,max:100', 'owner',          FALSE),
('billing.rounding',           'Bill rounding',      'How the final bill total is rounded.',
 6, 6, 'nearest_1', 3, '{nearest_1,none}', '',         'owner',          FALSE),
('billing.currency',           'Currency',           'ISO currency code for prices and bills.',
 3, 3, 'INR', 1, '{}',                'min:3,max:3',   'platform_admin', FALSE),
-- ordering ------------------------------------------------------------------
('ordering.require_prepay',    'Require prepayment', 'Require customers to pay before the order is sent to kitchen.',
 2, 2, 'false', 3, '{}',              '',              'owner',          FALSE),
-- floor ---------------------------------------------------------------------
('floor.nudge.greet_secs',     'Greet nudge (secs)', 'Seconds after seating before the waiter is nudged to greet.',
 1, 1, '30',  3, '{}',                'min:0,max:600', 'manager',        FALSE),
('floor.nudge.checkin_secs',   'Check-in nudge (secs)', 'Seconds after serving before the waiter is nudged to check in.',
 1, 1, '300', 3, '{}',                'min:0,max:3600','manager',        FALSE),
('floor.call.cooldown_secs',   'Call cooldown (secs)', 'Minimum seconds between repeated customer call-waiter requests.',
 1, 1, '60',  3, '{}',                'min:0,max:600', 'manager',        FALSE),
('floor.call.escalate_secs',   'Call escalate (secs)', 'Seconds an unattended customer call waits before it escalates to a manager (read by the servicerequests service).',
 1, 1, '30',  3, '{}',                'min:0,max:600', 'manager',        FALSE),
-- brand ---------------------------------------------------------------------
('brand.theme.accent',         'Accent colour',      'Brand accent colour (hex) used across surfaces.',
 3, 3, '#9E7C46', 2, '{}',            'min:4,max:9',   'owner',          FALSE)
ON CONFLICT (key) DO NOTHING;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM definitions WHERE key IN (
    'billing.gst_pct', 'billing.service_charge_pct', 'billing.rounding',
    'billing.currency', 'ordering.require_prepay', 'floor.nudge.greet_secs',
    'floor.nudge.checkin_secs', 'floor.call.cooldown_secs',
    'floor.call.escalate_secs', 'brand.theme.accent'
);
-- +goose StatementEnd
