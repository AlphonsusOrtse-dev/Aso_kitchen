# Aso Kitchen — Backend

A Go backend for Aso Kitchen: digital menu browsing, pre-ordering, and
live order tracking over WebSockets, with JWT-based authentication and
role-based access control (customer / staff / admin).

**Status:** Core backend complete and tested — auth, menu, orders, live
tracking, stock management. Frontend not yet started.

---

## Quickstart

```bash
git clone <your repo url>
cd aso-kitchen-backend
cp .env.example .env   # then fill in your real DATABASE_URL and JWT_SECRET
go mod tidy
go run ./cmd/server
```

You should see:
```
connected to postgres
aso kitchen backend listening on :8080
```

Health check: `curl http://localhost:8080/healthz` → `ok`

> **`.env` is never committed** (see `.gitignore`). Every fresh clone needs
> its own `.env`, copied from `.env.example` and filled in with real
> secrets from your password manager. This is intentional — see
> [Environment & Secrets](#environment--secrets) below.

---

## Tech stack

- **Go 1.22** + [chi](https://github.com/go-chi/chi) router
- **PostgreSQL**, hosted on [Neon](https://neon.tech) (serverless — see note on cold starts below)
- **WebSockets** ([gorilla/websocket](https://github.com/gorilla/websocket)) for live order tracking
- **JWT** ([golang-jwt/jwt](https://github.com/golang-jwt/jwt)) + **bcrypt** for auth

---

## Project layout

```
aso-kitchen-backend/
├── cmd/server/main.go        # entrypoint: routing, middleware wiring, graceful shutdown
├── internal/
│   ├── config/                 # env var loading
│   ├── database/                # pgx pool setup, with Neon cold-start retry logic
│   ├── models/                   # domain structs, OrderStatus state machine, Role type
│   ├── handlers/                  # HTTP handlers: auth, menu, orders
│   └── middleware/                 # JWT auth middleware, role-gating
├── db/schema.sql               # full Postgres DDL
├── .env.example                # safe placeholder values only — never real secrets
└── go.mod
```

---

## Architecture

```
                         ┌────────────────────────────┐
                         │        CLIENTS               │
                         │  Web / Mobile (not yet built)  │
                         └───────────┬─────────┬─────────┘
                                      │         │
                     REST (HTTPS)     │         │  WebSocket
                     + JWT header     │         │  /ws/orders/{id}
                                      ▼         ▼
                         ┌──────────────────────────────┐
                         │        GO BACKEND (chi)        │
                         │                                 │
                         │  Public routes (no token):       │
                         │    /api/signup  /api/login         │
                         │    /api/menu (GET)                  │
                         │                                       │
                         │  Authenticated routes (RequireAuth):    │
                         │    POST /api/orders                      │
                         │    GET  /api/orders/{id}                   │
                         │                                              │
                         │  Staff/admin only (+ RequireRole):             │
                         │    PATCH /api/orders/{id}/status                │
                         │    PATCH /api/menu/{id}/stock                     │
                         │                                                     │
                         │  WS Hub — orderID → set of live client connections   │
                         └───────────┬───────────────────────────────────────┘
                                     │
                                     ▼
                         ┌────────────────────┐
                         │   PostgreSQL (Neon)   │
                         │  users, menu_items,     │
                         │  orders, order_items,     │
                         │  order_status_history        │
                         └────────────────────┘
```

---

## Authentication & authorization model

**Why it works this way, not just what it does:**

- **Passwords are never stored in plain text.** They're hashed with
  bcrypt before hitting the database — a one-way transformation, so even
  someone with direct database access can't recover the original
  password. Two users with the same password get different hashes
  (bcrypt salts automatically), which also prevents pattern-spotting
  across accounts.
- **Public signup can only ever create `customer` accounts.** The
  `role` field is hardcoded server-side in `Signup` — a client can send
  whatever it wants in the request body, it's ignored. Letting signup
  accept a client-supplied role would let anyone grant themselves staff
  permissions. Staff/admin accounts are currently created by direct
  database insert (a deliberate YAGNI decision — see below).
- **A JWT is a signed "ticket", not a lookup.** On login, the server
  issues a token containing `user_id` and `role`, signed with a secret
  only the server knows (`JWT_SECRET`). On every later request, the
  server re-verifies the signature instead of hitting the database —
  if the signature is valid, the claims inside are trusted. **This
  means the JWT secret is the single point of trust in the whole
  system** — if it leaks, anyone can forge a token claiming to be any
  user with any role. It must be a long random value (`openssl rand
  -hex 32`), different per environment, and never committed to git.
- **Middleware runs before handlers, not inside them.** `RequireAuth`
  verifies the token and attaches `user_id`/`role` to the request
  context; `RequireRole` reads that context and rejects anything not
  in the allowed list. `RequireRole` has a hard dependency on
  `RequireAuth` having already run on the same request — this is why
  role-gated routes are *nested inside* the authenticated route group
  in `main.go`, not declared as siblings.
- **`CreateOrder` trusts the JWT's `user_id`, never a request body
  field.** Earlier versions of this API accepted `user_id` as JSON
  input, which let any client place orders as any user — a real
  privilege escalation bug, fixed by sourcing identity exclusively
  from the verified token.
- **`GetOrder` returns 404, not 403, for orders you don't own.** A 403
  confirms "this exists, you can't have it" — which leaks information
  to someone probing random order IDs. A 404 is deliberately
  ambiguous: "doesn't exist" and "exists but isn't yours" look
  identical from the outside.

---

## Order status state machine

Defined in `internal/models/models.go`:

```
pending → confirmed → preparing → cooking → ready → fulfilled
   └───────────────────┴─────────────┴─────────┴──── cancelled (from any non-terminal state)
```

Implemented as a map of legal next-states plus a `CanTransitionTo`
check, enforced in `UpdateOrderStatus` before any database write. This
exists because an unconstrained status field would let a mistaken or
out-of-order update (e.g. `pending → ready`, skipping actual
preparation) silently corrupt both the customer's live progress bar
and the historical data used for any future reporting. `fulfilled` and
`cancelled` map to an empty list of next-states — they're deliberate
dead ends.

---

## Live order tracking (WebSocket hub)

`internal/websocket/hub.go` replaces polling (repeatedly asking "any
updates yet?") with a standing connection the server can push through
the instant something changes.

Key structure:
```go
watchers map[uuid.UUID]map[*client]bool // orderID -> set of clients
```
Each order ID maps to a *set* of connections (not a single connection),
so multiple tabs/devices watching the same order all receive updates —
none of them silently overwrite each other in the map.

`sync.RWMutex` guards this shared map because `register` (new
connection), `unregister` (disconnect), and `Broadcast` (status change)
can all happen concurrently from different goroutines. Without a lock,
concurrent map access in Go can crash the process or silently corrupt
data — an intermittent, load-dependent bug rather than an immediate,
obvious one. Lock hold times are microseconds (fast in-memory map
operations only, never I/O), so this has no meaningful performance
cost even under real traffic.

**Known gap:** the WebSocket endpoint (`/ws/orders/{id}`) is not yet
authenticated — browsers can't set custom headers on the initial
WebSocket handshake, so this needs a different approach (e.g. a
short-lived token passed as a query parameter) than the header-based
JWT auth used elsewhere. Flagged as a conscious next step, not an
oversight.

---

## Environment & Secrets

- `.env` holds real secrets and is **git-ignored** — it will not exist
  after a fresh `git clone`. Recreate it every time with
  `cp .env.example .env`, then fill in real values from your password
  manager (never from git, never from chat history).
- `.env.example` holds **only safe placeholders** and is committed —
  if you ever see a real-looking password in this file, that's a bug;
  fix it immediately and rotate whatever leaked.
- **Neon cold starts:** the free tier suspends its compute after
  inactivity and takes a few seconds to wake up. `internal/database/database.go`
  retries the initial connection (up to 5 attempts, 15s timeout each)
  specifically to tolerate this — a single fast timeout was failing
  intermittently before this was added.
- If a real secret is ever accidentally committed: rotate it
  immediately (Neon: Roles → reset password). Removing it from a
  future commit does not erase it from git history — treat any
  committed secret as permanently compromised and replace it, rather
  than trying to scrub history.

---

## Testing the API locally

```bash
# Sign up (always creates role=customer)
curl -X POST http://localhost:8080/api/signup \
  -H "Content-Type: application/json" \
  -d '{"full_name":"Jane Customer","email":"jane@example.com","password":"supersecret123"}'

# Log in — returns a JWT
curl -X POST http://localhost:8080/api/login \
  -H "Content-Type: application/json" \
  -d '{"email":"jane@example.com","password":"supersecret123"}'

# Save the token for reuse in this terminal session
TOKEN="<paste the token from above>"

# Create an order (identity comes from the token, not the request body)
curl -X POST http://localhost:8080/api/orders \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"order_type":"pickup","items":[{"menu_item_id":"<id>","quantity":1}]}'

# Watch it live (browser console)
# const ws = new WebSocket("ws://localhost:8080/ws/orders/<order_id>");
# ws.onmessage = e => console.log(e.data);

# Update status — requires a staff/admin token (403 for customers)
curl -X PATCH http://localhost:8080/api/orders/<order_id>/status \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $STAFF_TOKEN" \
  -d '{"status":"confirmed"}'
```

Staff/admin accounts are created by direct database insert for now
(see [Staff accounts](#staff-accounts-a-deliberate-yagni-decision)),
not through public signup.

---

## Staff accounts: a deliberate YAGNI decision

There is currently no "admin invites staff" endpoint. Building one now
would mean permission checks and invite-token logic for a feature with
no real users yet. Staff accounts are inserted directly into the
database when needed:

```sql
INSERT INTO users (id, full_name, email, password_hash, role)
VALUES (gen_random_uuid(), 'Kitchen Staff', 'staff@asokitchen.com',
        '<bcrypt hash>', 'staff');
```

This will be revisited once there are actual staff members to onboard
— building it earlier would be solving an imagined problem instead of
a real one (YAGNI: "You Aren't Gonna Need It").

---

## Known gaps / next steps

- WebSocket authentication (see above)
- Payment provider integration (`payment_status` column exists, unused)
- Rate limiting on order creation / stock toggling
- Push notifications (APNs/FCM) for when the mobile app is backgrounded
- Admin-driven staff invite flow, once actually needed
- Frontend (web + mobile) — **next up**
