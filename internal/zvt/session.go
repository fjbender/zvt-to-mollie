package zvt

import (
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
)

type state int

const (
	stateIdle          state = iota
	stateInTransaction       // a Mollie payment is in-flight
	stateClose               // LogOff received; connection should be closed
)

// Session owns a single TCP connection from an ECR device and drives the
// per-connection ZVT state machine.
type Session struct {
	conn           net.Conn
	dispatcher     *Dispatcher
	state          state
	intermediateOK bool               // set by Registration config-byte bit 0x08
	cancelInFlight context.CancelFunc // non-nil while stateInTransaction
}

// NewSession creates a Session for the given connection using the provided dispatcher.
func NewSession(conn net.Conn, dispatcher *Dispatcher) *Session {
	return &Session{
		conn:       conn,
		dispatcher: dispatcher,
		state:      stateIdle,
	}
}

// Run is the APDU read loop for this connection. It blocks until EOF, a
// context cancellation, or the state machine transitions to stateClose.
func (s *Session) Run(ctx context.Context) {
	defer s.conn.Close()

	for {
		apdu, err := ReadAPDU(s.conn)
		if err != nil {
			if err != io.EOF {
				slog.Error("zvt read error", "remote", s.conn.RemoteAddr(), "err", err)
			}
			return
		}
		if raw, encErr := EncodeAPDU(apdu); encErr == nil {
			slog.Debug("zvt recv", "remote", s.conn.RemoteAddr(), "hex", hex.EncodeToString(raw))
		}

		// While a transaction is in-flight, only Abort is accepted; all other
		// commands receive FrameUnknown and are ignored.
		if s.state == stateInTransaction {
			isAbort := len(apdu.ControlField) == 2 &&
				apdu.ControlField[0] == ClassPayment &&
				apdu.ControlField[1] == InstrAbortECR
			if !isAbort {
				slog.Debug("zvt send", "remote", s.conn.RemoteAddr(), "hex", hex.EncodeToString(FrameUnknown))
				_, _ = s.conn.Write(FrameUnknown)
				continue
			}
		}

		resp, err := s.dispatcher.Dispatch(ctx, apdu, s)
		if err != nil {
			slog.Error("zvt dispatch error", "remote", s.conn.RemoteAddr(), "err", err)
			slog.Debug("zvt send", "remote", s.conn.RemoteAddr(), "hex", hex.EncodeToString(FrameUnknown))
			_, _ = s.conn.Write(FrameUnknown)
		} else if len(resp) > 0 {
			slog.Debug("zvt send", "remote", s.conn.RemoteAddr(), "hex", hex.EncodeToString(resp))
			_, _ = s.conn.Write(resp)
		}

		if s.state == stateClose {
			return
		}
	}
}
