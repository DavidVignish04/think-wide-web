# Campaign Events Service — Engineering Assignment Notes

This document contains the conceptual and analytical sections of the assignment: **Part 1 (Read and Think)**, **Part 4 (Scale Memo — 100M events/day)**, and **Part 5 (The Angry Marketer)**, followed by a summary of completed work and future improvements.

---

## Part 1 — Read and Think

### 1. Problem Interpretation
Relay (a fictional marketing communication platform) sends marketing messages on behalf of customers via third-party providers (e.g., SendGrid, Mailgun, Twilio). Providers call webhooks to notify Relay of event lifecycle transitions: `sent`, `delivered`, `opened`, and `clicked`.

We need an ingestion and querying service that:
1. **Receives batches of events** from providers via `POST /events`.
2. **Deduplicates events** because providers frequently retry requests upon network timeouts or transient errors.
3. **Serves per-campaign aggregate statistics** via `GET /campaigns/{id}/stats` to power a live dashboard.
4. **Handles late and out-of-order event delivery** (e.g., `delivered` arriving after `opened`, or events delayed by hours).

---

### 2. Assumptions
- **Deduplication Key**: Deduplication is performed by `event_id`. The third-party provider assigns this ID, and it uniquely identifies an atomic event occurrence.
- **Metric Definitions**: "How many opened" can mean either total open occurrences or unique contacts who opened. The dashboard likely requires both, so we track both `total_opened` and `unique_opened` (and similarly for clicks).
- **Valid Event Types**: Exactly `sent`, `delivered`, `opened`, and `clicked` (case-sensitive, strictly lowercase).
- **Required Fields**: Events missing `event_id`, `campaign_id`, `contact_id`, or `type` are rejected as invalid.
- **Timestamp Validation**: Timestamps must be valid RFC3339 / ISO-8601 strings. Invalid timestamps are rejected because accurate temporal display and ordering on the dashboard depend on them.
- **Partial Acceptance in Batches**: A batch `POST` should not fail atomically due to a single malformed event. Processing valid items while returning structured errors for invalid items is standard webhook UX and prevents head-of-line blocking.
- **Storage Scope**: An in-memory, thread-safe data store is sufficient for the current scale (~few thousand events/day).
- **Security & Network Boundary**: No authentication/authorization is implemented; the service is assumed to run within an internal network behind an API Gateway / reverse proxy handling TLS and auth.
- **Metadata**: The `metadata` field is an optional, opaque key-value JSON object. It should be stored as-is without domain schema validation.

---

### 3. Ambiguities & Resolutions
- **Definition of a Duplicate**:
  - *Ambiguity*: Is a duplicate defined by `event_id`, or by the tuple `(campaign_id, contact_id, type)`?
  - *Resolution*: The assignment specifies that providers retry "the same event" and `event_id` is assigned by the provider. Therefore, `event_id` is the primary and natural deduplication key. A contact can legitimately open an email multiple times, producing distinct `event_id`s.
- **Total Opens vs. Unique Opens**:
  - *Ambiguity*: Does the stats endpoint track gross opens or unique recipients?
  - *Resolution*: Marketers care about both raw engagement (total opens/clicks) and reach (unique recipients who opened/clicked). We compute both in the stats endpoint.
- **HTTP Status for Partial Failures**:
  - *Ambiguity*: What status code should `POST /events` return if 8 out of 10 events are valid?
  - *Resolution*: Return `HTTP 200 OK` with a detailed payload breaking down `accepted`, `duplicates`, and `errors`. A `4xx` error would trigger full-batch retries from naive webhook providers, leading to duplicate processing.
- **Handling Duplicate Event Submissions**:
  - *Ambiguity*: Should duplicates return an error?
  - *Resolution*: No. Return `200 OK` and count them as duplicates in the batch response summary. Providers retry automatically; failing retries harms provider deliverability metrics.
- **Conflicting Event Payloads (Same `event_id`, Different Data)**:
  - *Ambiguity*: If the same `event_id` arrives with a differing timestamp or event type, what happens?
  - *Resolution*: First-write-wins. The `event_id` is canonical; subsequent modifications under the same ID are discarded as retry noise.
