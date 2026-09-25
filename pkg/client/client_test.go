package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRelaySuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/relay" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "key-123" {
			t.Errorf("missing/incorrect API key header: %q", r.Header.Get("X-API-Key"))
		}
		var req RelayRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Target != "httpbin" {
			t.Errorf("target = %q, want httpbin", req.Target)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(RelayResponse{
			Status: "relayed", Target: "httpbin", ResponseCode: 200, Attempts: 1, Latency: "10ms",
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "key-123")
	resp, err := c.Relay(context.Background(), RelayRequest{Target: "httpbin", Method: "GET", Path: "/get"})
	if err != nil {
		t.Fatalf("Relay: %v", err)
	}
	if resp.Status != "relayed" || resp.ResponseCode != 200 {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestRelayAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid API key"})
	}))
	defer srv.Close()

	c := New(srv.URL, "wrong")
	_, err := c.Relay(context.Background(), RelayRequest{Target: "httpbin"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", apiErr.StatusCode)
	}
	if apiErr.Message != "invalid API key" {
		t.Errorf("message = %q, want 'invalid API key'", apiErr.Message)
	}
}

func TestWebhookReturnsJobID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted", "job_id": "job-abc"})
	}))
	defer srv.Close()

	c := New(srv.URL, "key-123")
	id, err := c.Webhook(context.Background(), WebhookRequest{Target: "httpbin", Method: "POST"})
	if err != nil {
		t.Fatalf("Webhook: %v", err)
	}
	if id != "job-abc" {
		t.Errorf("job id = %q, want job-abc", id)
	}
}

func TestJobStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/jobs/job-abc" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Job{ID: "job-abc", Status: "completed", ResponseCode: 200, Attempts: 1})
	}))
	defer srv.Close()

	c := New(srv.URL, "key-123")
	job, err := c.JobStatus(context.Background(), "job-abc")
	if err != nil {
		t.Fatalf("JobStatus: %v", err)
	}
	if job.Status != "completed" {
		t.Errorf("status = %q, want completed", job.Status)
	}
}

func TestJobStatusNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "job not found"})
	}))
	defer srv.Close()

	c := New(srv.URL, "key-123")
	_, err := c.JobStatus(context.Background(), "missing")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 APIError, got %v", err)
	}
}

func TestBaseURLTrailingSlashTrimmed(t *testing.T) {
	c := New("https://example.com/", "k")
	if c.baseURL != "https://example.com" {
		t.Errorf("baseURL = %q, want trimmed", c.baseURL)
	}
}
