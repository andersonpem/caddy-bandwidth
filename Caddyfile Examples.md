# Caddyfile Examples for Bandwidth Limiter

This document provides practical examples of using the Bandwidth Limiter plugin in various real-world scenarios.

## Basic Global Bandwidth Limiting

Apply a simple bandwidth limit to all responses:

```caddy
localhost {
    bandwidth {
        default_limit 100K  # 100 kilobytes per second
    }
    file_server
}
```

## Limiting Specific Routes

Apply bandwidth limits only to specific paths:

```caddy
localhost {
    route /downloads/* {
        bandwidth {
            default_limit 250K  # 250 kilobytes per second
        }
    }
    
    route /premium/downloads/* {
        bandwidth {
            default_limit 1M  # 1 megabyte per second
        }
    }
    
    file_server
}
```

## User-based Bandwidth Tiers

Implement different bandwidth tiers based on user authentication:

```caddy
localhost {
    @free-users header X-User-Tier Free
    handle @free-users {
        bandwidth {
            default_limit 50K  # 50 kilobytes per second
            identify_by header X-User-ID
        }
    }
    
    @premium-users header X-User-Tier Premium
    handle @premium-users {
        bandwidth {
            default_limit 500K  # 500 kilobytes per second
            identify_by header X-User-ID
        }
    }
    
    @enterprise-users header X-User-Tier Enterprise
    handle @enterprise-users {
        bandwidth {
            default_limit 5M  # 5 megabytes per second
            identify_by header X-User-ID
        }
    }
    
    reverse_proxy backend:8080
}
```

## Multi-CDN Node Setup with Redis

For a distributed setup with multiple CDN nodes:

```caddy
{
    order bandwidth before header
}

cdn.example.com {
    bandwidth cdn.example.com {
        default_limit 1M  # 1 megabyte per second
        identify_by header X-User-ID
        
        redis {
            address redis-master.internal:6379
            password {$REDIS_PASSWORD}
            db 0
            key_prefix cdn:bandwidth:
        }
    }
    
    # Add cache headers
    header {
        Cache-Control "public, max-age=3600"
        CDN-Cache-Control "max-age=86400"
    }
    
    # Log bandwidth usage
    log {
        output file /var/log/caddy/bandwidth.log
        format json
    }
    
    # Proxy to backend
    reverse_proxy {
        lb_policy random
        upstream origin-1:8080
        upstream origin-2:8080
        
        header_up Host {http.request.host}
        header_up X-Forwarded-For {http.request.remote}
    }
}
```

## Dynamic Bandwidth Limiting with Environment Variables

Use environment variables to configure bandwidth limits:

```caddy
{
    order bandwidth before header
}

api.example.com {
    bandwidth {
        default_limit {$API_BANDWIDTH_LIMIT:100K}  # Default to 100 KB/s
        identify_by header Authorization
    }
    
    # Rate limit requests in addition to bandwidth
    rate_limit {
        zone api_zone
        rate 10r/s
    }
    
    reverse_proxy api:3000
}
```

## Regional Bandwidth Management

Apply different bandwidth limits based on geographic regions:

```caddy
{
    order bandwidth before geoip
}

cdn.example.com {
    @europe-users {
        geoip {
            db_path /etc/caddy/geoip.mmdb
            countries EU ES FR DE IT GB
        }
    }
    handle @europe-users {
        bandwidth {
            default_limit 2M  # 2 megabytes per second for European users
            identify_by header X-User-ID
        }
    }
    
    @asia-users {
        geoip {
            db_path /etc/caddy/geoip.mmdb
            countries CN JP KR IN SG
        }
    }
    handle @asia-users {
        bandwidth {
            default_limit 1M  # 1 megabyte per second for Asian users
            identify_by header X-User-ID
        }
    }
    
    # Default for other regions
    bandwidth {
        default_limit 500K  # 500 kilobytes per second for all other regions
        identify_by header X-User-ID
    }
    
    file_server
}
```

## Traffic Shaping for Video Streaming

Implement adaptive bandwidth limits for video streaming with bit-based units:

```caddy
stream.example.com {
    @hd-video path *.hd.mp4
    handle @hd-video {
        bandwidth {
            default_limit 40m  # 40 megabits per second for HD video
            identify_by cookie session_id
        }
    }
    
    @sd-video path *.sd.mp4
    handle @sd-video {
        bandwidth {
            default_limit 12m  # 12 megabits per second for SD video
            identify_by cookie session_id
        }
    }
    
    file_server {
        root /var/www/videos
    }
}
```

## Multiple Bandwidth Tiers by File Type

Apply different bandwidth limits based on file types:

```caddy
files.example.com {
    @images path *.jpg *.jpeg *.png *.gif *.webp
    handle @images {
        bandwidth {
            default_limit 2M  # 2 megabytes per second for images
            identify_by cookie user_session
        }
    }
    
    @documents path *.pdf *.docx *.xlsx
    handle @documents {
        bandwidth {
            default_limit 1M  # 1 megabyte per second for documents
            identify_by cookie user_session
        }
    }
    
    @videos path *.mp4 *.webm *.mov
    handle @videos {
        bandwidth {
            default_limit 20m  # 20 megabits per second for videos
            identify_by cookie user_session
        }
    }
    
    file_server {
        root /var/www/files
    }
}
```

## Fault Tolerance with Multiple Redis Instances

For high-availability setups with Redis Sentinel:

```caddy
api.example.com {
    bandwidth {
        default_limit 200K  # 200 kilobytes per second
        identify_by header X-API-Key
        
        redis {
            address redis-sentinel-1:26379
            password {$REDIS_PASSWORD}
            db 0
            key_prefix api:bandwidth:
        }
    }
    
    # Configure fallback in case primary Redis is unavailable
    @redis_error status 5xx
    handle_errors @redis_error {
        log {
            output file /var/log/caddy/redis_errors.log
            format json
        }
        # Continue with local rate limiting only
    }
    
    reverse_proxy api:8080
}
```

## Mixed Units Example

Using a combination of bits and bytes units:

```caddy
mixed.example.com {
    # Backend API - limit in bytes for precision
    route /api/* {
        bandwidth {
            default_limit 500K  # 500 kilobytes per second
            identify_by header Authorization
        }
    }
    
    # Video streaming - limit in bits as common in media
    route /videos/* {
        bandwidth {
            default_limit 10m  # 10 megabits per second
            identify_by cookie session_id
        }
    }
    
    # Large downloads - limit in gigabits
    route /downloads/* {
        bandwidth {
            default_limit 1g  # 1 gigabit per second
            identify_by header X-Download-Token
        }
    }
    
    reverse_proxy backend:8080
}
```

## Enterprise Multi-Tenant Setup

Configure bandwidth limits for multiple tenants in a SaaS platform:

```caddy
{
    order bandwidth before header
}

*.saas-platform.com {
    bandwidth {$HTTP_HOST} {
        default_limit {$TENANT_BANDWIDTH:5M}  # Default to 5 MB/s
        identify_by header X-User-ID
        
        redis {
            address redis.internal:6379
            key_prefix tenant:{$HTTP_HOST}:
        }
    }
    
    reverse_proxy {
        header_up X-Original-Host {http.request.host}
        upstream backend:8080
    }
}
```