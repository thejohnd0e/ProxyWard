package domain

import "time"

// CheckKind is the type of check a job performs.
type CheckKind string

const (
	CheckAvailability CheckKind = "availability"
	CheckSpeed        CheckKind = "speed"
)

// State is the availability state of a proxy.
type State string

const (
	StateUnknown       State = "unknown"
	StateUp            State = "up"
	StateDown          State = "down"
	StateIndeterminate State = "indeterminate"
)

// Failure is a sanitized failure with an English message.
type Failure struct {
	Code, Message string
}

// Availability is one availability check result.
type Availability struct {
	ProxyID          ProxyID
	CheckedAt        time.Time
	State            State
	ExitIP, Endpoint string
	Duration         time.Duration
	Failure          *Failure
}

// Transfer is one directional speed sample.
type Transfer struct {
	Bytes         int64
	Duration      time.Duration
	Mbps          *float64 // nil means no completed sample
	LowConfidence bool
	Failure       *Failure
}

// Speed is one speed check result.
type Speed struct {
	ProxyID          ProxyID
	CheckedAt        time.Time
	Provider, Method string
	Download, Upload Transfer
	TotalBytes       int64 // includes warmup and failed transfers
}
