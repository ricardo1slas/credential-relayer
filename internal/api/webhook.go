package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ricardo1slas/private_relayer/internal/auth"
	"github.com/ricardo1slas/private_relayer/internal/queue"
	"github.com/ricardo1slas/private_relayer/internal/relay"
)

var allowedWebhookMethods = map[string]struct{}{
	http.MethodGet:    {},
	http.MethodPost:   {},
	http.MethodPut:    {},
	http.MethodPatch:  {},
	http.MethodDelete: {},
}

type webhookRequest struct {
	Target  string            `json:"target"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

func webhookHandler(q *queue.Queue, maxBytes int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

		var req webhookRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			status := http.StatusBadRequest
			if _, ok := err.(*http.MaxBytesError); ok {
				status = http.StatusRequestEntityTooLarge
			}
			writeError(w, status, "invalid request body: "+err.Error())
			return
		}

		if req.Target == "" {
			writeError(w, http.StatusBadRequest, "target is required")
			return
		}

		if !auth.TargetAllowed(r.Context(), req.Target) {
			writeError(w, http.StatusForbidden, "key not permitted for this target")
			return
		}

		if req.Method == "" {
			req.Method = http.MethodPost
		}
		req.Method = strings.ToUpper(req.Method)
		if _, ok := allowedWebhookMethods[req.Method]; !ok {
			writeError(w, http.StatusBadRequest, "method not allowed: "+req.Method)
			return
		}

		// Scrub auth/hop-by-hop headers before persisting -- defense in depth:
		// the relay handler also filters at forward-time, but stripping at the
		// queue boundary means sensitive values never hit Redis at all.
		scrubbed := relay.StripSensitiveHeaders(req.Headers)

		job, err := q.Enqueue(r.Context(), req.Target, req.Method, req.Path, scrubbed, req.Body, 3)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to enqueue: "+err.Error())
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "accepted",
			"job_id": job.ID,
		})
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
