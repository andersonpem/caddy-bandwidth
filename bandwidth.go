package bandwidth

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
	"golang.org/x/time/rate"

	"github.com/redis/go-redis/v9"
)

// Middleware implements a bandwidth limiting HTTP handler for Caddy.
// It can limit bandwidth on a per-user basis using various identification methods
// and supports both local and distributed (Redis-based) rate limiting.
type Middleware struct {
	// DefaultLimit is the rate limit for users without specific limits (bytes per second)
	DefaultLimit int `json:"default_limit,omitempty"`

	// UserIdentifier specifies how to identify users (cookie, header, query)
	UserIdentifier string `json:"user_identifier,omitempty"`

	// IdentifierName is the name of the cookie, header, or query parameter
	IdentifierName string `json:"identifier_name,omitempty"`

	// Redis configuration for distributed rate limiting
	Redis RedisConfig `json:"redis,omitempty"`

	// Private fields
	limiters      map[string]*userLimiter
	limitersMu    sync.RWMutex
	redisClient   *redis.Client
	cleanupTicker *time.Ticker
	cleanupDone   chan bool
	logger        *zap.Logger
}

// RedisConfig holds Redis connection details for distributed rate limiting
type RedisConfig struct {
	// Enabled determines if Redis-based distributed rate limiting should be used
	Enabled bool `json:"enabled,omitempty"`

	// Address is the Redis server address (host:port)
	Address string `json:"address,omitempty"`

	// Password for Redis authentication (optional)
	Password string `json:"password,omitempty"`

	// DB is the Redis database number to use
	DB int `json:"db,omitempty"`

	// KeyPrefix is prepended to all Redis keys to avoid collisions
	KeyPrefix string `json:"key_prefix,omitempty"`
}

// userLimiter holds a rate limiter with expiration time for cache management
type userLimiter struct {
	limiter    *rate.Limiter
	lastAccess time.Time
}

const (
	// redisKeyExpiration is the TTL for Redis rate limit counters
	redisKeyExpiration = 10 * time.Second

	// localCacheExpiration is how long unused limiters are kept in memory
	localCacheExpiration = 5 * time.Minute

	// cleanupInterval is how often the cache cleanup routine runs
	cleanupInterval = 10 * time.Minute
)

func init() {
	caddy.RegisterModule(Middleware{})
	httpcaddyfile.RegisterHandlerDirective("bandwidth", parseCaddyfile)
}

// CaddyModule returns the Caddy module information.
func (Middleware) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.bandwidth",
		New: func() caddy.Module { return new(Middleware) },
	}
}

// Provision sets up the middleware.
func (m *Middleware) Provision(ctx caddy.Context) error {
	// Get logger from Caddy
	m.logger = ctx.Logger()

	// Initialize the map of limiters
	m.limiters = make(map[string]*userLimiter)

	// Set default key prefix if not specified
	if m.Redis.Enabled {
		if m.Redis.Address == "" {
			return fmt.Errorf("redis address must be specified when redis is enabled")
		}

		if m.Redis.KeyPrefix == "" {
			m.Redis.KeyPrefix = "bandwidth:"
		} else if !strings.HasSuffix(m.Redis.KeyPrefix, ":") {
			m.Redis.KeyPrefix += ":"
		}

		// Initialize Redis client
		m.redisClient = redis.NewClient(&redis.Options{
			Addr:     m.Redis.Address,
			Password: m.Redis.Password,
			DB:       m.Redis.DB,
		})

		// Test Redis connection
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := m.redisClient.Ping(ctx).Result()
		if err != nil {
			return fmt.Errorf("failed to connect to Redis: %w", err)
		}

		m.logger.Info("Redis connection established successfully",
			zap.String("address", m.Redis.Address),
			zap.Int("db", m.Redis.DB),
			zap.String("key_prefix", m.Redis.KeyPrefix))
	}

	// Start the cleanup goroutine for local cache
	m.cleanupDone = make(chan bool)
	m.cleanupTicker = time.NewTicker(cleanupInterval)
	go m.cleanupExpiredLimiters()

	return nil
}

