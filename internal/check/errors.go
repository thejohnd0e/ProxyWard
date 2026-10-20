// Package check runs availability and speed checks and classifies their outcomes.
package check

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"

	"proxy-checker/internal/domain"
	"proxy-checker/internal/providers"
	"proxy-checker/internal/proxy"
)

// Failure codes stored with results.
const (
	CodeConnectionRefused   = "connection_refused"
	CodeTimeout             = "timeout"
	CodeAuthRequired        = "auth_required"
	CodeAuthFailed          = "auth_failed"
	CodeProxyTLS            = "proxy_tls"
	CodeTargetTLS           = "target_tls"
	CodeProviderRateLimited = "provider_rate_limited"
	CodeProviderUnavailable = "provider_unavailable"
	CodeProviderForbidden   = "provider_forbidden"
	CodeInvalidResponse     = "invalid_response"
	CodeInvalidConfig       = "invalid_config"
	CodeCanceled            = "canceled"
	CodeInterrupted         = "interrupted"
)

var messages = map[string]string{
	CodeConnectionRefused:   "Could not connect to the proxy.",
	CodeTimeout:             "The request timed out.",
	CodeAuthRequired:        "The proxy requires authentication.",
	CodeAuthFailed:          "The proxy rejected the credentials.",
	CodeProxyTLS:            "The proxy's TLS certificate could not be verified.",
	CodeTargetTLS:           "The test service's TLS certificate could not be verified.",
	CodeProviderRateLimited: "The test service is rate limiting requests.",
	CodeProviderUnavailable: "The test service or the route to it is unavailable.",
	CodeProviderForbidden:   "The test service rejected the request.",
	CodeInvalidResponse:     "The response was not valid.",
	CodeInvalidConfig:       "The speed test settings are not valid.",
	CodeCanceled:            "The check was canceled.",
	CodeInterrupted:         "The check was interrupted by a restart.",
}

// NewFailure builds a Failure with the fixed English message for code.
func NewFailure(code string) *domain.Failure {
	return &domain.Failure{Code: code, Message: messages[code]}
}

// Classification is the outcome of classifying one request error.
type Classification struct {
	Failure domain.Failure
	// Definite is true when the failure is attributable to the proxy itself
	// (connecting, TLS to the proxy, authentication, garbage replies). Other
	// failures cannot be told apart from provider or route problems.
	Definite bool
}

func result(code string, definite bool) Classification {
	return Classification{Failure: *NewFailure(code), Definite: definite}
}

// Classify maps an error from a request sent through a proxy to a failure
// code. The returned message is fixed text: it never contains upstream data,
// addresses or credentials.
func Classify(err error) Classification {
	if errors.Is(err, context.Canceled) {
		return result(CodeCanceled, false)
	}
	timeout := isTimeout(err)

	var se *proxy.StageError
	if errors.As(err, &se) {
		switch se.Stage {
		case proxy.StageConnect:
			if timeout {
				return result(CodeTimeout, true)
			}
			return result(CodeConnectionRefused, true)
		case proxy.StageProxyTLS:
			if timeout {
				return result(CodeTimeout, true)
			}
			return result(CodeProxyTLS, true)
		default: // proxy.StageTunnel
			switch {
			case errors.Is(err, proxy.ErrAuthRequired):
				return result(CodeAuthRequired, true)
			case errors.Is(err, proxy.ErrAuthFailed):
				return result(CodeAuthFailed, true)
			case errors.Is(err, proxy.ErrProxyProtocol):
				return result(CodeInvalidResponse, true)
			case timeout:
				// A proxy waiting on an unreachable target looks the same.
				return result(CodeTimeout, false)
			}
			return result(CodeProviderUnavailable, false)
		}
	}

	var pe *providers.Error
	if errors.As(err, &pe) {
		switch pe.Kind {
		case providers.KindRateLimited:
			return result(CodeProviderRateLimited, false)
		case providers.KindUnavailable:
			return result(CodeProviderUnavailable, false)
		case providers.KindForbidden:
			return result(CodeProviderForbidden, false)
		}
		return result(CodeInvalidResponse, false)
	}

	if isTLSError(err) {
		return result(CodeTargetTLS, false)
	}
	if timeout {
		return result(CodeTimeout, false)
	}
	return result(CodeProviderUnavailable, false)
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func isTLSError(err error) bool {
	var (
		unknown   x509.UnknownAuthorityError
		hostname  x509.HostnameError
		invalid   x509.CertificateInvalidError
		verify    *tls.CertificateVerificationError
		record    tls.RecordHeaderError
		alertErr  tls.AlertError
		sysRoots  x509.SystemRootsError
		unsafeAlg x509.InsecureAlgorithmError
	)
	return errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid) ||
		errors.As(err, &verify) || errors.As(err, &record) || errors.As(err, &alertErr) ||
		errors.As(err, &sysRoots) || errors.As(err, &unsafeAlg)
}
