# Deploying gostripenav

The `gostripenav` container is a small HTTP server that receives Stripe
webhook events and reports the corresponding invoices to NAV's Online
Számla v3.0 API. It's published as `ghcr.io/bancsdan/go-stripenav` and
runs anywhere a container does: ECS, GKE, Cloud Run, Fly.io, Railway,
plain Docker on a VM, k8s.

## Required environment

| Variable | Required | Notes |
| --- | --- | --- |
| `STRIPE_WEBHOOK_SECRET` | yes | Stripe endpoint signing secret (`whsec_…`). |
| `NAV_BASE_URL` | yes | `https://api.onlineszamla.nav.gov.hu/invoiceService/v3` for production, `https://api-test.onlineszamla.nav.gov.hu/invoiceService/v3` for the test env. |
| `NAV_LOGIN` | yes | NAV technical user login. |
| `NAV_PASSWORD` | yes | NAV technical user password (plaintext — the binary hashes it). |
| `NAV_TAX_NUMBER` | yes | 8-digit Hungarian tax base. Any extra characters are stripped. |
| `NAV_SIGN_KEY` | yes | Technical user signature key (32 chars). |
| `NAV_EXCHANGE_KEY` | yes | Technical user exchange key (exactly 16 chars — AES-128). |
| `NAV_SOFTWARE_ID` | recommended | 18 chars `[0-9A-Z]`. Convention: `<ISO 3166 alpha-2 country><dev tax-base><serial>`. Defaults to `HU00000000GOSTRPNV` (a placeholder — replace before production). |
| `NAV_SOFTWARE_NAME` | no | Defaults to `gostripenav`. |
| `NAV_SOFTWARE_OPERATION` | no | `LOCAL_SOFTWARE` (default) or `ONLINE_SERVICE`. |
| `NAV_SOFTWARE_VERSION` | no | Software version string. |
| `NAV_DEV_NAME` / `NAV_DEV_CONTACT` / `NAV_DEV_COUNTRY` | no | Developer info on the software block. |
| `NAV_DEBUG` | no | Set to `true` to log every NAV request/response body. **Local debugging only — bodies include the signed envelope.** |
| `SUPPLIER_TAX_NUMBER` | yes | 11-character Hungarian VAT number for the merchant (with or without hyphens). |
| `SUPPLIER_NAME` | yes | Supplier's registered legal name. |
| `SUPPLIER_COUNTRY` | no | Defaults to `HU`. |
| `SUPPLIER_POSTAL_CODE` | yes | |
| `SUPPLIER_CITY` | yes | |
| `SUPPLIER_ADDRESS` | no | Street + number etc. |
| `LISTEN_ADDR` | no | Defaults to `:8080`. |
| `STORE_URL` | no | Submission store URL — see [Persistence](#persistence). Unset / `memory:` → in-memory (dev only). `postgres://...` → Postgres adapter, migrations run on boot. |

## Endpoints

| Path | Purpose |
| --- | --- |
| `POST /webhooks/stripe` | The webhook endpoint to register with Stripe. |
| `GET /healthz` | Liveness probe — returns 204 No Content. |
| `GET /readyz` | Readiness probe — returns 204 No Content. |

## Quick start

### Docker

```bash
docker run --rm -p 8080:8080 --env-file .env \
  ghcr.io/bancsdan/go-stripenav:latest
```

### docker-compose

```yaml
services:
  gostripenav:
    image: ghcr.io/bancsdan/go-stripenav:latest
    ports:
      - "8080:8080"
    env_file: .env
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "/usr/local/bin/gostripenav", "--healthcheck"]
      # If you don't have the binary on PATH inside the container for
      # this, use a sidecar curl image or wget — the distroless image
      # has neither installed.
      interval: 30s
      timeout: 5s
      retries: 3
```

### Kubernetes

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: gostripenav
spec:
  replicas: 1   # see "Scaling" below
  selector:
    matchLabels:
      app: gostripenav
  template:
    metadata:
      labels:
        app: gostripenav
    spec:
      containers:
        - name: gostripenav
          image: ghcr.io/bancsdan/go-stripenav:v0.1.0
          ports:
            - containerPort: 8080
          envFrom:
            - secretRef:
                name: gostripenav-env
          livenessProbe:
            httpGet:
              path: /healthz
              port: 8080
          readinessProbe:
            httpGet:
              path: /readyz
              port: 8080
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
            limits:
              cpu: 500m
              memory: 256Mi
```

## Persistence

The container ships with two `SubmissionStore` implementations selected
by `STORE_URL`:

| `STORE_URL` value | Adapter | When to use |
| --- | --- | --- |
| unset, or `memory:` | in-memory | Local dev, smoke tests. State lost on restart. |
| `postgres://user:pw@host:port/db?sslmode=…` | Postgres | Production. |
| `postgresql://…` | Postgres (same) | Production. |
| `mysql://…` | not built in | Returns startup error. Embed the library to provide your own. |
| `dynamodb://…` | not built in | Returns startup error. Embed the library to provide your own. |

### In-memory (default) — IMPORTANT

State is lost on restart. That means:

- A pod restart between a CREATE going to NAV and the worker polling its
  status loses the transaction id; the submission is never marked
  `accepted`.
- Retries of failed submissions don't survive restart.
- The bridge's event-id deduplication breaks across restarts; you'll get
  duplicate NAV submissions on Stripe re-deliveries.

OK for: local dev, staging against the NAV test env, low-volume
production where occasional restart-loss is acceptable.

### Postgres

Set `STORE_URL` to a libpq connection string:

```
STORE_URL=postgres://stripenav:secret@db.internal:5432/stripenav?sslmode=require
```

On boot the binary:

1. Opens a connection pool (max 10 conns, 1 hr lifetime).
2. Applies the embedded migration `001_init.sql` — creates the
   `stripenav_submissions` table and its two indexes (`invoice_number`
   for parent lookup, partial index on `next_attempt_at` for the worker
   tick). The migration is idempotent.
3. Serves requests.

Granted to the user in the DSN should be at least: `INSERT`, `SELECT`,
`UPDATE` on `stripenav_submissions`, and `CREATE TABLE`, `CREATE INDEX`
the first time so the migration succeeds.

For local development with docker-compose:

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: stripenav
      POSTGRES_PASSWORD: stripenavpw
      POSTGRES_DB: stripenav
    volumes:
      - pgdata:/var/lib/postgresql/data
    ports:
      - "5432:5432"

  gostripenav:
    image: ghcr.io/bancsdan/go-stripenav:latest
    depends_on:
      - postgres
    ports:
      - "8080:8080"
    env_file: .env
    environment:
      STORE_URL: postgres://stripenav:stripenavpw@postgres:5432/stripenav?sslmode=disable
    restart: unless-stopped

volumes:
  pgdata: {}
```

## Scaling

With the in-memory store, run **exactly one replica.** Two pods sharing
nothing means two independent stores and two independent workers —
they'll race on submissions, the second pod's worker can't see the
first's submitted records, and you'll get duplicates and orphans.

With the Postgres store: `UpdateStatus` is atomic (it uses
`SELECT … FOR UPDATE`), so multi-pod is *safe* for state updates. The
remaining caveat is `ListPending`: it does not yet use
`SELECT … FOR UPDATE SKIP LOCKED` to claim rows, so two workers reading
the table at the same instant may both attempt to submit the same
record. Each `attemptSubmit` round trip is fronted by the
`UpdateStatus` lock, so the *second* worker will see the row already
in `submitted` state and skip it — but NAV may still receive duplicate
`manageInvoice` calls in a tight race.

Until claim-with-skip-locked lands, run **one worker replica** (set
`Config.DisableWorker = true` on additional pods if you embed the
library, or simply run one container).

## Observability

The binary writes JSON-structured logs to stdout via `slog`. Levels:

- `INFO` — server lifecycle, submission state transitions.
- `WARN` — bad signatures, missing parent for a STORNO, deferred work.
- `ERROR` — NAV submission failures, processing exceptions, deadline
  breaches.

There is no built-in metrics endpoint yet. Wire `stripenav.MetricsRecorder`
in your own binary if you need Prometheus integration (see `EMBED.md`).

## Updates

The image is published on every git tag matching `v*`. Pin to a specific
version in production:

```
ghcr.io/bancsdan/go-stripenav:v0.1.0   # exact version
ghcr.io/bancsdan/go-stripenav:0.1     # minor track
ghcr.io/bancsdan/go-stripenav:0       # major track
ghcr.io/bancsdan/go-stripenav:latest  # tip — fine for dev, never prod
```

## Compliance reminder

The bridge submits invoices to NAV. NAV's reporting requirement is a
legal obligation; the package's `aborted` terminal state and 24-hour
deadline logging are how the bridge reports its own failure modes
back to you. Wire those logs into your oncall.
