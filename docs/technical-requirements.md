# Technical Requirements — ZVT-to-Mollie Converter

## 1. Technology Stack

| Concern | Choice | Rationale |
|---|---|---|
| Language | Go (1.23+) | Excellent networking primitives, binary encoding, and concurrency model; strong fit for a protocol bridge |
| Mollie API client | `github.com/mollie/mollie-api-golang` | Official Mollie Go client; covers Payments, Refunds, and Cancellations |
| Persistent state | Embedded key-value store (e.g. `bbolt`) | Zero-dependency local store for the receipt-number → Mollie ID mapping; survives restarts |
| Configuration | Environment variables + optional YAML/TOML file | Twelve-factor compatible; easy to inject secrets in containerised environments |
| Logging | `log/slog` (stdlib structured logging) | Available in Go 1.21+; zero extra dependency |

## 2. Architecture Overview

```
ECR (ZVT client)
     │  TCP (port 20007)
     ▼
┌────────────────────────────────────┐
│           ZVT Listener             │  Accepts one TCP connection per ECR session
│   (net.Listener + goroutine/conn)  │
└─────────────┬──────────────────────┘
              │ parsed APDU
              ▼
┌────────────────────────────────────┐
│         Command Dispatcher         │  Routes by CLASS+INSTR bytes
└─────────────┬──────────────────────┘
              │
       ┌──────┴────────┐
       │               │
       ▼               ▼
 ┌──────────┐   ┌────────────┐   ┌────────────┐
 │ Auth     │   │ Reversal   │   │ Refund     │  (one handler per command)
 │ Handler  │   │ Handler    │   │ Handler    │
 └────┬─────┘   └─────┬──────┘   └─────┬──────┘
      │               │                │
      └───────┬────────┘────────────────┘
              │ Mollie API calls
              ▼
┌────────────────────────────────────┐
│         Mollie API Client          │  VictorAvelar/mollie-api-go
└─────────────┬──────────────────────┘
              │ HTTPS
              ▼
         Mollie Cloud

              │ receipt-no ↔ payment-id
              ▼
┌────────────────────────────────────┐
│       Receipt Store (bbolt)        │
└────────────────────────────────────┘
```

## 3. ZVT Protocol Implementation

### 3.1 Transport

- **Protocol:** TCP/IP only. Serial/RS-232 is not supported.
- **Default port:** `20007` (standard ZVT PT port).
- **Connection model:** One concurrent ZVT session per ECR. The listener accepts one connection at a
  time per configured ECR address; a second connection from the same ECR while one is active must
  be queued or rejected with a busy response.
- **TLS:** Optional. When a certificate and key are configured, wrap the TCP listener with `tls.NewListener`.
  The ZVT transport TLS specification (PA00P016) applies.

### 3.2 APDU Framing

ZVT APDUs have the following structure (ref. ZVT 13.13, §1):

```
[CLASS byte] [INSTR byte] [LENGTH] [DATA BLOCK]
```

**Length encoding:**
- If `len(data) ≤ 254`: LENGTH is 1 byte.
- If `len(data) > 254`: LENGTH byte is `0xFF`, followed by 2 bytes (big-endian) containing the actual
  length.

**DLE escaping:** Not required for TCP/IP transport.

**Minimum read loop:**
1. Read 2 bytes (CLASS, INSTR).
2. Read 1 byte (LENGTH).
3. If LENGTH == `0xFF`, read 2 more bytes for extended length.
4. Read exactly `length` bytes as the data block.
5. Dispatch based on CLASS+INSTR.

**Write loop:** Mirror the length encoding above when constructing response APDUs.

### 3.3 BMP (Bitmap) Field Encoding

BMP fields appear in the data block as `<tag byte> <data>` pairs, where the length is implicit from the
tag definition. The converter must parse and encode the following BMPs:

| BMP | Field | Encoding | Used In |
|---|---|---|---|
| 04 | Amount | 6 byte BCD packed | Auth start, Status-Info |
| 0B | Trace number | 3 byte BCD | Status-Info |
| 0C | Time | 3 byte BCD HHMMSS | Status-Info |
| 0D | Date | 2 byte BCD MMDD | Status-Info |
| 19 | Payment type | 1 byte bitmask | Auth start (parsed, not used) |
| 22 | PAN | LLVAR BCD | Status-Info |
| 27 | Result code | 1 byte | Status-Info |
| 29 | Terminal ID | 4 byte BCD | Status-Info |
| 37 | Original trace | 3 byte BCD | Status-Info (Reversal) |
| 49 | Currency code | 2 byte BCD | Auth start, Status-Info |
| 87 | Receipt number | 2 byte BCD | Reversal start, Status-Info |
| 8A | Card type | 1 byte binary | Status-Info |
| 8B | Card name | LLVAR ASCII | Status-Info |

Unknown BMPs in the incoming data block must be silently ignored (as per ZVT spec §10).

