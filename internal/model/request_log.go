package model

// RequestLog is a single proxied request captured by the gateway.
type RequestLog struct {
	ServiceID string
	RequestID string
	Method    string
	Path      string
	Status    int
	LatencyMS int
	Error     string
}
