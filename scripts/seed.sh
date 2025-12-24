#!/bin/bash
set -euo pipefail

# Load environment
if [ -f .env ]; then
    source .env
fi

BEACON_URL="${BEACON_HTTP_ADDR:-localhost:8080}"
SECRET="${BEACON_CONTROLPLANE_SECRET:-dev-control-secret-change-in-prod}"

echo "Creating test tables..."
docker compose exec -T postgres psql -U beacon -d beacon_dev <<'SQL'
CREATE TABLE IF NOT EXISTS public.users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    status TEXT DEFAULT 'active',
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE IF NOT EXISTS public.orders (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID REFERENCES public.users(id),
    total NUMERIC(10,2) NOT NULL,
    status TEXT DEFAULT 'pending',
    created_at TIMESTAMPTZ DEFAULT now()
);
SQL

echo "Applying Beacon config..."
curl -s -X POST "http://${BEACON_URL}/apply" \
    -H "Authorization: Bearer ${SECRET}" \
    -H "Content-Type: application/x-yaml" \
    -d '
version: 1

destinations:
  - name: local-webhook
    url: http://webhook:9000/
    timeout_ms: 5000
    max_in_flight: 10

  - name: slow-webhook
    url: http://webhook:9000/slow?delay=2s
    timeout_ms: 10000
    max_in_flight: 5

subscriptions:
  - name: users-insert
    table: public.users
    operation: INSERT
    destination: local-webhook

  - name: users-update
    table: public.users
    operation: UPDATE
    destination: local-webhook
    trigger_on: [email, name, status]
    select: [id, email, name, status, updated_at]

  - name: orders-insert
    table: public.orders
    operation: INSERT
    destination: local-webhook
    select: [id, user_id, total, status]
'

echo ""
echo "Seed complete! Try these commands:"
echo ""
echo "  # Insert a user (triggers webhook)"
echo "  make psql"
echo "  INSERT INTO users (email, name) VALUES ('test@example.com', 'Test User');"
echo ""
echo "  # Watch webhook receiver"
echo "  docker compose logs -f webhook"
