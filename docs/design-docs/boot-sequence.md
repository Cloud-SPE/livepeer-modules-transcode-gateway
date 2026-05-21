# Boot sequence

What `gateway/cmd/gateway/main.go` does between process start and
"listening on :4000". Mirrors `livepeer-modules-openai/gateway/src/index.ts`
in spirit; the Go shape is different.

## Steps

1. **Parse env.** `caarlos0/env` populates `config.Config{}`. Missing
   required vars exit with a clear error. Pepper variables that are
   unset log a startup warning.
2. **Connect Postgres.** `pgxpool.New(ctx, cfg.DatabaseURL)`. Failure
   to connect kills the process — there is no DB-less mode.
3. **Run migrations.** `golang-migrate` against
   `gateway/migrations/`, recorded in `schema_migrations`. Idempotent.
4. **Open S3 client.** `aws-sdk-go-v2` configured with `S3_ENDPOINT`
   + path-style addressing (MinIO in dev, any S3-compatible store
   elsewhere). A startup head-bucket call confirms reachability;
   failure logs a warning but does not exit (the SaaS surface still
   works without VOD ingest).
5. **Wire gRPC clients.** Best-effort dial to
   `LIVEPEER_RESOLVER_SOCKET` and `LIVEPEER_PAYER_DAEMON_SOCKET`.
   Missing sockets log a warning; `/api/v1/*` returns 500/503 at request
   time until they're reachable.
6. **Start RegistryCatalog refresh loop.** First refresh runs
   synchronously so `/api/v1/capabilities` is non-empty by the time the
   server accepts traffic. Subsequent refreshes run on a `time.Ticker`
   every `REGISTRY_REFRESH_INTERVAL_MS`.
7. **Construct ServerDeps.** A single struct threaded into every
   handler: db pool, repos, S3 client, payer client, route selector,
   rate limiter, email client, config, logger.
8. **Mount huma API.** Each handler registers a `huma.Operation` —
   that's both the route table and the OpenAPI spec.
9. **Listen.** chi router on `:PORT` (default 4000). `/health`
   composes the per-subsystem checks defined in `RELIABILITY.md`.

Step 9 finishing means the server accepts traffic; the
`Livepeer-Request-Id` middleware is already attached at this point.

## Shutdown

`os.Interrupt` triggers `http.Server.Shutdown(ctx)` with a 15s
deadline. In-flight handlers drain; the registry refresh ticker stops.
The payment + resolver gRPC clients close. The DB pool closes last.

There is no "drain mode" exposed to load balancers in v1 — the
existing `/health` contract is what they read.
