package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ricardo1slas/private_relayer/internal/queue"
	"github.com/ricardo1slas/private_relayer/internal/relay"
)

type webhookRequest struct {
	Target  string            `json:"target"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

func webhookHandler(q *queue.Queue, rh *relay.Handler, maxBytes int64) http.HandlerFunc {
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

		// Same gate as /relay, applied before enqueue: an unknown target, bad path or policy
		// violation gets a 4xx now instead of a job that can only dead-letter.
		req.Method = relay.NormalizeMethod(req.Method)
		if _, err := rh.Authorize(r.Context(), req.Target, req.Method, req.Path, len(req.Body)); err != nil {
			var rerr *relay.RequestError
			if !errors.As(err, &rerr) {
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			writeError(w, rerr.Status, rerr.Msg)
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
