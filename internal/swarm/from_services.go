package swarm

import (
	"kenosm/internal/model"
	"kenosm/internal/transport"
)

// AgentsFromServices converts gateway services into swarm agents. Each enabled
// service is one agent: its upstream_url is the model base URL, its stored
// (decrypted) API key is the credential, and its SOCKS5 proxy routes traffic.
func AgentsFromServices(services []model.Service) []Agent {
	out := make([]Agent, 0, len(services))
	for _, svc := range services {
		name := svc.Name
		if name == "" {
			name = svc.Slug
		}
		out = append(out, Agent{
			Name:        name,
			Slug:        svc.Slug,
			BaseURL:     svc.UpstreamURL,
			APIKey:      svc.UpstreamAPIKey,
			Provider:    svc.Provider,
			Proxy:       transport.ProxySpec{Host: svc.ProxyHost, Port: svc.ProxyPort, Username: svc.ProxyUsername, Password: svc.ProxyPassword},
			ManualModel: svc.Model,
		})
	}
	return out
}
