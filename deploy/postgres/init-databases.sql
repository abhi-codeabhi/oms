-- Restorna Platform — create one database per service (DB-per-service).
--
-- WHY: every service runs its own goose migrations and several define tables with
-- the SAME name (outbox, processed_events) plus goose's own goose_db_version
-- table. Sharing one database would collide. Each service gets an isolated DB on
-- the same Postgres server; point that service's DATABASE_URL at its own db name.
--
-- Run ONCE against a fresh Postgres server as the owner role, e.g.:
--   psql "$ADMIN_DATABASE_URL" -f deploy/postgres/init-databases.sql
-- (On Render: use the External Connection string of restorna-postgres.)

CREATE DATABASE restorna_identity;
CREATE DATABASE restorna_tenant;
CREATE DATABASE restorna_entitlements;
CREATE DATABASE restorna_staff;
CREATE DATABASE restorna_settings;
CREATE DATABASE restorna_onboarding;
CREATE DATABASE restorna_catalog;
CREATE DATABASE restorna_ordering;
CREATE DATABASE restorna_kitchen;
CREATE DATABASE restorna_floor;
CREATE DATABASE restorna_billing;
CREATE DATABASE restorna_promotions;
CREATE DATABASE restorna_servicerequests;
CREATE DATABASE restorna_connectorhub;
CREATE DATABASE restorna_payments;
CREATE DATABASE restorna_notifications;
CREATE DATABASE restorna_aggregators;
-- gateway has no database.

-- NOTE on RLS: services set app.tenant_id per transaction and rely on RLS. Make
-- sure the app connects as a NON-superuser, NON-BYPASSRLS role, otherwise RLS is
-- skipped. If you use the default owner role, create a scoped app role instead:
--   CREATE ROLE restorna_app LOGIN PASSWORD '...';
--   GRANT ALL ON DATABASE restorna_<svc> TO restorna_app;  (per db)
-- and point DATABASE_URL at restorna_app. (Platform-admin lookups that need to
-- read across tenants, e.g. tenant.ListOwners, require a BYPASSRLS role or an
-- explicit admin policy — see BUILD_NOTES.md.)
