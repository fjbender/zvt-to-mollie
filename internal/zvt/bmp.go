package zvt

import (
	"fmt"
)

// BMP tag byte constants.
const (
	BMPAmount      byte = 0x04 // 6 bytes BCD
	BMPTrace       byte = 0x0B // 3 bytes BCD
	BMPTime        byte = 0x0C // 3 bytes BCD HHMMSS
	BMPDate        byte = 0x0D // 2 bytes BCD MMDD
	BMPPaymentType byte = 0x19 // 1 byte bitmask
	BMPPAN         byte = 0x22 // LLVAR BCD
	BMPResultCode  byte = 0x27 // 1 byte
	BMPTerminalID  byte = 0x29 // 4 bytes BCD
	BMPOrigTrace   byte = 0x37 // 3 bytes BCD
	BMPCurrency    byte = 0x49 // 2 bytes BCD
	BMPReceiptNo   byte = 0x87 // 2 bytes BCD
	BMPCardType    byte = 0x8A // 1 byte
	BMPCardName    byte = 0x8B // LLVAR ASCII
)

// bmpFixedLen maps each known BMP tag to its fixed byte length.
// A value of -1 signals an LLVAR field (length-prefixed).
var bmpFixedLen = map[byte]int{
	BMPAmount:      6,
	BMPTrace:       3,
	BMPTime:        3,
	BMPDate:        2,
	BMPPaymentType: 1,
	BMPPAN:         -1,
	BMPResultCode:  1,
	BMPTerminalID:  4,
	BMPOrigTrace:   3,
	BMPCurrency:    2,
	BMPReceiptNo:   2,
	BMPCardType:    1,
	BMPCardName:    -1,
}

// BMP (Bitmap) is a ZVT field identified by a single tag byte.
type BMP struct {
	Tag  byte
	Data []byte
}

// EncodeBMP serialises a slice of BMP fields into the ZVT binary format.
// Each field is written as [tag][data]; LLVAR fields prepend a length byte.
func EncodeBMP(fields []BMP) ([]byte, error) {
	var out []byte
	for _, f := range fields {
		out = append(out, f.Tag)
		l, known := bmpFixedLen[f.Tag]
		if !known {
			return nil, fmt.Errorf("unknown BMP tag 0x%02X", f.Tag)
		}
		if l == -1 {
			// LLVAR: length byte followed by data
			if len(f.Data) > 0xFF {
				return nil, fmt.Errorf("BMP tag 0x%02X data too long: %d", f.Tag, len(f.Data))
			}
			out = append(out, byte(len(f.Data)))
		} else if len(f.Data) != l {
			return nil, fmt.Errorf("BMP tag 0x%02X: expected %d bytes, got %d", f.Tag, l, len(f.Data))
		}
		out = append(out, f.Data...)
	}
	return out, nil
}

// DecodeBMP parses ZVT BMP-encoded bytes into a slice of BMP fields.
// Unknown tags stop parsing silently (the remaining bytes are returned unused).
func DecodeBMP(data []byte) ([]BMP, error) {
	var fields []BMP
	i := 0
	for i < len(data) {
		tag := data[i]
		i++

		l, known := bmpFixedLen[tag]
		if !known {
			// Unknown tag — stop; we cannot determine its length.
			break
		}

		var fieldLen int
		if l == -1 {
			// LLVAR: next byte is the length
			if i >= len(data) {
				return nil, fmt.Errorf("BMP tag 0x%02X: missing LLVAR length byte", tag)
			}
			fieldLen = int(data[i])
			i++
		} else {
			fieldLen = l
		}

		if i+fieldLen > len(data) {
			return nil, fmt.Errorf("BMP tag 0x%02X: need %d bytes, only %d remaining", tag, fieldLen, len(data)-i)
		}

		payload := make([]byte, fieldLen)
		copy(payload, data[i:i+fieldLen])
		fields = append(fields, BMP{Tag: tag, Data: payload})
		i += fieldLen
	}
	return fields, nil
}

