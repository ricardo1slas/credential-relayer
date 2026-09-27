package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ricardo1slas/credential-relayer/internal/auth"
	"github.com/ricardo1slas/credential-relayer/internal/queue"
)

func jobStatusHandler(q *queue.Queue) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jobID := chi.URLParam(r, "id")
		if jobID == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "job ID is required"})
			return
		}

		job, err := q.GetJob(r.Context(), jobID)
		// 404 (not 403) for someone else's job so job IDs can't be probed.
		if err == nil && (job.Owner == "" || job.Owner != auth.KeyName(r.Context())) {
			err = fmt.Errorf("job not found")
		}
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job)
	}
}
