package providers

import (
	"crypto/tls"

	"github.com/thejohnd0e/ProxyWard/internal/testkit"
)

func testTLS(ca *testkit.CA) *tls.Config {
	return &tls.Config{RootCAs: ca.Pool, MinVersion: tls.VersionTLS12}
}
