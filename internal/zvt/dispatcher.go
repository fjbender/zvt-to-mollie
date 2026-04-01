package zvt

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/fjbender/zvt-to-mollie/internal/mollie"
	"github.com/fjbender/zvt-to-mollie/internal/store"
)

// Dispatcher routes parsed ZVT APDUs to the appropriate command handler.
type Dispatcher struct {
	mollie       *mollie.Client
	store        *store.Store
	password     uint64 // ZVT terminal password as a BCD-decoded integer (e.g. "000000" → 0)
	currencyCode uint64 // ZVT numeric currency code (e.g. 978 for EUR)
	txCounter    atomic.Uint64 // monotonic counter; source for PT-generated trace and receipt numbers
}

// NewDispatcher creates a Dispatcher wired to the given Mollie client and state store.
// password is the 6-digit ZVT terminal password string (e.g. "000000").
// currencyCode is the ISO 4217 numeric currency code string (e.g. "978").
func NewDispatcher(m *mollie.Client, s *store.Store, password, currencyCode string) *Dispatcher {
	pwd, _ := strconv.ParseUint(password, 10, 64)
	curr, _ := strconv.ParseUint(currencyCode, 10, 64)
	return &Dispatcher{
		mollie:       m,
		store:        s,
		password:     pwd,
		currencyCode: curr,
	}
}

// Dispatch processes a single APDU and returns the response bytes to send back to the ECR.
// session is the per-connection state machine; handlers may mutate it (e.g. state, intermediateOK).
func (d *Dispatcher) Dispatch(ctx context.Context, apdu *APDU, session *Session) ([]byte, error) {
	if len(apdu.ControlField) != 2 {
		return FrameUnknown, nil
	}
	class, instr := apdu.ControlField[0], apdu.ControlField[1]
	switch {
	case class == ClassPayment && instr == InstrRegistration:
		return d.handleRegistration(apdu, session)
	case class == ClassPayment && instr == InstrAuthorization:
		return d.handleAuthorization(ctx, apdu, session)
	case class == ClassPayment && instr == InstrLogOff:
		return d.handleLogOff(session)
	default:
		return FrameUnknown, nil
	}
}

// handleRegistration processes a Registration command (06 00).
//
// Frame data layout:
//
//	[0..2]  password    3 bytes BCD (6 decimal digits)
//	[3]     config byte bit 0x08 → intermediate status enabled
//	[4..]   optional BMPs (e.g. BMP 49 currency code)
//
// Sequence (spec §2.1):
//  1. PT → ECR: ACK (80 00 00)
//  2. PT → ECR: Completion (06 0F) with status-byte
//  3. ECR → PT: ACK (80 00 00)
func (d *Dispatcher) handleRegistration(apdu *APDU, session *Session) ([]byte, error) {
	data := apdu.Data
	if len(data) < 4 {
		return FrameUnknown, nil
	}

	// Validate password.
	if DecodeBCD(data[0:3]) != d.password {
		return FrameUnknown, nil
	}

	// Apply config byte.
	session.intermediateOK = data[3]&0x08 != 0

	// Validate optional BMP 49 (currency code).
	if len(data) > 4 {
		bmps, err := DecodeBMP(data[4:])
		if err != nil {
			return FrameUnknown, nil
		}
		if currData, ok := FindBMP(bmps, BMPCurrency); ok {
			if DecodeBCD(currData) != d.currencyCode {
				// Wrong currency: send PT-initiated abort with result code 6F.
				return []byte{ClassPayment, InstrAbortPT, 0x01, ResultWrongCurrency}, nil
			}
		}
	}

	// Step 1: Send ACK.
	slog.Debug("zvt send", "remote", session.conn.RemoteAddr(), "hex", hex.EncodeToString(FrameACK))
	if _, err := session.conn.Write(FrameACK); err != nil {
		return nil, err
	}

	// Step 2: Send Completion (06 0F) with status-byte 0x00 (no issues).
	completion := buildRegistrationCompletion()
	slog.Debug("zvt send", "remote", session.conn.RemoteAddr(), "hex", hex.EncodeToString(completion))
	if _, err := session.conn.Write(completion); err != nil {
		return nil, err
	}

	// Step 3: Wait for ECR ACK (80 00 00).
	ackBuf := make([]byte, 3)
	if _, err := io.ReadFull(session.conn, ackBuf); err != nil {
		slog.Error("registration: failed to read ECR ACK", "err", err)
	}

	return nil, nil
}

