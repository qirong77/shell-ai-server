package main

import (
	"context"
	"net/http"
	"regexp"
	"time"
)

// paramsHandler is an http.HandlerFunc that additionally receives path params.
type paramsHandler func(http.ResponseWriter, *http.Request, map[string]string)

// wrapper adapts a paramsHandler into a plain http.HandlerFunc for the router.
func wrapper(h paramsHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The router passes params via context.
		params, _ := r.Context().Value(ctxParamsKey).(map[string]string)
		if params == nil {
			params = map[string]string{}
		}
		h(w, r, params)
	}
}

type ctxKey int

const ctxParamsKey ctxKey = 1

// routerHandler converts the pattern Router into an http.Handler.
func routerHandler(rt *Router) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		handler, params := rt.Match(r.Method, path)
		if handler == nil {
			sendJSON(w, 404, fail("NOT_FOUND", "Not found: "+r.Method+" "+path, nil, nil))
			return
		}
		// Inject params into context for the wrapper.
		ctx := r.Context()
		if len(params) > 0 {
			ctx = context.WithValue(ctx, ctxParamsKey, params)
		}
		handler(w, r.WithContext(ctx))
	})
}

// corsMiddleware handles CORS preflight (OPTIONS) and sets permissive headers on
// every response, matching the original server's behaviour.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// contextWithTimeout returns a cancellable context with a deadline.
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

var _ = regexp.MustCompile
