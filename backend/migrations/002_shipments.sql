-- Courier-neutral shipment records. GORM AutoMigrate also creates this table
-- during application bootstrap for local development.
CREATE TABLE IF NOT EXISTS shipments (
    id uuid PRIMARY KEY,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    order_id uuid NOT NULL REFERENCES orders(id),
    provider varchar(32) NOT NULL,
    provider_order_id varchar(160),
    tracking_number varchar(160),
    status varchar(32) NOT NULL,
    delivery_fee bigint NOT NULL DEFAULT 0,
    request_payload text,
    response_payload text,
    CONSTRAINT uq_shipments_tenant_order_provider UNIQUE (tenant_id, order_id, provider)
);
CREATE INDEX IF NOT EXISTS idx_shipments_tenant_id ON shipments(tenant_id);
CREATE INDEX IF NOT EXISTS idx_shipments_order_id ON shipments(order_id);
CREATE INDEX IF NOT EXISTS idx_shipments_provider_order_id ON shipments(provider_order_id);
