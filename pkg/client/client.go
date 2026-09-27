// Package client is a small, dependency-free Go SDK for the Credential Relayer API.
//
// It wraps the three public endpoints -- synchronous relay, async webhook
// enqueue, and job status polling -- and handles auth, JSON encoding, and error
// mapping so callers don't hand-roll net/http requests.
//
//	c := client.New("https://relayer.example.com", "my-api-key")
//	resp, err := c.Relay(ctx, client.RelayRequest{Target: "httpbin", Method: "GET", Path: "/get"})
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to a Credential Relayer instance. Construct it with New.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// Option customizes a Client at construction time.
type Option func(*Client)

// WithHTTPClient overrides the underlying *http.Client (e.g. to set custom
// transport, proxy, or timeouts).
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.httpClient = hc
		}
	}
}

// WithTimeout sets the per-request timeout on the default HTTP client.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.httpClient.Timeout = d }
}

// New creates a Client for the given base URL (scheme + host, no trailing path)
// and API key. The key is sent as the X-API-Key header on every request.
func New(baseURL, apiKey string, opts ...Option) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// RelayRequest is the payload for a synchronous relay.
type RelayRequest struct {
	Target  string            `json:"target"`
	Method  string            `json:"method,omitempty"`
	Path    string            `json:"path,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

// RelayResponse is the relayer's reply to a synchronous relay.
type RelayResponse struct {
	Status       string          `json:"status"`
	Target       string          `json:"target"`
	ResponseCode int             `json:"response_code,omitempty"`
	ResponseBody json.RawMessage `json:"response_body,omitempty"`
	Error        string          `json:"error,omitempty"`
	Attempts     int             `json:"attempts"`
	Latency      string          `json:"latency"`
}

// WebhookRequest is the payload for an async (queued) relay.
type WebhookRequest struct {
	Target  string            `json:"target"`
	Method  string            `json:"method,omitempty"`
	Path    string            `json:"path,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

// Job is the status record returned by JobStatus.
type Job struct {
	ID           string    `json:"id"`
	Target       string    `json:"target"`
	Method       string    `json:"method"`
	Path         string    `json:"path"`
	Status       string    `json:"status"`
	Attempts     int       `json:"attempts"`
	MaxRetries   int       `json:"max_retries"`
	ResponseCode int       `json:"response_code,omitempty"`
	Error        string    `json:"error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// APIError is returned when the relayer responds with a non-2xx status. It
// carries the HTTP status code and the server's error message.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("relayer returned %d: %s", e.StatusCode, e.Message)
}

// Relay performs a synchronous relay. A 2xx from the relayer (including upstream
// 4xx forwarded back) returns the decoded RelayResponse; a relayer-level failure
// (502, 4xx auth/validation) returns an *APIError.
func (c *Client) Relay(ctx context.Context, req RelayRequest) (*RelayResponse, error) {
	var out RelayResponse
	if err := c.do(ctx, http.MethodPost, "/api/v1/relay", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Webhook enqueues an async relay and returns the created job ID.
func (c *Client) Webhook(ctx context.Context, req WebhookRequest) (string, error) {
	var out struct {
		Status string `json:"status"`
		JobID  string `json:"job_id"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/webhook", req, &out); err != nil {
		return "", err
	}
	return out.JobID, nil
}

// JobStatus fetches the current state of a previously enqueued job.
func (c *Client) JobStatus(ctx context.Context, id string) (*Job, error) {
	var out Job
	if err := c.do(ctx, http.MethodGet, "/api/v1/jobs/"+id, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// do executes a request: marshals body (if any), sets headers, decodes the
// response into out, and maps non-2xx responses to *APIError.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshaling request: %w", err)
		}
		reader = bytes.NewReader(data)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	httpReq.Header.Set("X-API-Key", c.apiKey)
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{StatusCode: resp.StatusCode, Message: extractError(data)}
	}

	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}
	return nil
}

// extractError pulls the {"error":"..."} message out of a relayer error body,
// falling back to the raw body when it isn't the expected shape.
func extractError(data []byte) string {
	var e struct {
		Error  string `json:"error"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &e); err == nil && e.Error != "" {
		return e.Error
	}
	return strings.TrimSpace(string(data))
}
