# Raw vector API

The `/api/raw/v1` API stores vectors supplied by your application. It requires PostgreSQL. Legacy collection routes and Go interfaces remain separate.

LocalRecall does not embed raw records, fetch media, or manage source assets. Your application owns embedding models, chunking, asset storage, and publication.

## Deployment

Set `VECTOR_ENGINE=postgres` and `DATABASE_URL`. Install `vector` and `pg_textsearch` through your database administrator. The tested BM25 extension version is 1.3.1.

The service role needs schema creation, table creation, index creation, and DML privileges for its raw registry and tables. Collection deletion requires ownership privileges.
Raw startup does not repair unknown schemas or corrupt tables. Back up the registry and collection tables together.

Raw uses one shared pool per service. The default maximum is four connections. `POSTGRES_POOL_MAX_CONNS` changes this maximum; a DSN `pool_max_conns` takes precedence.
Legacy PostgreSQL collections retain their separate pools. Budget approximately `raw pool maximum + loaded legacy collections × legacy pool maximum` per process.
Include administrative connections and replicas in your database budget.

Raw operations have a 30-second deadline and a five-second database lock timeout. Server shutdown drains HTTP requests, cancels outstanding work, and closes the raw pool.

## Authentication and readiness

Raw routes use the existing `API_KEYS` authentication boundary. **When `API_KEYS` is unset, routes are unauthenticated. Use this mode only on a private, trusted network; do not expose it publicly.** Keys do not provide collection-level authorization or tenant isolation.
Treat metadata filters as query predicates, not access controls. Use a trusted application gateway when users require different permissions.

Read `GET /api/raw/v1/capabilities` before selecting a search mode. Implementation support and current readiness are separate fields.
A core database outage produces unavailable responses. Missing BM25 or ANN support disables dependent operations rather than silently changing retrieval semantics.
Non-PostgreSQL raw requests return HTTP 501 before mutation. Legacy backends remain available independently.

Errors distinguish invalid requests (400), authentication failures (401), missing collections (404), conflicts (409), oversized bodies (413), unsupported operations (501), and unavailable dependencies (503).

## Create and populate a collection

Use your deployment address and authentication header in these requests:

```http
POST /api/raw/v1/collections
Content-Type: application/json

{"name":"catalog-v1","dimensions":3,"metric":"cosine","embedding_space_id":"example-v1"}
```

The response contains a UUID `collection_id`. Raw names occupy a separate namespace from legacy names.
Dimensions, embedding space, metric, and lexical configuration are immutable. Dimensions range from 1 to 2000. Only cosine similarity is supported.
Lexical indexing defaults to enabled. Its configuration captures `BM25_TEXT_CONFIG` at creation, or `english` when unset.

```http
POST /api/raw/v1/collections/COLLECTION_ID/records/upsert
Content-Type: application/json

{"embedding_space_id":"example-v1","records":[{"id":"item-1","vector":[1,0,0],"text":"An astronomy telescope","metadata":{"category":"science"}}]}
```

Vectors must contain finite values and a nonzero norm. LocalRecall normalizes vectors for storage.
Each upsert replaces the entire record. Omitted text clears text; omitted metadata becomes an empty object.
A batch contains at most 100 unique IDs. The whole batch commits atomically. Request bodies cannot exceed 8 MiB.

## Retrieve records

```http
POST /api/raw/v1/collections/COLLECTION_ID/search
Content-Type: application/json

{"mode":"vector","accuracy":"exact","embedding_space_id":"example-v1","vector":[1,0,0],"top_k":10,"filter":{"metadata_eq":{"category":"science"}}}
```

Choose `vector`, `lexical`, or `hybrid` explicitly. Lexical queries supply `text` instead of a vector. Hybrid queries supply both.
Vector-bearing searches default to `approximate`. Use `exact` when you require exhaustive filtered ranking.
Approximate search can return fewer results than requested, especially with selective filters. It does not guarantee recall.

