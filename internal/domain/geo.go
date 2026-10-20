package domain

import "time"

// Geo is geolocation data for an observed exit IP.
type Geo struct {
	IP, CountryCode, Country, City, Organization string
	ASN                                          *uint32
	FetchedAt                                    time.Time
}
