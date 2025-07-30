// Copyright 2025 The Prox Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package prox

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

// Backend represents a backend service that can handle HTTP requests.
// Implementations can provide simple proxying or complex load balancing.
type Backend interface {
	// ServeHTTP handles the HTTP request, implementing http.Handler.
	ServeHTTP(w http.ResponseWriter, r *http.Request)
	// HealthStatus returns the current health status of all servers in the backend.
	// The map key is the server URL.
	HealthStatus() map[string]HealthStatus
}

// LoadBalancer defines the interface for server selection strategies.
// Implementations can provide various algorithms like round-robin, least connections, etc.
type LoadBalancer interface {
	// Select chooses a server from the available pool based on the implementation's algorithm.
	// It returns an error if no healthy servers are available.
	Select(servers []*Server, req *http.Request) (*Server, error)
}

// Server represents a single backend server instance.
type Server struct {
	// URL is the server's endpoint URL.
	URL *url.URL
	// Healthy indicates whether the server is currently healthy.
	Healthy bool
	// mu protects concurrent access to the Healthy field.
	mu sync.RWMutex
}

// HealthStatus contains health check information for a server.
type HealthStatus struct {
	// Healthy indicates whether the server passed its last health check.
	Healthy bool
	// LastCheck is the timestamp of the most recent health check.
	LastCheck time.Time
	// Error contains any error from the last health check.
	Error error
}

// BackendBuilder provides a fluent interface for configuring backends.
type BackendBuilder interface {
	// AddServers adds one or more server URLs to the backend pool.
	AddServers(urls ...string) BackendBuilder
	// WithLoadBalancer sets the load balancing strategy for this backend.
	// If not set, defaults to round-robin for multiple servers.
	WithLoadBalancer(lb LoadBalancer) BackendBuilder
	// WithHealthCheck configures health checking for the backend servers.
	// The path is appended to each server URL for health checks.
	WithHealthCheck(path string, interval time.Duration) BackendBuilder
	// Build creates the Backend with the configured settings.
	Build() Backend
}

// simpleBackend is a basic backend without load balancing
type simpleBackend struct {
	name  string
	url   *url.URL
	proxy *httputil.ReverseProxy
}

func (b *simpleBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.proxy.ServeHTTP(w, r)
}

func (b *simpleBackend) HealthStatus() map[string]HealthStatus {
	return map[string]HealthStatus{
		b.url.String(): {
			Healthy:   true,
			LastCheck: time.Now(),
		},
	}
}

// clusterBackend is a backend with multiple servers and load balancing
type clusterBackend struct {
	name         string
	servers      []*Server
	loadBalancer LoadBalancer
	healthCheck  *healthChecker
}

func (b *clusterBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	server, err := b.loadBalancer.Select(b.servers, r)
	if err != nil {
		http.Error(w, "No available backend", http.StatusServiceUnavailable)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(server.URL)
	proxy.ServeHTTP(w, r)
}

func (b *clusterBackend) HealthStatus() map[string]HealthStatus {
	status := make(map[string]HealthStatus)
	for _, server := range b.servers {
		server.mu.RLock()
		healthy := server.Healthy
		server.mu.RUnlock()

		status[server.URL.String()] = HealthStatus{
			Healthy:   healthy,
			LastCheck: time.Now(),
		}
	}
	return status
}

// healthChecker performs health checks on servers
type healthChecker struct {
	path     string
	interval time.Duration
	// TODO: implement health checking logic
}

// defaultBackendBuilder builds backend configurations
type defaultBackendBuilder struct {
	proxy          *defaultProxy
	name           string
	servers        []string
	loadBalancer   LoadBalancer
	healthPath     string
	healthInterval time.Duration
}

func (bb *defaultBackendBuilder) AddServers(urls ...string) BackendBuilder {
	bb.servers = append(bb.servers, urls...)
	return bb
}

func (bb *defaultBackendBuilder) WithLoadBalancer(lb LoadBalancer) BackendBuilder {
	bb.loadBalancer = lb
	return bb
}

func (bb *defaultBackendBuilder) WithHealthCheck(path string, interval time.Duration) BackendBuilder {
	bb.healthPath = path
	bb.healthInterval = interval
	return bb
}

func (bb *defaultBackendBuilder) Build() Backend {
	if len(bb.servers) == 0 {
		panic("no servers defined for backend")
	}

	// Single server - use simple backend
	if len(bb.servers) == 1 {
		u, err := url.Parse(bb.servers[0])
		if err != nil {
			panic(fmt.Sprintf("invalid server URL: %v", err))
		}

		backend := &simpleBackend{
			name:  bb.name,
			url:   u,
			proxy: httputil.NewSingleHostReverseProxy(u),
		}

		bb.proxy.mu.Lock()
		bb.proxy.backends[bb.name] = backend
		bb.proxy.mu.Unlock()

		return backend
	}

	// Multiple servers - use cluster backend
	servers := make([]*Server, 0, len(bb.servers))
	for _, serverURL := range bb.servers {
		u, err := url.Parse(serverURL)
		if err != nil {
			panic(fmt.Sprintf("invalid server URL: %v", err))
		}
		servers = append(servers, &Server{
			URL:     u,
			Healthy: true, // Initially assume healthy
		})
	}

	backend := &clusterBackend{
		name:         bb.name,
		servers:      servers,
		loadBalancer: bb.loadBalancer,
	}

	if bb.healthPath != "" {
		backend.healthCheck = &healthChecker{
			path:     bb.healthPath,
			interval: bb.healthInterval,
		}
	}

	bb.proxy.mu.Lock()
	bb.proxy.backends[bb.name] = backend
	bb.proxy.mu.Unlock()

	return backend
}

// createSimpleBackend is a helper function to create a simple backend from a URL string
func createSimpleBackend(name, urlStr string) Backend {
	u, err := url.Parse(urlStr)
	if err != nil {
		panic(fmt.Sprintf("invalid URL: %v", err))
	}

	return &simpleBackend{
		name:  name,
		url:   u,
		proxy: httputil.NewSingleHostReverseProxy(u),
	}
}
