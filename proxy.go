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
	"net/http"
	"sync"
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
	// It waits for ongoing requests to complete or until the context is canceled.
	Shutdown(ctx context.Context) error
}

// Middleware is a function that wraps an http.Handler to provide additional functionality.
// Middleware can be used for logging, authentication, rate limiting, etc.
type Middleware func(http.Handler) http.Handler

// defaultProxy is the default implementation of Proxy
type defaultProxy struct {
	mu          sync.RWMutex
	middlewares []Middleware
	routes      []*Route
	backends    map[string]Backend
	server      *http.Server
}

// Use adds global middleware that will be applied to all routes.
func (p *defaultProxy) Use(middlewares ...Middleware) Proxy {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.middlewares = append(p.middlewares, middlewares...)
	return p
}

// Route creates a new route for the given pattern.
func (p *defaultProxy) Route(pattern string) RouteBuilder {
	return &defaultRouteBuilder{
		proxy:   p,
		pattern: pattern,
	}
}

// Backend creates a new backend configuration with the given name.
func (p *defaultProxy) Backend(name string) BackendBuilder {
	return &defaultBackendBuilder{
		proxy: p,
		name:  name,
	}
}

// ListenAndServe starts the proxy server on the given address.
func (p *defaultProxy) ListenAndServe(addr string) error {
	p.server = &http.Server{
		Addr:    addr,
		Handler: p.buildHandler(),
	}
	return p.server.ListenAndServe()
}

// Shutdown gracefully shuts down the proxy server.
func (p *defaultProxy) Shutdown(ctx context.Context) error {
	if p.server != nil {
		return p.server.Shutdown(ctx)
	}
	return nil
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
