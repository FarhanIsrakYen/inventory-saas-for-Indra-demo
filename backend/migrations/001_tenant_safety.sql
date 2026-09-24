-- Reference migration: GORM AutoMigrate is used for the initial executable bootstrap.
-- All tenant-owned tables use a non-null tenant_id and tenant-scoped indexes.
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
-- Production deployments should run versioned SQL migrations generated from these models,
-- and apply PostgreSQL RLS policies as a second isolation layer where operationally suitable.
