# Architectural Design Document: Idempotent Payment Processing API

This document details the engineering rationale, distributed systems theory, and architectural trade-offs behind the Idempotent Payment Processing API.

---

## 1. Problem Statement: The Fallacy of Network Reliability

In distributed systems, the network is fundamentally unreliable. When a client initiates an HTTP `POST /v1/payments` request, three failure modes can occur:

1. **Request Drops in Transit**: The request never reaches the server; no money is charged.
2. **Server Crashes During Execution**: The server processes the charge or reaches the payment gateway, but crashes before acknowledging the client.
3. **Response Drops in Transit**: The server successfully processes the payment and charges the card, but a network disconnect or timeout prevents the HTTP 201 response from reaching the client.

From the client's perspective, Failure 1 and Failure 3 look identical: a network timeout or connection reset. If the client blindly retries a non-idempotent endpoint, the customer risks being **double-charged**.

Idempotency guarantees that an operation can be applied multiple times without changing the result beyond the initial application:
$$f(f(x)) = f(x)$$

---

## 2. Why Idempotency Keys?

### Client-Generated vs. Server-Generated Keys
An idempotency key is a unique client-supplied identifier (typically a UUID v4) sent in the HTTP request header:
```http
Idempotency-Key: 9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d
```

- **Why must the client generate it?**  
  If the server generates the key, a network drop during key generation requires a retry, which creates a recursive idempotency problem. By generating the key on the client side at the inception of the user action (e.g., when the user clicks the "Pay" button), the key uniquely binds to the *intent* of that specific user action.
- **Header vs. Body**:  
  Placing `Idempotency-Key` in the HTTP header separates transport concerns (deduplication) from domain payload data (amount, currency).

---

## 3. Why Canonical Request Hashing?

Storing only the idempotency key is insufficient. Consider this scenario:
1. A client submits a request with `Idempotency-Key: order_123` for \$15.00.
2. Due to a client-side bug, another request uses the same key `order_123`, but with amount \$150.00.

If the server only checked the key's existence, it would return the cached \$15.00 receipt for the \$150.00 request. This would be a catastrophic data mismatch.

### The Solution: Deterministic SHA-256 Request Fingerprint
The service binds each idempotency key to a cryptographic digest of the request:
$$\text{RequestHash} = \text{SHA256}(\text{METHOD} + \text{"\n"} + \text{PATH} + \text{"\n"} + \text{CanonicalBody})$$

### Why Canonicalization is Essential
In JSON, object key ordering is unordered by specification:
```json
{"amount": 1500, "currency": "INR"}
{"currency": "INR", "amount": 1500}
```
If hashed directly as raw bytes, differences in whitespace or JSON key serialization would produce different SHA-256 hashes, falsely triggering a `422 Payload Mismatch`.

Our `CanonicalizeJSON` implementation:
1. Parses incoming JSON into generic dynamic data structures (`any`).
2. Re-serializes the data structure using Go's `json.Marshal`, which guarantees lexicographical sorting of map keys and eliminates extraneous whitespace.
3. Hashes the canonical output, ensuring identical semantic payloads always yield identical hashes.

---

## 4. Why Database Constraints Over In-Memory Locks?

A common fresher mistake is using Go's `sync.Mutex` or in-memory map to manage concurrency:
```go
// ANTI-PATTERN: Never use this for distributed idempotency
var mu sync.Mutex
var activeKeys = make(map[string]bool)
```

### Why In-Memory Locks Fail:
1. **Multi-Instance Deployments**: Production systems run behind load balancers with multiple stateless container replicas. An in-memory mutex only protects requests within a single process. Two simultaneous requests routed to different containers will bypass the lock.
2. **Process Restarts**: If the container restarts or crashes, all in-memory lock state vanishes.

### The Solution: Database-Enforced Atomic Locks
PostgreSQL provides ACID transactional guarantees and unique constraints. We enforce concurrency control using an atomic `INSERT ... ON CONFLICT DO NOTHING`:

```sql
INSERT INTO idempotency_keys (key, request_hash, status, locked_at, expires_at, created_at)
VALUES ($1, $2, 'in_progress', NOW(), NOW() + INTERVAL '24 hours', NOW())
ON CONFLICT (key) DO NOTHING
RETURNING key;
```

