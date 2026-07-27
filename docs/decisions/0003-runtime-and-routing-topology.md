# ADR 0003: Runtime And Routing Topology

## Status

Accepted

## Context

ADR 0001 defines separate UI, API, and worker components and images. Browser
authentication uses secure cookies and CSRF protection, while private tiles must
pass through the API. The project needs a local development topology and a
Kubernetes topology that do not introduce unnecessary cross-origin behavior or
couple UI asset serving to the Go API process.

The Helm chart is namespace-agnostic and has ingress disabled by default. Live
hostnames and ingress configuration belong to deployment values in the separate
GitOps repository.

## Decision

### Production services

Run the UI and API as separate Kubernetes Deployments, Services, and images.
The worker remains a separate Deployment without a browser-facing Service.

The UI image serves compiled SPA assets through a small non-root static HTTP
server with history fallback to `index.html`. The API image does not contain or
serve UI build artifacts.

### Browser origin and routes

Expose one browser origin through ingress or gateway path routing:

| Path | Owner |
|---|---|
| `/` and SPA routes | UI service |
| `/api/*` | API service |
| `/swagger` and Swagger assets | API service |

The API owns `/api/config`, `/api/openapi.yaml`, authentication, application
resources, and authenticated private tile routes. Browser code uses relative
same-origin URLs for these endpoints.

Ordinary production operation does not require CORS. If an operator deliberately
deploys separate origins later, that is a new security and deployment decision,
not an implicit chart option.

### Health routes

API and worker liveness and readiness endpoints are intended for cluster probes.
They are not routed through public ingress by default. The API uses
`/health/live` and `/health/ready`; the worker exposes equivalent process-local
probe endpoints on its own listener.

Liveness reports whether the process is running. Readiness reports whether the
component can serve its owned workload, including required database and schema
compatibility. SMTP, OTel export, source availability, and OSM refresh health do
not make the interactive API unready.

### Local development

Vite serves the UI during development and proxies API and Swagger paths to the
local API. This preserves relative URLs and same-origin browser behavior.
API and worker still run as independent Go processes with independent database
roles and configuration.

### Runtime configuration

The SPA loads a public allowlisted configuration document from `/api/config`.
It contains presentation and safe polling/map settings only. It never exposes
database topology, internal service URLs, source configuration, credentials, or
Kubernetes Secret values.

### Private tiles and public maps

`pg_tileserv` remains cluster-internal and is never routed through public
ingress. The API authenticates private tile requests and proxies only approved
account and selection parameters.

The browser may call the configured public base-map provider directly. Its URL
and attribution are public runtime configuration.

## Alternatives Considered

### API serves the SPA

Serving embedded UI assets from the API would simplify routing but couple image
builds and resource scaling, weaken the three-component release boundary, and
make independent UI deployment less meaningful.

### Separate UI and API origins

Separate origins could preserve independent services but require CORS and more
complex cookie, CSRF, and deployment policy without providing a product benefit.

### Browser calls `pg_tileserv` directly

`pg_tileserv` does not implement end-user authentication. Direct access would
expose private route and coverage data and is prohibited by the product security
boundary.

### UI reverse-proxies the API

Making the static UI server the application proxy would move authentication and
proxy policy into an otherwise simple image. Ingress path routing keeps service
ownership explicit and lets the API remain the sole private-data boundary.

## Consequences

### Positive

- Cookie and CSRF behavior remains same-origin.
- UI and API retain separate images, resources, probes, and scaling.
- Browser configuration uses stable relative URLs.
- CORS is absent from the normal production security surface.
- Private tile authorization remains centralized in the API.

### Negative

- Ingress requires path routing to two Services.
- Local development needs a Vite proxy that matches production paths.
- The UI image needs a correctly configured non-root static server and SPA
  fallback.
- Probe routes need explicit cluster configuration because they are not public
  application routes.

## Conditions That Would Trigger Reconsideration

Reconsider if the product must support independently hosted UI origins, native
clients require a different public gateway topology, or the deployment platform
cannot route one host to multiple Services. Any change must preserve secure
cookie behavior and authenticated private tiles.
