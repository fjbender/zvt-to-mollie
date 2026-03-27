package zvt

import (
	"encoding/binary"
	"fmt"
	"io"
)

// APDU represents a parsed ZVT Application Protocol Data Unit.
type APDU struct {
	// ControlField holds the two command bytes [CLASS, INSTR].
	ControlField []byte
	// Data holds the raw payload (BMP-encoded fields or fixed-format fields).
	Data []byte
}

// ReadAPDU reads exactly one APDU from r using the ZVT framing rules.
//
// Frame format (ZVT 13.13 §1):
//
//	[CLASS] [INSTR] [LEN] [DATA...]
//
// LEN is 1 byte if the data length is ≤ 254.
// If LEN == 0xFF, the actual length follows as 2 big-endian bytes.
func ReadAPDU(r io.Reader) (*APDU, error) {
	// Read CLASS + INSTR.
	header := make([]byte, 2)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, fmt.Errorf("read APDU header: %w", err)
	}

	// Read length byte.
	lenBuf := make([]byte, 1)
	if _, err := io.ReadFull(r, lenBuf); err != nil {
		return nil, fmt.Errorf("read APDU length: %w", err)
	}

	var dataLen int
	if lenBuf[0] == 0xFF {
		// Extended length: read 2 more bytes.
		extLen := make([]byte, 2)
		if _, err := io.ReadFull(r, extLen); err != nil {
			return nil, fmt.Errorf("read APDU extended length: %w", err)
		}
		dataLen = int(binary.BigEndian.Uint16(extLen))
	} else {
		dataLen = int(lenBuf[0])
	}

	data := make([]byte, dataLen)
	if dataLen > 0 {
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, fmt.Errorf("read APDU data (%d bytes): %w", dataLen, err)
		}
	}

	return &APDU{
		ControlField: header,
		Data:         data,
	}, nil
}

// WriteAPDU encodes apdu and writes it to w in the ZVT wire format.
func WriteAPDU(w io.Writer, apdu *APDU) error {
	encoded, err := EncodeAPDU(apdu)
	if err != nil {
		return err
	}
	_, err = w.Write(encoded)
	return err
}

// ParseAPDU parses raw bytes into an APDU. The input must contain exactly one
// complete APDU frame; any trailing bytes are ignored.
func ParseAPDU(raw []byte) (*APDU, error) {
	if len(raw) < 3 {
		return nil, fmt.Errorf("APDU too short: need at least 3 bytes, got %d", len(raw))
	}

	header := raw[:2]
	offset := 2

	var dataLen int
	if raw[offset] == 0xFF {
		if len(raw) < 5 {
			return nil, fmt.Errorf("APDU too short for extended length")
		}
		dataLen = int(binary.BigEndian.Uint16(raw[offset+1 : offset+3]))
		offset += 3
	} else {
		dataLen = int(raw[offset])
		offset++
	}

	if offset+dataLen > len(raw) {
		return nil, fmt.Errorf("APDU data underrun: need %d bytes, %d available", dataLen, len(raw)-offset)
	}

	data := make([]byte, dataLen)
	copy(data, raw[offset:offset+dataLen])

	return &APDU{
		ControlField: []byte{header[0], header[1]},
		Data:         data,
	}, nil
}

// EncodeAPDU serialises an APDU into the ZVT wire format.
func EncodeAPDU(apdu *APDU) ([]byte, error) {
	if len(apdu.ControlField) != 2 {
		return nil, fmt.Errorf("ControlField must be exactly 2 bytes")
	}

	dataLen := len(apdu.Data)
	var buf []byte
	buf = append(buf, apdu.ControlField...)

	if dataLen <= 254 {
		buf = append(buf, byte(dataLen))
	} else {
		buf = append(buf, 0xFF)
		ext := make([]byte, 2)
		binary.BigEndian.PutUint16(ext, uint16(dataLen))
		buf = append(buf, ext...)
	}

	buf = append(buf, apdu.Data...)
	return buf, nil
}
