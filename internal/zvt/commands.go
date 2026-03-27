package zvt

// ZVT command class bytes.
const (
	ClassPayment byte = 0x06
	ClassStatus  byte = 0x04
	ClassACK     byte = 0x80
)

// ZVT instruction bytes for commands received from the ECR (ECR → PT).
const (
	InstrRegistration  byte = 0x00
	InstrAuthorization byte = 0x01
	InstrLogOff        byte = 0x02
	InstrReversal      byte = 0x30
	InstrRefund        byte = 0x31
	InstrAbortECR      byte = 0xB0
)

// ZVT instruction bytes for messages sent to the ECR (PT → ECR).
const (
	InstrStatusInfo         byte = 0x0F // class ClassStatus (0x04)
	InstrIntermediateStatus byte = 0xFF // class ClassStatus (0x04)
	InstrCompletion         byte = 0x0F // class ClassPayment (0x06)
	InstrAbortPT            byte = 0x1E // class ClassPayment (0x06)
)

// ZVT result codes returned to the ECR (ZVT spec chapter 10).
const (
	ResultSuccess        byte = 0x00
	ResultCardError      byte = 0x62 // card not readable / method not supported
	ResultCommError      byte = 0x63 // communication error with Mollie API
	ResultNotFound       byte = 0x64 // transaction not found
	ResultCanceled       byte = 0x6A // transaction cancelled
	ResultRevNotPossible byte = 0x6C // reversal not possible
	ResultWrongCurrency  byte = 0x6F // wrong currency code
	ResultTimeout        byte = 0x9C // timeout waiting for payment
	ResultSystemError    byte = 0xA0 // unexpected / system error
)

// FrameACK is the positive acknowledgement frame (80-00-00), used by the ECR
// to acknowledge PT→ECR messages, and by the PT to acknowledge Registration.
var FrameACK = []byte{0x80, 0x00, 0x00}

// FrameUnknown is sent by the PT when an unsupported command is received.
var FrameUnknown = []byte{0x84, 0x83, 0x00}