- **Timestamp Limits**:
  - *Ambiguity*: Should events far in the future or past be rejected?
  - *Resolution*: For now, we only validate parseability to avoid rejecting legitimately delayed provider retries.
- **Maximum Batch Size**:
  - *Ambiguity*: What is the maximum acceptable batch size for `POST /events`?
  - *Resolution*: Uncapped in the prototype, but bounded by memory; in production, an explicit ceiling (e.g., 500 or 1,000 events) would be enforced.

---

### 4. Questions for the Product Manager
1. **Metrics Definition**: Does the dashboard require total opens, unique opens per contact, or both? Are rates (e.g., open rate = unique opened / delivered) calculated by the backend or dashboard UI?
2. **Event Immutability & Corrections**: Can providers emit event updates or corrections for an existing `event_id`, or can we strictly enforce first-write-wins immutability?
3. **Lateness / Retention Windows**: Is there a cutoff window for late events (e.g., ignore/flag events arriving >30 days late)?
4. **Contact Timeline**: Do we need to expose an individual contact's chronological interaction timeline, or are campaign-level aggregates sufficient for the foreseeable roadmap?
5. **Event Replay & Deletion**: Do we need to support GDPR "Right to be Forgotten" contact data deletion, or whole-campaign data deletion/replays?
6. **Freshness SLA**: What is the acceptable latency between webhook receipt and dashboard reflection? (e.g., sub-second real-time vs. 5–30 second eventual consistency?)
7. **Campaign Lifecycle**: Can events arrive for campaigns before they are formally registered in Relay's core system? Should unregistered campaign IDs be accepted or rejected?

---

### 5. Prioritisation
1. **`POST /events` with Robust Validation and Deduplication**: The core entry point. Malformed or duplicate data corrupts downstream metrics immediately.
2. **`GET /campaigns/{id}/stats`**: The primary query endpoint required to power the live dashboard.
3. **Concurrency and Deduplication Tests**: Race conditions and overcounting are the most embarrassing customer-facing bugs in analytics services.
4. **`GET /campaigns/{id}/events` (with Pagination)**: Detailed inspection endpoint; secondary to aggregate metrics.
5. **Edge Case Handling (Seed Data)**: Covering missing fields, malformed timestamps, unknown types, and zero counts.
6. **Documentation and Architecture Specs**: Explaining trade-offs, operational playbooks, and scale strategy.

---

## Part 4 — Scale Memo (100M events/day)

### 1. What Breaks First
At 100M events/day, the system must process an average of:
$$\frac{100{,}000{,}000}{86{,}400} \approx 1{,}157 \text{ events/sec sustained}$$
Peak volume during major marketing blasts (e.g., Cyber Monday or 09:00 AM local time send windows) typically reaches 5x–10x the average: **5,000 to 12,000 events/second**.

**The initial points of failure will be:**
1. **In-Memory Storage Exhaustion**: 100M events with metadata (~300 bytes each) equals ~30 GB of raw data per day, or ~900 GB/month. An in-memory Go map will trigger OOM (Out Of Memory) kills within hours.
2. **Global Mutex / Lock Contention**: A single in-memory store guarded by a `sync.RWMutex` serializes writes. Under thousands of concurrent requests, lock contention creates severe thread starvation, elevated p99 latency, and HTTP request timeouts.
3. **HTTP Webhook Connection Dropping**: Providers send large batch bursts. Synchronously parsing, deduplicating, and persisting in the HTTP request loop will back up HTTP worker pools and cause providers to back off or fail.

---

### 2. Architectural Evolution (What to Change, in Order)

```
[ Providers ]
      │ (POST /events batches)
      ▼
[ Edge Load Balancer / Ingress ]
      │
      ▼
[ Webhook Ingestion API Nodes (Stateless Go)] ──▶ [ Kafka Topic: raw-events ] (partitioned by campaign_id)
      │ (HTTP 200 Fast ACK)                               │
                                                          ▼
                                            [ Stream Processors / Consumers ]
                                            ├── Dedup via Redis / RocksDB (sliding window)
                                            ├── Write Raw Events ──▶ [ ClickHouse / TimescaleDB ]
                                            └── Update Aggregates ──▶ [ PostgreSQL / Redis Cache ]
                                                                             ▲
                                                                             │
[ Marketer Dashboard ] ──▶ [ Stats API Service ] ────────────────────────────┘
```

