# StockPilot — Multi-tenant Order & Inventory SaaS

StockPilot is a Gin + React application for tenant-isolated inventory, variants, stock movements, delivery configuration, and orders. PostgreSQL is authoritative; Elasticsearch is provisioned as an optional search adapter.

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

## 1688 import and storage

The `/api/products/import/1688` endpoint is deliberately a non-persisting importer boundary. It validates public 1688 URLs and returns a useful manual-entry fallback when no compliant parser is available. A production `ProductImporter` implementation should use only public, permitted page data with timeouts/rate limits; never bypass access controls or CAPTCHAs. Image records are provider-neutral URLs, allowing local, S3, or R2 storage adapters without changing product data.

## Production notes

Set a strong `JWT_SECRET`, use TLS, enable Elasticsearch authentication, limit CORS origins, and replace bootstrap AutoMigrate with reviewed versioned SQL migration execution. Compose Elasticsearch security is disabled strictly for local development.
