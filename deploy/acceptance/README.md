# Disposable Coverage Acceptance

This stack is isolated in `xdev/workouts-acceptance`. Never point these tools at
the existing `workouts-explorer` release or an external OSM database. Database
storage and worker source/staging storage use emptyDir, not PVC or NFS. The
script rejects ambient non-disposable database URLs and requires an explicit
switch. Cleanup additionally requires confirmation.

Select `COVERAGE_ACCEPTANCE_PROFILE=typical|stress` (default `typical`). Each
profile is one account with a complete 365-day history, not combined users.
The shared definition is `profiles.json`; see
[`coverage-acceptance.md`](../../docs/coverage-acceptance.md) for
activity quotas, distances, durations, calendar rules and verified results.
The full measured tables and evidence limitations are in
[`coverage-acceptance-results.md`](../../docs/coverage-acceptance-results.md).

New fixture runs use revision-97 `dev-20261001` image digests pinned in
`values.yaml` through `image.digests`, including migration hooks, with requests
`25m/64Mi` and limits `250m/256Mi`. The real
Coverage worker retains global matcher concurrency 1. PostGIS uses requests
`100m/256Mi`, limits `1 CPU/1Gi`, 128MB shared buffers, and a 4Gi emptyDir.
The local metrics collector discards traces; saved results contain aggregate
metrics, not SQL, credentials, account identifiers, or private coordinates.

## Provision

Run from the repository root with Helm 3. The installed Helm 4 uses a different
post-renderer plugin interface; this fixture deliberately does not alter the
system Helm configuration. Shared chart digest support defaults to an empty map.

The measured reports retain the image/schema receipts from their captured runs.
Updating future pins does not relabel those measurements. New fixtures use the
consolidated application schema 20/minimum 18; schema-21-capable binaries are not
used against a fresh schema-20 database. UI/Martin collection sidecars now reflect
the current chart defaults and must be included in any new resource measurements.

```sh
export COVERAGE_ACCEPTANCE_DISPOSABLE=true
export COVERAGE_ACCEPTANCE_PROFILE=typical
export COVERAGE_ACCEPTANCE_HELM=/tmp/opencode/linux-amd64/helm
export HELM_PLUGINS=/tmp/opencode
scripts/coverage-acceptance.sh provision
scripts/coverage-acceptance.sh forward
```

Provision refuses an existing namespace; it does not overwrite a seeded stack.
The namespace, ConfigMap and protected database marker bind the selected profile,
definition version and manifest hash. Switching profiles requires cleanup and
reprovisioning, not merely changing an environment variable on a populated stack.
Keep the forward process running in another terminal. It reconnects the same
localhost ports after disposable application rollouts:

- UI and proxied API: `http://localhost:18080/`
- API: `http://127.0.0.1:18081/`
- Martin: `http://127.0.0.1:18083/`
- Aggregate metrics: `http://127.0.0.1:18089/metrics`
- PostgreSQL: `127.0.0.1:15432`

Browser access from the host-cluster Playwright pod is available at
`http://workouts-explorer-ui-x-workouts-acceptance-x-dev.vcluster-dev.svc.cluster.local/`.
The unique disposable ingress is
`https://acceptance.workouts.xdev.fourteeners.local/`. No existing ingress host
is rerouted. The synthetic basemap has roads, blocks, parks, and public invented
names, near (0,0); it does not download or export live map data.

## Seed And Measure

```sh
eval "$(scripts/coverage-acceptance.sh env)"
scripts/coverage-acceptance.sh seed
scripts/coverage-acceptance.sh validate
scripts/coverage-acceptance.sh matcher-smoke
COVERAGE_ACCEPTANCE_PHASE=baseline scripts/coverage-acceptance.sh reads
VIEWPORT_BATCHES=5 scripts/coverage-acceptance.sh viewport
COVERAGE_ACCEPTANCE_BACKLOG=48 scripts/coverage-acceptance.sh queue
COVERAGE_ACCEPTANCE_PHASE=backlog48 scripts/coverage-acceptance.sh reads
COVERAGE_ACCEPTANCE_BACKLOG=96 scripts/coverage-acceptance.sh queue
COVERAGE_ACCEPTANCE_PHASE=backlog96 scripts/coverage-acceptance.sh reads
COVERAGE_ACCEPTANCE_PHASE=warm scripts/coverage-acceptance.sh reads
scripts/coverage-acceptance.sh collect
```

`viewport` measures 35 distinct neighboring private tiles for a 3095x1973 CSS
canvas, with client limits 1/6/16/35 and five batches per source by default.
It records whole-viewport fetch time separately from request percentiles and
feature-bearing versus empty responses. For actual browser source scheduling,
run `npm --prefix ui run test:coverage-viewport` in the same guarded profile
environment; this is not the full visual matrix or a render-acceptance pass.

