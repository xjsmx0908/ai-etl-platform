package main

import (
	"encoding/json"
	"net/http"
)

// writeJSON writes a JSON response with the given status code. Encode errors are
// intentionally ignored: the headers are already committed by then.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error envelope: {"error": "<message>"}.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
