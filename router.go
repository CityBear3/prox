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
	"net/http"
	"sync"
)

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

// Route represents a configured route in the proxy.
type Route struct {
	pattern     string
	backend     Backend
	middlewares []Middleware
	handler     http.Handler
}

// defaultRouteBuilder implements RouteBuilder
type defaultRouteBuilder struct {
	proxy       *defaultProxy
	pattern     string
	middlewares []Middleware
}

// Use adds middleware specific to this route.
func (rb *defaultRouteBuilder) Use(middlewares ...Middleware) RouteBuilder {
	rb.middlewares = append(rb.middlewares, middlewares...)
	return rb
}

// To routes requests to the specified backend.
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

// ToHandler routes requests to a custom http.Handler.
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

// ToURL routes requests to a single backend URL.
func (rb *defaultRouteBuilder) ToURL(urlStr string) *Route {
	backend := createSimpleBackend("", urlStr)
	return rb.To(backend)
}

// Router handles HTTP request routing
type Router struct {
	mu          sync.RWMutex
	routes      []*Route
	middlewares []Middleware
}

// NewRouter creates a new Router instance
func NewRouter() *Router {
	return &Router{
		routes:      make([]*Route, 0),
		middlewares: make([]Middleware, 0),
	}
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