The graph has 1600 nodes, 3120 edges, and 80 named roads. Typical seeds 365
workouts (219 running, 73 walking, 37 cycling, 36 route-less); stress seeds 730
(292 running, 146 walking, 73 cycling, 219 route-less). All 365 dates in calendar
year 2026 are populated, including future synthetic dates. September contains
30/60 workouts; annual Maps selections contain only the 329/511 routed workouts.
Baseline routed workouts have applied evidence, not an artificial readiness split.
Coverage evidence is precomputed, but writes use real ingest claims/fences,
readiness functions, claimed coverage children, and `persist_coverage_route`.
The benchmark changes route revisions while retaining earlier applied evidence.
Routes preserve the profile's complete mileage/duration and five-second GPS
cadence plus corners. Backlogs revise existing routed workouts across the year
without altering their calendar/type/distance/duration; no 81/313-point toy
replacement is used. The bounded matcher smoke runs one full route of each
routed type through the actual host runner with a read-only OSM role; it does
not measure container-capped capacity. Account reconciliation is deferred for 30 days so
starting the real Coverage worker cannot expand the bounded fixture queue.

Each endpoint uses 30 sequential samples per profile. First calls are cold-ish,
not cold-cache measurements: no OS or database cache is flushed. Stale map
selection refresh cost is included in logical-read latency, and HTTP status
counts plus endpoint windows and active-child counts disclose partial overlap.
Backend response timing is not browser rendering time. The 500ms API p95,
2-second Summary, and 3-second initial-map render targets are targets, not
blanket assertions of success. Read the JSON artifact for measured misses and
profile-specific results.

The collector reads host cAdvisor/Prometheus resource metrics when a read-only
Prometheus forward exists:

```sh
kubectl --context homelab -n monitoring port-forward service/prometheus-stack-prometheus 19090:9090
```

That endpoint uses HTTPS; `collect.cjs` uses loopback `curl -k` for that
port-forward only. Prometheus is not modified. Missing metrics remain null.
Application SDK metrics arrive only after their periodic export interval.

Optional probes and synthetic visual exports:

```sh
scripts/coverage-acceptance.sh diagnostics
scripts/coverage-acceptance.sh general-worker
COVERAGE_ACCEPTANCE_ACTION=benchmark go test ./worker -run '^$' -bench '^BenchmarkCoverageAcceptanceReads$' -benchtime=2s
node deploy/acceptance/export.cjs
scripts/coverage-acceptance.sh token
```

The diagnostic probe occupies a real bounded diagnostic slot for three seconds
and verifies eight `429` responses without increasing concurrency. General
worker probes use a separate owner and 16 public synthetic workouts outside the
OSM provider boundary, queueing no matching children. They check real source
connection, API ingest, physical deletion, and cross-owner denial. A separate
route-less indoor probe exposed an existing split-summary failure; production
code is intentionally not changed by this acceptance work.

The token command prints only a fake, fixture-only bearer to the caller. It is
not included in results or exports. For browser acceptance, inject that bearer
into all `/api/` requests; bearer authentication avoids cookie Origin/CSRF
requirements. No real user's password or token is needed.

## Item-5 Tests

`env` emits separate `acceptance_test` and `acceptance_test_osm` role URLs;
deployed applications use `acceptance_benchmark` and the scratch database named
`osm` on the same disposable server. No deployed worker consumes item-5 jobs.

```sh
export COVERAGE_ACCEPTANCE_DISPOSABLE=true
eval "$(scripts/coverage-acceptance.sh env)"
go test ./worker -run '^TestCoverageRunnerRunOnce.*Integration$' -count=1 -v -timeout=15m
```

The shared runtime suite is in `worker/coverage_runner_integration_test.go`.
All ten cases and three race repetitions pass against the guarded disposable
databases, including after destination/reader-privilege hardening. Run
`make coverage-worker-test` for the same migration/setup/race wrapper used by CI. Normal
acceptance tests and benchmarks skip unless the explicit disposable switch and
an action are supplied. `validate` checks the retained counts and sampled route
digest contract without reseeding. New seeds default the account diagnostics
preference to false; the bounded diagnostic probe temporarily enables it and
restores the previous value.

## Artifacts And Cleanup

`deploy/acceptance/results/{typical,stress}-results.json` contains measured aggregates and
explicit `FAIL` target gaps. Read probes retain measurements before returning
nonzero on a p95 budget miss; continue to `collect` to summarize those failures.
`collect` defaults to the selected profile's path in `results/`; browser report
names remain `docs/coverage-visual-{typical,stress}-results.json`. See the
[human-readable report](../../docs/coverage-acceptance-results.md) for full tables.
Raw duplicate SDK/pod snapshots remain only in
`/tmp/opencode/coverage-{profile}-acceptance-{namespace_uid}-raw.json`. The generated directory
`/tmp/opencode/coverage-{profile}-exports/{namespace_uid}/` contains optional real synthetic API responses
and Martin MVT exports; these contain only invented
geometry and disposable toy identifiers. Neither contains bearer credentials.
`/tmp/opencode/coverage-*.json` contains intermediate aggregate samples.
Intermediate evidence is bound to the namespace UID, profile/version and manifest
hash; a new fixture does not
silently inherit historical measurements. Diagnostics are recorded explicitly,
and unmeasured fields remain null rather than receiving historical defaults.

Leave the namespace and databases available until cleanup is confirmed:

```sh
COVERAGE_ACCEPTANCE_DISPOSABLE=true COVERAGE_ACCEPTANCE_CLEANUP_CONFIRMED=yes scripts/coverage-acceptance.sh cleanup
```

The emptyDir database survives application and Helm rollouts, but not replacement
of its PostgreSQL pod. Namespace deletion is the intended final cleanup.