// handleLogOff processes a Log-Off command (06 02).
//
// Sequence (spec §2.25):
//  1. PT resets the Registration config-byte to 0x86 (intermediate status disabled).
//  2. PT → ECR: ACK (80 00 00).
//  3. Connection is closed.
func (d *Dispatcher) handleLogOff(session *Session) ([]byte, error) {
	// Reset config-byte to 0x86: bit 0x08 is not set, so intermediate status is disabled.
	session.intermediateOK = false
	session.state = stateClose
	return FrameACK, nil
}

// handleAuthorization processes an Authorization command (06 01).
//
// The handler owns the connection for the full duration of the transaction:
// it writes the ACK, polls Mollie, sends Status-Info, waits for the ECR ACK,
// then sends Completion before returning control to the session loop.
func (d *Dispatcher) handleAuthorization(ctx context.Context, apdu *APDU, session *Session) ([]byte, error) {
	// Step 1: ACK immediately and enter transaction state.
	slog.Debug("zvt send", "remote", session.conn.RemoteAddr(), "hex", hex.EncodeToString(FrameACK))
	if _, err := session.conn.Write(FrameACK); err != nil {
		return nil, err
	}
	session.state = stateInTransaction

	// Step 2: Parse BMPs from the APDU data.
	bmps, err := DecodeBMP(apdu.Data)
	if err != nil {
		slog.Error("authorization: failed to decode BMPs", "err", err)
		d.writeStatusAndAbort(session, ResultCommError, nil)
		session.state = stateIdle
		return nil, nil
	}

	var amountCents int64
	currencyCode := d.currencyCode
	var ecrTraceNumber uint64

	if amtData, ok := FindBMP(bmps, BMPAmount); ok && len(amtData) == 6 {
		var b [6]byte
		copy(b[:], amtData)
		amountCents = DecodeBCDAmount(b)
	}
	if currData, ok := FindBMP(bmps, BMPCurrency); ok {
		currencyCode = DecodeBCD(currData)
	}
	// BMP 0B is not part of the spec's 06 01 ECR→PT data block; accept it only
	// as a vendor extension and use it solely as a Mollie idempotency key.
	if traceData, ok := FindBMP(bmps, BMPTrace); ok {
		ecrTraceNumber = DecodeBCD(traceData)
	}

	// Assign PT-generated transaction identifiers from the monotonic counter.
	// Receipt number (BMP 87): 4 decimal digits (mod 10000).
	// Trace number  (BMP 0B): 6 decimal digits (mod 1000000).
	seq := d.txCounter.Add(1)
	ptTraceNumber := seq % 1_000_000
	receiptNo := uint16(seq % 10_000)

	currency := CurrencyCodeToISO(currencyCode)
	var idempotencyKey string
	if ecrTraceNumber != 0 {
		idempotencyKey = fmt.Sprintf("zvt-trace-%d", ecrTraceNumber)
	}
	description := fmt.Sprintf("ZVT Payment receipt=%d trace=%d", receiptNo, ptTraceNumber)

	// Step 3: Create a cancellable context for the in-flight Mollie calls.
	pollCtx, cancel := context.WithCancel(ctx)
	session.cancelInFlight = cancel
	defer func() {
		cancel()
		session.cancelInFlight = nil
	}()

	// Step 4: Create the Mollie payment.
	payment, err := d.mollie.CreatePayment(pollCtx, amountCents, currency, description, idempotencyKey)
	if err != nil {
		slog.Error("authorization: CreatePayment failed", "err", err)
		d.writeStatusAndAbort(session, ResultCommError, nil)
		session.state = stateIdle
		return nil, nil
	}
	if payment.ChangePaymentStateURL != "" {
		slog.Info("test-mode checkout URL — open in browser to set payment outcome", "url", payment.ChangePaymentStateURL)
	}

	// Step 5: Poll until terminal status.
	payment, resultCode := d.pollPayment(pollCtx, session, payment)

	// Step 6: Build Status-Info BMPs.
	now := time.Now()
	timeVal := uint64(now.Hour())*10000 + uint64(now.Minute())*100 + uint64(now.Second())
	dateVal := uint64(now.Month())*100 + uint64(now.Day())

	extraBMPs := []BMP{
		{Tag: BMPTrace, Data: EncodeBCD(ptTraceNumber, 3)},
		{Tag: BMPTime, Data: EncodeBCD(timeVal, 3)},
		{Tag: BMPDate, Data: EncodeBCD(dateVal, 2)},
		{Tag: BMPCurrency, Data: EncodeBCD(currencyCode, 2)},
		{Tag: BMPReceiptNo, Data: EncodeBCD(uint64(receiptNo), 2)},
	}
	if resultCode == ResultSuccess {
		amtBytes := EncodeBCDAmount(amountCents)
		extraBMPs = append([]BMP{{Tag: BMPAmount, Data: amtBytes[:]}}, extraBMPs...)
	}

	// Step 7: Send Status-Info.
	statusFrame := buildStatusInfo(resultCode, extraBMPs)
	slog.Debug("zvt send", "remote", session.conn.RemoteAddr(), "hex", hex.EncodeToString(statusFrame))
	if _, err := session.conn.Write(statusFrame); err != nil {
		slog.Error("authorization: failed to write Status-Info", "err", err)
		session.state = stateIdle
		return nil, nil
	}

	// Step 8: Wait for ECR ACK (80 00 00).
	ackBuf := make([]byte, 3)
	if _, err := io.ReadFull(session.conn, ackBuf); err != nil {
		slog.Error("authorization: failed to read ECR ACK", "err", err)
		session.state = stateIdle
		return nil, nil
	}

	// Step 9: Persist receipt → payment ID mapping.
	if resultCode == ResultSuccess && payment != nil {
		receiptKey := fmt.Sprintf("%d", receiptNo)
		if err := d.store.SaveReceipt(receiptKey, payment.ID); err != nil {
			slog.Error("authorization: SaveReceipt failed", "err", err)
		}
	}

	// Step 10: Send Completion on success, Abort on failure (spec §2.2.9).
	var finalFrame []byte
	if resultCode == ResultSuccess {
		finalFrame = buildCompletion()
	} else {
		finalFrame = buildAbort(resultCode)
	}
	slog.Debug("zvt send", "remote", session.conn.RemoteAddr(), "hex", hex.EncodeToString(finalFrame))
	if _, err := session.conn.Write(finalFrame); err != nil {
		slog.Error("authorization: failed to write Completion/Abort", "err", err)
	}

	session.state = stateIdle
	return nil, nil
}

