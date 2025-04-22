package bandwidth

import (
	"net/http"
	"strconv"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"golang.org/x/time/rate"
)

func init() {
	caddy.RegisterModule(Middleware{})
	httpcaddyfile.RegisterHandlerDirective("bandwidth", parseCaddyfile)
}

type Middleware struct {
	// Default limit for users without specific limits
	DefaultLimit int `json:"default_limit,omitempty"`
	// UserIdentifier specifies how to identify users (cookie, header, query)
	UserIdentifier string `json:"user_identifier,omitempty"`
	// IdentifierName is the name of the cookie, header, or query parameter
	IdentifierName string `json:"identifier_name,omitempty"`

	// Store limiters by user identifiers
	limiters   map[string]*rate.Limiter
	limitersMu sync.RWMutex
}

func (Middleware) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.bandwidth",
		New: func() caddy.Module { return new(Middleware) },
	}
}

func (m *Middleware) Provision(ctx caddy.Context) error {
	// Initialize the map of limiters
	m.limiters = make(map[string]*rate.Limiter)

	// If identify_by is not set, we'll use IP-based identification by default
	// No need to set defaults for UserIdentifier and IdentifierName

	return nil
}

// getUserIdentifier extracts the user identifier from the request
func (m *Middleware) getUserIdentifier(r *http.Request) string {
	// If no identification method is specified, use IP address directly
	if m.UserIdentifier == "" || m.IdentifierName == "" {
		return r.RemoteAddr
	}

	var userID string

	switch m.UserIdentifier {
	case "cookie":
		if cookie, err := r.Cookie(m.IdentifierName); err == nil {
			userID = cookie.Value
		}
	case "header":
		userID = r.Header.Get(m.IdentifierName)
	case "query":
		userID = r.URL.Query().Get(m.IdentifierName)
	}

	// If no identifier found, use IP as fallback
	if userID == "" {
		userID = r.RemoteAddr
	}

	return userID
}

// getLimiter returns (or creates) a rate limiter for the given user
func (m *Middleware) getLimiter(userID string) *rate.Limiter {
	// First try to get existing limiter
	m.limitersMu.RLock()
	limiter, exists := m.limiters[userID]
	m.limitersMu.RUnlock()

	if exists {
		return limiter
	}

	// Create new limiter if none exists
	m.limitersMu.Lock()
	defer m.limitersMu.Unlock()

	// Check again in case another goroutine created it
	if limiter, exists = m.limiters[userID]; exists {
		return limiter
	}

	// Create new limiter with default limit
	if m.DefaultLimit > 0 {
		limiter = rate.NewLimiter(rate.Limit(m.DefaultLimit), m.DefaultLimit)
		m.limiters[userID] = limiter
	}

	return limiter
}

func (m *Middleware) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	userID := m.getUserIdentifier(r)
	limiter := m.getLimiter(userID)

	if limiter != nil {
		w = &limitedResponseWriter{
			ResponseWriter: w,
			limiter:        limiter,
			r:              r,
		}
	}

	return next.ServeHTTP(w, r)
}

type limitedResponseWriter struct {
	http.ResponseWriter
	limiter *rate.Limiter
	r       *http.Request
}

func (l *limitedResponseWriter) Write(p []byte) (int, error) {
	n := len(p)
	err := l.limiter.WaitN(l.r.Context(), n)
	if err != nil {
		return 0, err
	}
	return l.ResponseWriter.Write(p)
}

func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var m Middleware

	for h.Next() {
		for h.NextBlock(0) {
			switch h.Val() {
			case "default_limit":
				args := h.RemainingArgs()
				if len(args) != 1 {
					return nil, h.ArgErr()
				}
				var err error
				m.DefaultLimit, err = strconv.Atoi(args[0])
				if err != nil {
					return nil, h.Errf("parsing default_limit value: %v", err)
				}

			case "identify_by":
				args := h.RemainingArgs()
				if len(args) != 2 {
					return nil, h.Errf("identify_by requires two arguments: method and name")
				}
				method := args[0]
				if method != "cookie" && method != "header" && method != "query" {
					return nil, h.Errf("identify_by method must be one of: cookie, header, query")
				}

				m.UserIdentifier = method
				m.IdentifierName = args[1]

			default:
				return nil, h.Errf("unrecognized parameter '%s'", h.Val())
			}
		}
	}

	return &m, nil
}
