package mollie

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"time"

	molliesdk "github.com/mollie/mollie-api-golang"
	"github.com/mollie/mollie-api-golang/models/components"
)

// Client wraps the Mollie SDK client with application-level configuration.
type Client struct {
	sdk        *molliesdk.Client
	terminalID string
}

// NewClient creates a new Mollie API client authenticated with the given API key.
// When verbose is true, all HTTP requests and responses are logged at debug level.
func NewClient(apiKey, terminalID string, timeout time.Duration, verbose bool) *Client {
	opts := []molliesdk.SDKOption{
		molliesdk.WithSecurity(components.Security{
			APIKey: &apiKey,
		}),
		molliesdk.WithTimeout(timeout),
	}
	if verbose {
		opts = append(opts, molliesdk.WithClient(&loggingHTTPClient{
			inner: &http.Client{Timeout: timeout},
		}))
	}
	sdk := molliesdk.New(opts...)
	return &Client{sdk: sdk, terminalID: terminalID}
}

// loggingHTTPClient logs raw HTTP request and response bodies at debug level.
type loggingHTTPClient struct {
	inner *http.Client
}

func (c *loggingHTTPClient) Do(req *http.Request) (*http.Response, error) {
	var reqBody string
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(b))
		reqBody = string(b)
	}
	slog.Debug("mollie request", "method", req.Method, "url", req.URL.String(), "body", reqBody)

	resp, err := c.inner.Do(req)
	if err != nil {
		return resp, err
	}

	b, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(b))
	slog.Debug("mollie response", "status", resp.StatusCode, "body", string(b))

	return resp, nil
}