**BCD encoding rules:**
- BCD values are packed (2 decimal digits per byte).
- Right-padded with `0` to fill the last byte if the value has odd length.
- Amounts are in the smallest currency unit (cents), formatted as a 12-digit decimal packed into 6 bytes.

### 3.4 Supported Command Set

| Command | CLASS | INSTR | Direction |
|---|---|---|---|
| Registration | `06` | `00` | ECR → PT |
| Authorization | `06` | `01` | ECR → PT |
| Reversal | `06` | `30` | ECR → PT |
| Refund | `06` | `31` | ECR → PT |
| Abort | `06` | `B0` | ECR → PT |
| Log-Off | `06` | `02` | ECR → PT |
| Status-Information | `04` | `0F` | PT → ECR |
| Intermediate Status-Info | `04` | `FF` | PT → ECR |
| Completion | `06` | `0F` | PT → ECR |
| Abort (PT→ECR) | `06` | `1E` | PT → ECR |
| Positive ACK | `80` | `00` | ECR → PT (response to PT commands) |

All other CLASS+INSTR combinations: respond with `84-83-00`.

### 3.5 Command Flow State Machine

Each ZVT session is a state machine. Transitions:

```
IDLE
 └─ Registration (06 00) → acknowledge → IDLE
 └─ Authorization (06 01) → ack → IN_TRANSACTION
 └─ Reversal (06 30)      → ack → IN_TRANSACTION
 └─ Refund (06 31)        → ack → IN_TRANSACTION
 └─ Abort (06 B0)         → ack → IDLE (if IN_TRANSACTION: cancel in-flight op)
 └─ Log-Off (06 02)       → ack → CLOSE

IN_TRANSACTION
 └─ [Intermediate Status loop] → ECR acks with 80-00
 └─ Status-Information sent to ECR → ECR acks
 └─ [Receipt-Printout if applicable]
 └─ Completion (06 0F) or Abort (06 1E) → IDLE
```

Only one command may be in-flight at a time per connection. If the ECR sends a second command while
`IN_TRANSACTION`, respond with `84-83-00`.

### 3.6 Intermediate Status-Information

When the ECR has requested intermediate status (config-byte bit `0x08` set during Registration):
- While awaiting Mollie API confirmation, send `04-FF-01-<status>` at a regular interval (default 5 s).
- The status byte value `00` ("please wait") is appropriate for all intermediate states in this converter.
- Stop sending once the Mollie response (success or error) is received.

## 4. Mollie API Integration

### 4.1 Client Configuration

```go
import "github.com/mollie/mollie-api-golang/mollie"

client, _ := mollie.NewClient(mollie.WithAPIKey(os.Getenv("MOLLIE_API_KEY")))
```

### 4.2 Authorization → Mollie Payment

- **Endpoint:** `POST /v2/payments`
- **Method:** `pointofsale` — the payment is initiated on a Mollie card reader associated with the
  configured terminal/profile ID.
- **Required fields:**
  - `amount.value` — decimal string with 2 decimal places (e.g. `"12.50"`)
  - `amount.currency` — ISO 4217 code (e.g. `"EUR"`)
  - `method` — `"pointofsale"`
  - `description` — human-readable; include terminal ID and trace number
- **Idempotency:** Set `Idempotency-Key` header to a value derived from the ZVT trace number.
- **Status polling:** The converter polls `GET /v2/payments/{id}` until the payment reaches a terminal
  state (`paid`, `failed`, `canceled`, `expired`) or the configured API timeout is exceeded.
  - Polling interval: 2 s initial, doubling each attempt (4 s, 8 s, …), capped at 10 s.
  - A future enhancement may replace polling with Mollie webhook callbacks once the deployment
    environment supports inbound HTTP from Mollie. This should be tracked as a follow-up feature.

### 4.3 Reversal → Mollie Cancel or Refund

- If Mollie payment status is `open` or `pending` and the payment method supports cancellation:
  - **Endpoint:** `DELETE /v2/payments/{id}` (cancel)
- If the payment has been captured (status `paid`):
  - **Endpoint:** `POST /v2/payments/{id}/refunds` with the full amount.
- The choice between cancel and refund is transparent to the ECR; both are reported as a successful
  Reversal with result-code `00`.

### 4.4 Refund → Mollie Refund

- **Endpoint:** `POST /v2/payments/{id}/refunds`
- The Mollie payment ID is resolved from the receipt number (BMP 87) via the persistent store.
- If BMP 87 is absent from the Refund request, return ZVT result-code `64` without calling the Mollie
  API. Unreferenced refunds are not supported.
- **Required fields:** `amount.value`, `amount.currency`
- Partial refunds: use the amount from BMP 04 if present; otherwise refund the full payment amount.
- **Idempotency:** Derive key from ZVT trace number.

### 4.5 Error Mapping

