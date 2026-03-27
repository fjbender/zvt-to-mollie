# Functional Requirements — ZVT-to-Mollie Converter

## 1. Purpose

The ZVT-to-Mollie Converter is a network service that bridges Electronic Cash Register (ECR) systems using
the German ZVT protocol (PA00P015, revision 13.13) with the Mollie Payments API. It presents itself as a
Payment Terminal (PT) to the ECR and translates ZVT payment commands into Mollie API calls.

## 2. Actors

| Actor | Role |
|---|---|
| **ECR** | Electronic Cash Register or vending machine that initiates ZVT commands |
| **Converter** | This service — acts as PT toward the ECR, and as a Mollie merchant client toward the API |
| **Mollie API** | Cloud payment service provider; processes payments, refunds, and cancellations |

## 3. Supported Commands

### 3.1 Registration (06 00)

The converter must handle the Registration command from the ECR as a session initialisation step.

**Requirements:**
- Accept a Registration request containing a password, config-byte, and optional currency code.
- Validate the password against a configured credential.
- Echo a Completion (06 0F) response to return master-rights to the ECR.
- Reject registrations with an incorrect currency code with error code `6F`.
- Store the config-byte flags for the session: whether the ECR handles receipt printing, whether ECR
  controls payment functions, and whether intermediate status-information is requested.
- A Registration is not required before an Authorization; the converter must accept Authorization without
  prior Registration.

### 3.2 Authorization / Purchase (06 01)

The Authorization command initiates a payment process. The ECR sends an amount; the converter creates
a Mollie `pointofsale` payment and reports the result back to the ECR.

**Requirements:**
- Accept an Authorization request containing at minimum an amount (BMP 04) in BCD format.
- Accept optional currency code (BMP 49); default to EUR (09 78) when absent.
- Immediately acknowledge receipt to the ECR with `80-00-00`.
- Create a Mollie payment using the `pointofsale` method for the given amount and currency.
- If intermediate status-information was requested during Registration, send periodic Intermediate
  Status-Information (04 FF) to the ECR while awaiting Mollie payment confirmation.
- Poll the Mollie API for payment status until it reaches a terminal state (`paid`, `failed`, `canceled`,
  `expired`) or the configured timeout is exceeded (see §8 and §9).
- Once the Mollie payment is confirmed (`paid`):
  - Send a Status-Information (04 0F) to the ECR with result-code `00` (success) and available
    transaction data (amount, trace number, date/time, card type if available).
  - Perform the receipt-printout step per the session's configured receipt mode.
  - Send a Completion (06 0F) to return master-rights to the ECR.
- If the Mollie payment fails or is cancelled:
  - Send a Status-Information (04 0F) with the appropriate non-zero result-code.
  - Send an Abort (06 1E) with the result-code.
- Store a mapping between the ZVT receipt number (BMP 87) and the Mollie payment ID so that Reversal
  can reference it later.

**Out of scope for Authorization:**
- Vending machine / filling station goods-dispensing loops (84-9C and partial-goods response 84-9D).
- Manual card data entry fields (BMP 0E, 22, 23, 24, 2D).
- Payment-type selection (BMP 19); the Mollie payment method is determined by Mollie.

### 3.3 Reversal / Storno (06 30)

The Reversal command cancels a previously completed Authorization. The ECR identifies the transaction by
its receipt number.

**Requirements:**
- Accept a Reversal request containing a password and receipt number (BMP 87).
- Look up the Mollie payment ID that corresponds to the receipt number.
- If the Mollie payment is still cancellable (not yet captured), cancel it via the Mollie API.
- If the Mollie payment has already been captured, create a full refund for it via the Mollie API.
- Follow the same Status-Information → Completion (or Abort) sequence as Authorization.
- Return error result-code `64` (transaction not found) if the receipt number does not match any stored
  payment.
- Validate the password against the configured credential before processing.

### 3.4 Refund (06 31)

The Refund command credits an amount back to the cardholder, referencing a prior transaction by its
receipt number.

**Requirements:**
- Accept a Refund request containing a password and a receipt number (BMP 87) that identifies the
  original transaction.
- Accept an optional amount (BMP 04): if present, create a partial refund; if absent, create a full refund.
- Look up the Mollie payment ID from the receipt number in the persistent store.
- Return error result-code `64` (transaction not found) if the receipt number is not in the store.
- Create a Mollie refund on the referenced payment for the specified (or full) amount.
- Follow the same Status-Information → Completion (or Abort) sequence as Authorization.
- Validate the password against the configured credential before processing.

> **Note:** Mollie does not support refunds without a payment reference. ZVT Refund requests that do
> not include a receipt number (BMP 87) cannot be processed and must be rejected with result-code `64`.
> Unreferenced refunds are out of scope for this converter.

## 4. Out of Scope

The following ZVT commands and features are explicitly **not supported**:

- Administrative commands: End-of-Day (06 50), Diagnosis (06 70), Initialisation (06 93), Send Turnover
  Totals (06 10), Print Turnover Receipts (06 12), Repeat Receipt (06 20).
