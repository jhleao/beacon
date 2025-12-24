<div align="center">
  <h1>Beacon</h1>
  <p><strong>PostgreSQL-native webhook delivery that just works</strong></p>

[![Go](https://img.shields.io/badge/Go-1.23+-00ADD8?style=flat-square&logo=go)](https://go.dev)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-14+-336791?style=flat-square&logo=postgresql)](https://postgresql.org)
[![License](https://img.shields.io/badge/License-MIT-green?style=flat-square)](LICENSE)

</div>

---

## About

Beacon delivers webhooks from your PostgreSQL database changes. No message queues. No external dependencies. Just your existing Postgres.

Insert a row, Beacon delivers a webhook. Update a row, Beacon delivers a webhook. It's that simple.

- **Transactional guarantees** — Events are captured in the same transaction as your data changes
- **Automatic retries** — Exponential backoff with jitter, configurable max attempts
- **Dead letter queue** — Failed events are preserved with full context for debugging
- **SSRF protection** — Built-in safeguards against internal network attacks
- **Observable** — Prometheus metrics and structured logging out of the box

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
  -H "Authorization: Bearer $BEACON_API_KEY" \
  -H "Content-Type: application/yaml" \
  --data-binary @config.yaml
```

Beacon installs PostgreSQL triggers on your tables. When rows change, webhooks fire.

## Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `DATABASE_URL` | PostgreSQL connection string | required |
| `BEACON_API_KEY` | Control plane authentication | required |
| `BEACON_HMAC_SECRET` | Webhook signing secret | optional |
| `BEACON_LOG_LEVEL` | Log level (debug, info, warn, error) | `info` |
| `BEACON_LOG_FORMAT` | Log format (json, text) | `json` |
| `BEACON_CONTROL_ADDR` | Control plane listen address | `:8080` |
| `BEACON_POLL_INTERVAL` | Outbox polling interval | `100ms` |
| `BEACON_BATCH_SIZE` | Events to claim per poll | `100` |
| `BEACON_WORKERS` | Concurrent delivery workers | `10` |

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
  "pk": {"id": 42},
  "old": null,
  "new": {"id": 42, "status": "pending", "total": 99.99}
}
```

### Request Signing

When `BEACON_HMAC_SECRET` is set, Beacon signs requests:

```
Beacon-Timestamp: 1703356800
Beacon-Signature: sha256=abc123...
```

Verify with: `HMAC-SHA256(timestamp + "." + body, secret)`

## API

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
GET /metrics  # Prometheus format
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

## License

[MIT](LICENSE)
