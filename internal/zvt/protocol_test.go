package zvt

import (
	"bytes"
	"testing"
)

func TestEncodeDecodeAPDU_shortLength(t *testing.T) {
	apdu := &APDU{
		ControlField: []byte{0x06, 0x01},
		Data:         []byte{0x04, 0x00, 0x00, 0x00, 0x12, 0x50}, // amount BMP
	}
	encoded, err := EncodeAPDU(apdu)
	if err != nil {
		t.Fatal(err)
	}
	// [0x06][0x01][0x06][data...]
	if encoded[0] != 0x06 || encoded[1] != 0x01 {
		t.Errorf("control field bytes wrong: %v", encoded[:2])
	}
	if encoded[2] != 6 {
		t.Errorf("length byte = %d, want 6", encoded[2])
	}
	if len(encoded) != 9 {
		t.Errorf("total length = %d, want 9", len(encoded))
	}

	got, err := ParseAPDU(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.ControlField[0] != 0x06 || got.ControlField[1] != 0x01 {
		t.Errorf("control field %v, want [0x06 0x01]", got.ControlField)
	}
	if !bytes.Equal(got.Data, apdu.Data) {
		t.Errorf("data %v, want %v", got.Data, apdu.Data)
	}
}

func TestEncodeDecodeAPDU_emptyData(t *testing.T) {
	apdu := &APDU{
		ControlField: []byte{0x80, 0x00},
		Data:         []byte{},
	}
	encoded, err := EncodeAPDU(apdu)
	if err != nil {
		t.Fatal(err)
	}
	// [0x80][0x00][0x00]
	if len(encoded) != 3 {
		t.Fatalf("length = %d, want 3", len(encoded))
	}
	if encoded[2] != 0x00 {
		t.Errorf("length byte = %d, want 0", encoded[2])
	}
}

func TestEncodeDecodeAPDU_extendedLength(t *testing.T) {
	// Data longer than 254 bytes triggers extended length encoding.
	data := make([]byte, 300)
	for i := range data {
		data[i] = byte(i)
	}
	apdu := &APDU{
		ControlField: []byte{0x06, 0x01},
		Data:         data,
	}
	encoded, err := EncodeAPDU(apdu)
	if err != nil {
		t.Fatal(err)
	}
	// [CLASS][INSTR][0xFF][hi][lo][data...]
	if encoded[2] != 0xFF {
		t.Errorf("length sentinel = 0x%02X, want 0xFF", encoded[2])
	}
	extLen := int(encoded[3])<<8 | int(encoded[4])
	if extLen != 300 {
		t.Errorf("extended length = %d, want 300", extLen)
	}
	if len(encoded) != 5+300 {
		t.Errorf("total length = %d, want %d", len(encoded), 5+300)
	}

	got, err := ParseAPDU(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Data, data) {
		t.Error("data mismatch after round-trip")
	}
}

func TestReadWriteAPDU_stream(t *testing.T) {
	apdu := &APDU{
		ControlField: []byte{0x04, 0x0F},
		Data:         []byte{0x27, 0x00, 0x04, 0x00, 0x00, 0x00, 0x12, 0x50},
	}

	var buf bytes.Buffer
	if err := WriteAPDU(&buf, apdu); err != nil {
		t.Fatal(err)
	}

	got, err := ReadAPDU(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.ControlField, apdu.ControlField) {
		t.Errorf("control field %v, want %v", got.ControlField, apdu.ControlField)
	}
	if !bytes.Equal(got.Data, apdu.Data) {
		t.Errorf("data %v, want %v", got.Data, apdu.Data)
	}
}

func TestReadWriteAPDU_extendedStream(t *testing.T) {
	data := make([]byte, 500)
	for i := range data {
		data[i] = byte(i % 256)
	}
	apdu := &APDU{
		ControlField: []byte{0x06, 0x01},
		Data:         data,
	}

	var buf bytes.Buffer
	if err := WriteAPDU(&buf, apdu); err != nil {
		t.Fatal(err)
	}

	got, err := ReadAPDU(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Data, data) {
		t.Error("data mismatch for extended-length stream APDU")
	}
}

func TestReadAPDU_eof(t *testing.T) {
	var buf bytes.Buffer // empty
	_, err := ReadAPDU(&buf)
	if err == nil {
		t.Error("expected error on empty reader")
	}
}

func TestParseAPDU_tooShort(t *testing.T) {
	_, err := ParseAPDU([]byte{0x06})
	if err == nil {
		t.Error("expected error for 1-byte input")
	}
}

func TestParseAPDU_dataUnderrun(t *testing.T) {
	// Header + length says 10 bytes but only 3 follow.
	raw := []byte{0x06, 0x01, 0x0A, 0xAA, 0xBB, 0xCC}
	_, err := ParseAPDU(raw)
	if err == nil {
		t.Error("expected error for data underrun")
	}
}

func TestEncodeAPDU_wrongControlField(t *testing.T) {
	apdu := &APDU{ControlField: []byte{0x06}, Data: nil}
	_, err := EncodeAPDU(apdu)
	if err == nil {
		t.Error("expected error for 1-byte ControlField")
	}
}

// TestFrameACK verifies the canonical ACK frame bytes.
func TestFrameACK(t *testing.T) {
	if FrameACK[0] != 0x80 || FrameACK[1] != 0x00 || FrameACK[2] != 0x00 {
		t.Errorf("FrameACK = %v, want [0x80 0x00 0x00]", FrameACK)
	}
}

// TestFrameUnknown verifies the unknown-command response bytes.
func TestFrameUnknown(t *testing.T) {
	if FrameUnknown[0] != 0x84 || FrameUnknown[1] != 0x83 || FrameUnknown[2] != 0x00 {
		t.Errorf("FrameUnknown = %v, want [0x84 0x83 0x00]", FrameUnknown)
	}
}
