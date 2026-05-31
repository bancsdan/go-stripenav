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

## Persistence — IMPORTANT

The default build uses the in-memory submission store. **State is lost on
restart**, which means:

- A pod restart between a CREATE going to NAV and the worker polling its
  status loses the transaction id; the submission is never marked
  `accepted`.
- Retries of failed submissions don't survive restart.
- The bridge's idempotency check (event id → existing submission) breaks
  across restarts; you'll get duplicate NAV submissions on Stripe
  re-deliveries.

For any production deployment you must implement `stripenav.SubmissionStore`
against your durable storage (Postgres, MySQL, DynamoDB, etc.) and run
your own binary that wires it in. See [`EMBED.md`](./EMBED.md) for how to
do that.

The bundled container is suitable for: local dev, staging against the
NAV test env, low-volume production where occasional restart-loss is
acceptable.

## Scaling

Until a real `SubmissionStore` is wired in, run **exactly one replica.**
Two pods sharing nothing means two independent stores and two
independent workers — they'll race on submissions, the second pod's
worker can't see the first's submitted records, and you'll get
duplicates and orphans.

With a shared durable store, multiple replicas are safe (the store's
`UpdateStatus` is atomic by interface contract). Implement
`FindByInvoiceNumber` and `ListPending` to be cluster-aware (e.g.,
`SELECT … FOR UPDATE SKIP LOCKED`) before you scale.

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