1. **Decouple Ingestion with a Message Queue (Kafka)**: Webhook receivers do minimal validation (valid JSON payload), write raw message batches to Kafka topic `events.raw`, and immediately return HTTP 200.
2. **Horizontal Scaling of Ingestion Nodes**: Stateless HTTP ingestion pods scale dynamically behind a load balancer based on CPU and request latency.
3. **Persistent Analytics Store (ClickHouse or PostgreSQL)**: Offload raw events to an append-only columnar database (e.g., ClickHouse) optimized for event analytics, partitioning by `(campaign_id, date)`.
4. **Pre-aggregated Counter Tables / Stream Aggregation**: Use Kafka consumers (or Flink/ksqlDB) to increment counter summaries in PostgreSQL/Redis as events arrive, rather than scanning millions of raw rows on every dashboard load.
5. **Partitioning by `campaign_id`**: Partition Kafka topics and database shards by `campaign_id` so that all events for a campaign are processed by deterministic consumers in order.

---

### 3. API Contract Changes
- **`POST /events`**: Remains functionally the same from the provider's viewpoint (accepts batch, returns accepted count). It becomes a fast buffering endpoint.
- **`GET /campaigns/{id}/stats`**: Transitions from strictly real-time to **eventually consistent** (lagging by hundreds of milliseconds to a few seconds). We can introduce an optional `data_freshness_timestamp` or `cached_at` in the JSON response to inform the UI of ingestion lag.
- **`GET /campaigns/{id}/events`**: Requires **cursor-based pagination** (`?limit=50&cursor=eyJ...`) instead of naive offset/limit, ensuring stable pagination against an actively streaming append-only database.

---

### 4. Storage Architecture
- **Raw Event Store**: Columnar store (ClickHouse) with an `events` table:
  - Partition key: `toYYYYMM(timestamp)`
  - Order key: `(campaign_id, event_type, contact_id, timestamp, event_id)`
  - TTL: Automatically archive or drop raw logs after 90 days.
- **Aggregate Counter Store**: PostgreSQL or Redis:
  - Table: `campaign_stats (campaign_id UUID PRIMARY KEY, sent BIGINT, delivered BIGINT, opened BIGINT, clicked BIGINT, unique_opened BIGINT, unique_clicked BIGINT, updated_at TIMESTAMPTZ)`
  - Unique contacts can be tracked using HyperLogLog in Redis for rapid cardinality estimation, or deduplicated sets within a defined sliding window.

---

### 5. Message Queue Trade-offs & New Problems
- **Queue Technology**: Apache Kafka or AWS Kinesis.
- **New Challenges Introduced**:
  - **At-least-once delivery duplicates**: Kafka ensures messages are delivered at least once; consumer retries after failure will replay batches.
  - **Consumer Lag**: Ingestion bursts can create temporary consumer lag, causing dashboard stats to appear delayed.
  - **Partitioning Skew**: A massive viral campaign might overwhelm a single partition if partitioned strictly by `campaign_id`. (Mitigation: compound keys or salt hashing for massive campaigns).
  - **Dead-Letter Queues (DLQ)**: Malformed "poison-pill" messages must be routed to a DLQ so they do not block topic consumption.

---

### 6. Where Duplicates Sneak In
1. **Network Retries between Provider and Ingestion Node**: Provider sends batch, ingestion node writes to Kafka, but network blips before the HTTP 200 response reaches provider. Provider retries the entire batch.
2. **Consumer Rebalance or Crash**: Consumer processes a batch of 500 events and writes to the DB, but crashes before committing its Kafka consumer offset. The replacement consumer re-consumes the same 500 events.
3. **Concurrent Duplicate Delivery**: Provider issues parallel retries across different ingestion pods at the exact same millisecond; both pods write to Kafka before any deduplication filter has executed.

---

