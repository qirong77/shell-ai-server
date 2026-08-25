package main

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// route is a single compiled route entry.
type route struct {
	method     string
	pattern    *regexp.Regexp
	paramNames []string
	handler    http.HandlerFunc
}

// Router implements a simple pattern-based HTTP router.
//
// Patterns use ":param" for a single path segment and ":param*" for a
// wildcard capturing the rest of the path, e.g. "/files/:path*".
type Router struct {
	routes []*route
}

func NewRouter() *Router {
	return &Router{}
}

func (rt *Router) Handle(method, pattern string, handler http.HandlerFunc) {
	var paramNames []string
	var sb strings.Builder
	sb.WriteString("^")
	i := 0
	for i < len(pattern) {
		ch := pattern[i]
		if ch == ':' {
			// Read the parameter name.
			j := i + 1
			for j < len(pattern) && (isAlphaNum(pattern[j]) || pattern[j] == '_') {
				j++
			}
			name := pattern[i+1 : j]
			if name == "" {
				// Colon without a name: escape it literally.
				sb.WriteString(":")
				i++
				continue
			}
			paramNames = append(paramNames, name)
			if j < len(pattern) && pattern[j] == '*' {
				// wildcard: captures rest
				sb.WriteString("(.*)")
				i = j + 1
				continue
			}
			sb.WriteString("([^/]+)")
			i = j
			continue
		}
		if ch == '/' {
			sb.WriteString("\\/")
			i++
			continue
		}
		if strings.ContainsRune(`._$-^+(){}|[]`, rune(ch)) {
			sb.WriteByte('\\')
			sb.WriteByte(ch)
		} else if ch == '*' {
			sb.WriteString(".*")
		} else {
			sb.WriteByte(ch)
		}
		i++
	}
	sb.WriteString("$")

	re, err := regexp.Compile(sb.String())
	if err != nil {
		// Fall back to exact match if the pattern is malformed.
		re = regexp.MustCompile("^" + regexp.QuoteMeta(pattern) + "$")
		paramNames = nil
	}
	rt.routes = append(rt.routes, &route{
		method:     strings.ToUpper(method),
		pattern:    re,
		paramNames: paramNames,
		handler:    handler,
	})
}

func (rt *Router) Match(method, pathname string) (http.HandlerFunc, map[string]string) {
	method = strings.ToUpper(method)
	for _, r := range rt.routes {
		if r.method != method {
			continue
		}
		sm := r.pattern.FindStringSubmatch(pathname)
		if sm == nil {
			continue
		}
		params := make(map[string]string, len(r.paramNames))
		for idx, name := range r.paramNames {
			v := ""
			if idx+1 < len(sm) {
				v = sm[idx+1]
			}
			if decoded, err := url.PathUnescape(v); err == nil {
				v = decoded
			}
			params[name] = v
		}
		return r.handler, params
	}
	return nil, nil
}

func isAlphaNum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
