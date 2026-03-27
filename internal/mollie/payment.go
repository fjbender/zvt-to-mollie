package mollie

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/mollie/mollie-api-golang/models/apierrors"
	"github.com/mollie/mollie-api-golang/models/components"
	"github.com/mollie/mollie-api-golang/models/operations"
)

// PaymentResult holds the essential fields returned after creating or fetching
// a Mollie payment.
type PaymentResult struct {
	ID     string
	Status string
	// Amount fields for receipt population.
	AmountValue    string
	AmountCurrency string
	// ChangePaymentStateURL is set in test mode; the operator opens this URL
	// to select the payment outcome manually (no physical terminal required).
	ChangePaymentStateURL string
}

// RefundResult holds the essential fields returned after creating a Mollie
// refund.
type RefundResult struct {
	ID     string
	Status string
}

// ZVTError wraps a Mollie API error with the HTTP status code so callers can
// map it to the appropriate ZVT result code.
type ZVTError struct {
	StatusCode int
	Err        error
}

func (e *ZVTError) Error() string { return fmt.Sprintf("mollie %d: %v", e.StatusCode, e.Err) }
func (e *ZVTError) Unwrap() error { return e.Err }

// CreatePayment creates a new point-of-sale payment for the given amount (in
// cents), ISO 4217 currency code, and human-readable description.
// idempotencyKey should be derived from the ZVT trace number if set.
func (c *Client) CreatePayment(ctx context.Context, amountCents int64, currency, description, idempotencyKey string) (*PaymentResult, error) {
	amountValue := formatAmount(amountCents)

	method := components.CreateMethodMethodEnum(components.MethodEnumPointofsale)
	req := &components.PaymentRequest{
		Description: description,
		Amount: components.Amount{
			Currency: currency,
			Value:    amountValue,
		},
		Method:      &method,
		RedirectURL: nil, // not required for pointofsale payments
		TerminalID:  &c.terminalID,
	}

	var ikey *string
	if idempotencyKey != "" {
		ikey = &idempotencyKey
	}

	resp, err := c.sdk.Payments.Create(ctx, nil, ikey, req)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	if resp.PaymentResponse == nil {
		return nil, &ZVTError{StatusCode: http.StatusInternalServerError, Err: errors.New("empty payment response")}
	}

	p := resp.PaymentResponse
	links := p.GetLinks()
	return &PaymentResult{
		ID:             p.ID,
		Status:         string(p.Status),
		AmountValue:    p.Amount.Value,
		AmountCurrency: p.Amount.Currency,
		ChangePaymentStateURL: func() string {
			if s := links.GetChangePaymentState().GetHref(); s != nil {
				return *s
			}
			return ""
		}(),
	}, nil
}

// GetPayment retrieves the current state of a Mollie payment by ID.
func (c *Client) GetPayment(ctx context.Context, molliePaymentID string) (*PaymentResult, error) {
	resp, err := c.sdk.Payments.Get(ctx, operations.GetPaymentRequest{
		PaymentID: molliePaymentID,
	})
	if err != nil {
		return nil, wrapAPIError(err)
	}
	if resp.PaymentResponse == nil {
		return nil, &ZVTError{StatusCode: http.StatusInternalServerError, Err: errors.New("empty payment response")}
	}

	p := resp.PaymentResponse
	return &PaymentResult{
		ID:             p.ID,
		Status:         string(p.Status),
		AmountValue:    p.Amount.Value,
		AmountCurrency: p.Amount.Currency,
	}, nil
}

// CancelPayment cancels an open or pending Mollie payment.
func (c *Client) CancelPayment(ctx context.Context, molliePaymentID string) error {
	_, err := c.sdk.Payments.Cancel(ctx, molliePaymentID, nil, nil)
	if err != nil {
		return wrapAPIError(err)
	}
	return nil
}

// CreateRefund issues a refund for a paid Mollie payment.
// amountCents ≤ 0 means a full refund (uses the payment's own amount).
// idempotencyKey should be derived from the ZVT trace number.
func (c *Client) CreateRefund(ctx context.Context, molliePaymentID string, amountCents int64, currency, description, idempotencyKey string) (*RefundResult, error) {
	amountValue := formatAmount(amountCents)

	refundReq := &components.RefundRequest{
		Description: description,
		Amount: components.Amount{
			Currency: currency,
			Value:    amountValue,
		},
	}

	var ikey *string
	if idempotencyKey != "" {
		ikey = &idempotencyKey
	}

	resp, err := c.sdk.Refunds.Create(ctx, molliePaymentID, ikey, refundReq)
	if err != nil {
		return nil, wrapAPIError(err)
	}
	if resp.EntityRefundResponse == nil {
		return nil, &ZVTError{StatusCode: http.StatusInternalServerError, Err: errors.New("empty refund response")}
	}

	r := resp.EntityRefundResponse
	return &RefundResult{
		ID:     r.ID,
		Status: string(r.Status), // EntityRefundResponseStatus is a string typedef
	}, nil
}

// wrapAPIError inspects the error returned by the Mollie SDK and wraps it in a
// ZVTError so callers can inspect the HTTP status code.
func wrapAPIError(err error) error {
	var apiErr *apierrors.APIError
	if errors.As(err, &apiErr) {
		return &ZVTError{StatusCode: apiErr.StatusCode, Err: err}
	}
	// Network / timeout errors get mapped to 503.
	return &ZVTError{StatusCode: http.StatusServiceUnavailable, Err: err}
}

// formatAmount converts an integer cent amount to the Mollie decimal string.
func formatAmount(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

// IsTerminalStatus returns true when the Mollie payment status string
// represents a state from which the payment will not transition further.
func IsTerminalStatus(status string) bool {
	switch status {
	case "paid", "failed", "canceled", "expired":
		return true
	default:
		return false
	}
}

// IsPaid returns true when the status indicates a successful capture.
func IsPaid(status string) bool { return status == "paid" }

