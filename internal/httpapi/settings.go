package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"proxy-checker/internal/apierr"
	"proxy-checker/internal/domain"
	"proxy-checker/internal/notify"
	"proxy-checker/internal/providers"
	"proxy-checker/internal/store"
)

// geoDailyLimit mirrors geo.DefaultDailyLimit for display.
const geoDailyLimit = 1000

// WithReconcile registers a function that applies schedule changes at once
// after the settings were updated.
func WithReconcile(f func(context.Context) error) Option { return func(s *Server) { s.reconcile = f } }

func (s *Server) registerSettingsRoutes(mux *http.ServeMux) {
	protected := func(h http.HandlerFunc) http.Handler { return s.auth.Require(h) }
	mux.Handle("GET /api/settings", protected(s.getSettings))
	mux.Handle("PUT /api/settings", protected(s.putSettings))
	mux.Handle("POST /api/settings/telegram/test", protected(s.telegramTest))
	mux.Handle("POST /api/settings/speed/resume", protected(s.speedResume))
	mux.Handle("GET /api/events", protected(s.listEvents))
}

type scheduleUpdate struct {
	Enabled         optional[bool] `json:"enabled"`
	IntervalMinutes optional[int]  `json:"interval_minutes"`
}

type settingsUpdate struct {
	Monitoring *struct {
		Availability                 *scheduleUpdate `json:"availability"`
		Speed                        *scheduleUpdate `json:"speed"`
		AvailabilityConcurrency      optional[int]   `json:"availability_concurrency"`
		AvailabilityTimeoutSeconds   optional[int]   `json:"availability_timeout_seconds"`
		SpeedSampleMiB               optional[int]   `json:"speed_sample_mib"`
		SpeedDirectionTimeoutSeconds optional[int]   `json:"speed_direction_timeout_seconds"`
		DownThreshold                optional[int]   `json:"down_threshold"`
		RetentionDays                optional[int]   `json:"retention_days"`
	} `json:"monitoring"`
	Geo *struct {
		APIKey optional[string] `json:"api_key"`
	} `json:"geo"`
	Telegram *struct {
		BotToken optional[string] `json:"bot_token"`
		ChatID   optional[string] `json:"chat_id"`
	} `json:"telegram"`
}

