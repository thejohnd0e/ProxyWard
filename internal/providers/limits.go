package providers

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxRetryAfter bounds absurd Retry-After values.
const maxRetryAfter = 24 * time.Hour

// ParseRetryAfter parses a Retry-After header, either delta-seconds or an
// HTTP date, relative to now. Missing, malformed, zero and past values give 0.
func ParseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(value, 10, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		if time.Duration(secs) > maxRetryAfter/time.Second {
			return maxRetryAfter
		}
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if d := at.Sub(now); d > 0 {
			if d > maxRetryAfter {
				return maxRetryAfter
			}
			return d
		}
	}
	return 0
}

// RetryAt returns the time at which a request may be retried according to a
// Retry-After header, or the zero time when the header is missing or invalid.
func RetryAt(header string, now time.Time) time.Time {
	d := ParseRetryAfter(header, now)
	if d <= 0 {
		return time.Time{}
	}
	return now.Add(d)
}

const (
	minBackoff = time.Minute
	maxBackoff = time.Hour
)

// Backoff returns the pause after the attempt-th consecutive failure
// (1-based): 1 minute, doubling each time, capped at 1 hour.
func Backoff(attempt int) time.Duration {
	d := minBackoff
	for i := 1; i < attempt && d < maxBackoff; i++ {
		d *= 2
	}
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}
