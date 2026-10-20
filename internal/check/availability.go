package check

import (
	"context"
	"net/http"
	"time"

	"proxy-checker/internal/domain"
	"proxy-checker/internal/providers"
)

// Checker runs availability checks. It has no way to reach a provider
// except through the client it is given, so a direct connection can never
// establish proxy success.
type Checker struct {
	// Providers are tried in order; the first valid answer establishes up.
	Providers []providers.IPProvider
	Clock     domain.Clock
}

// NewChecker returns a Checker using the production providers (ipify, then
// AWS checkip) and the wall clock.
func NewChecker() *Checker {
	return &Checker{
		Providers: []providers.IPProvider{providers.DefaultIPify(), providers.DefaultAWS()},
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
	if chosen.class.Definite {
		res.State = domain.StateDown
	} else {
		res.State = domain.StateIndeterminate
	}
	return done()
}