// FindBMP returns the data bytes for the first BMP with the given tag, and
// reports whether it was found.
func FindBMP(fields []BMP, tag byte) ([]byte, bool) {
	for _, f := range fields {
		if f.Tag == tag {
			return f.Data, true
		}
	}
	return nil, false
}

// --- BCD helpers ---

// EncodeBCDAmount packs an integer amount (in currency smallest units, e.g. cents)
// into the 6-byte BCD format used by ZVT (12 decimal digits, big-endian).
func EncodeBCDAmount(cents int64) [6]byte {
	var b [6]byte
	// Work right-to-left; each byte holds 2 decimal digits.
	for i := 5; i >= 0; i-- {
		lo := cents % 10
		cents /= 10
		hi := cents % 10
		cents /= 10
		b[i] = byte(hi<<4) | byte(lo)
	}
	return b
}

// DecodeBCDAmount unpacks a 6-byte BCD amount into an integer (smallest currency unit).
func DecodeBCDAmount(b [6]byte) int64 {
	var v int64
	for _, x := range b {
		v = v*100 + int64((x>>4)&0x0F)*10 + int64(x&0x0F)
	}
	return v
}

// EncodeBCD packs an unsigned integer into n BCD bytes.
// The value is formatted as 2*n decimal digits, big-endian.
func EncodeBCD(value uint64, n int) []byte {
	b := make([]byte, n)
	for i := n - 1; i >= 0; i-- {
		lo := value % 10
		value /= 10
		hi := value % 10
		value /= 10
		b[i] = byte(hi<<4) | byte(lo)
	}
	return b
}

// DecodeBCD unpacks n BCD bytes into an unsigned integer.
func DecodeBCD(b []byte) uint64 {
	var v uint64
	for _, x := range b {
		v = v*100 + uint64((x>>4)&0x0F)*10 + uint64(x&0x0F)
	}
	return v
}

// AmountToMollieValue converts an integer amount in the smallest currency unit
// (e.g. 1250 cents) into the Mollie decimal string format ("12.50").
func AmountToMollieValue(cents int64) string {
	euros := cents / 100
	c := cents % 100
	return fmt.Sprintf("%d.%02d", euros, c)
}

// MollieValueToCents parses a Mollie decimal string ("12.50") into the smallest
// currency unit (1250).
func MollieValueToCents(s string) (int64, error) {
	var euros, cents int64
	n, err := fmt.Sscanf(s, "%d.%02d", &euros, &cents)
	if err != nil || n != 2 {
		return 0, fmt.Errorf("invalid Mollie amount %q", s)
	}
	return euros*100 + cents, nil
}

// CurrencyCodeToISO converts a ZVT numeric currency code (e.g. 978) to its
// ISO 4217 three-letter code (e.g. "EUR").  Only codes used by this converter
// are mapped; unknown codes fall back to the 4-digit numeric string.
func CurrencyCodeToISO(code uint64) string {
	switch code {
	case 978:
		return "EUR"
	case 840:
		return "USD"
	case 826:
		return "GBP"
	default:
		return fmt.Sprintf("%04d", code)
	}
}

// ISOToCurrencyCode converts a 3-letter ISO 4217 currency code to its ZVT
// numeric equivalent. Returns 0 and an error for unknown codes.
func ISOToCurrencyCode(iso string) (uint16, error) {
	switch iso {
	case "EUR":
		return 978, nil
	case "USD":
		return 840, nil
	case "GBP":
		return 826, nil
	default:
		return 0, fmt.Errorf("unknown ISO currency %q", iso)
	}
}

// EncodeCurrencyBMP returns a BMP field for tag 0x49 with the given numeric
// currency code packed as 2-byte BCD (4 digits, e.g. 978 → 0x09 0x78).
func EncodeCurrencyBMP(code uint16) BMP {
	return BMP{Tag: BMPCurrency, Data: EncodeBCD(uint64(code), 2)}
}
