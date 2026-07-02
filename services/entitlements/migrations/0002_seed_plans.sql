-- +goose Up
-- +goose StatementBegin

-- Default plan catalog. Quotas: -1 = unlimited. Features default to absent=false.
-- Keys mirror what constrained services check: outlets, staff.<role>, tables,
-- brands, connectors; feature flags: multi_brand, aggregators, analytics_pro, crm.

INSERT INTO plans (id, name, quotas, features) VALUES
('free', 'Free',
 '{
    "outlets": 1,
    "staff.manager": 1,
    "staff.waiter": 3,
    "staff.kitchen": 2,
    "staff.cashier": 1,
    "tables": 10,
    "brands": 1,
    "connectors": 0
  }'::jsonb,
 '{
    "multi_brand": false,
    "aggregators": false,
    "analytics_pro": false,
    "crm": false
  }'::jsonb),

('growth', 'Growth',
 '{
    "outlets": 3,
    "staff.manager": 3,
    "staff.waiter": 15,
    "staff.kitchen": 8,
    "staff.cashier": 3,
    "tables": 40,
    "brands": 1,
    "connectors": 2
  }'::jsonb,
 '{
    "multi_brand": false,
    "aggregators": true,
    "analytics_pro": false,
    "crm": false
  }'::jsonb),

('pro', 'Pro',
 '{
    "outlets": 10,
    "staff.manager": 10,
    "staff.waiter": 50,
    "staff.kitchen": 30,
    "staff.cashier": 10,
    "tables": 200,
    "brands": 3,
    "connectors": 10
  }'::jsonb,
 '{
    "multi_brand": true,
    "aggregators": true,
    "analytics_pro": true,
    "crm": false
  }'::jsonb),

('enterprise', 'Enterprise',
 '{
    "outlets": -1,
    "staff.manager": -1,
    "staff.waiter": -1,
    "staff.kitchen": -1,
    "staff.cashier": -1,
    "tables": -1,
    "brands": -1,
    "connectors": -1
  }'::jsonb,
 '{
    "multi_brand": true,
    "aggregators": true,
    "analytics_pro": true,
    "crm": true
  }'::jsonb)
ON CONFLICT (id) DO NOTHING;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM plans WHERE id IN ('free', 'growth', 'pro', 'enterprise');
-- +goose StatementEnd
