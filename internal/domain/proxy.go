package domain

import "time"

// ProxyID identifies a stored proxy.
type ProxyID int64

// Protocol is the proxy connection protocol.
type Protocol string

const (
	ProtocolHTTP   Protocol = "http"
	ProtocolHTTPS  Protocol = "https"
	ProtocolSOCKS5 Protocol = "socks5"
)

// Valid reports whether p is a supported protocol.
func (p Protocol) Valid() bool {
	switch p {
	case ProtocolHTTP, ProtocolHTTPS, ProtocolSOCKS5:
		return true
	}
	return false
}

// Credentials are proxy authentication values. They exist at runtime only and
// must never be serialized into API responses or logs.
type Credentials struct {
	Username, Password string
}

// Proxy is a monitored proxy endpoint.
type Proxy struct {
	ID       ProxyID
	Name     string
	Protocol Protocol
	Host     string
	Port     uint16
	Auth     *Credentials // nil disables authentication
	Tags     []string

	// Per-proxy schedule overrides; nil inherits the global schedule. An
	// override with Enabled=false disables the schedule even if globally enabled.
	AvailabilitySchedule *Schedule
	SpeedSchedule        *Schedule
}

// MaxProxies is the maximum number of stored proxies.
const MaxProxies = 100

// Schedule interval bounds.
const (
	MinInterval = time.Minute
	MaxInterval = 10080 * time.Minute
)
