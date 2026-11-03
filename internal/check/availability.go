package check

import (
	"context"
	"net/http"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/providers"
)

// Checker runs availability checks. It has no way to reach a provider
// except through the client it is given, so a direct connection can never
// establish proxy success.
type Checker struct {
	// Providers are tried in order; the first valid answer establishes up.
	Providers []providers.IPProvider
	Clock     domain.Clock
}

// NewChecker returns a Checker using the production providers (IPv4 ipify, then
// AWS checkip, then dual-stack ipify) and the wall clock.
func NewChecker() *Checker {
	return &Checker{
		Providers: []providers.IPProvider{providers.DefaultIPify(), providers.DefaultAWS(), providers.DefaultIPifyDual()},
		Clock:     domain.SystemClock{},
	}
}

type attempt struct {
	provider string
	class    Classification
}

// Check sends HTTPS requests to the providers through client (which must
// route through p) and derives the proxy state:
//
//   - a valid public exit IP from any provider: up;
//   - an explicit proxy failure (refused, authentication, proxy TLS, invalid
//     reply): down, without trying further providers;
//   - every provider timing out: down, because a route that answers on none of
//     them is treated as a proxy or upstream-tunnel failure (one slow provider
//     still falls back, and a mix of timeouts and provider errors stays
//     indeterminate);
//   - otherwise, when every provider fails: indeterminate, because the cause
//     cannot be told apart from a provider or route problem;
//   - cancellation: indeterminate with the canceled code, and no fallback.
func (c *Checker) Check(ctx context.Context, p domain.Proxy, client *http.Client) domain.Availability {
	start := time.Now()
	res := domain.Availability{ProxyID: p.ID}
	done := func() domain.Availability {
		res.CheckedAt = c.Clock.Now()
		return res
	}

	var failed []attempt
	for _, prov := range c.Providers {
		if ctx.Err() != nil {
			failed = append(failed, attempt{prov.Name(), Classify(ctx.Err())})
			break
		}
		attemptStart := time.Now()
		addr, err := prov.Lookup(ctx, client)
		if err == nil {
			res.State = domain.StateUp
			res.ExitIP = addr.String()
			res.Endpoint = prov.Name()
			res.Duration = time.Since(attemptStart)
			return done()
		}
		cl := Classify(err)
		failed = append(failed, attempt{prov.Name(), cl})
		if cl.Failure.Code == CodeCanceled || cl.Definite {
			break
		}
	}

	res.Duration = time.Since(start)
	chosen := attempt{class: result(CodeProviderUnavailable, false)}
	if len(failed) > 0 {
		chosen = failed[0]
		for _, a := range failed {
			if a.class.Failure.Code == CodeCanceled {
				chosen = a
				break
			}
			if a.class.Definite {
				chosen = a
			}
		}
	}
	res.Endpoint = chosen.provider
	res.Failure = &domain.Failure{Code: chosen.class.Failure.Code, Message: chosen.class.Failure.Message}
	switch {
	case chosen.class.Definite:
		res.State = domain.StateDown
	case allTimedOut(failed):
		// No provider answered and none failed explicitly: the tunnel through
		// the proxy stalled, which is a definite failure of the monitor's
		// route rather than a provider-specific problem.
		res.State = domain.StateDown
		res.Failure = &domain.Failure{Code: CodeTimeout, Message: messages[CodeTimeout]}
	default:
		res.State = domain.StateIndeterminate
	}
	return done()
}

// allTimedOut reports whether every provider attempt failed only because it
// timed out.
func allTimedOut(failed []attempt) bool {
	if len(failed) == 0 {
		return false
	}
	for _, a := range failed {
		if a.class.Definite || a.class.Failure.Code != CodeTimeout {
			return false
		}
	}
	return true
}
