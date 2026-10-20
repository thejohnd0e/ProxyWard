// Package apierr writes the API's JSON error and response bodies.
package apierr

import (
	"encoding/json"
	"net/http"
)

type body struct {
	Error detail `json:"error"`
}

type detail struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields"`
}

// Write sends {"error":{"code","message","fields"}} with the given status.
// Messages must be fixed English text; never pass upstream errors or secrets.
func Write(w http.ResponseWriter, status int, code, message string, fields map[string]string) {
	if fields == nil {
		fields = map[string]string{}
	}
	JSON(w, status, body{Error: detail{Code: code, Message: message, Fields: fields}})
}

// JSON sends v as a JSON response with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
