package model

import "time"

// Service is a configured upstream AI API together with the SOCKS5 proxy that
// all traffic to it must be routed through.
//
// UpstreamAPIKey and ProxyPassword are plaintext in memory but are always
// encrypted at rest (see internal/crypto) and never serialized to JSON.
type Service struct {
	ID              string    `json:"id"`
	Slug            string    `json:"slug"`
	Name            string    `json:"name"`
	UpstreamURL     string    `json:"upstream_url"`
	UpstreamAPIKey  string    `json:"-"`
	AuthHeader      string    `json:"auth_header"`
	AuthScheme      string    `json:"auth_scheme"`
	ProxyHost       string    `json:"proxy_host"`
	ProxyPort       int       `json:"proxy_port"`
	ProxyUsername   string    `json:"proxy_username"`
	ProxyPassword   string    `json:"-"`
	Model           string    `json:"model"`
	Enabled         bool      `json:"enabled"`
	HasAPIKey       bool      `json:"has_api_key"`
	HasProxyPassword bool     `json:"has_proxy_password"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
