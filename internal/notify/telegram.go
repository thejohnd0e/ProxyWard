package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"proxy-checker/internal/providers"
)

// TelegramURL is the Bot API base URL.
const TelegramURL = "https://api.telegram.org"

// MaxMessageRunes is Telegram's limit for the text of one message.
const MaxMessageRunes = 4096

const (
	telegramTimeout = 15 * time.Second
	maxReplyBytes   = 64 << 10
)

var (
	// The token becomes part of the request path, so only the characters Telegram
	// uses are accepted: this also rules out path or query injection.
	tokenPattern = regexp.MustCompile(`^[0-9]{3,20}:[A-Za-z0-9_-]{20,100}$`)
	chatPattern  = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z0-9_]{5,32})$`)
)

// ValidToken reports whether s looks like a bot token.
func ValidToken(s string) bool { return tokenPattern.MatchString(s) }

// ValidChatID reports whether s is a numeric chat ID or an @channel name.
func ValidChatID(s string) bool { return chatPattern.MatchString(s) }

// SendError is a delivery failure. Code is a short stable identifier. The
// error text never includes the request URL (which contains the bot token) or
// any text received from the network.
type SendError struct {
	Code       string
	RetryAfter time.Duration // from Telegram, when it asked us to wait
}

func (e *SendError) Error() string { return "telegram: " + e.Code }

func fail(code string) error { return &SendError{Code: code} }

// TokenSource returns the bot token, or ok=false when none is configured.
type TokenSource func(ctx context.Context) (token string, ok bool, err error)

// Telegram sends messages through the Bot API. It connects directly and is
// independent of the proxies being checked.
type Telegram struct {
	BaseURL string
	Client  *http.Client
	Timeout time.Duration
	Token   TokenSource
}

// NewTelegram returns a client with production defaults.
func NewTelegram(token TokenSource) *Telegram {
	return &Telegram{
		BaseURL: TelegramURL, Timeout: telegramTimeout, Token: token,
		Client: &http.Client{
			Transport: &http.Transport{
				Proxy:                  nil,
				TLSHandshakeTimeout:    telegramTimeout,
				ResponseHeaderTimeout:  telegramTimeout,
				MaxResponseHeaderBytes: 16 << 10,
				DisableKeepAlives:      true,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Send posts text to chatID as plain text (no markup is interpreted, so
// hostile names cannot inject formatting). Delivery is at-least-once: when a
// reply is lost the caller cannot tell whether Telegram accepted the message.
func (t *Telegram) Send(ctx context.Context, chatID, text string) error {
	if !ValidChatID(chatID) {
		return fail("invalid_chat")
	}
	n := utf8.RuneCountInString(text)
	if n == 0 {
		return fail("bad_request")
	}
	if n > MaxMessageRunes {
		return fail("too_long")
	}
	if t.Token == nil {
		return fail("not_configured")
	}
	token, ok, err := t.Token(ctx)
	if err != nil {
		return err
	}
	if !ok || token == "" {
		return fail("not_configured")
	}
	if !ValidToken(token) {
		return fail("invalid_token")
	}

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = telegramTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	payload, err := json.Marshal(map[string]any{"chat_id": chatID, "text": text, "disable_web_page_preview": true})
	if err != nil {
		return fail("bad_request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.BaseURL, "/")+"/bot"+token+"/sendMessage", bytes.NewReader(payload))
	if err != nil {
		return fail("network")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "proxy-checker")
	client := t.Client
	if client == nil {
		client = NewTelegram(nil).Client
	}
	resp, err := client.Do(req)
	if err != nil {
		// The error value contains the URL, and with it the token: classify, never forward.
		return netFailure(ctx, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
	if err != nil {
		return netFailure(ctx, err)
	}
	return interpret(resp, body)
}

func netFailure(ctx context.Context, err error) error {
	var ne net.Error
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return fail("timeout")
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		return fail("canceled")
	}
	return fail("network")
}

type apiReply struct {
	OK         *bool `json:"ok"`
	ErrorCode  int   `json:"error_code"`
	Parameters struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func interpret(resp *http.Response, body []byte) error {
	var r apiReply
	parsed := json.Unmarshal(body, &r) == nil && r.OK != nil

	if resp.StatusCode == http.StatusOK {
		switch {
		case !parsed:
			return fail("invalid_response")
		case *r.OK:
			return nil
		}
	}
	status := resp.StatusCode
	if status == http.StatusOK && r.ErrorCode != 0 {
		status = r.ErrorCode // the real status is inside an HTTP 200 reply
	}
	retry := time.Duration(r.Parameters.RetryAfter) * time.Second
	if retry == 0 {
		retry = providers.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	}
	switch {
	case status == http.StatusTooManyRequests:
		return &SendError{Code: "rate_limited", RetryAfter: retry}
	case status == http.StatusUnauthorized:
		return fail("unauthorized")
	case status == http.StatusForbidden:
		return fail("forbidden")
	case status == http.StatusBadRequest:
		return fail("bad_request")
	case status == http.StatusNotFound:
		return fail("not_found")
	case status >= 500:
		return fail("server_error")
	}
	return fail("rejected")
}

// itoa is used by message formatting helpers.
func itoa(n int) string { return strconv.Itoa(n) }