// pollPayment polls Mollie until the payment reaches a terminal status.
// It uses a two-phase poll interval: every 2 s for the first 16 s, then
// every 5 s until the context is cancelled (timeout).
// An intermediate status is sent to the ECR every 5 s when enabled.
func (d *Dispatcher) pollPayment(ctx context.Context, session *Session, initial *mollie.PaymentResult) (*mollie.PaymentResult, byte) {
	const (
		fastInterval  = 2 * time.Second
		slowInterval  = 5 * time.Second
		fastPhaseEnd  = 16 * time.Second
	)

	payment := initial
	if mollie.IsTerminalStatus(payment.Status) {
		return payment, statusToResultCode(payment.Status)
	}

	start := time.Now()
	var intermediateCh <-chan time.Time
	if session.intermediateOK {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		intermediateCh = t.C
	}
	pollTimer := time.NewTimer(fastInterval)
	defer pollTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return payment, ResultTimeout

		case <-intermediateCh:
			frame := buildIntermediateStatus(0x00)
			slog.Debug("zvt send", "remote", session.conn.RemoteAddr(), "hex", hex.EncodeToString(frame))
			_, _ = session.conn.Write(frame)

		case <-pollTimer.C:
			p, err := d.mollie.GetPayment(ctx, payment.ID)
			interval := fastInterval
			if time.Since(start) >= fastPhaseEnd {
				interval = slowInterval
			}
			if err != nil {
				slog.Warn("authorization: GetPayment transient error, retrying", "err", err)
				pollTimer.Reset(interval)
				continue
			}
			payment = p
			if mollie.IsTerminalStatus(payment.Status) {
				return payment, statusToResultCode(payment.Status)
			}
			pollTimer.Reset(interval)
		}
	}
}

