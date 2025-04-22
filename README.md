# 🚀 Caddy Bandwidth Limiter Plugin

Rev up your [Caddy v2](https://caddyserver.com/v2) server with the ability to finely control the bandwidth of your HTTP responses. Perfectly designed for CDN use-cases, this is not just another plugin — it's a **key component** of our soon-to-launch _Media Edge_ software, meticulously crafted from scratch! 🛠

🔗 Dive into Caddy's magic on their [GitHub](https://github.com/caddyserver/caddy).

## 📦 Installation

Plug into the full power of Caddy by integrating our plugin. Let's get started:

1. Grab [xcaddy](https://github.com/caddyserver/xcaddy):

```bash
go get -u github.com/caddyserver/xcaddy/cmd/xcaddy
```

2. Build Caddy with our bandwidth plugin:

```bash
xcaddy build --with github.com/mediafoundation/caddy-bandwidth
```

🎉 Voilà! You've got a `caddy` binary, now supercharged with our plugin.

## 🖋 Usage

### Basic Usage

Eager to throttle bandwidth? Use the `bandwidth` directive in your Caddyfile to set the max bandwidth limits using human-readable formats:

```caddy
localhost
route /myroute {
    bandwidth {
        default_limit 1M  # 1 Megabyte per second
    }
}
```

For more detailed configuration examples, check out the [Caddyfile Examples](./docs/Caddyfile%20Examples.md) document.

### Human-Readable Bandwidth Units

The plugin supports flexible, human-readable bandwidth specifications:

| Format | Description | Example Value |
|--------|-------------|---------------|
| `K` or `KB` | Kilobytes per second | `500K` = 500 kilobytes/s |
| `k` or `Kb` | Kilobits per second | `500k` = 500 kilobits/s |
| `M` or `MB` | Megabytes per second | `5M` = 5 megabytes/s |
| `m` or `Mb` | Megabits per second | `5m` = 5 megabits/s |
| `G` or `GB` | Gigabytes per second | `1G` = 1 gigabyte/s |
| `g` or `Gb` | Gigabits per second | `1g` = 1 gigabit/s |
| Plain number | Bytes per second | `10000` = 10,000 bytes/s |

Examples in configuration:
```caddy
bandwidth {
    default_limit 5M    # 5 megabytes per second (5*1024*1024 bytes)
}

bandwidth {
    default_limit 10m   # 10 megabits per second (10*1024*1024/8 bytes)
}

bandwidth {
    default_limit 1G    # 1 gigabyte per second (1*1024*1024*1024 bytes)
}
```

### User Identification

You can throttle different users from the same IP address by identifying them through cookies, headers, or query parameters:

```caddy
localhost {
    bandwidth {
        default_limit 10M  # 10 megabytes per second
        identify_by cookie session_id
    }
}
```

#### PHP Session Example

For PHP applications, you can easily limit bandwidth per PHP session identifier:

```caddy
example.com {
    bandwidth {
        default_limit 5M  # 5 megabytes per second
        identify_by cookie PHPSESSID
    }
    php_fastcgi unix//var/run/php-fpm.sock
}
```

This is perfect for shared hosting environments where multiple users share the same IP address.

Supported identification methods:
- `cookie` - Identify users by cookie value
- `header` - Identify users by header value
- `query` - Identify users by query parameter

If no identification method is specified, the user's IP address will be used as fallback.

### Distributed Rate Limiting

For environments with multiple Caddy instances, you can enable Redis-based distributed rate limiting:

```caddy
localhost {
    bandwidth {
        default_limit 5M  # 5 megabytes per second
        identify_by header X-User-ID
        
        redis {
            address localhost:6379
            password mypassword  # Optional
            db 0                 # Optional
            key_prefix myapp:    # Optional
        }
    }
}
```

### Domain-Specific Keys

You can specify a domain directly in the bandwidth directive to use as a prefix in Redis keys:

```caddy
bandwidth example.com {
    default_limit 2M  # 2 megabytes per second
    redis {
        address localhost:6379
    }
}
```

This helps keep Redis keys organized in multi-tenant environments.

### 💡 Real-World CDN Example

Designed with CDN use-cases in mind, you can add bandwidth limits dynamically based on headers or other conditions:

```caddy
{
    order bandwidth before header
}

header Server "MediaEdge vX.Y.Z"
reverse_proxy http://localhost:8080 {
    @hasBandwidthLimit header X-Bandwidth-Limit Yes
    handle_response @hasBandwidthLimit {
        bandwidth {
            default_limit {$BANDWIDTH_LIMIT:1M}  # Default to 1MB/s
            identify_by header X-User-ID
        }
    }
}
```

- `order bandwidth before header`: Place the bandwidth module before the header module in the processing order.
  
This allows you to have fine-grained control over bandwidth limits on a per-request basis!

### Distributing Traffic Across Multiple CDN Nodes

For high-traffic environments with multiple CDN nodes, configure Redis for distributed rate limiting:

```caddy
{
    order bandwidth before header
}

cdn.example.com {
    bandwidth cdn.example.com {
        default_limit 10M  # 10MB/s default
        identify_by header X-User-ID
        
        redis {
            address redis.internal:6379
            key_prefix cdn:
        }
    }
    
    reverse_proxy {
        lb_policy random
        upstream backend-1:8080
        upstream backend-2:8080
        upstream backend-3:8080
    }
}
```

This setup ensures that bandwidth limits are maintained consistently across all CDN nodes.

## Full Configuration Reference

### Bandwidth Directive

| Directive | Description | Example |
|-----------|-------------|---------|
| `default_limit` | Bandwidth limit (human-readable format) | `default_limit 5M` |
| `identify_by` | Method and parameter name for user identification | `identify_by cookie session_id` |
| `redis` | Enable and configure Redis for distributed rate limiting | See Redis options below |

### Redis Options

| Option | Description | Example |
|--------|-------------|---------|
| `address` | Redis server address | `address localhost:6379` |
| `password` | Redis password (optional) | `password secret123` |
| `db` | Redis database number (optional) | `db 0` |
| `key_prefix` | Prefix for Redis keys (optional) | `key_prefix myapp:` |

## 🛠 Development

Our plugin adheres to standard Go conventions, featuring a `Middleware` struct that uses the `caddyhttp.MiddlewareHandler` interface. The `limitedResponseWriter` is meticulously designed to limit bandwidth.

### Architecture Highlights

- **User Identification**: Flexible identification through cookies, headers, or query parameters
- **Local Rate Limiting**: In-memory rate limiters using Go's `rate` package
- **Distributed Rate Limiting**: Optional Redis integration for multi-instance deployments
- **Human-Readable Units**: Support for kilobytes, megabytes, gigabytes and their bit equivalents
- **Resource Management**: Proper cleanup and TTL for both local cache and Redis keys
- **Error Handling**: Graceful fallback to local limiting if Redis is unavailable

💡 Ideas? Contributions are welcome! Feel free to submit issues and pull requests.

## 📜 License

Under the MIT License. Use responsibly.

## 📢 Join Our Community

- 🎮 [Discord](https://discord.gg/nyCS7ePWzf)
- 📫 [Telegram](https://t.me/Media_FDN)
- 🐦 [X](https://t.me/Media_FDN)