// Validate validates the module's configuration.
func (m *Middleware) Validate() error {
	if m.DefaultLimit < 0 {
		return fmt.Errorf("default_limit cannot be negative")
	}

	if m.UserIdentifier != "" && m.UserIdentifier != "cookie" &&
		m.UserIdentifier != "header" && m.UserIdentifier != "query" {
		return fmt.Errorf("user_identifier must be one of: cookie, header, query")
	}

	if m.UserIdentifier != "" && m.IdentifierName == "" {
		return fmt.Errorf("identifier_name must be specified when user_identifier is set")
	}

	if m.Redis.Enabled && m.Redis.Address == "" {
		return fmt.Errorf("redis address must be specified when redis is enabled")
	}

	return nil
}

// Cleanup performs necessary cleanup when the server shuts down.
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
			expiredCount := 0

			for id, limiter := range m.limiters {
				if now.Sub(limiter.lastAccess) > localCacheExpiration {
					delete(m.limiters, id)
					expiredCount++
				}
			}

			m.limitersMu.Unlock()

			if expiredCount > 0 {
				m.logger.Debug("Cleaned up expired rate limiters",
					zap.Int("expired_count", expiredCount),
					zap.Int("remaining_count", len(m.limiters)))
			}

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

	// Create a new local limiter
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

// checkAndUpdateRedisCounter checks if bandwidth can be consumed and updates counter
func (m *Middleware) checkAndUpdateRedisCounter(ctx context.Context, userID string, bytes int) error {
	if !m.Redis.Enabled || m.redisClient == nil {
		return nil
	}

	ttl := redisKeyExpiration

	// Implement a sliding window rate limiter with Redis
	// This is a more refined approach than simple counter with TTL

	// Get current timestamp in seconds
	now := time.Now().Unix()
	windowKey := fmt.Sprintf("%s%s:%d", m.Redis.KeyPrefix, userID, now)

	pipe := m.redisClient.Pipeline()

	// Increment the current window
	pipe.IncrBy(ctx, windowKey, int64(bytes))
	pipe.Expire(ctx, windowKey, ttl)

	// Get the sum of the current window and previous window
	// (for a smooth sliding window effect)
	prevWindowKey := fmt.Sprintf("%s%s:%d", m.Redis.KeyPrefix, userID, now-1)
	getCurrentCmd := pipe.Get(ctx, windowKey)
	getPrevCmd := pipe.Get(ctx, prevWindowKey)

	_, err := pipe.Exec(ctx)
	if err != nil && err != redis.Nil {
		return fmt.Errorf("redis pipeline execution failed: %w", err)
	}

	// Calculate total usage over the sliding window
	var currentUsage, prevUsage int64

	currentStr, err := getCurrentCmd.Result()
	if err == nil {
		currentUsage, _ = strconv.ParseInt(currentStr, 10, 64)
	}

	prevStr, err := getPrevCmd.Result()
	if err == nil && err != redis.Nil {
		prevUsage, _ = strconv.ParseInt(prevStr, 10, 64)
		// Weight the previous window less as time passes
		prevUsage = prevUsage / 2
	}

	totalUsage := currentUsage + prevUsage

	// If over limit, calculate delay needed to stay within limits
	if totalUsage > int64(m.DefaultLimit) {
		delay := time.Duration((totalUsage - int64(m.DefaultLimit)) * int64(time.Second) / int64(m.DefaultLimit))

		// Cap the maximum delay to avoid excessive waiting
		maxDelay := 5 * time.Second
		if delay > maxDelay {
			delay = maxDelay
		}

		if delay > 0 {
			m.logger.Debug("Rate limiting applied",
				zap.String("user", userID),
				zap.Int64("usage", totalUsage),
				zap.Int("limit", m.DefaultLimit),
				zap.Duration("delay", delay))

			time.Sleep(delay)
		}
	}

	return nil
}

// ServeHTTP implements the caddyhttp.MiddlewareHandler interface.
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
			bytesSent:      0,
			bufferSize:     32 * 1024, // 32KB buffer for batching updates
		}
	}

	return next.ServeHTTP(w, r)
}

// limitedResponseWriter wraps the original ResponseWriter to apply rate limiting
type limitedResponseWriter struct {
	http.ResponseWriter
	middleware *Middleware
	limiter    *rate.Limiter
	userID     string
	r          *http.Request
	bytesSent  int
	bufferSize int
}

