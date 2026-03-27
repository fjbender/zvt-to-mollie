# Plan: Milestone 3 — Command Handlers

## Context

The ZVT-to-Mollie converter bridges ECR devices (using the German ZVT protocol) to the Mollie Payments API. The infrastructure layer (config, HTTP health endpoints, BoltDB store), ZVT protocol framing (APDU read/write, BMP encoding), and Mollie API client are all 100% complete with unit tests. The binary is non-functional because the connection loop and all command handlers are stubbed out with TODOs. This milestone makes the binary functional end-to-end.

## Scope

Implement everything tagged `// TODO: implement command routing in milestone 3`:

1. `internal/zvt/listener.go` — connection read loop
2. `internal/zvt/session.go` — per-connection state machine (new file)
3. `internal/zvt/dispatcher.go` — command routing + all handlers

## Approach

### 1. `internal/zvt/session.go` (new file)

Define the per-connection state machine and the `Session` type that owns a single TCP connection.

```go
type state int
const (
    stateIdle          state = iota
    stateInTransaction
    stateClose
)

type Session struct {
    conn           net.Conn
    dispatcher     *Dispatcher
    state          state
    intermediateOK bool   // set by Registration config-byte bit 0x08
    cancelInFlight context.CancelFunc // non-nil while IN_TRANSACTION
}
```

`Session.Run(ctx)`:
- APDU read loop (calls `ReadAPDU`)
- On each APDU: if `stateInTransaction` and CLASS+INSTR is not Abort → write `FrameUnknown`, continue
- Otherwise: call `dispatcher.Dispatch(ctx, apdu, session)` which may set/clear state
- On `stateClose` or EOF: return

### 2. `internal/zvt/listener.go`

Replace `_ = conn.Close()` with `go session.Run(ctx)` — one goroutine per connection.

### 3. `internal/zvt/dispatcher.go` — routing + handlers

`Dispatch(ctx, apdu, session)` routes on `(apdu.Class, apdu.Instr)`:

| CLASS | INSTR | Handler |
|-------|-------|---------|
| 06 | 00 | `handleRegistration` |
| 06 | 01 | `handleAuthorization` |
| 06 | 02 | `handleLogOff` |
| 06 | 30 | `handleReversal` |
| 06 | 31 | `handleRefund` |
| 06 | B0 | `handleAbort` |
| *   | *   | write `FrameUnknown` |

**Registration (06 00)**
- Parse password (first 3 bytes of data, BCD) → compare to configured ZVT password
- Parse config byte: bit `0x08` → `session.intermediateOK = true`
- Parse optional BMP 49 (currency code) → validate against configured currency
- On success: write `FrameACK`; on failure: write `FrameUnknown` (or abort with 06-1E + result code 6F for wrong currency)

**Authorization (06 01)**
- ACK immediately (write `FrameACK`), transition to `stateInTransaction`
- Parse BMP 04 (amount, 6-byte BCD), BMP 49 (currency), BMP 0B (trace number)
- Derive idempotency key from trace number
- Call `mollie.CreatePayment(ctx, amountCents, currency, description, idempotencyKey)`
- Poll `mollie.GetPayment` with exponential backoff (2s → 4s → 8s → capped at 10s)
- While polling, if `session.intermediateOK`: send `04-FF-01-00` every 5 s (ticker in select)
- On terminal status:
  - `paid` → build Status-Info APDU (04 0F) with BMPs 04, 0B, 0C, 0D, 27=00, 49, 87 (receipt no)
  - `failed` → Status-Info with result code 63
  - `canceled` → result code 6A
  - `expired` → result code 9C
- Send Status-Info; wait for ECR ACK (80 00 00)
- Save `store.SaveReceipt(receiptNo, paymentID)`
- Send Completion (06 0F); transition to `stateIdle`

**Reversal (06 30)**
- ACK, transition to `stateInTransaction`
- Parse BMP 87 (receipt number)
- Lookup `store.GetReceipt(receiptNo)` → Mollie payment ID
- `GetPayment` to check status
- If open/pending: `CancelPayment` → result code 00
- If paid: `CreateRefund` (full amount) → result code 00
- On 404: result code 64; on 409: result code 6C; on network err: 63
- Send Status-Info then Completion; transition to `stateIdle`

**Refund (06 31)**
- ACK, transition to `stateInTransaction`
- Require BMP 87 (receipt number) — if absent: send Status-Info result code 64, Completion, back to idle
- Optional BMP 04 (partial amount) — if absent: full refund (amountCents=0)
- `CreateRefund(ctx, paymentID, amountCents, currency, desc, idempotencyKey)`
- Error mapping same as Reversal
- Send Status-Info then Completion; transition to `stateIdle`

**LogOff (06 02)**
- ACK, send Completion (06 0F), transition to `stateClose`

**Abort (06 B0)**
- ACK
- If `stateInTransaction` and `cancelInFlight != nil`: call it (cancels Mollie polling goroutine)
- Send Completion (06 0F); transition to `stateIdle`

### APDU builders (helpers in dispatcher.go)

```go
func buildStatusInfo(result byte, bmps []BMP) []byte  // 04 0F + encoded BMPs
func buildCompletion() []byte                          // 06 0F 00
func buildIntermediateStatus(status byte) []byte       // 04 FF 01 <status>
```

### Global in-flight mutex (§6 of tech requirements)

Add `sync.Mutex` to `Dispatcher` guarding a `map[uint16]struct{}` of in-flight receipt numbers. Lock before Authorization/Reversal/Refund, unlock on completion. If receipt already in-flight, respond with `FrameUnknown`.

## Files to Modify

- [internal/zvt/listener.go](internal/zvt/listener.go) — wire `Session.Run` instead of `conn.Close()`
- [internal/zvt/dispatcher.go](internal/zvt/dispatcher.go) — full implementation
- [internal/zvt/session.go](internal/zvt/session.go) — new file, state machine + run loop

## Files to Reuse (no changes)

- [internal/zvt/protocol.go](internal/zvt/protocol.go) — `ReadAPDU`, `WriteAPDU`, `ParseAPDU`, `EncodeAPDU`
- [internal/zvt/bmp.go](internal/zvt/bmp.go) — `DecodeBMPs`, `EncodeBMP`, `AmountFromBCD`, `AmountToBCD`
- [internal/zvt/commands.go](internal/zvt/commands.go) — all constants, `FrameACK`, `FrameUnknown`
- [internal/mollie/payment.go](internal/mollie/payment.go) — `CreatePayment`, `GetPayment`, `CancelPayment`, `CreateRefund`, `IsTerminalStatus`, `IsPaid`
- [internal/store/store.go](internal/store/store.go) — `SaveReceipt`, `GetReceipt`

## Tests to Add

`internal/zvt/dispatcher_test.go`:
- Unknown command → `84-83-00` (protocol conformance)
- Double command while IN_TRANSACTION → `84-83-00`
- Registration with wrong password → abort/error response
- Registration with invalid currency BMP → result code 6F
- LogOff → ACK + Completion

`internal/zvt/session_test.go`:
- State transitions (IDLE → IN_TRANSACTION → IDLE)
- EOF on connection closes cleanly

## Verification

```bash
make test          # all unit tests pass
make build         # binary compiles cleanly

# Manual smoke test (requires MOLLIE_API_KEY set to test key):
MOLLIE_API_KEY=test_xxx ZVT_PASSWORD=000000 go run ./cmd/zvt-to-mollie &
# Send a Registration APDU over TCP to :20007, verify ACK response
# Send an Authorization APDU, verify 04-FF polling and final 04-0F Status-Info
```
