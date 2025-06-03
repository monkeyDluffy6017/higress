---
title: AI Quota Management
keywords: [ AI Gateway, AI Quota ]
description: AI quota management plugin configuration reference
---
## Function Description
The `ai-quota` plugin implements AI Token quota limiting based on user ID, and supports quota management capabilities, including querying quotas, refreshing quotas, and increasing or decreasing quotas.

The plugin extracts JWT token from request headers, decodes it to extract user ID as the key for quota limiting. Administrative operations require verification through specified request headers and secret keys.

## Runtime Properties
Plugin execution phase: `default phase`
Plugin execution priority: `750`

## Configuration Description
| Name                 | Data Type        | Required Conditions | Default Value | Description                                       |
|---------------------|------------------|---------------------|---------------|---------------------------------------------------|
| `redis_key_prefix`  | string           | Optional           | chat_quota:   | Quota redis key prefix                            |
| `token_header`      | string           | Optional           | authorization | Request header name storing JWT token            |
| `admin_header`      | string           | Optional           | x-admin-key   | Request header name for admin operation verification |
| `admin_key`         | string           | Required           | -             | Secret key for admin operation verification      |
| `admin_path`        | string           | Optional           | /quota        | Prefix for the path to manage quota requests     |
| `redis`             | object           | Yes                | -             | Redis related configuration                       |

Explanation of each configuration field in `redis`
| Configuration Item | Type   | Required | Default Value                                           | Explanation                                                                                             |
|--------------------|--------|----------|---------------------------------------------------------|---------------------------------------------------------------------------------------------------------|
| service_name       | string | Required | -                                                       | Redis service name, full FQDN name with service type, e.g., my-redis.dns, redis.my-ns.svc.cluster.local |
| service_port       | int    | No       | Default value for static service is 80; others are 6379 | Service port for the redis service                                                                      |
| username           | string | No       | -                                                       | Redis username                                                                                          |
| password           | string | No       | -                                                       | Redis password                                                                                          |
| timeout            | int    | No       | 1000                                                    | Redis connection timeout in milliseconds                                                                |
| database           | int    | No       | 0                                                       | The database ID used, for example, configured as 1, corresponds to `SELECT 1`.                          |

## Configuration Example
### Basic Configuration
```yaml
redis_key_prefix: "chat_quota:"
token_header: "authorization"
admin_header: "x-admin-key"
admin_key: "your-admin-secret"
admin_path: /quota
redis:
  service_name: redis-service.default.svc.cluster.local
  service_port: 6379
  timeout: 2000
```

## JWT Token Format

The plugin expects to obtain JWT token from the specified request header. After decoding, the token should contain user ID information. Token format:

```json
{
  "id": "user123",
  "other_claims": "..."
}
```

The plugin will extract the user ID from the `id` field of the token as the key for quota limiting.

## Administrative Operations

### Query Quota
If the plugin is effective on route `example.com/v1/chat/completions`, you can query quotas in the following way:

```bash
curl -H "x-admin-key: your-admin-secret" \
  "https://example.com/v1/chat/completions/quota?user_id=user123"
```

### Refresh Quota
Set the quota for a specific user:

```bash
curl -X POST \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "user_id=user123&quota=1000" \
  "https://example.com/v1/chat/completions/quota/refresh"
```

### Increase or Decrease Quota
Add or subtract quota for a specific user:

```bash
# Increase quota
curl -X POST \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "user_id=user123&value=100" \
  "https://example.com/v1/chat/completions/quota/delta"

# Decrease quota
curl -X POST \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "user_id=user123&value=-50" \
  "https://example.com/v1/chat/completions/quota/delta"
```

Note: Administrative operations do not require carrying JWT tokens, only need to provide the correct administrative secret key in the specified request header.