// clampInt keeps a client-supplied number small enough that unit conversion
// cannot overflow; out-of-range values still fail validation afterwards.
func clampInt(n int) int64 {
	const lim = 1 << 31
	switch {
	case n > lim:
		return lim
	case n < -lim:
		return -lim
	}
	return int64(n)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var in settingsUpdate
	if !decodeJSON(w, r, &in) {
		return
	}
	cur, err := s.db.GetSettings(r.Context())
	if err != nil {
		s.internalError(w, "read settings", err)
		return
	}
	fields := map[string]string{}
	next := cur

	if m := in.Monitoring; m != nil {
		applySchedule(&next.Availability, m.Availability, "availability", fields)
		applySchedule(&next.Speed, m.Speed, "speed", fields)
		setInt := func(field string, o optional[int], apply func(int64)) {
			switch {
			case !o.Present:
			case o.Null:
				fields[field] = "Must not be null."
			default:
				apply(clampInt(o.Value))
			}
		}
		setInt("availability_concurrency", m.AvailabilityConcurrency, func(v int64) { next.AvailabilityConcurrency = int(v) })
		setInt("availability_timeout", m.AvailabilityTimeoutSeconds, func(v int64) { next.AvailabilityTimeout = time.Duration(v) * time.Second })
		setInt("speed_sample_bytes", m.SpeedSampleMiB, func(v int64) { next.SpeedSampleBytes = v << 20 })
		setInt("speed_direction_timeout", m.SpeedDirectionTimeoutSeconds, func(v int64) { next.SpeedDirectionTimeout = time.Duration(v) * time.Second })
		setInt("down_threshold", m.DownThreshold, func(v int64) { next.DownThreshold = int(v) })
		setInt("retention_days", m.RetentionDays, func(v int64) { next.RetentionDays = int(v) })
	}

	var tg store.TelegramUpdate
	if t := in.Telegram; t != nil {
		if o := t.BotToken; o.Present {
			switch {
			case o.Null:
				fields["bot_token"] = "Must not be null."
			case o.Value != "" && !notify.ValidToken(strings.TrimSpace(o.Value)):
				fields["bot_token"] = "Not a valid bot token."
			default:
				v := strings.TrimSpace(o.Value)
				tg.Token = &v
			}
		}
		if o := t.ChatID; o.Present {
			switch {
			case o.Null:
				fields["chat_id"] = "Must not be null."
			case o.Value != "" && !notify.ValidChatID(strings.TrimSpace(o.Value)):
				fields["chat_id"] = "Must be a numeric chat ID or an @channel name."
			default:
				v := strings.TrimSpace(o.Value)
				tg.ChatID = &v
			}
		}
	}
	var geoKey *string
	if g := in.Geo; g != nil && g.APIKey.Present {
		switch {
		case g.APIKey.Null:
			fields["api_key"] = "Must not be null."
		case g.APIKey.Value != "" && !validGeoKey(strings.TrimSpace(g.APIKey.Value)):
			fields["api_key"] = "Must be 8 to 200 visible characters without spaces."
		default:
			v := strings.TrimSpace(g.APIKey.Value)
			geoKey = &v
		}
	}

	if len(fields) == 0 && in.Monitoring != nil {
		var se *domain.SettingsError
		if err := next.Validate(); errors.As(err, &se) {
			fields[se.Field] = capitalize(se.Message) + "."
		}
	}
	if len(fields) > 0 {
		writeValidation(w, fields)
		return
	}

	ctx := r.Context()
	now := s.clock.Now()
	if in.Monitoring != nil {
		if err := s.db.PutSettings(ctx, next, now); err != nil {
			s.internalError(w, "store settings", err)
			return
		}
	}
	if tg.Token != nil || tg.ChatID != nil {
		if err := s.db.SetTelegram(ctx, tg, now); err != nil {
			s.secretStoreError(w, "store telegram settings", err)
			return
		}
	}
	if geoKey != nil {
		if err := s.db.SetGeoKey(ctx, *geoKey, now); err != nil {
			s.secretStoreError(w, "store geolocation key", err)
			return
		}
	}
	if in.Monitoring != nil && s.reconcile != nil {
		if err := s.reconcile(ctx); err != nil {
			s.logger.Error("applying schedule changes failed", "error", err)
		}
	}
	s.getSettings(w, r)
}

func (s *Server) secretStoreError(w http.ResponseWriter, op string, err error) {
	if errors.Is(err, store.ErrSecretsUnavailable) {
		apierr.Write(w, http.StatusServiceUnavailable, "secrets_unavailable", "Secret storage is not available.", nil)
		return
	}
	s.internalError(w, op, err)
}

func applySchedule(dst *domain.Schedule, in *scheduleUpdate, name string, fields map[string]string) {
	if in == nil {
		return
	}
	switch {
	case in.Enabled.Present && in.Enabled.Null:
		fields[name+"_enabled"] = "Must not be null."
	case in.Enabled.Present:
		dst.Enabled = in.Enabled.Value
	}
	switch {
	case in.IntervalMinutes.Present && in.IntervalMinutes.Null:
		fields[name+"_interval"] = "Must not be null."
	case in.IntervalMinutes.Present:
		dst.Interval = time.Duration(clampInt(in.IntervalMinutes.Value)) * time.Minute
	}
}