### 7. Keeping Counters Trustworthy
- **Idempotent Writes via Distributed Deduplication**:
  - Use an in-memory sliding window cache (Redis with TTL or RocksDB) storing `event_id`s for 24–72 hours.
  - In ClickHouse, leverage the `ReplacingMergeTree` engine with `event_id` as the versioned deduplication key.
  - In relational counter updates: use conditional idempotent updates or unique constraints (`INSERT INTO event_dedup (event_id) VALUES (...) ON CONFLICT DO NOTHING`).
- **Periodic Asynchronous Reconciliation Job**:
  - Run an hourly or nightly batch query against the raw event store to recompute exact aggregate counts (`SELECT count(*), count(DISTINCT contact_id)... WHERE campaign_id = ?`).
  - Detect and reconcile any drift in the real-time counter table caused by transient consumer anomalies.
- **Monotonic Guardrails**:
  - Sent and Delivered counts should be monotonically increasing under standard operations; alert if counters decrease outside of scheduled reconciliations.

---

### 8. Observability & Monitoring
- **Ingestion Metrics**: Ingestion RPS, p50/p95/p99 HTTP response times, provider error rates (4xx, 5xx).
- **Queue Metrics**: Kafka consumer lag per partition, message backlog size, DLQ write rates.
- **Deduplication Rate**: Percentage of incoming events flagged as duplicates (sudden spikes indicate provider retry storms).
- **Query Performance**: `GET /campaigns/{id}/stats` p99 latency, cache hit ratios.
- **System Health**: Worker memory usage, CPU load, disk I/O, database connection pool exhaustion.
- **Reconciliation Delta**: Drift metric measuring discrepancy between pre-aggregated counters and raw log audits.

---

### 9. What NOT to Solve Yet
- **Global Total Ordering**: Enforcing global ordering across independent campaigns adds massive consensus overhead (Raft/Paxos) without any business value.
- **Complex Complex Event Processing (CEP)**: Do not build multi-event sequence triggers (e.g., "opened within 5 minutes of delivery") until required.
- **Cross-Region Active-Active Replication**: Adds multi-master replication conflicts; keep ingestion within a single primary cloud region until global latency necessitates edge ingestion.
- **Sub-Millisecond Exactly-Once Semantics**: High complexity and latency cost; at-least-once pipeline with idempotent database persistence provides the exact same correctness guarantees at a fraction of the cost.

---

## Part 5 — The Angry Marketer

### Scenario
A marketer reviews their dashboard on launch morning:
- **10:00 AM**: Sent: 1,000,000 | Delivered: 970,000 | Opened: 250,000 | Clicked: 20,000
- **10:30 AM**: Sent: 1,000,000 | Delivered: 975,000 | Opened: 248,000 | Clicked: 20,000

The marketer submits an urgent ticket: *"Delivered went up, but Opened went DOWN by 2,000! Are you losing my customer engagement data?"*

---

### Plausible Explanations (Before Assuming a Software Bug)
1. **Asynchronous Deduplication Window**: A burst of duplicate open events from an aggressive provider retry storm was ingested prior to 10:00 AM and tentatively counted in real-time. A scheduled deduplication/compaction process completed between 10:00 and 10:30, pruning the duplicate records and adjusting the metric to the true count.
2. **Scheduled Reconciliation / Recount**: A background reconciliation job executed at 10:15 AM, querying raw canonical event logs to correct real-time drift, revealing that the true open count was 248,000.
3. **Data Privacy / Contact Suppression (GDPR/CCPA)**: A compliance or suppression request purged a batch of contacts (and their associated interaction events) from the system between 10:00 and 10:30.
4. **Provider Event Retraction / Correction**: Email service providers sometimes issue retraction webhooks or report bot-detection adjustments (e.g., Apple Mail Privacy Protection or security scanner pre-fetch filtering), invalidating automated false opens.
5. **Campaign Reassignment / Re-attribution**: An admin or automated rule reassigned a misattributed batch of events from this campaign ID to another related campaign.
6. **Cache Invalidation & Staleness**: The 10:00 AM dashboard query fetched from a stale or partitioned cache node that held an inaccurate estimation, whereas the 10:30 AM reading hit an updated cache or primary store.
7. **Metric Semantics Shift in Dashboard**: The dashboard switched from rendering **Total Opens** (which was 250K) to **Unique Contact Opens** (which is 248K) due to a UI toggle or deployment.
8. **Sliding Time Window Display**: The dashboard view was filtered to a rolling window (e.g., "Last 24 Hours"), and an earlier block of opens from the previous day aged out of the window.

