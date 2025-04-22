package bandwidth

import (
	"context"
	"go.uber.org/zap"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"golang.org/x/time/rate"

	"github.com/redis/go-redis/v9"
)

func init() {
	caddy.RegisterModule(Middleware{})
	httpcaddyfile.RegisterHandlerDirective("bandwidth", parseCaddyfile)
}

type Middleware struct {
	// Default limit for users without specific limits (bytes per second)
	DefaultLimit int `json:"default_limit,omitempty"`
	// UserIdentifier specifies how to identify users (cookie, header, query)
	UserIdentifier string `json:"user_identifier,omitempty"`
	// IdentifierName is the name of the cookie, header, or query parameter
	IdentifierName string `json:"identifier_name,omitempty"`
	// Redis configuration for distributed rate limiting
	Redis RedisConfig `json:"redis,omitempty"`

	// Local limiters cache with TTL
	limiters      map[string]*userLimiter
	limitersMu    sync.RWMutex
	redisClient   *redis.Client
	cleanupTicker *time.Ticker
	cleanupDone   chan bool
}

// RedisConfig holds Redis connection details
type RedisConfig struct {
	Enabled   bool   `json:"enabled,omitempty"`
	Address   string `json:"address,omitempty"`
	Password  string `json:"password,omitempty"`
	DB        int    `json:"db,omitempty"`
	KeyPrefix string `json:"key_prefix,omitempty"`
}

// userLimiter holds a rate limiter with expiration time
type userLimiter struct {
	limiter    *rate.Limiter
	lastAccess time.Time
}

const (
	// Redis key expiration time
	redisKeyExpiration = 10 * time.Second
	// Local cache expiration time
	localCacheExpiration = 5 * time.Minute
	// Cleanup interval for local cache
	cleanupInterval = 10 * time.Minute
)

func (Middleware) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.bandwidth",
		New: func() caddy.Module { return new(Middleware) },
	}
}

func (m *Middleware) Provision(ctx caddy.Context) error {
	// Initialize the map of limiters
	m.limiters = make(map[string]*userLimiter)

	// Set default key prefix if not specified
	if m.Redis.Enabled && m.Redis.KeyPrefix == "" {
		// Try to get the hostname from the request or use a default prefix
		m.Redis.KeyPrefix = "bandwidth:"
	}

	// Initialize Redis client if enabled
	if m.Redis.Enabled {
		m.redisClient = redis.NewClient(&redis.Options{
			Addr:     m.Redis.Address,
			Password: m.Redis.Password,
			DB:       m.Redis.DB,
		})

		// Test Redis connection
		_, err := m.redisClient.Ping(context.Background()).Result()
		if err != nil {
			return err
		}
	}

	// Start the cleanup goroutine for local cache
	m.cleanupDone = make(chan bool)
	m.cleanupTicker = time.NewTicker(cleanupInterval)
	go m.cleanupExpiredLimiters()

	return nil
}

// Cleanup when the server shuts down
func (m *Middleware) Cleanup() error {
	if m.cleanupTicker != nil {
		m.cleanupTicker.Stop()
		m.cleanupDone <- true
	}

	if m.redisClient != nil {
		return m.redisClient.Close()
	}

	return nil
}

// cleanupExpiredLimiters removes expired limiters from local cache
func (m *Middleware) cleanupExpiredLimiters() {
	for {
		select {
		case <-m.cleanupTicker.C:
			m.limitersMu.Lock()
			now := time.Now()
			for id, limiter := range m.limiters {
				if now.Sub(limiter.lastAccess) > localCacheExpiration {
					delete(m.limiters, id)
				}
			}
			m.limitersMu.Unlock()
		case <-m.cleanupDone:
			return
		}
	}
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
	if m.DefaultLimit <= 0 {
		return nil
	}

	// First check local cache
	m.limitersMu.RLock()
	cachedLimiter, exists := m.limiters[userID]
	m.limitersMu.RUnlock()

	if exists {
		// Update last access time
		m.limitersMu.Lock()
		cachedLimiter.lastAccess = time.Now()
		m.limitersMu.Unlock()
		return cachedLimiter.limiter
	}

	// If Redis is enabled, use distributed rate limiting
	if m.Redis.Enabled && m.redisClient != nil {
		return m.getDistributedLimiter(userID)
	}

	// Otherwise, create a new local limiter
	m.limitersMu.Lock()
	defer m.limitersMu.Unlock()

	// Check again in case another goroutine created it
	if cachedLimiter, exists = m.limiters[userID]; exists {
		cachedLimiter.lastAccess = time.Now()
		return cachedLimiter.limiter
	}

	// Create new limiter with default limit
	limiter := rate.NewLimiter(rate.Limit(m.DefaultLimit), m.DefaultLimit)
	m.limiters[userID] = &userLimiter{
		limiter:    limiter,
		lastAccess: time.Now(),
	}

	return limiter
}

