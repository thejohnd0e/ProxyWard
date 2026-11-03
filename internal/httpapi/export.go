package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/apierr"
	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/store"
)

// exportSchemaVersion identifies the exported document layout.
const exportSchemaVersion = 1

// WithExportLimit overrides the number of records one export may carry. The
// store still refuses anything above store.ExportMaxRecords.
func WithExportLimit(n int) Option { return func(s *Server) { s.exportLimit = n } }

type exportRequest struct {
	From     string  `json:"from"`
	To       string  `json:"to"`
	ProxyIDs []int64 `json:"proxy_ids"`
}

// exportDocument is the downloaded file: fixed metadata around the selected
// records. It carries no credentials, settings, sessions or free-form text.
type exportDocument struct {
	SchemaVersion int    `json:"schema_version"`
	GeneratedAt   string `json:"generated_at"`
	From          string `json:"from"`
	To            string `json:"to"`
	store.ExportData
}

func (s *Server) createExport(w http.ResponseWriter, r *http.Request) {
	var req exportRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	fields := map[string]string{}
	from, errFrom := time.Parse(time.RFC3339, req.From)
	if errFrom != nil {
		fields["from"] = "Must be a date and time with a time zone, such as 2026-01-01T00:00:00Z."
	}
	to, errTo := time.Parse(time.RFC3339, req.To)
	if errTo != nil {
		fields["to"] = "Must be a date and time with a time zone, such as 2026-01-01T00:00:00Z."
	} else if errFrom == nil && !from.Before(to) {
		fields["to"] = "Must be later than the start of the period."
	}
	ids := make([]domain.ProxyID, 0, len(req.ProxyIDs))
	for _, id := range req.ProxyIDs {
		if id <= 0 {
			fields["proxy_ids"] = "Must be a list of proxy numbers."
			break
		}
		ids = append(ids, domain.ProxyID(id))
	}
	if len(ids) == 0 && fields["proxy_ids"] == "" {
		fields["proxy_ids"] = "Select at least one proxy."
	}
	if len(fields) > 0 {
		writeValidation(w, fields)
		return
	}

	data, err := s.db.Export(r.Context(), store.ExportFilter{From: from, To: to, ProxyIDs: ids, Limit: s.exportLimit})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrExportTooLarge):
			apierr.Write(w, http.StatusRequestEntityTooLarge, "export_too_large",
				fmt.Sprintf("The selected period and proxies hold more than %d records. Narrow the period or select fewer proxies.", s.exportLimit), nil)
		case errors.Is(err, store.ErrNotFound):
			apierr.Write(w, http.StatusNotFound, "not_found", "A selected proxy no longer exists. Refresh the list and try again.", nil)
		case errors.Is(err, store.ErrInvalid):
			writeValidation(w, nil)
		default:
			s.internalError(w, "export", err)
		}
		return
	}

	doc := exportDocument{
		SchemaVersion: exportSchemaVersion,
		GeneratedAt:   s.clock.Now().UTC().Format(time.RFC3339),
		From:          from.UTC().Format(time.RFC3339),
		To:            to.UTC().Format(time.RFC3339),
		ExportData:    data,
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		s.internalError(w, "encode export", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="proxyward-export.json"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}