- Girocard-specific payment types (ELV, EuroELV, ecTrack2, ecEMV, GeldKarte, girogo).
- Pre-Authorisation / Reservation (06 22), Partial-Reversal of Pre-Authorisation (06 23), Book Total (06 24),
  Pre-Authorisation Reversal (06 25), Reversal of external transaction (06 26).
- Filling station and vending machine multi-step dispensing loops.
- Serial / RS-232 transport; only TCP/IP is supported.
- OPT actions (08 20–08 23), Software Update (08 10), Read/Write/Delete File (08 11–08 14).
- Display commands (06 E0–06 E3, 06 85–06 88, 06 F0–06 F1).
- Card-reader commands (06 C0, 08 50, 06 C5, 06 C6).
- Telephonic Authorisation (06 21), Procurement (06 05), Book Tip (06 0C), Tax Free (06 0A).
- DCC (Dynamic Currency Conversion).
- Bonus / loyalty points transactions.

Commands not listed as supported must be answered with error `84-83-00` (unknown command).

## 5. Transaction State and Idempotency

- The converter maintains a persistent mapping of ZVT receipt numbers to Mollie payment/refund IDs so
  that Reversals can reference prior transactions across restarts.
- If the ECR retransmits the same Authorization (same amount and receipt context) before a prior attempt
  is resolved, the converter must not create a duplicate Mollie payment. The outcome of the in-flight
  transaction is returned instead.
- Mollie API calls must use idempotency keys derived from the ZVT trace number or receipt number.

## 6. Security

- The ZVT password is validated for all commands that require it (Reversal, Refund, Registration). The
  password is a 6-digit decimal value stored as 3-byte BCD.
- The configured password must not be logged in plain text.
- The ZVT TCP listener should support TLS (as per the ZVT transport specification PA00P016) to prevent
  man-in-the-middle attacks on local networks. TLS is optional but recommended for production.
- All communication with the Mollie API uses HTTPS.

## 7. Receipts and Status-Information

- The converter respects the receipt mode negotiated during Registration.
- The converter does **not** itself print receipts; if the ECR takes responsibility for receipt printing (config-
  byte bit 1 set), the converter must populate the Status-Information with available transaction fields
  (amount, date, time, trace number, card type, terminal ID).
- Receipt data fields returned in Status-Information (04 0F) after Authorization/Reversal/Refund:
  - BMP 04 `<amount>` — confirmed payment amount.
  - BMP 0B `<trace>` — internal trace number assigned by the converter.
  - BMP 0C `<time>` — transaction time (HHMMSS).
  - BMP 0D `<date>` — transaction date (MMDD).
  - BMP 49 `<CC>` — currency code, if the ECR sent one in the request.
  - BMP 87 `<receipt-no>` — receipt number.
  - BMP 8A `<card-type>` — card type ID if determinable from the Mollie payment method.
  - BMP 29 `<terminal-ID>` — configured terminal ID.

## 8. Error Handling

- ZVT result-codes returned to the ECR must match the standard error table from the ZVT specification
  (chapter 10). The converter must at minimum support:
  - `00` — success.
  - `62` — card not readable / payment method not supported.
  - `63` — communication error with Mollie API.
  - `64` — transaction not found (for Reversal).
  - `6C` — reversal not possible (payment already fully refunded).
  - `6F` — wrong currency code.
  - `9C` — timeout waiting for payment confirmation.
  - `A0` — system error / unexpected Mollie API response.
- If the Mollie API is unreachable, the converter returns a network error result-code and does not leave
  the ECR waiting indefinitely. A configurable timeout (default 60 s) applies to Mollie API calls.

## 9. Configuration

The converter must be configurable via a configuration file or environment variables:

| Parameter | Description |
|---|---|
| `MOLLIE_API_KEY` | Mollie API key (live or test) |
| `ZVT_PASSWORD` | 6-digit ECR password |
| `ZVT_LISTEN_ADDR` | TCP address and port to listen on (default `:20007`) |
| `ZVT_TERMINAL_ID` | 8-digit numeric terminal ID returned in Status-Information |
| `ZVT_CURRENCY_CODE` | Default currency code (default `0978` = EUR) |
| `ZVT_TLS_CERT` | Path to TLS certificate (optional) |
| `ZVT_TLS_KEY` | Path to TLS private key (optional) |
| `MOLLIE_API_TIMEOUT` | Timeout for Mollie API calls in seconds (default `60`) |
| `STATE_DB_PATH` | Path for the persistent receipt-to-payment-ID store |

## 10. Observability

- Structured logging (JSON) for every ZVT command received and Mollie API call made, including:
  - Command class and instruction bytes.
  - Result code returned to ECR.
  - Mollie payment/refund ID (never log raw card data or the ZVT password in plain text).
- Expose a `/healthz` HTTP endpoint for liveness checks.
- Expose a `/readyz` HTTP endpoint that verifies connectivity to the Mollie API.
