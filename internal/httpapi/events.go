package httpapi

import (
	"encoding/base64"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"proxy-checker/internal/apierr"
	"proxy-checker/internal/domain"
	"proxy-checker/internal/store"
)

const (
	defaultEventLimit = 50
	maxEventLimit     = 200
)

var kindPattern = regexp.MustCompile(`^[a-z_]{1,32}$`)

// encodeCursor makes the (created_at, id) position opaque to clients.
func encodeCursor(c *store.EventCursor) any {
	if c == nil {
		return nil
	}
	raw := strconv.FormatInt(c.CreatedAt.UnixMilli(), 10) + ":" + strconv.FormatInt(c.ID, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(s string) (*store.EventCursor, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, false
	}
	ms, id, ok := strings.Cut(string(raw), ":")
	if !ok {
		return nil, false
	}
	msN, err1 := strconv.ParseInt(ms, 10, 64)
	idN, err2 := strconv.ParseInt(id, 10, 64)
	if err1 != nil || err2 != nil || idN <= 0 || msN < 0 {
		return nil, false
	}
	return &store.EventCursor{CreatedAt: time.UnixMilli(msN).UTC(), ID: idN}, true
}

type deliveryDTO struct {
	Status      string  `json:"status"`
	Attempts    int     `json:"attempts"`
	LastError   string  `json:"last_error"`
	DeliveredAt *string `json:"delivered_at"`
}

type eventDTO struct {
	ID        int64       `json:"id"`
	ProxyID   *int64      `json:"proxy_id"`
	ProxyName *string     `json:"proxy_name"`
	Kind      string      `json:"kind"`
	Message   string      `json:"message"`
	CreatedAt string      `json:"created_at"`
	Delivery  deliveryDTO `json:"delivery"`
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fields := map[string]string{}
	f := store.EventFilter{Limit: defaultEventLimit}

	if v, ok := q["limit"]; ok {
		n, err := strconv.Atoi(v[0])
		if err != nil || n < 1 || n > maxEventLimit {
			fields["limit"] = "Must be a number from 1 to 200."
		} else {
			f.Limit = n
		}
	}
	if v, ok := q["proxy_id"]; ok {
		n, err := strconv.ParseInt(v[0], 10, 64)
		if err != nil || n <= 0 {
			fields["proxy_id"] = "Must be a positive number."
		} else {
			id := domain.ProxyID(n)
			f.ProxyID = &id
		}
	}
	for _, k := range q["kind"] {
		if !kindPattern.MatchString(k) {
			fields["kind"] = "Must be an event kind such as proxy_down."
			break
		}
		f.Kinds = append(f.Kinds, k)
	}
	if v, ok := q["cursor"]; ok {
		c, valid := decodeCursor(v[0])
		if !valid {
			fields["cursor"] = "Not a valid cursor."
		}
		f.Before = c
	}
	if len(fields) > 0 {
		writeValidation(w, fields)
		return
	}

	rows, next, err := s.db.ListEvents(r.Context(), f)
	if err != nil {
		s.internalError(w, "list events", err)
		return
	}
	out := make([]eventDTO, len(rows))
	for i, e := range rows {
		d := eventDTO{ID: e.ID, ProxyID: e.ProxyID, Kind: e.Kind, Message: e.Message, CreatedAt: ts(e.CreatedAt),
			Delivery: deliveryDTO{Status: e.Delivery.Status, Attempts: e.Delivery.Attempts, LastError: e.Delivery.LastError, DeliveredAt: tsPtr(e.Delivery.DeliveredAt)}}
		if e.ProxyName != "" {
			name := e.ProxyName
			d.ProxyName = &name
		}
		out[i] = d
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"events": out, "next_cursor": encodeCursor(next)})
}
