package zvt

import (
	"testing"
)

// --- BCD helpers ---

func TestEncodeBCDAmount_roundtrip(t *testing.T) {
	cases := []int64{0, 1, 99, 100, 1250, 99999999, 999999999999}
	for _, cents := range cases {
		b := EncodeBCDAmount(cents)
		got := DecodeBCDAmount(b)
		if got != cents {
			t.Errorf("amount %d: encoded %v, decoded %d", cents, b, got)
		}
	}
}

func TestEncodeBCDAmount_wire(t *testing.T) {
	// €12.50 = 1250 cents → BCD "000000001250" → 0x00 0x00 0x00 0x00 0x12 0x50
	b := EncodeBCDAmount(1250)
	want := [6]byte{0x00, 0x00, 0x00, 0x00, 0x12, 0x50}
	if b != want {
		t.Errorf("EncodeBCDAmount(1250) = %v, want %v", b, want)
	}
}

func TestEncodeBCD_roundtrip(t *testing.T) {
	cases := []struct {
		value uint64
		n     int
	}{
		{0, 2},
		{978, 2},
		{1, 3},
		{42, 3},
		{999999, 3},
	}
	for _, tc := range cases {
		b := EncodeBCD(tc.value, tc.n)
		got := DecodeBCD(b)
		if got != tc.value {
			t.Errorf("value %d n=%d: encoded %v, decoded %d", tc.value, tc.n, b, got)
		}
	}
}

func TestEncodeBCD_currencyEUR(t *testing.T) {
	// EUR = 978 → 2 bytes → 0x09 0x78
	b := EncodeBCD(978, 2)
	if b[0] != 0x09 || b[1] != 0x78 {
		t.Errorf("EncodeBCD(978,2) = %v, want [0x09 0x78]", b)
	}
}

// --- BMP encode/decode ---

func TestEncodeBMP_knownFixed(t *testing.T) {
	amtBytes := EncodeBCDAmount(1250)
	fields := []BMP{
		{Tag: BMPAmount, Data: amtBytes[:]},
		{Tag: BMPResultCode, Data: []byte{0x00}},
	}
	out, err := EncodeBMP(fields)
	if err != nil {
		t.Fatal(err)
	}
	// Expected: [0x04][6 bytes amount][0x27][0x00]
	if len(out) != 9 {
		t.Fatalf("encoded length %d, want 9", len(out))
	}
	if out[0] != BMPAmount {
		t.Errorf("first tag byte = 0x%02X, want 0x%02X", out[0], BMPAmount)
	}
	if out[7] != BMPResultCode {
		t.Errorf("result code tag byte = 0x%02X, want 0x%02X", out[7], BMPResultCode)
	}
	if out[8] != 0x00 {
		t.Errorf("result code value = 0x%02X, want 0x00", out[8])
	}
}

func TestEncodeBMP_LLVAR(t *testing.T) {
	fields := []BMP{
		{Tag: BMPCardName, Data: []byte("Mastercard")},
	}
	out, err := EncodeBMP(fields)
	if err != nil {
		t.Fatal(err)
	}
	// [0x8B][len=10][data...]
	if len(out) != 12 {
		t.Fatalf("encoded length %d, want 12", len(out))
	}
	if out[0] != BMPCardName {
		t.Errorf("tag = 0x%02X, want 0x%02X", out[0], BMPCardName)
	}
	if out[1] != 10 {
		t.Errorf("LLVAR len byte = %d, want 10", out[1])
	}
}

func TestEncodeBMP_wrongLength(t *testing.T) {
	fields := []BMP{
		{Tag: BMPAmount, Data: []byte{0x00}}, // should be 6 bytes
	}
	_, err := EncodeBMP(fields)
	if err == nil {
		t.Error("expected error for wrong data length")
	}
}

func TestEncodeBMP_unknownTag(t *testing.T) {
	fields := []BMP{
		{Tag: 0xFF, Data: []byte{0x00}},
	}
	_, err := EncodeBMP(fields)
	if err == nil {
		t.Error("expected error for unknown tag")
	}
}

