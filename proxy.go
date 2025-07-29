// Package prox provides a flexible and extensible HTTP reverse proxy framework for Go.
// It allows developers to build custom API gateways and reverse proxies with minimal code.
//
// Basic usage:
//
//	proxy := prox.New()
//	proxy.Route("/api/*").ToURL("http://backend:8080")
//	proxy.ListenAndServe(":3000")
//
// With middleware and load balancing:
//
//	proxy := prox.New()
//	proxy.Use(middleware.Logger(), middleware.Recovery())
//
//	backend := proxy.Backend("api-cluster").
//		AddServers("http://api1:8080", "http://api2:8080").
//		WithLoadBalancer(loadbalancer.RoundRobin()).
//		Build()
//
//	proxy.Route("/api/*").To(backend)
//	proxy.ListenAndServe(":3000")
package prox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

// Proxy is the main interface for creating and configuring a reverse proxy server.
// It provides methods for setting up routes, middleware, and backends.
type Proxy interface {
	// Use adds global middleware that will be applied to all routes.
	// Middleware is executed in the order it is added.
	Use(middlewares ...Middleware) Proxy
	// Route creates a new route for the given pattern.
	// Patterns can include wildcards (e.g., "/api/*").
	Route(pattern string) RouteBuilder
	// Backend creates a new backend configuration with the given name.
	// This is useful for defining reusable backends with load balancing.
	Backend(name string) BackendBuilder
	// ListenAndServe starts the proxy server on the given address.
	// The address format is "host:port" (e.g., ":8080" or "localhost:8080").
	ListenAndServe(addr string) error
	// Shutdown gracefully shuts down the proxy server.
	// It waits for ongoing requests to complete or until the context is cancelled.
	Shutdown(ctx context.Context) error
}

// RouteBuilder provides a fluent interface for configuring routes.
type RouteBuilder interface {
	// Use adds middleware specific to this route.
	// Route middleware is executed after global middleware.
	Use(middlewares ...Middleware) RouteBuilder
	// To routes requests to the specified backend.
	To(backend Backend) *Route
	// ToURL routes requests to a single backend URL.
	// This is a convenience method for simple proxy scenarios.
	ToURL(url string) *Route
	// ToHandler routes requests to a custom http.Handler.
	// This allows for custom request handling logic.
	ToHandler(handler http.Handler) *Route
}

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

// Route represents a configured route in the proxy.
type Route struct {
	pattern     string
	backend     Backend
	middlewares []Middleware
	handler     http.Handler
}

type defaultProxy struct {
	mu          sync.RWMutex
	middlewares []Middleware
	routes      []*Route
	backends    map[string]Backend
	server      *http.Server
}

type defaultRouteBuilder struct {
	proxy       *defaultProxy
	pattern     string
	middlewares []Middleware
}

func (rb *defaultRouteBuilder) Use(middlewares ...Middleware) RouteBuilder {
	rb.middlewares = append(rb.middlewares, middlewares...)
	return rb
}

func (rb *defaultRouteBuilder) To(backend Backend) *Route {
	route := &Route{
		pattern:     rb.pattern,
		backend:     backend,
		middlewares: rb.middlewares,
	}

	rb.proxy.mu.Lock()
	rb.proxy.routes = append(rb.proxy.routes, route)
	rb.proxy.mu.Unlock()

	return route
}

func (rb *defaultRouteBuilder) ToHandler(handler http.Handler) *Route {
	route := &Route{
		pattern:     rb.pattern,
		handler:     handler,
		middlewares: rb.middlewares,
	}

	rb.proxy.mu.Lock()
	rb.proxy.routes = append(rb.proxy.routes, route)
	rb.proxy.mu.Unlock()

	return route
}

// Middleware is a function that wraps an http.Handler to provide additional functionality.
// Middleware can be used for logging, authentication, rate limiting, etc.
type Middleware func(http.Handler) http.Handler

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

func (p *defaultProxy) Use(middlewares ...Middleware) Proxy {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.middlewares = append(p.middlewares, middlewares...)
	return p
}

func (p *defaultProxy) Route(pattern string) RouteBuilder {
	return &defaultRouteBuilder{
		proxy:   p,
		pattern: pattern,
	}
}

func (p *defaultProxy) Backend(name string) BackendBuilder {
	return &defaultBackendBuilder{
		proxy: p,
		name:  name,
	}
}

func (p *defaultProxy) ListenAndServe(addr string) error {
	p.server = &http.Server{
		Addr:    addr,
		Handler: p.buildHandler(),
	}
	return p.server.ListenAndServe()
}

func (p *defaultProxy) Shutdown(ctx context.Context) error {
	if p.server != nil {
		return p.server.Shutdown(ctx)
	}
	return nil
}

// buildHandler creates the HTTP handler with all middlewares applied
func (p *defaultProxy) buildHandler() http.Handler {
	mux := http.NewServeMux()

	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, route := range p.routes {
		handler := route.handler
		if handler == nil && route.backend != nil {
			handler = route.backend
		}

		// Apply route middlewares
		for i := len(route.middlewares) - 1; i >= 0; i-- {
			handler = route.middlewares[i](handler)
		}

		// Apply global middlewares
		for i := len(p.middlewares) - 1; i >= 0; i-- {
			handler = p.middlewares[i](handler)
		}

		mux.Handle(route.pattern, handler)
	}

	return mux
}

// New creates a new Proxy instance
func New(opts ...Option) Proxy {
	p := &defaultProxy{
		middlewares: make([]Middleware, 0),
		routes:      make([]*Route, 0),
		backends:    make(map[string]Backend),
	}

	// Apply options
	for _, opt := range opts {
		opt(p)
	}

	return p
}

// Option configures a Proxy
type Option func(*defaultProxy)

// WithErrorHandler sets a custom error handler
func WithErrorHandler(handler func(w http.ResponseWriter, r *http.Request, err error)) Option {
	return func(p *defaultProxy) {
		// TODO: implement error handler support
	}
}

// ToURL implements RouteBuilder.ToURL.
func (rb *defaultRouteBuilder) ToURL(urlStr string) *Route {
	u, err := url.Parse(urlStr)
	if err != nil {
		panic(fmt.Sprintf("invalid URL: %v", err))
	}

	backend := &simpleBackend{
		url:   u,
		proxy: httputil.NewSingleHostReverseProxy(u),
	}

	return rb.To(backend)
}
