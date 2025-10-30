# LinkPeek

LinkPeek is a Go service with a small web UI for inspecting inbound traffic, testing payload variants, and exercising retry scenarios. The backend exposes HTTP APIs and WebSocket updates, while the frontend is built from ES modules that subscribe to realtime topics.

## Quick Start

### Docker Compose

```bash
docker compose up -d --build
```

Open <http://localhost:9009>. The compose stack also starts PostgreSQL (optional analytics) and Cloudflared for Quick Tunnel support. Environment defaults live in `docker-compose.yml`.

### Local Go Run

```bash
DATA_DIR=./data go run ./cmd/linkpeek
```

Useful flags:

- `PORT` – HTTP port (default `9009`).
- `DATA_DIR` – writable directory for payloads, capture logs, templated assets, and auth store.
- `DATABASE_URL` – optional PostgreSQL DSN for analytics endpoints.
- `REALTIME_ENABLED=0` – disable the websocket hub when troubleshooting.

## Development Workflow

```bash
make build   # builds ./bin/linkpeek using ./cmd/linkpeek
make run     # builds then runs with DATA_DIR=./data
make test    # go test ./...
make tidy    # go mod tidy
```

You can also run ad‑hoc targets:

```bash
go run ./cmd/linkpeek            # start the server
PORT=9010 go run ./cmd/wstest    # websocket smoke client
```

All Go source follows standard formatting (`gofmt`).

## Repository Layout

```
.
├── cmd/
│   ├── linkpeek/        # production entrypoint
│   └── wstest/          # helper that exercises the realtime API
├── handlers/            # thin HTTP adapters around domain services
├── internal/
│   ├── app/runtime/     # dependency graph for the live server
│   ├── domain/          # cohesive services (auth, capture, payload, …)
│   ├── http/router/     # mux + middleware assembly
│   ├── realtime/        # websocket hub and topic snapshots
│   └── server/          # orchestration and public Run(ctx) entrypoint
├── middleware/          # reusable HTTP middleware
├── static/
│   ├── css & js modules (static/js/app.js bootstraps feature modules)
│   └── assets
└── templates/           # HTML templates for the dashboard
```

## Architecture Snapshot

A short capsule of the runtime.

- `cmd/linkpeek` delegates to `internal/server.Run`, which loads configuration, initialises `internal/app/runtime.Runtime`, and builds the mux from `internal/http/router`.
- Runtime aggregates domain services: `auth`, `capture`, `payload`, `retry`, `scanner`, `snippet`, `tunnel`, and the realtime hub. Each service owns its persistence (filesystem or Postgres), background workers, and publish hooks.
- Request flow: middlewares (`withLogging`, `withAuth`, gzip, security headers) → handler package (e.g. `handlers/payload`) → domain service.
- The realtime hub exposes topic snapshots and broadcasts updates when services mutate state (payload uploads, capture hits, scanner job changes, retry stats, tunnel events).
- Frontend code lives under `static/js/`. `static/js/app.js` boots feature modules (`features/*.js`) that subscribe to the topics they care about and share helpers from `static/js/lib/`.

## Feature Reference

- **Auth & Sessions** – Form or JSON login, secure cookies, `/access` workflow for rotating the admin password, rate limiters, and logout.
- **Capture Hooks** – Create webhook endpoints, ingest requests (headers, body previews, IP metadata), stream activity in realtime, and export as NDJSON.
- **Payload Lab** – Upload artefacts (default 250 MB cap) and exercise variants: inline/attachment, MIME mismatch, corrupt/slow/chunked streams, redirect, range, and deterministic spectrum mutations (including HTML/OG wrappers).
- **Snippet Preview Lab** – Generate ephemeral snippets with user‑selected MIME types plus `raw`, `html`, and `og` routes for crawler testing.
- **Scanner** – Schedule HTTP jobs with custom bodies/content‑types, inspect results, and clear history; jobs publish updates to the dashboard as they complete.
- **Retry Lab** – Access `/retrylab/*` scenarios (retry hints, truncated streams, wrong content length) and watch aggregate statistics via `/api/retrylab/stats`.
- **Tunnel Integration** – Track Cloudflared quick tunnels, restart via `/api/tunnel/restart`, surface history, and banner warnings when requests arrive from shared hosts.
- **Realtime Feed** – `/api/realtime` websocket issues topic snapshots (`payload.list`, `capture.activity`, `scanner.jobs`, etc.) and pushes subsequent events; `/api/events` still exposes the rolling HTTP log buffer.

## Manual QA

Baseline flows worth exercising during regressions or large refactors:

- Authentication and session lifecycle.
- Capture hook creation and request ingestion.
- Payload Lab upload/variant round-trip.
- Scanner job lifecycle.
- Retry Lab scenario + stats updates.
- Tunnel banner/state and realtime event stream.

## Deployment Notes

- Use Docker Compose for the canonical stack. Mount `/var/run/docker.sock` only when the tunnel restart endpoint is required.
- The binary is statically linked (`CGO_ENABLED=0`) and copies templates + static assets into the final image (`Dockerfile`).
- Set `DBIP_API_KEY` to enable geo‑lookups during analytics; otherwise the service gracefully skips remote calls.
- All data written under `DATA_DIR` (payloads, events, auth store, tunnel logs). Bind or volume mount it for persistence.

## Docs Site (GitHub Pages Ready)

Everything under `docs/` is a self-contained, static presentation layer that mirrors the in-app LinkPeek styling. Publish it directly via GitHub Pages by using the `docs` folder as the deployment source.

- `docs/index.html` – modular landing page covering the feature labs, architecture, workflows, realtime hub, and CTA. Sections are annotated with `data-module` attributes so cards can be reordered or duplicated without touching global layout.
- `docs/styles.css` – dark LinkPeek dashboard palette with reusable card/workflow/realtime components. Token variables sit at the top of the file for quick palette or spacing adjustments.
- `docs/script.js` – progressive enhancement for navigation state, mobile menu, year stamp, and the back-to-top affordance.
- `docs/favicon.svg` – LinkPeek icon for browsers and share previews.

To extend the page with new modules:

1. Duplicate an existing section wrapper (for example `.feature-card` or `.workflow-card`) inside `docs/index.html`.
2. Adjust copy and links, keeping the `data-module` attribute for future automation compatibility.
3. Add new styling tokens or component classes near the bottom of `docs/styles.css` to preserve override ordering.
4. Mirror the defensive patterns in `docs/script.js` (query once, guard against missing nodes) for any additional interactivity.

Happy debugging!
