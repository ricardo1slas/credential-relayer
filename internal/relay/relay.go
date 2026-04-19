package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rs/zerolog"
)

type RelayRequest struct {
	Target  string            `json:"target"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

type RelayResponse struct {
	Status       string          `json:"status"`
	Target       string          `json:"target"`
	ResponseCode int             `json:"response_code,omitempty"`
	ResponseBody json.RawMessage `json:"response_body,omitempty"`
	Error        string          `json:"error,omitempty"`
	Attempts     int             `json:"attempts"`
	Latency      string          `json:"latency"`
}

type Handler struct {
	registry *TargetRegistry
	client   *http.Client
	logger   zerolog.Logger
}

func NewHandler(registry *TargetRegistry, logger zerolog.Logger) *Handler {
	return &Handler{
		registry: registry,
		client:   &http.Client{},
		logger:   logger,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	var req RelayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, RelayResponse{
			Status:  "error",
			Error:   "invalid request body: " + err.Error(),
			Latency: time.Since(start).String(),
		})
		return
	}

	if req.Target == "" {
		writeJSON(w, http.StatusBadRequest, RelayResponse{
			Status:  "error",
			Error:   "target is required",
			Latency: time.Since(start).String(),
		})
		return
	}

	if req.Method == "" {
		req.Method = http.MethodPost
	}

	target, err := h.registry.Get(req.Target)
	if err != nil {
		writeJSON(w, http.StatusNotFound, RelayResponse{
			Status:  "error",
			Target:  req.Target,
			Error:   err.Error(),
			Latency: time.Since(start).String(),
		})
		return
	}

	resp, attempts, err := h.relayWithRetries(r.Context(), target, req)
	if err != nil {
		h.logger.Error().
			Err(err).
			Str("target", req.Target).
			Int("attempts", attempts).
			Msg("relay failed")

		writeJSON(w, http.StatusBadGateway, RelayResponse{
			Status:   "failed",
			Target:   req.Target,
			Error:    err.Error(),
			Attempts: attempts,
			Latency:  time.Since(start).String(),
		})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)

	h.logger.Info().
		Str("target", req.Target).
		Int("status", resp.StatusCode).
		Int("attempts", attempts).
		Dur("latency", time.Since(start)).
		Msg("relay succeeded")

	writeJSON(w, http.StatusOK, RelayResponse{
		Status:       "relayed",
		Target:       req.Target,
		ResponseCode: resp.StatusCode,
		ResponseBody: body,
		Attempts:     attempts,
		Latency:      time.Since(start).String(),
	})
}

// ExecuteRelay performs a relay to a named target. Used by both the HTTP handler and the async worker pool.
func (h *Handler) ExecuteRelay(ctx context.Context, targetName, method, path string, headers map[string]string, body json.RawMessage) (*http.Response, int, error) {
	target, err := h.registry.Get(targetName)
	if err != nil {
		return nil, 0, err
	}

	req := RelayRequest{
		Target:  targetName,
		Method:  method,
		Path:    path,
		Headers: headers,
		Body:    body,
	}
	if req.Method == "" {
		req.Method = http.MethodPost
	}

	return h.relayWithRetries(ctx, target, req)
}

func (h *Handler) relayWithRetries(ctx context.Context, target TargetConfig, req RelayRequest) (*http.Response, int, error) {
	var lastErr error

	for attempt := 0; attempt <= target.Retries; attempt++ {
		if attempt > 0 {
			delay := backoff(attempt - 1)
			h.logger.Warn().
				Str("target", target.Name).
				Int("attempt", attempt+1).
				Dur("backoff", delay).
				Err(lastErr).
				Msg("retrying relay")

			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, attempt, fmt.Errorf("request cancelled: %w", ctx.Err())
			}
		}

		resp, err := h.doRelay(ctx, target, req)
		if err != nil {
			lastErr = err
			continue
		}

		// Don't retry on client errors (4xx) -- only on server errors (5xx) and transport failures.
		if resp.StatusCode >= 500 {
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("target returned %d", resp.StatusCode)
			continue
		}

		return resp, attempt + 1, nil
	}

	return nil, target.Retries + 1, fmt.Errorf("all %d attempts failed: %w", target.Retries+1, lastErr)
}

func (h *Handler) doRelay(ctx context.Context, target TargetConfig, req RelayRequest) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, target.Timeout)
	defer cancel()

	url := target.URL + req.Path

	var bodyReader io.Reader
	if req.Body != nil {
		bodyReader = bytes.NewReader(req.Body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	return h.client.Do(httpReq)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
