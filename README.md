# StockPilot — Multi-tenant Order & Inventory SaaS

StockPilot is a Gin + React application for tenant-isolated inventory, variants, stock movements, delivery configuration, and orders. PostgreSQL is authoritative; Elasticsearch is provisioned as an optional search adapter.

Set `ELASTICSEARCH_ENABLED=true` to use Elasticsearch for product text searches. It defaults to `false`, which uses PostgreSQL. If Elasticsearch is enabled but unavailable, product search falls back to PostgreSQL and the API logs a warning. Product records are indexed on startup and kept synchronized when products are created, updated, or deleted.

## Run locally

```bash
copy .env.example .env
docker compose up --build
```

Open `http://localhost:5173`. Register an account to create a workspace, become its Owner, and automatically seed **Inside Dhaka (৳80)** and **Outside Dhaka (৳120)**.

## Development

```bash
# backend
cd backend && go mod tidy && go run ./cmd/api

# frontend (another shell)
cd frontend && npm.cmd install && npm.cmd run dev
```

The API lives at `http://localhost:8080/api`; its liveness endpoint is `/health`.

## Security model

- Passwords use bcrypt; short-lived access JWTs and rotating refresh tokens are issued on login.
- The active tenant comes only from the signed access token. The API validates active membership on every request.
- Product, variant, inventory, order, delivery, and audit reads/writes include `tenant_id` filtering. Unknown/cross-tenant resources return 404.
- Inventory adjustments and order creation acquire row locks, write immutable inventory transactions, and reject negative stock.
- Product prices, delivery fees, subtotals, and totals are recalculated by the API.
- RBAC is enforced server-side. Owners have full access; the role table is ready for Admin/Manager/Staff policies.

## API overview

`POST /api/auth/register`, `login`, `refresh`, `logout`; `GET /api/auth/me`; `GET/POST /api/products`; `POST /api/inventory/adjustments`; `GET/POST /api/orders`; `GET/POST/PUT/DELETE /api/delivery-options`; `GET /api/dashboard`.

All business APIs require `Authorization: Bearer <access-token>`. Pagination accepts `page` and `size`. Error responses use `{ success, code, message }`.

## Pathao Courier integration

Courier submission is provider-neutral:

```text
Gin handler -> OrderService -> CourierService -> CourierProvider -> Pathao API
```

Configure these variables in `.env` for sandbox or production credentials; placeholders are provided in `.env.example`:

```env
PATHAO_BASE_URL=https://courier-api-sandbox.pathao.com
PATHAO_CLIENT_ID=
PATHAO_CLIENT_SECRET=
PATHAO_USERNAME=
PATHAO_PASSWORD=
PATHAO_STORE_ID=
```

Endpoint:

```text
POST /api/orders/:orderId/courier/pathao
```

It uses the existing JWT tenant middleware and requires `orders.update`. The order must have a customer name, phone, address, and at least one item. Successful submissions are stored in the provider-neutral `shipments` table. Repeated requests return the existing successful shipment, while concurrent submissions are guarded by a database reservation and unique tenant/order/provider constraint.

The current Pathao order API accepts a complete recipient address and performs address mapping, so this integration does not require separate city, zone, or area fields. The provider uses cached access/refresh tokens for `/aladdin/api/v1/issue-token` and submits orders to `/aladdin/api/v1/orders`. See Pathao's address-mapping announcement: https://pathao.com/bn/blog/api-merchant-auto-address-feature/

The external API call is intentionally made outside the database transaction. A `submitting` reservation is created first; success updates it to `created`, while failures are marked `failed`. If the process stops after Pathao accepts the request but before persistence completes, the reservation remains in progress to avoid creating a duplicate.

## 1688 import and storage

The `/api/products/import/1688` endpoint is deliberately a non-persisting importer boundary. It validates public 1688 URLs and returns a useful manual-entry fallback when no compliant parser is available. A production `ProductImporter` implementation should use only public, permitted page data with timeouts/rate limits; never bypass access controls or CAPTCHAs. Image records are provider-neutral URLs, allowing local, S3, or R2 storage adapters without changing product data.

## Production notes

Set a strong `JWT_SECRET`, use TLS, enable Elasticsearch authentication, limit CORS origins, and replace bootstrap AutoMigrate with reviewed versioned SQL migration execution. Compose Elasticsearch security is disabled strictly for local development.