// getDistributedLimiter implements distributed rate limiting using Redis
func (m *Middleware) getDistributedLimiter(userID string) *rate.Limiter {
	// Create a local limiter that will be used
	limiter := rate.NewLimiter(rate.Limit(m.DefaultLimit), m.DefaultLimit)

	// Add to local cache
	m.limitersMu.Lock()
	m.limiters[userID] = &userLimiter{
		limiter:    limiter,
		lastAccess: time.Now(),
	}
	m.limitersMu.Unlock()

	return limiter
}

// checkAndUpdateRedisCounter checks if bandwidth can be consumed and updates counter
func (m *Middleware) checkAndUpdateRedisCounter(ctx context.Context, userID string, bytes int) error {
	if !m.Redis.Enabled || m.redisClient == nil {
		return nil
	}

	key := m.Redis.KeyPrefix + "count:" + userID
	ttl := redisKeyExpiration

	// Use Redis to track bandwidth usage with INCRBY and TTL
	pipe := m.redisClient.Pipeline()

	// Increment the counter
	incr := pipe.IncrBy(ctx, key, int64(bytes))

	// Check if key exists and set TTL if not
	pipe.PExpire(ctx, key, ttl)

	_, err := pipe.Exec(ctx)
	if err != nil {
		return err
	}

	currentUsage, err := incr.Result()
	if err != nil {
		return err
	}

	// Check if limit is exceeded
	if currentUsage > int64(m.DefaultLimit) {
		// Sleep to respect the rate limit
		time.Sleep(time.Duration((currentUsage - int64(m.DefaultLimit)) * int64(time.Second) / int64(m.DefaultLimit)))
	}

	return nil
}

func (m *Middleware) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	userID := m.getUserIdentifier(r)
	limiter := m.getLimiter(userID)

	if limiter != nil {
		w = &limitedResponseWriter{
			ResponseWriter: w,
			middleware:     m,
			limiter:        limiter,
			userID:         userID,
			r:              r,
		}
	}

	return next.ServeHTTP(w, r)
}

type limitedResponseWriter struct {
	http.ResponseWriter
	middleware *Middleware
	limiter    *rate.Limiter
	userID     string
	r          *http.Request
}

func (l *limitedResponseWriter) Write(p []byte) (int, error) {
	n := len(p)

	// Use local limiter (this will limit bursts)
	err := l.limiter.WaitN(l.r.Context(), n)
	if err != nil {
		return 0, err
	}

	// If Redis is enabled, also track distributed usage
	if l.middleware.Redis.Enabled && l.middleware.redisClient != nil {
		err = l.middleware.checkAndUpdateRedisCounter(l.r.Context(), l.userID, n)
		if err != nil {
			// Log error but continue - fallback to local limiting only
			// Fix the field type issue by using caddy's structured logging correctly
			caddy.Log().Error("Redis rate limiting error",
				zap.Error(err),
				zap.String("user", l.userID))
		}
	}

	return l.ResponseWriter.Write(p)
}

func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var m Middleware

	for h.Next() {
		// Get the domain if specified directly
		domainArgs := h.RemainingArgs()
		if len(domainArgs) == 1 {
			// Use domain as key prefix if specified
			m.Redis.KeyPrefix = "bandwidth:" + domainArgs[0] + ":"
		}

		for h.NextBlock(0) {
			switch h.Val() {
			case "default_limit":
				args := h.RemainingArgs()
				if len(args) != 1 {
					return nil, h.ArgErr()
				}

				// Parse human-readable bandwidth limit
				limit, err := parseHumanReadableBandwidth(args[0])
				if err != nil {
					return nil, h.Errf("parsing default_limit value: %v", err)
				}
				m.DefaultLimit = limit

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

			case "redis":
				m.Redis.Enabled = true
				for h.NextBlock(1) {
					switch h.Val() {
					case "address":
						if !h.Args(&m.Redis.Address) {
							return nil, h.ArgErr()
						}
					case "password":
						if !h.Args(&m.Redis.Password) {
							return nil, h.ArgErr()
						}
					case "db":
						var dbStr string
						if !h.Args(&dbStr) {
							return nil, h.ArgErr()
						}
						var err error
						m.Redis.DB, err = strconv.Atoi(dbStr)
						if err != nil {
							return nil, h.Errf("parsing redis db: %v", err)
						}
					case "key_prefix":
						if !h.Args(&m.Redis.KeyPrefix) {
							return nil, h.ArgErr()
						}
						// Make sure prefix ends with colon
						if !strings.HasSuffix(m.Redis.KeyPrefix, ":") {
							m.Redis.KeyPrefix += ":"
						}
					default:
						return nil, h.Errf("unrecognized redis parameter '%s'", h.Val())
					}
				}

			default:
				return nil, h.Errf("unrecognized parameter '%s'", h.Val())
			}
		}
	}

	return &m, nil
}