// Write implements the http.ResponseWriter interface with rate limiting
func (l *limitedResponseWriter) Write(p []byte) (int, error) {
	n := len(p)

	// Use local limiter (this will limit bursts)
	if err := l.limiter.WaitN(l.r.Context(), n); err != nil {
		l.middleware.logger.Error("Local rate limiting error",
			zap.Error(err),
			zap.String("user", l.userID))
		return 0, err
	}

	// If Redis is enabled, also track distributed usage
	// Only update Redis when we've sent enough data or it's been long enough
	l.bytesSent += n
	if l.middleware.Redis.Enabled && l.middleware.redisClient != nil && l.bytesSent >= l.bufferSize {
		if err := l.middleware.checkAndUpdateRedisCounter(l.r.Context(), l.userID, l.bytesSent); err != nil {
			// Log error but continue - fallback to local limiting only
			l.middleware.logger.Error("Redis rate limiting error",
				zap.Error(err),
				zap.String("user", l.userID))
		}
		l.bytesSent = 0
	}

	return l.ResponseWriter.Write(p)
}

// parseHumanReadableBandwidth parses human-readable bandwidth specifications
// Examples:
// - "5M" or "5MB" = 5 megabytes per second = 5*1024*1024 bytes
// - "5m" or "5Mb" = 5 megabits per second = 5*1024*1024/8 bytes
// - "5K" or "5KB" = 5 kilobytes per second = 5*1024 bytes
// - "5k" or "5Kb" = 5 kilobits per second = 5*1024/8 bytes
// - "5G" or "5GB" = 5 gigabytes per second = 5*1024*1024*1024 bytes
// - "5g" or "5Gb" = 5 gigabits per second = 5*1024*1024*1024/8 bytes
// - "500" = 500 bytes per second
// - "1.5M" = 1.5 megabytes per second
func parseHumanReadableBandwidth(input string) (int, error) {
	// Trim whitespace
	input = strings.TrimSpace(input)

	// Reject negative values
	if strings.HasPrefix(input, "-") {
		return 0, fmt.Errorf("bandwidth limit cannot be negative: %s", input)
	}

	// Simple case: just a number (bytes per second)
	if matched, _ := regexp.MatchString(`^\d+(?:\.\d+)?$`, input); matched {
		val, err := strconv.ParseFloat(input, 64)
		if err != nil {
			return 0, err
		}
		return int(math.Round(val)), nil
	}

	// Regular expression to match a number followed by a unit
	// Captures: [1]=number, [2]=unit
	re := regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([KkMmGgTtBb][Bb]?)$`)
	matches := re.FindStringSubmatch(input)

	if matches == nil {
		return 0, fmt.Errorf("invalid bandwidth format: %s (expected format like '5M', '10KB', etc.)", input)
	}

	// Parse the number part
	value, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number in bandwidth: %s", matches[1])
	}

	// Handle single B/b unit (bytes/bits)
	unit := matches[2]
	if unit == "B" {
		return int(math.Round(value)), nil
	} else if unit == "b" {
		return int(math.Round(value / 8)), nil
	}

	// Extract the unit and determine if it's bits or bytes
	unitChar := strings.ToLower(unit)[0]
	isBits := len(unit) > 1 && strings.ToLower(unit)[1] == 'b' && strings.ToLower(unit) != "b"

	// Map unit prefixes to their multipliers
	multipliers := map[byte]float64{
		'k': 1024,
		'm': 1024 * 1024,
		'g': 1024 * 1024 * 1024,
		't': 1024 * 1024 * 1024 * 1024,
	}

	multiplier, ok := multipliers[unitChar]
	if !ok {
		return 0, fmt.Errorf("unknown unit prefix in bandwidth: %s", unit)
	}

	// Calculate bytes and round to nearest integer
	bytes := value * multiplier

	// Convert bits to bytes if needed
	if isBits || (len(unit) == 2 && unit[1] == 'b') {
		bytes /= 8
	}

	// Check for potential overflow
	if bytes > float64(math.MaxInt) {
		return 0, fmt.Errorf("bandwidth value too large: %s", input)
	}

	return int(math.Round(bytes)), nil
}

// parseCaddyfile unmarshals tokens from h into a new Middleware.
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

// Interface guards
var (
	_ caddy.Module                = (*Middleware)(nil)
	_ caddy.Provisioner           = (*Middleware)(nil)
	_ caddy.Validator             = (*Middleware)(nil)
	_ caddy.CleanerUpper          = (*Middleware)(nil)
	_ caddyhttp.MiddlewareHandler = (*Middleware)(nil)
)
