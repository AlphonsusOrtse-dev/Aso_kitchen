# Aso Kitchen — Backend

Go backend for Aso Kitchen: digital menu, pre-ordering, and live order
tracking over WebSockets.

## Stack

- **Go 1.22** + [chi](https://github.com/go-chi/chi) router
- **PostgreSQL** (via `pgx/v5`)
- **WebSockets** (via `gorilla/websocket`) for live order status push
- Redis is *not* wired in yet — see "Scaling to multiple instances" below
  for when/why you'd add it.

## Project layout

```
aso-kitchen-backend/
├── cmd/server/main.go        # entrypoint: wiring, routes, graceful shutdown
├── internal/
│   ├── config/                # env var loading
│   ├── database/               # pgx pool setup
│   ├── models/                 # domain structs + order status state machine
│   ├── handlers/                # HTTP handlers (menu, orders)
│   └── websocket/              # per-order WebSocket hub
├── db/schema.sql              # full Postgres DDL
├── .env.example
└── go.mod
```

## 1. Prerequisites

- Go 1.22+
- PostgreSQL 14+ running locally (or via Docker)

## 2. Set up the database

```bash
# Using Docker is the fastest path:
docker run --name aso-postgres -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=aso_kitchen -p 5432:5432 -d postgres:16

# Apply the schema
psql "postgres://postgres:postgres@localhost:5432/aso_kitchen" -f db/schema.sql
```

## 3. Configure environment

```bash
cp .env.example .env
# edit .env if your DB credentials differ
```

## 4. Install dependencies & run

```bash
go mod tidy
go run ./cmd/server
```

You should see:
```
connected to postgres
aso kitchen backend listening on :8080
```

## 5. Test it locally

**Health check**
```bash
curl http://localhost:8080/healthz
```

**Browse the menu**
```bash
curl http://localhost:8080/api/menu
```

**Toggle stock (mark an item out of stock)**
```bash
curl -X PATCH http://localhost:8080/api/menu/<MENU_ITEM_ID>/stock \
  -H "Content-Type: application/json" \
  -d '{"is_available": false}'
```

**Create a pre-order** (set `scheduled_for` for a future pickup/delivery time;
omit it for an ASAP order)
```bash
curl -X POST http://localhost:8080/api/orders \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": "<A_USER_UUID>",
    "order_type": "pickup",
    "scheduled_for": "2026-07-18T17:30:00Z",
    "items": [
      {"menu_item_id": "<MENU_ITEM_ID>", "quantity": 2}
    ]
  }'
```

**Watch an order live** — open a WebSocket to:
```
ws://localhost:8080/ws/orders/<ORDER_ID>
```
A quick way to test without writing frontend code is `websocat`:
```bash
websocat ws://localhost:8080/ws/orders/<ORDER_ID>
```
Leave that running, then in a second terminal update the status — the
first terminal should print the JSON event instantly:
```bash
curl -X PATCH http://localhost:8080/api/orders/<ORDER_ID>/status \
  -H "Content-Type: application/json" \
  -d '{"status": "confirmed"}'
```

The order status machine only allows forward transitions defined in
`internal/models/models.go` (`pending → confirmed → preparing → cooking →
ready → fulfilled`, with `cancelled` reachable from any non-terminal
state). Attempting an invalid jump (e.g. `pending → ready`) returns
`409 Conflict`.

## Scaling to multiple instances (Redis)

The `Hub` in `internal/websocket/hub.go` is in-memory and per-process —
fine for a single backend instance. Once you run more than one instance
behind a load balancer, a customer's WebSocket might land on instance A
while the kitchen's status update hits instance B, so the browadcast
needs to fan out across processes. Two ways to do that:

1. **Redis Pub/Sub** — each instance subscribes to a channel per order
   (or one global `order-events` channel it filters locally). Replace
   `h.Broadcast(event)` in `UpdateOrderStatus` with a `PUBLISH` to Redis,
   and have each instance's hub `SUBSCRIBE` and re-broadcast to its own
   local WebSocket clients.
2. **Redis for order-state cache** — even without Pub/Sub, storing
   `order:<id>:status` in Redis with a short TTL lets any instance answer
   "what's the current status" quickly without hitting Postgres, useful
   for reconnect/catch-up when a client's socket drops and reopens.

Both are additive — nothing in the current schema or handlers needs to
change to add them later.

## Notes on what's intentionally left out (for you to add next)

- **Auth**: no JWT/session middleware yet. `user_id` is currently trusted
  from the request body — add an auth middleware in
  `internal/middleware/` before going to production.
- **Payments**: `payment_status` column exists but no payment provider
  integration.
- **Rate limiting** on order creation and stock toggling.
- **Push notifications** (APNs/FCM) as a companion to WebSocket for when
  the mobile app is backgrounded.