// statusToResultCode maps a terminal Mollie payment status to the ZVT result code.
func statusToResultCode(status string) byte {
	switch status {
	case "paid":
		return ResultSuccess
	case "failed":
		return ResultCardError
	case "canceled":
		return ResultCanceled
	case "expired":
		return ResultTimeout
	default:
		return ResultSystemError
	}
}

// writeStatusAndAbort sends a Status-Info APDU followed by a Completion or Abort APDU
// to the ECR, depending on the result code (spec §2.2.9).
func (d *Dispatcher) writeStatusAndAbort(session *Session, result byte, bmps []BMP) {
	statusFrame := buildStatusInfo(result, bmps)
	slog.Debug("zvt send", "remote", session.conn.RemoteAddr(), "hex", hex.EncodeToString(statusFrame))
	if _, err := session.conn.Write(statusFrame); err != nil {
		slog.Error("zvt: failed to write Status-Info", "err", err)
		return
	}
	var finalFrame []byte
	if result == ResultSuccess {
		finalFrame = buildCompletion()
	} else {
		finalFrame = buildAbort(result)
	}
	slog.Debug("zvt send", "remote", session.conn.RemoteAddr(), "hex", hex.EncodeToString(finalFrame))
	if _, err := session.conn.Write(finalFrame); err != nil {
		slog.Error("zvt: failed to write Completion/Abort", "err", err)
	}
}

// buildStatusInfo encodes a Status-Info APDU (04 0F) with result code BMP 0x27
// prepended to any additional BMPs.
func buildStatusInfo(result byte, bmps []BMP) []byte {
	allBMPs := append([]BMP{{Tag: BMPResultCode, Data: []byte{result}}}, bmps...)
	data, _ := EncodeBMP(allBMPs)
	frame := []byte{ClassStatus, InstrStatusInfo}
	if len(data) <= 254 {
		frame = append(frame, byte(len(data)))
	} else {
		frame = append(frame, 0xFF, byte(len(data)>>8), byte(len(data)))
	}
	return append(frame, data...)
}

// buildCompletion returns a Completion APDU (06 0F 00).
func buildCompletion() []byte {
	return []byte{ClassPayment, InstrCompletion, 0x00}
}

// buildRegistrationCompletion returns the Completion APDU (06 0F) for the Registration
// sequence (spec §2.1). It includes BMP 19 (status-byte = 0x00: no issues).
func buildRegistrationCompletion() []byte {
	return []byte{ClassPayment, InstrCompletion, 0x02, 0x19, 0x00}
}

// buildAbort returns an Abort APDU (06 1E 01 <result-code>) as required by
// spec §2.2.9 when the transaction and/or issue of goods failed.
func buildAbort(resultCode byte) []byte {
	return []byte{ClassPayment, InstrAbortPT, 0x01, resultCode}
}

// buildIntermediateStatus returns an Intermediate Status APDU (04 FF 01 <status>).
func buildIntermediateStatus(status byte) []byte {
	return []byte{ClassStatus, InstrIntermediateStatus, 0x01, status}
}