func TestDecodeBMP_roundtrip(t *testing.T) {
	amtBytes := EncodeBCDAmount(5000)
	traceBytes := EncodeBCD(42, 3)
	original := []BMP{
		{Tag: BMPAmount, Data: amtBytes[:]},
		{Tag: BMPTrace, Data: traceBytes},
		{Tag: BMPResultCode, Data: []byte{0x00}},
	}
	encoded, err := EncodeBMP(original)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeBMP(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != len(original) {
		t.Fatalf("decoded %d fields, want %d", len(decoded), len(original))
	}
	for i, f := range decoded {
		if f.Tag != original[i].Tag {
			t.Errorf("field %d: tag 0x%02X, want 0x%02X", i, f.Tag, original[i].Tag)
		}
		if string(f.Data) != string(original[i].Data) {
			t.Errorf("field %d: data %v, want %v", i, f.Data, original[i].Data)
		}
	}
}

func TestDecodeBMP_LLVAR(t *testing.T) {
	encoded, err := EncodeBMP([]BMP{
		{Tag: BMPCardName, Data: []byte("Visa")},
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeBMP(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 {
		t.Fatalf("decoded %d fields, want 1", len(decoded))
	}
	if string(decoded[0].Data) != "Visa" {
		t.Errorf("card name = %q, want %q", decoded[0].Data, "Visa")
	}
}

func TestDecodeBMP_unknownTagStops(t *testing.T) {
	// Two known fields followed by an unknown tag byte.
	encoded, err := EncodeBMP([]BMP{
		{Tag: BMPResultCode, Data: []byte{0x00}},
		{Tag: BMPCardType, Data: []byte{0x01}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Append an unknown tag at the end.
	encoded = append(encoded, 0xFE, 0xAB)

	decoded, err := DecodeBMP(encoded)
	if err != nil {
		t.Fatal(err)
	}
	// Should have decoded the 2 known fields and stopped at the unknown one.
	if len(decoded) != 2 {
		t.Errorf("decoded %d fields, want 2", len(decoded))
	}
}

func TestFindBMP(t *testing.T) {
	fields := []BMP{
		{Tag: BMPResultCode, Data: []byte{0x42}},
		{Tag: BMPCardType, Data: []byte{0x01}},
	}
	data, ok := FindBMP(fields, BMPResultCode)
	if !ok || data[0] != 0x42 {
		t.Errorf("FindBMP(BMPResultCode) = %v, %v", data, ok)
	}
	_, ok = FindBMP(fields, BMPAmount)
	if ok {
		t.Error("FindBMP(BMPAmount) should return not-found")
	}
}

// --- Amount / currency helpers ---

func TestAmountToMollieValue(t *testing.T) {
	cases := []struct {
		cents int64
		want  string
	}{
		{0, "0.00"},
		{1, "0.01"},
		{100, "1.00"},
		{1250, "12.50"},
		{9999, "99.99"},
	}
	for _, tc := range cases {
		got := AmountToMollieValue(tc.cents)
		if got != tc.want {
			t.Errorf("AmountToMollieValue(%d) = %q, want %q", tc.cents, got, tc.want)
		}
	}
}

func TestMollieValueToCents(t *testing.T) {
	cases := []struct {
		s    string
		want int64
	}{
		{"0.00", 0},
		{"0.01", 1},
		{"1.00", 100},
		{"12.50", 1250},
		{"99.99", 9999},
	}
	for _, tc := range cases {
		got, err := MollieValueToCents(tc.s)
		if err != nil || got != tc.want {
			t.Errorf("MollieValueToCents(%q) = %d, %v; want %d", tc.s, got, err, tc.want)
		}
	}
}

func TestCurrencyCodeToISO(t *testing.T) {
	if got := CurrencyCodeToISO(978); got != "EUR" {
		t.Errorf("978 → %q, want EUR", got)
	}
	if got := CurrencyCodeToISO(840); got != "USD" {
		t.Errorf("840 → %q, want USD", got)
	}
}
