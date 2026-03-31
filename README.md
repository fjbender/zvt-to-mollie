# zvt-to-mollie

A network service that bridges ZVT-speaking Electronic Cash Register (ECR) systems to the [Mollie Payments API](https://docs.mollie.com/). It acts as a Payment Terminal (PT) on the ECR's side, translating ZVT protocol messages into Mollie API calls — allowing legacy ECR devices to process payments through Mollie without any firmware changes.

## How it works

```
ECR device  ──ZVT/TCP──►  zvt-to-mollie  ──HTTPS──►  Mollie API
```

The service listens for ZVT APDU frames over TCP, dispatches them to the appropriate handler, calls the Mollie API, and returns ZVT-compliant responses. Payment state (receipt-to-payment-ID mappings) is persisted locally in an embedded BoltDB store so reversals and refunds can look up previous transactions across restarts.

## Configuration

All configuration is via environment variables (12-factor compliant).

| Variable | Required | Default | Description |
|---|---|---|---|
| `MOLLIE_API_KEY` | Yes | — | Mollie API key (live or test) |
| `MOLLIE_TERMINAL_ID` | Yes | — | Mollie terminal ID for point-of-sale payments |
| `ZVT_PASSWORD` | Yes | — | 6-digit ZVT terminal password (numeric) |
| `ZVT_LISTEN_ADDR` | No | `:20007` | TCP address for the ZVT listener |
| `ZVT_TERMINAL_ID` | No | — | 8-digit terminal ID included in receipts (BMP 29) |
| `ZVT_CURRENCY_CODE` | No | `978` | ISO 4217 numeric currency code (978 = EUR) |
| `ZVT_TLS_CERT` | No | — | Path to TLS certificate for the ZVT listener |
| `ZVT_TLS_KEY` | No | — | Path to TLS private key for the ZVT listener |
| `MOLLIE_API_TIMEOUT` | No | `30s` | HTTP timeout for Mollie API calls |
| `STATE_DB_PATH` | No | `state.db` | Path to the BoltDB state file |
| `HTTP_LISTEN_ADDR` | No | `:8080` | TCP address for health/readiness endpoints |

## Running

```bash
MOLLIE_API_KEY=test_xxx \
MOLLIE_TERMINAL_ID=term_xxx \
ZVT_PASSWORD=000000 \
go run ./cmd/zvt-to-mollie
```

Health and readiness probes are available at `GET /healthz` and `GET /readyz` on the HTTP listener.

## Implemented features

### Protocol infrastructure
- APDU framing — read and write with both short (1-byte) and extended (3-byte) length encoding
- BMP field encoding and decoding — fixed-length and LLVAR fields
- BCD conversions — amounts (12 digits / 6 bytes), dates (MMDD), times (HHMMSS), trace numbers
- State machine — `IDLE → IN_TRANSACTION → IDLE/CLOSE` per connection
- Unknown commands are rejected with `84-83-00`; commands received while `IN_TRANSACTION` (other than Abort) are rejected
- Optional TLS wrapping on the ZVT listener

### Commands
| Command | Code | Status |
|---|---|---|
| Registration | `06 00` | Implemented |
| Authorization | `06 01` | Implemented |
| LogOff | `06 02` | Roadmap |
| Reversal | `06 30` | Roadmap |
| Refund | `06 31` | Roadmap |
| Abort | `06 B0` | Roadmap |

**Registration (`06 00`)** validates the 6-digit password, parses the config byte (intermediate status enable flag), validates an optional currency code (BMP 49), and returns ACK or a PT-initiated abort with result code `6F` on currency mismatch.

**Authorization (`06 01`)** parses amount (BMP 04) and optional currency (BMP 49). Per spec §2.2.1 the ECR does **not** include a trace number in the `06 01` request; BMP `0B` is absent from the defined data block. The implementation accepts BMP `0B` if an ECR sends it as a vendor extension and uses it as a Mollie idempotency key when present. The PT assigns a receipt number (BMP `87`) which is returned in the Status-Info frame — this is the authoritative link between the ECR's checkout and the PT's payment (see _Transaction correlation_ below). The implementation polls Mollie with exponential backoff (2 s → 4 s → 8 s, capped at 10 s), sends intermediate status frames (04 FF) every 5 s when enabled, and returns a Status-Info frame (04 0F) followed by Completion (06 0F). The receipt-to-payment-ID mapping is persisted in BoltDB, keyed on BMP `87`.

## Transaction correlation

ZVT assigns different identifiers to the two sides of a payment:

| Field | Tag | Owner | Meaning |
|---|---|---|---|
| Receipt number | BMP `87` | **PT-generated** | Primary cross-domain key. PT assigns it, returns it in Status-Info (`04 0F`), ECR stores it and sends it back in Reversal (`06 30`) / Refund (`06 31`). |
| Trace number | BMP `0B` | **PT-generated** | PT-internal counter echoed in Status-Info. Also returned in End-of-Day (`06 50`) reconciliation. |
| Orig. trace | BMP `37` | PT (in Reversal Status-Info) | Trace number of the original payment being reversed. |

The flow per spec §2.2 and §2.9:

1. **ECR → PT** `06 01`: amount, optional currency/payment-type. No receipt or trace number.
2. **PT → ECR** `04 0F` (Status-Info): PT sends BMP `87` (receipt number) and BMP `0B` (trace). The ECR records the receipt number against its local checkout.
3. **ECR → PT** `06 30` (Reversal): ECR sends BMP `87` to identify the original transaction. The PT looks it up in its turnover storage.

BMP `0B` is **not** part of the `06 01` ECR→PT request in the spec. Some ECR implementations send it as a vendor extension; when present this service accepts it and uses it as a Mollie idempotency key. Regardless, the receipt number (BMP `87`) is always PT-generated and is the canonical link between an ECR checkout and a Mollie payment.

## Roadmap

### Abort (`06 B0`)
Send ACK and, if a transaction is in flight, cancel it via the Mollie API before returning to `IDLE`.

### LogOff (`06 02`)
Send ACK, send Completion (06 0F), and close the connection gracefully.

### Reversal (`06 30`)
Look up the original payment by receipt number (BMP 87), cancel it if still open/pending or issue a full refund if already paid, and return Status-Info + Completion. Error codes: `64` (not found), `6C` (already refunded / reversal not possible).

### Refund (`06 31`)
Look up the original payment by receipt number (BMP 87), call the Mollie refund API with an optional partial amount (BMP 04), and return Status-Info + Completion.

### In-flight deduplication
Guard against duplicate Mollie payments when an ECR retries an Authorization while one is already in flight, using a mutex keyed on the receipt number.

### Webhook support
Replace polling with Mollie webhook callbacks for faster payment status updates. Requires the service to be reachable from the public internet.

## Tech stack

- **Go 1.23+** — static binary, no runtime dependencies
- [`github.com/mollie/mollie-api-golang`](https://github.com/mollie/mollie-api-golang) — official Mollie SDK
- [`go.etcd.io/bbolt`](https://github.com/etcd-io/bbolt) — embedded key-value store for receipt state
- `log/slog` — structured JSON logging

## Protocol reference

The implementation follows the ZVT protocol specification **PA00P015, revision 13.13**. The spec PDF is included in the repository under `docs/`.