Hybrid search combines independent ranks with equal-weight reciprocal rank fusion. Each contribution is `0.5 / (60 + rank)`.
Each retrieval branch considers `min(1000, max(100, 10 * top_k))` candidates. Responses expose component ranks and fusion parameters.
Raw lexical retrieval never substitutes native full-text search for BM25.

Additional routes:

| Method | Collection-relative path | Purpose |
| --- | --- | --- |
| POST | `/records/get` | Fetch 1–100 IDs; report missing IDs |
| POST | `/records/list` | Traverse records with a keyset cursor |
| POST | `/records/delete` | Delete IDs or a bounded metadata selection |
| POST | `/reset` | Delete all records transactionally |
| DELETE | collection URL | Delete the descriptor and table atomically |

List pages default to 50 records and allow at most 100. Cursors bind to collection, filters, and projection.
Cursors do not provide snapshots across concurrent writes. Filter deletion rejects more than 1000 matches without partial deletion.

## Retries and migrations

Writes use last-committed-write-wins semantics. Retrying an older batch can overwrite a newer record.
After an uncertain commit, read the affected IDs before deciding whether to retry. The API has no revision locks or idempotency receipts.

Reset is a transactional deletion, not a barrier against concurrent writers. Stop your writers when you require an empty collection after reset.

For an incompatible embedding space, create a new collection. Populate and validate it before switching your application's active collection reference.
Your application owns that switch. Keep the old collection until in-flight readers finish, then delete it explicitly.
LocalRecall does not activate generations or automatically remove stale collections.

## Verification and evidence limits

Run `sh test/raw/verify.sh all` from the worktree root after provisioning the [genuine consumer dependencies](../test/raw/LOCALAI-SMOKE.md).
Missing dependencies, failed suites, and skipped required tests fail this command. Logs and private artifacts stay under `.sdd-work`.
The runner uses disposable databases and owned loopback processes, not existing host services.

The synthetic retrieval fixture contains 600 three-dimensional vectors with a selective 1-in-17 filter. Logs record EXPLAIN, recall@10, and underfill.
These measurements test query behavior; they do not predict production latency or embedding quality.
The legacy planning fixture uses 2000 eight-dimensional synthetic vectors. Its plan checks vector index ordering.

The real consumer fixture uses pinned LocalAI source and genuine 384-dimensional MiniLM text embeddings. Provider identity differs from consumer identity.
Its forwarding counter measures actual consumer embedding requests and checks zero additional requests during raw operations.
This evidence does not establish multimodal quality, large-corpus throughput, or production resource limits.

### Recorded acceptance sample

The owned attempt5 full gate passed, including ten race-enabled repetitions of
outage recovery and active-query shutdown. An actual `pg_ctl stop/start` retained
the vectors; unavailable operations returned the raw unavailable error (HTTP 503),
and readiness became false. The supervisor keeps private tmpfs alive, and restart
preserves the required BM25 preload. Shutdown canceled a database-lock waiter and
released all sessions and pool connections.

The real consumer overlap sample held one embedding HTTP request open for
28.13 ms while three raw collections completed writes and exact searches.
The counter observed exactly one legacy request and zero additional raw requests.
Two legacy collections and one shared raw pool used three sampled database
sessions, excluding the observer connection; the configured pool ceiling was 12.
This is a sampled count, not a continuous peak measurement. Public catalog objects
numbered 8 before raw bootstrap, 30 with the overlap collections, and 18 after
removing them: 12 objects removed. The remainder includes shared raw schema and
the second legacy collection, not leaked per-collection indexes.
The raw lifecycle and overlap portion took 71.04 ms. These are local synthetic
measurements, not throughput or production sizing guarantees.

The filtered 600-vector fixture returned 10 of 10 results from 36 eligible rows:
recall@10 was 1.00 and underfill was zero. Planner choice can affect this result;
it does not establish universal ANN recall. Full EXPLAIN JSON and timings are in
`.sdd-work/logs/full-attempt5.log`; repeated suites are in `race10.log`, legacy
coverage in `legacy.log`, and downstream evidence in `consumer-all.log`.
