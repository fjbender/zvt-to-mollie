package zvt

import (
	"context"
	"net"
	"testing"
)

// newTestDispatcher returns a Dispatcher with no mollie/store dependencies
// (safe for handlers that don't reach those layers).
func newTestDispatcher(password, currencyCode string) *Dispatcher {
	return NewDispatcher(nil, nil, password, currencyCode)
}

// newTestSession returns a Session backed by a net.Pipe connection, plus the
// peer end so tests can write inbound bytes / read responses if needed.
func newTestSession(d *Dispatcher) (*Session, net.Conn) {
	client, server := net.Pipe()
	s := NewSession(server, d)
	return s, client
}

func dispatch(t *testing.T, d *Dispatcher, s *Session, raw []byte) []byte {
	t.Helper()
	apdu, err := ParseAPDU(raw)
	if err != nil {
		t.Fatalf("ParseAPDU: %v", err)
	}
	resp, err := d.Dispatch(context.Background(), apdu, s)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	return resp
}

// --- Unknown command ---

func TestDispatch_UnknownCommand(t *testing.T) {
	d := newTestDispatcher("000000", "978")
	s, peer := newTestSession(d)
	defer peer.Close()

	// An arbitrary command not in the routing table.
	raw := []byte{0x06, 0xFF, 0x00}
	resp := dispatch(t, d, s, raw)

	if string(resp) != string(FrameUnknown) {
		t.Errorf("expected FrameUnknown %x, got %x", FrameUnknown, resp)
	}
}

// --- Registration (06 00) ---

func registrationAPDU(password [3]byte, configByte byte, extraBMPs []byte) []byte {
	data := append(password[:], configByte)
	data = append(data, extraBMPs...)
	frame := []byte{ClassPayment, InstrRegistration, byte(len(data))}
	return append(frame, data...)
}

func TestRegistration_HappyPath(t *testing.T) {
	d := newTestDispatcher("000000", "978")
	s, peer := newTestSession(d)
	defer peer.Close()

	raw := registrationAPDU([3]byte{0x00, 0x00, 0x00}, 0x00, nil)
	resp := dispatch(t, d, s, raw)

	if string(resp) != string(FrameACK) {
		t.Errorf("expected FrameACK %x, got %x", FrameACK, resp)
	}
	if s.intermediateOK {
		t.Error("intermediateOK should be false when config byte 0x08 is not set")
	}
}

func TestRegistration_IntermediateOK(t *testing.T) {
	d := newTestDispatcher("000000", "978")
	s, peer := newTestSession(d)
	defer peer.Close()

	raw := registrationAPDU([3]byte{0x00, 0x00, 0x00}, 0x08, nil)
	resp := dispatch(t, d, s, raw)

	if string(resp) != string(FrameACK) {
		t.Errorf("expected FrameACK %x, got %x", FrameACK, resp)
	}
	if !s.intermediateOK {
		t.Error("intermediateOK should be true when config byte bit 0x08 is set")
	}
}

func TestRegistration_WrongPassword(t *testing.T) {
	d := newTestDispatcher("123456", "978")
	s, peer := newTestSession(d)
	defer peer.Close()

	// Send password 000000 (all zero BCD) instead of 123456.
	raw := registrationAPDU([3]byte{0x00, 0x00, 0x00}, 0x00, nil)
	resp := dispatch(t, d, s, raw)

	if string(resp) != string(FrameUnknown) {
		t.Errorf("expected FrameUnknown %x, got %x", FrameUnknown, resp)
	}
}

func TestRegistration_WrongCurrency(t *testing.T) {
	d := newTestDispatcher("000000", "978") // configured EUR (978)
	s, peer := newTestSession(d)
	defer peer.Close()

	// BMP 49 with USD (840 = 0x08 0x40).
	bmp49 := []byte{BMPCurrency, 0x08, 0x40}
	raw := registrationAPDU([3]byte{0x00, 0x00, 0x00}, 0x00, bmp49)
	resp := dispatch(t, d, s, raw)

	want := []byte{ClassPayment, InstrAbortPT, 0x01, ResultWrongCurrency}
	if string(resp) != string(want) {
		t.Errorf("expected abort frame %x, got %x", want, resp)
	}
}

func TestRegistration_CorrectCurrencyBMP(t *testing.T) {
	d := newTestDispatcher("000000", "978")
	s, peer := newTestSession(d)
	defer peer.Close()

	// BMP 49 with EUR (978 = 0x09 0x78).
	bmp49 := []byte{BMPCurrency, 0x09, 0x78}
	raw := registrationAPDU([3]byte{0x00, 0x00, 0x00}, 0x00, bmp49)
	resp := dispatch(t, d, s, raw)

	if string(resp) != string(FrameACK) {
		t.Errorf("expected FrameACK %x, got %x", FrameACK, resp)
	}
}

func TestRegistration_TooShort(t *testing.T) {
	d := newTestDispatcher("000000", "978")
	s, peer := newTestSession(d)
	defer peer.Close()

	// Only 2 bytes of data (need at least 4).
	raw := []byte{ClassPayment, InstrRegistration, 0x02, 0x00, 0x00}
	resp := dispatch(t, d, s, raw)

	if string(resp) != string(FrameUnknown) {
		t.Errorf("expected FrameUnknown %x, got %x", FrameUnknown, resp)
	}
}
