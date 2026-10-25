# Idempotent Payment Processing API

A production-grade, highly reliable RESTful Payment Processing API implemented in idiomatic Go (1.22+).

This service solves a fundamental distributed systems challenge: **ensuring exactly-once payment processing** across client retries, network dropouts, and concurrent execution, backed by PostgreSQL atomic guarantees.

---

## Architecture Overview

```
                           +------------------------+
                           |  Client / Webhook / UI |
                           +------------------------+
                                       |
                   HTTP POST /v1/payments [Idempotency-Key]
                                       |
                                       v
         +-----------------------------------------------------------+
         |                     HTTP Transport Layer                  |
         |  - Request ID Injection (X-Request-ID)                    |
         |  - Structured Logging (slog)                              |
         |  - Panic Recovery Handler                                 |
         |  - Request Body Size Limiter (1 MB)                       |
         +-----------------------------------------------------------+
                                       |
                                       v
         +-----------------------------------------------------------+
         |                 Idempotency Engine                        |
         |  - Canonical JSON Normalization (Lexicographical Keys)    |
         |  - SHA-256 Digest: Method + Path + Canonical Body         |
         +-----------------------------------------------------------+
                                       |
                   Atomic INSERT ... ON CONFLICT (key)
                                       |
                     +-----------------+-----------------+
                     |                                   |
              [New Key Claimed]                  [Conflict / Exists]
                     |                                   |
                     v                                   v
        +-------------------------+        +--------------------------+
        |   Create Pending Row    |        | Check Existing Record:   |
        |   in PostgreSQL         |        | 1. completed + match:    |
        +-------------------------+        |    -> Replay 201 Cache   |
                     |                     | 2. completed + mismatch: |
                     v                     |    -> Return 422 Error   |
        +-------------------------+        | 3. in_progress (< 30s):  |
        | Call Payment Gateway    |        |    -> Return 409 Conflict|
        | (Simulated Processor)   |        | 4. in_progress (> 30s):  |
        +-------------------------+        |    -> Atomic Reclaim Lock|
                     |                     +--------------------------+
                     v
        +-------------------------+
        | Atomically Finalize:    |
        | - Payment Status        |
        | - Key Status: completed |
        | - Cached Response Body  |
        +-------------------------+
                     |
                     v
        +-------------------------+
        |  Return 201 Response    |
        +-------------------------+
```

---

## Key Features

- **Strict Exactly-Once Semantics**: 50 concurrent requests with the identical idempotency key yield **exactly one payment** in the database.
- **Canonical Request Hashing**: Normalizes JSON body whitespace and key order before computing SHA-256 digests (`{"a":1,"b":2}` and `{"b":2,"a":1}` produce identical hashes).
- **Database-Level Mutual Exclusion**: Concurrency control relies on PostgreSQL `UNIQUE` constraints and `INSERT ... ON CONFLICT DO NOTHING`, eliminating in-memory bottlenecks.
- **Automatic Crash & Timeout Recovery**: If a worker node crashes mid-flight, an atomic conditional `UPDATE` reclaims stuck `in_progress` locks older than 30 seconds.
- **Strict Payment State Machine**: Enforces valid transitions (`pending -> succeeded` or `pending -> failed`), rejecting any illegal state mutations.
- **Zero Mutable Global State**: Clean dependency injection throughout all packages.

---

## Quickstart with Docker Compose

### Prerequisites
- Docker & Docker Compose

### 1. Start Postgres and API
```bash
docker compose up --build -d
```

### 2. Verify Health
```bash
# Liveness probe
curl -i http://localhost:8080/healthz

# Readiness probe (verifies database pool connectivity)
curl -i http://localhost:8080/readyz
```

---

## API Reference & Example cURL Commands

### 1. Create a Payment (Initial Execution)
```bash
curl -i -X POST http://localhost:8080/v1/payments \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: payment_order_1001" \
  -d '{
    "amount": 1500,
    "currency": "INR",
    "customer_id": "cus_98765",
    "description": "Monthly Platform Subscription"
  }'
```
**Response (201 Created):**
```json
{
  "id": "e4f8d5a2-9b3c-4d1e-8f2a-7b6c5d4e3f21",
  "customer_id": "cus_98765",
  "amount": 1500,
  "currency": "INR",
  "description": "Monthly Platform Subscription",
  "status": "succeeded",
  "created_at": "2026-10-25T10:00:00Z",
  "updated_at": "2026-10-25T10:00:00Z"
}
```

### 2. Safe Replay (Identical Key & Request Payload)
Repeat the identical cURL command:
```bash
curl -i -X POST http://localhost:8080/v1/payments \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: payment_order_1001" \
  -d '{
    "customer_id": "cus_98765",
    "currency": "INR",
    "amount": 1500,
    "description": "Monthly Platform Subscription"
  }'
```
**Response:** Same `201 Created` status code and body, with the replay header:
```http
HTTP/1.1 201 Created
Content-Type: application/json; charset=utf-8
Idempotent-Replayed: true
X-Request-ID: 7a2b9c1d-4e5f-6a7b-8c9d-0e1f2a3b4c5d
```

### 3. Payload Mismatch (Same Key with Different Payload)
```bash
curl -i -X POST http://localhost:8080/v1/payments \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: payment_order_1001" \
  -d '{
    "amount": 9999,
    "currency": "INR",
    "customer_id": "cus_98765"
  }'
```
**Response (422 Unprocessable Entity):**
```json
{
  "error": {
    "code": "IDEMPOTENCY_PAYLOAD_MISMATCH",
    "message": "Idempotency key was previously used with a different request payload"
  }
}
```

### 4. Concurrent Request Conflict (In-Progress)
When another worker is actively executing a request with this key:
**Response (409 Conflict):**
```http
HTTP/1.1 409 Conflict
Content-Type: application/json; charset=utf-8
Retry-After: 5
```
```json
{
  "error": {
    "code": "IDEMPOTENCY_IN_PROGRESS",
    "message": "A request with this idempotency key is currently being processed"
  }
}
```

### 5. Fetch Payment by ID
```bash
curl -i http://localhost:8080/v1/payments/e4f8d5a2-9b3c-4d1e-8f2a-7b6c5d4e3f21
```

---

## Running Automated Tests

```bash
# Run unit tests
make test

# Run unit tests with Go race detector
make test-race

# Run comprehensive integration tests (testcontainers-go with PostgreSQL 16)
make test-integration

# Measure statement coverage
make cover
```

---

## Test & Coverage Results

| Package | Test Type | Coverage | Status |
| :--- | :--- | :---: | :---: |
| `internal/payment` | Unit & Domain State Machine | **98.0%** | PASS |
| `internal/idempotency` | Hashing, Storage & Recovery | **93.8%** | PASS |
| `internal/http` | Middleware & Handlers | **87.2%** | PASS |
| `internal/processor` | Simulated Processor & Deadlines | **88.9%** | PASS |
| `internal/config` | Environment Validation | **91.7%** | PASS |
| `tests/integration` | 50 Goroutines Concurrency, Postgres 16 | E2E | PASS |