- When 50 concurrent goroutines or containers execute this simultaneously for the same key, PostgreSQL's index b-tree lock guarantees that **exactly one row is inserted**.
- The winning request acquires the key (`Acquired: true`).
- The remaining 49 requests receive zero rows (`Acquired: false`) and immediately read the existing row, returning HTTP 409 Conflict with a `Retry-After: 5` header.

---

## 5. In-Progress Handling & Crash Recovery Strategy

### The Problem of Orphaned In-Progress Keys
If worker node $A$ acquires a key and crashes while calling the external payment gateway, or if the gateway times out:
- Without recovery, the key remains `status = 'in_progress'` indefinitely.
- All future retries by the client would be permanently blocked with HTTP 409 Conflict.

### The Recovery Rule
An `in_progress` key is considered **stuck** if its `locked_at` timestamp is older than the recovery threshold (30 seconds):
$$\text{Now} - \text{LockedAt} \ge 30\text{s}$$

When a retry arrives for a stuck key, the service attempts an atomic reclamation query:
```sql
UPDATE idempotency_keys
SET locked_at = NOW(),
    request_hash = $2
WHERE key = $1
  AND status = 'in_progress'
  AND locked_at < NOW() - INTERVAL '30 seconds'
RETURNING response_body;
```

### Why This is Race-Condition Proof
If two concurrent retries both attempt to reclaim a stuck key:
1. PostgreSQL executes the `UPDATE` with row-level write locks.
2. The first query updates the row and returns `RowsAffected = 1`.
3. The second query finds that `locked_at` was just updated to `NOW()`, which is no longer `< NOW() - 30s`. Its `RowsAffected` is `0`.
4. The winning worker proceeds to execute; the losing worker receives HTTP 409. **Two workers can never both reclaim the same key.**

---

## 6. The Payment State Machine

Financial transactions must enforce a monotonic, deterministic lifecycle. The state machine allows only valid transitions:

```
           +--------------+
           |   pending    |
           +--------------+
             /          \
            /            \
           v              v
    +-----------+    +-----------+
    | succeeded |    |  failed   |
    +-----------+    +-----------+
    (terminal)       (terminal)
```

- `pending -> succeeded`: Permitted when the gateway approves the transaction.
- `pending -> failed`: Permitted when the gateway declines the transaction or cards fail.
- `succeeded -> *` or `failed -> *`: Strictly forbidden (`ErrInvalidStateTransition`). Once finalized, financial records are immutable.

---

## 7. Architectural Trade-offs & Design Decisions

| Decision | Selected Approach | Trade-off / Rationale |
| :--- | :--- | :--- |
| **HTTP Routing** | Go 1.22+ Standard `net/http` | Zero third-party router dependencies. Native pattern matching (`POST /v1/payments`, `GET /v1/payments/{id}`) is efficient and standard in modern Go. |
| **Database Driver** | `pgx/v5` with `pgxpool` | Significantly faster than `database/sql` + `pq`, supports native binary protocols, connection lifecycle management, and typed PostgreSQL errors. |
| **Response Storage** | `JSONB` in `idempotency_keys` | Storing the exact serialized HTTP response alongside the status code ensures byte-level consistency when replaying responses. |
| **Key Eviction** | 24-hour TTL + Background Worker | Idempotency keys are ephemeral; keeping them indefinitely degrades index performance. A periodic background job deletes expired records. |

---

## 8. Scaling to Higher Traffic (10,000+ RPS)

While PostgreSQL easily handles hundreds of requests per second with connection pooling, scaling to enterprise volumes (Stripe/PayPal scale) introduces the following architectural evolutions:

### 1. Redis Distributed Lock & Cache Layer
- Place a Redis cluster in front of PostgreSQL.
- Use Redis `SET key value NX PX 30000` for sub-millisecond fast-path acquisition.
- Cache completed responses in Redis with a 24-hour TTL, offloading 95% of replay queries from the primary relational database.

### 2. Transactional Outbox Pattern
- Rather than synchronously awaiting gateway HTTP calls during the request cycle, write payment intent to an `outbox` table in the same transaction.
- A reliable CDC (Change Data Capture) tool like Debezium or an asynchronous polling worker publishes events to Apache Kafka or AWS SQS.

### 3. Sharding & Partitioning
- Partition the `idempotency_keys` table by consistent hashing of the key (`hash(key) % N`).
- Partition by date ranges (`PARTITION BY RANGE (created_at)`) so old partitions can be dropped instantly via `DROP TABLE` without expensive vacuuming.
