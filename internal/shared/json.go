package shared

import (
	"encoding/json"
	"net/http"
)

// WriteJSON writes v as a JSON response body with status 200.
func WriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// WriteJSONStatus writes v as a JSON response body with the given status.
func WriteJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// WriteJSONNoEscape writes v as a JSON response body with status 200 and
// HTML escaping off, for payloads that carry raw text.
func WriteJSONNoEscape(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

// WriteJSONError writes a JSON error object with the given status.
func WriteJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
