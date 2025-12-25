<div align="center">
  <h1>Beacon 📡</h1>
  <p><strong>Watch Postgres data changes as webhooks, without the complexity.</strong></p>

[![Go](https://img.shields.io/badge/Go-1.23+-00ADD8?style=flat-square&logo=go)](https://go.dev)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-14+-336791?style=flat-square&logo=postgresql)](https://postgresql.org)
[![License](https://img.shields.io/badge/License-MIT-green?style=flat-square)](LICENSE)

</div>

---

## About

Beacon delivers webhooks from your PostgreSQL database changes. No message queues, no external dependencies, just your existing Postgres.

Events are captured in the same transaction as your data changes, delivered with automatic retries (exponential backoff with jitter, configurable max attempts), preserved in a DLQ when delivery ultimately fails (with full context for debugging), and exposed with Prometheus metrics and structured logging.

Build real-time integrations, event-driven workflows, audit logs, and cross-system sync powered by your existing PostgreSQL writes, with zero adoption hassle.

## Why does this exist?

Beacon fills the gap between “just use Postgres” hacks (like LISTEN/NOTIFY or bespoke HTTP-in-SQL) and heavyweight CDC stacks (like Debezium + Kafka). If you want reliable, transactional change-to-webhook delivery but you don’t want to adopt a BaaS or SaaS, Beacon is the middle path.

```
                              Simple
                                 ↑
                                 │       ● Beacon
                                 │
                                 │
                                 │
                                 │
  Fragile ───────────────────────┼─────────────────────────-─ Reliable
                                 │
                                 │       ● Hasura / Supabase
                                 │       ● Prisma Pulse
     ● LISTEN/NOTIFY             │
     ● pg_net                    │                  ● Debezium + Kafka
                                 ↓
                              Complex
```

| Product           | Requirements          | Model    | Consider if                                                      |
| ----------------- | --------------------- | -------- | ---------------------------------------------------------------- |
| **Beacon**        | One Docker container  | OSS      | Can host a Docker container; Single binary, below 10k writes/sec |
| Debezium + Kafka  | Kafka infrastructure  | OSS      | Enterprise-scale; already using Kafka; multi-system streaming;   |
| Hasura Events     | Adoption of BaaS      | OSS/SaaS | Already using Hasura BaaS                                        |
| Supabase Webhooks | Adoption of BaaS      | OSS/SaaS | Already using Supabase BaaS                                      |
| Prisma Pulse      | Prisma ORM at runtime | SaaS     | Already using Prisma ORM + prefer a managed service              |
| pg_net            | Handroll everything   | OSS      | -                                                                |
| LISTEN/NOTIFY     | Handroll everything   | OSS      | -                                                                |

## Quick Start

### 1. Run Beacon

```bash
# With Docker
docker run -e DATABASE_URL=postgres://... beacon:latest serve

# Or build from source
go build -o beacon ./cmd/beacon
./beacon serve
```

### 2. Define your webhooks

Create a `config.yaml`:

```yaml
destinations:
  - name: order-service
    url: https://orders.example.com/webhooks
    method: POST
    timeout_ms: 5000

subscriptions:
  - name: new-orders
    destination: order-service
    table: public.orders
    events: [INSERT]

  - name: user-changes
    destination: order-service
    table: public.users
    events: [INSERT, UPDATE, DELETE]
```

### 3. Apply configuration

```bash
curl -X POST http://localhost:8080/apply \
  -H "Authorization: Bearer $BEACON_CONTROLPLANE_SECRET" \
  -H "Content-Type: application/yaml" \
  --data-binary @config.yaml
```

Beacon installs PostgreSQL triggers on your tables. When rows change, webhooks fire.

## Configuration

### Environment Variables

| Variable                     | Description                                                 | Default        |
| ---------------------------- | ----------------------------------------------------------- | -------------- |
| `DATABASE_URL`               | PostgreSQL connection string                                | required       |
| `BEACON_CONTROLPLANE_SECRET` | Control plane authentication (Bearer token)                 | required       |
| `BEACON_HTTP_ADDR`           | Control plane listen address                                | `:8080`        |
| `BEACON_POLL_INTERVAL`       | Outbox polling interval                                     | `100ms`        |
| `BEACON_BATCH_SIZE`          | Events to claim per poll                                    | `100`          |
| `BEACON_WORKER_COUNT`        | Concurrent delivery workers                                 | `10`           |
| `BEACON_HMAC_SECRET`         | Webhook signing secret                                      | optional       |
| `BEACON_LOG_LEVEL`           | Log level (debug, info, warn, error)                        | `info`         |
| `BEACON_LOG_FORMAT`          | Log format (json, text)                                     | `json`         |
| `BEACON_MAX_PAYLOAD_BYTES`   | Max request body bytes sent to destinations                 | `1048576`      |
| `BEACON_RETENTION_HOURS`     | Retention period for delivered events                       | `168` (7 days) |
| `BEACON_JANITOR_INTERVAL`    | Cleanup interval                                            | `1h`           |
| `BEACON_JANITOR_BATCH_SIZE`  | Max events cleaned per cycle                                | `1000`         |
| `BEACON_SEED_CONFIG_PATH`    | Path to seed config to auto-apply on startup if DB is clean | optional       |

### Webhook Payload

Beacon sends a JSON payload for each event:

```json
{
  "version": 1,
  "trigger": {
    "schema": "public",
    "table": "orders",
    "operation": "INSERT"
  },
  "pk": { "id": 42 },
  "old": null,
  "new": { "id": 42, "status": "pending", "total": 99.99 }
}
```

## How It Works

```
┌─────────────┐     ┌──────────────┐     ┌─────────────┐
│ Your App    │────▶│  PostgreSQL  │────▶│   Beacon    │────▶ Webhooks
│             │     │  + Triggers  │     │  Dispatcher │
└─────────────┘     └──────────────┘     └─────────────┘
```

1. **Capture** — PostgreSQL triggers write events to an outbox table (same transaction as your data)
2. **Claim** — Worker processes claim events using `FOR UPDATE SKIP LOCKED`
3. **Deliver** — HTTP requests with retries, timeouts, and signing
4. **Ack/Retry** — Success removes the event; failure schedules a retry with backoff

## Scaling

Beacon scales horizontally. Run multiple instances against the same database. No extra coordination required.

- **Lock-free claiming** — Workers use `FOR UPDATE SKIP LOCKED` to claim events without blocking each other
- **Crash recovery** — Heartbeat-based detection automatically reclaims events from dead workers within 30 seconds
- **Per-destination limits** — `max_in_flight` prevents any single slow destination from consuming all workers

```yaml
destinations:
  - name: slow-service
    url: https://slow.example.com/webhook
    max_in_flight: 10 # Max concurrent requests to this destination
```

### Request Signing

When `BEACON_HMAC_SECRET` is set, Beacon signs requests:

```
Beacon-Timestamp: 1703356800
Beacon-Signature: sha256=abc123...
```

Verify with: `HMAC-SHA256(timestamp + "." + body, secret)`

### Persistence and Schema

Beacon persists its own configuration and delivery state inside your Postgres database under a dedicated `beacon` schema. This is created and migrated automatically on startup.

| Table                      | Purpose                                     |
| -------------------------- | ------------------------------------------- |
| `beacon.schema_version`    | Tracks applied migrations                   |
| `beacon.destinations`      | Webhook endpoints + delivery settings       |
| `beacon.subscriptions`     | Table/operation → destination routing rules |
| `beacon.outbox_events`     | Transactional outbox queue + delivery state |
| `beacon.delivery_attempts` | Delivery attempt audit log                  |
| `beacon.dead_letters`      | Exhausted-retry events + snapshot           |
| `beacon.worker_heartbeats` | Worker liveness for recovery                |

Schema changes to your application tables are handled automatically: the trigger function uses dynamic column introspection, so adding/removing columns doesn’t require restarting Beacon or reinstalling triggers.

## Control Plane HTTP API

> All endpoints except `GET /health` require an `Authorization: Bearer <token>` header. Beacon validates this token against `BEACON_CONTROLPLANE_SECRET`. Missing/invalid tokens receive `401 Unauthorized`.

### Apply Configuration

```bash
POST /apply
Content-Type: application/yaml

# Dry run (preview changes)
POST /apply?dry_run=true
```

### Export Current Config

```bash
GET /config
Accept: application/yaml
```

### Health Check

```bash
GET /health
```

### Metrics

```bash
GET /metrics  # Prometheus format (authenticated)
```

## Maintenance

Beacon automatically cleans up delivered events older than the retention period (default: 7 days). Configure with `BEACON_RETENTION_HOURS`.

Dead letters are preserved for manual review:

```sql
-- Inspect recent dead letters
SELECT * FROM beacon.dead_letters WHERE dead_at > now() - INTERVAL '1 day';

-- Archive old dead letters (manual)
DELETE FROM beacon.dead_letters WHERE dead_at < now() - INTERVAL '30 days';
```

## Metrics

Beacon exposes Prometheus metrics at `/metrics` (authenticated with `Authorization: Bearer $BEACON_CONTROLPLANE_SECRET`):

| Metric                             | Type      | Description                           |
| ---------------------------------- | --------- | ------------------------------------- |
| `beacon_delivery_total`            | counter   | Deliveries by destination and status  |
| `beacon_delivery_duration_seconds` | histogram | Delivery latency (p50, p95, p99)      |
| `beacon_dead_letters_total`        | counter   | Events that exhausted retries         |
| `beacon_outbox_depth`              | gauge     | Current event count by state          |
| `beacon_workers_active`            | gauge     | Active worker goroutines              |
| `beacon_events_reaped_total`       | counter   | Events recovered from crashed workers |
| `beacon_events_cleaned_total`      | counter   | Old events cleaned by janitor         |

**Recommended alerts:**

- `beacon_outbox_depth{state="pending"} > 10000` for 5min — backlog building
- `beacon_dead_letters_total` increasing — destination issues
- `beacon_workers_active == 0` — no workers processing

Example:

```bash
curl -sS \
  -H "Authorization: Bearer $BEACON_CONTROLPLANE_SECRET" \
  http://localhost:8080/metrics
```

## License

[MIT](LICENSE)