| Mollie error / HTTP status | ZVT result-code | Description |
|---|---|---|
| 2xx success | `00` | Transaction successful |
| `422` / invalid amount | `6F` | Invalid amount or currency |
| `404` payment not found | `64` | Transaction not found |
| `409` conflict (already refunded) | `6C` | Reversal/refund not possible |
| `503` / network timeout | `63` | Communication error |
| Mollie payment status `failed` | `63` | Authorization declined |
| Mollie payment status `canceled` | `6A` | Transaction cancelled |
| Mollie payment status `expired` | `9C` | Timeout |
| Unexpected / 5xx | `A0` | System error |

## 5. Persistent State Store

- **Technology:** `go.etcd.io/bbolt` (embedded, single-file BoltDB).
- **Purpose:** Map ZVT receipt numbers to Mollie payment/refund IDs so Reversals work after restarts.
- **Bucket layout:**

```
receipts/
  <receipt-no (hex)> → <mollie-payment-id (string)>

refunds/
  <receipt-no (hex)> → <mollie-refund-id (string)>
```

- The store file path is configurable via `STATE_DB_PATH`.
- On startup, if the file is missing it is created. If it is corrupted, the converter logs an error and starts
  with an empty state (existing transactions cannot be reversed by receipt number).

## 6. Concurrency Model

- The converter handles **one ZVT transaction at a time per connection**.
- Multiple ECR connections may be active simultaneously; each is served by a dedicated goroutine.
- The Mollie API client is safe for concurrent use across goroutines.
- The BoltDB store is safe for concurrent use via its built-in transaction locking.
- A global `sync.Mutex` guards the in-flight transaction map to prevent double-processing of the same
  receipt number from different connections.

## 7. Testing Requirements

### 7.1 Unit Tests

- BMP field encoding and decoding (round-trip tests for all supported BMPs).
- APDU framing: encode/decode with short (1-byte) and extended (3-byte) length fields.
- BCD conversion: amounts, dates, times, trace numbers.
- ZVT result-code mapping from Mollie API errors.
- Command dispatcher routing.

### 7.2 Integration Tests

- Full Authorization flow using Mollie test API key and a test payment method.
- Full Reversal flow (cancel path and refund path).
- Full Refund flow (partial and full).
- Receipt-number persistence: verify that a payment ID is retrievable after a store write/read cycle.
- Error path: Mollie API returns 422 → ECR receives correct ZVT result-code.

### 7.3 Protocol Conformance Tests

- Unknown command → `84-83-00`.
- Extended-length APDU (length > 254 bytes) framed correctly.
- Registration rejected with wrong currency code → `84-1E-xx 6F`.

## 8. Project Layout (proposed)

```
zvt-to-mollie/
├── cmd/
│   └── zvt-to-mollie/
│       └── main.go          # Entry point; reads config, starts listener
├── internal/
│   ├── zvt/
│   │   ├── apdu.go          # APDU framing (read/write)
│   │   ├── bmp.go           # BMP field encoding/decoding
│   │   ├── commands.go      # Command constants (CLASS+INSTR bytes)
│   │   ├── session.go       # Per-connection state machine
│   │   └── errors.go        # ZVT result-code constants
│   ├── mollie/
│   │   ├── client.go        # Thin wrapper around VictorAvelar client
│   │   └── mapper.go        # ZVT ↔ Mollie field mapping
│   └── store/
│       └── receipt_store.go # bbolt-backed receipt-number → payment-ID map
├── config/
│   └── config.go            # Config struct, env/file loading
├── docs/
│   ├── PA00P015_13.13_final.pdf
│   ├── functional-requirements.md
│   └── technical-requirements.md
└── go.mod
```

## 9. Non-Functional Requirements

| Requirement | Target |
|---|---|
| Startup time | < 1 second |
| ZVT response latency (ack) | < 100 ms (excludes Mollie API round-trip) |
| Mollie API timeout | Configurable; default 60 s |
| Memory footprint | < 50 MB under normal load |
| Supported Go version | 1.23+ |
| OS support | Linux (primary), macOS (development) |
| Container image | Single static binary; scratch or distroless base image |

## 10. Decisions Log

The following design decisions have been made and are reflected in this document:

| # | Decision |
|---|---|
| 1 | **Mollie payment method:** `pointofsale` is used for all Authorization payments. |
| 2 | **Unreferenced refunds:** Not supported. Refund (06 31) requires a receipt number (BMP 87); requests without one are rejected with result-code `64`. |
| 3 | **Webhook vs. polling:** The converter polls for payment status. It cannot receive inbound webhooks due to NAT/firewall constraints. Webhook support is a future enhancement to be tracked separately. |
| 4 | **Deployment model:** One converter instance per physical terminal. Multi-terminal support is out of scope. |
| 5 | **Go client:** `github.com/mollie/mollie-api-golang` (official Mollie Go client). |