func validGeoKey(k string) bool {
	if len(k) < 8 || len(k) > 200 {
		return false
	}
	for _, r := range k {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur, err := s.db.GetSettings(ctx)
	if err != nil {
		s.internalError(w, "read settings", err)
		return
	}
	tgCfg, err := s.db.GetTelegram(ctx)
	if err != nil {
		s.internalError(w, "read telegram settings", err)
		return
	}
	_, hasGeo, err := s.db.GeoKey(ctx)
	if err != nil {
		s.internalError(w, "read geolocation key", err)
		return
	}
	geoState, err := s.db.GeoState(ctx)
	if err != nil {
		s.internalError(w, "read geolocation state", err)
		return
	}
	out, err := s.db.OutboxSummary(ctx)
	if err != nil {
		s.internalError(w, "read delivery state", err)
		return
	}
	pause, err := s.db.GetProviderPause(ctx, providers.NewCloudflare().Name())
	if err != nil {
		s.internalError(w, "read provider state", err)
		return
	}
	now := s.clock.Now()

	used := 0
	if geoState.BudgetDay == now.UTC().Format("2006-01-02") {
		used = geoState.Used
	}
	geoPaused := geoState.PauseReason != "" && (geoState.PauseUntil.IsZero() || geoState.PauseUntil.After(now))

	apierr.JSON(w, http.StatusOK, map[string]any{
		"monitoring": map[string]any{
			"availability":                    map[string]any{"enabled": cur.Availability.Enabled, "interval_minutes": int(cur.Availability.Interval / time.Minute)},
			"speed":                           map[string]any{"enabled": cur.Speed.Enabled, "interval_minutes": int(cur.Speed.Interval / time.Minute)},
			"availability_concurrency":        cur.AvailabilityConcurrency,
			"availability_timeout_seconds":    int(cur.AvailabilityTimeout / time.Second),
			"speed_sample_mib":                cur.SpeedSampleBytes >> 20,
			"speed_direction_timeout_seconds": int(cur.SpeedDirectionTimeout / time.Second),
			"down_threshold":                  cur.DownThreshold,
			"retention_days":                  cur.RetentionDays,
		},
		"geo": map[string]any{
			"has_key": hasGeo, "used_today": used, "daily_limit": geoDailyLimit,
			"paused": geoPaused, "pause_reason": pauseReason(geoPaused, geoState.PauseReason), "retry_at": nullTime(geoPaused, geoState.PauseUntil),
		},
		"telegram": map[string]any{
			"has_token": tgCfg.Token != "", "chat_id": tgCfg.ChatID, "configured": tgCfg.Configured(),
			"delivery": map[string]any{
				"pending": out.Pending, "failed": out.Failed, "last_delivered_at": tsPtr(out.LastDeliveredAt), "last_error": out.LastError,
			},
		},
		"speed_provider": map[string]any{
			"name": providers.NewCloudflare().Name(), "paused": pause.Active(now),
			"reason": pauseReason(pause.Active(now), pause.Reason), "until": nullTime(pause.Active(now), pause.Until),
		},
	})
}

func pauseReason(active bool, reason string) string {
	if !active {
		return ""
	}
	return reason
}

func nullTime(active bool, t time.Time) *string {
	if !active || t.IsZero() {
		return nil
	}
	v := ts(t)
	return &v
}

func (s *Server) telegramTest(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.db.GetTelegram(r.Context())
	if err != nil {
		s.internalError(w, "read telegram settings", err)
		return
	}
	if !cfg.Configured() {
		apierr.Write(w, http.StatusUnprocessableEntity, "telegram_not_configured",
			"Set the bot token and chat ID before sending a test message.", nil)
		return
	}
	id, err := s.db.EnqueueTestMessage(r.Context(), s.clock.Now())
	if err != nil {
		s.internalError(w, "queue test message", err)
		return
	}
	// Queued only: delivery happens in the background and is reported in the delivery status.
	apierr.JSON(w, http.StatusAccepted, map[string]any{"message_id": id, "status": "queued"})
}

func (s *Server) speedResume(w http.ResponseWriter, r *http.Request) {
	if err := s.db.ClearProviderPause(r.Context(), providers.NewCloudflare().Name()); err != nil {
		s.internalError(w, "resume speed provider", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
