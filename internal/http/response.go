package http

import (
	"encoding/json"
	"net/http"
)

// ErrorResponse defines the standard RFC-friendly error envelope for the entire API.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail describes the error code and human-readable explanation.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteJSON sends a JSON response with status code and Content-Type header.
func WriteJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if data != nil {
		_ = json.NewEncoder(w).Encode(data)
	}
}

// WriteError writes a structured JSON error response conforming to the project specification.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	resp := ErrorResponse{
		Error: ErrorDetail{
			Code:    code,
			Message: message,
		},
	}
	WriteJSON(w, status, resp)
}
