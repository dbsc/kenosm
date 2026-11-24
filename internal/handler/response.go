package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
)

// maxJSONBody caps request bodies on every JSON endpoint. Inline base64 images
// are the biggest legitimate payload; anything beyond this is rejected before
// it can exhaust memory.
const maxJSONBody = 32 << 20

// decodeJSON decodes a bounded request body into v. Callers map the returned
// error to a 400.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes an error response. Server errors (5xx) never leak internal
// details (DSNs, SQL errors) to the caller: the real cause goes to the log.
func writeError(w http.ResponseWriter, status int, msg string) {
	if status >= 500 {
		slog.Error("request failed", "status", status, "detail", msg)
		msg = "internal server error"
	}
	writeJSON(w, status, map[string]string{"error": msg})
}
