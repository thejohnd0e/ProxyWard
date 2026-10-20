package providers

import (
	"crypto/tls"

	"proxy-checker/internal/testkit"
)

func testTLS(ca *testkit.CA) *tls.Config {
	return &tls.Config{RootCAs: ca.Pool, MinVersion: tls.VersionTLS12}
}
