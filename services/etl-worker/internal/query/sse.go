package query

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"ai-etl-pipeline/internal/config"
)

func queryBodyLimit(cfg config.Config) int64 {
	if cfg.QueryMaxBodyBytes > 0 {
		return cfg.QueryMaxBodyBytes
	}
	return defaultQueryBodyBytes
}

func isMaxBytesError(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}

func decodeQueryRequest(r *http.Request, cfg config.Config) (Request, error) {
	r.Body = http.MaxBytesReader(nil, r.Body, queryBodyLimit(cfg))
	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return Request{}, err
	}
	return req, nil
}

func writeSSEError(w http.ResponseWriter, message string) {
	payload, _ := json.Marshal(map[string]string{"error": message})
	fmt.Fprintf(w, "event: error\ndata: %s\n\n", payload)
	if err := http.NewResponseController(w).Flush(); err != nil {
		slog.Debug("sse flush failed", "error", err)
	}
}