---

### Is "Delivered" Going Up Suspicious?
**No, delivered increasing is completely normal and expected.**
- Providers do not deliver 1,000,000 emails instantaneously; MTAs (Mail Transfer Agents) throttle delivery to comply with receiver rate limits (Gmail, Yahoo, Microsoft).
- Delivery reports can arrive minutes or even hours after sending as messages clear provider queues or retry temporary soft bounces (`421` / `451`).
- Gaining 5,000 newly confirmed deliveries over 30 minutes on a 1M send represents a 0.5% tail delivery rate, which is standard production behavior.

---

### Diagnostic Runbook: What to Check First
1. **Raw Database Count**:
   ```sql
   SELECT count(*), count(DISTINCT contact_id) 
   FROM events 
   WHERE campaign_id = '...' AND type = 'opened';
   ```
   Determine whether raw events were physically removed, or if this is purely a query/presentation artifact.
2. **Job Audit Logs**: Check whether background dedup jobs, reconciliation scripts, or GDPR deletion workflows executed between 10:00 and 10:30.
3. **Provider Ingestion Logs**: Inspect incoming webhook logs for event retractions or bot-filtering metadata from the provider.
4. **Dashboard Query & Release Logs**: Verify if a frontend release occurred around 10:00–10:30 altering query parameters (e.g., total vs. unique opens).
5. **Application Error Logs**: Check for database connection timeouts, counter rollback exceptions, or node crashes during stats aggregation.

---

### How to Decide: Bug vs. Expected Behavior
- **It is a Bug if**: The raw events table still contains the 250,000 valid, unique open events, but the stats query or counter table dropped to 248,000 due to race conditions, arithmetic underflow, or faulty cache eviction.
- **It is Expected Behavior if**: The audit logs confirm that 2,000 duplicate events were correctly deduplicated, contacts were deleted via compliance requests, bot-opens were retracted by the provider, or the previous 250,000 figure was an overcounted estimation corrected by reconciliation.

---

## Summary of Implementation & Future Roadmap

### What Was Completed
- **`POST /events` Ingestion**: Per-item parsing via `json.RawMessage`, resilient error isolation, type validation, and deduplication by `event_id`.
- **`GET /campaigns/{id}/stats`**: Accurate aggregation tracking sent, delivered, total opens, unique opens, total clicks, and unique clicks.
- **`GET /campaigns/{id}/events`**: Paginated retrieval of raw events supporting `limit` and `offset`.
- **Thread-Safe In-Memory Store**: Mutex-synchronized repository preventing data races under high concurrent load.
- **Automated Test Suite**: Unit, validation, deduplication, and concurrent race-condition tests (`go test -race ./...`).
- **Bug Fixes**: Identified and fixed all 4 concurrency and logic bugs from the debugging exercise.

### What Was Intentionally Skipped (And Why)
- **Persistent Database**: In-memory storage was chosen intentionally to match the assignment scope (~few thousand events/day) without unnecessary operational overhead.
- **Authentication & Rate Limiting**: Left to the surrounding API gateway layer where production perimeter security belongs.

### If I Had Another Day
1. **SQLite / Embedded Persistence**: Back in-memory storage with SQLite or BadgerDB to survive process restarts.
2. **Prometheus Metrics & Structured Logging**: Add request latency histograms, ingestion counters, and OpenTelemetry tracing.
3. **Provider-Level Rate Limiting**: Implement leaky-bucket rate limiting per provider IP to safeguard the ingestion pipeline.
4. **Health Check & Readiness Endpoints**: Add `/healthz` and `/readyz` for Kubernetes orchestration.
5. **OpenAPI / Swagger Specification**: Provide an interactive API contract documentation file for consumers.
6. **Seed Data Integration Test**: Automated benchmark loading the complete seed dataset and asserting exact metric counts.
