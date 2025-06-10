---
title: AI Quota Management
keywords: [ AI Gateway, AI Quota ]
description: AI quota management plugin configuration reference
---

## Function Description

The `ai-quota` plugin implements AI quota management based on user identity with JWT token authentication and precise quota control. It features a dual Redis key architecture that separately stores total quota and used quota, enabling precise tracking and control of user quota consumption.

The plugin extracts JWT token from request headers, decodes it to extract user ID as the key for quota limiting. Administrative operations require verification through specified request headers and secret keys.

## Runtime Properties

Plugin execution phase: `default phase`
Plugin execution priority: `750`

## Key Features

- **Dual Redis Key Architecture**: Separate storage for total quota and used quota, calculating remaining quota
- **JWT Authentication**: Extract user identity information from JWT tokens
- **Flexible Quota Deduction**: Header-based quota deduction triggering
- **Complete Management APIs**: Support for query, refresh, and delta operations on both total and used quotas
- **Redis Cluster Support**: Compatible with both Redis standalone and cluster modes

## How It Works

### Quota Calculation Logic
```
Remaining Quota = Total Quota - Used Quota
```

### Redis Key Structure
- `{redis_key_prefix}{user_id}` - Stores user's total quota
- `{redis_used_prefix}{user_id}` - Stores user's used quota

### Quota Deduction Mechanism
When a request contains specified headers and values, the system increments the user's used quota by 1. This mechanism allows flexible control over when quotas are deducted.

## Configuration Description

| Name                   | Data Type | Required Conditions | Default Value       | Description                                    |
|------------------------|-----------|---------------------|---------------------|------------------------------------------------|
| `redis_key_prefix`     | string    | Optional           | chat_quota:         | Redis key prefix for total quota              |
| `redis_used_prefix`    | string    | Optional           | chat_quota_used:    | Redis key prefix for used quota               |
| `token_header`         | string    | Optional           | authorization       | Request header name storing JWT token         |
| `admin_header`         | string    | Optional           | x-admin-key         | Request header name for admin verification    |
| `admin_key`            | string    | Required           | -                   | Secret key for admin operation verification   |
| `admin_path`           | string    | Optional           | /quota              | Prefix for quota management request paths     |
| `deduct_header`        | string    | Optional           | x-quota-identity    | Header name triggering quota deduction        |
| `deduct_header_value`  | string    | Optional           | true                | Header value triggering quota deduction       |
| `redis`                | object    | Yes                | -                   | Redis related configuration                    |

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
redis_used_prefix: "chat_quota_used:"
token_header: "authorization"
admin_header: "x-admin-key"
admin_key: "your-admin-secret"
admin_path: "/quota"
deduct_header: "x-quota-identity"
deduct_header_value: "user"
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

## API Reference

### User Quota Check

**Path**: `/v1/chat/completions`

**Method**: POST

**Headers**:
- `Authorization`: JWT token for user authentication
- `x-quota-identity`: Optional, triggers quota deduction when value is "true"

**Behavior**:
1. Extract user ID from JWT token
2. Check user's remaining quota (total - used)
3. Allow request to proceed if remaining quota > 0
4. Increment used quota by 1 if deduction trigger header is present

### Management APIs

All management APIs require admin authentication header:
```
x-admin-key: your-admin-secret-key
```

#### Total Quota Management

##### Query Total Quota
```bash
curl -H "x-admin-key: your-admin-secret" \
  "https://example.com/v1/chat/completions/quota?user_id=user123"
```

**Response Example**:
```json
{
  "user_id": "user123",
  "quota": 10000,
  "type": "total_quota"
}
```

##### Refresh Total Quota
```bash
curl -X POST \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "user_id=user123&quota=1000" \
  "https://example.com/v1/chat/completions/quota/refresh"
```

##### Delta Total Quota
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

#### Used Quota Management

##### Query Used Quota
```bash
curl -H "x-admin-key: your-admin-secret" \
  "https://example.com/v1/chat/completions/quota/used?user_id=user123"
```

**Response Example**:
```json
{
  "user_id": "user123",
  "quota": 2500,
  "type": "used_quota"
}
```

##### Refresh Used Quota
```bash
curl -X POST \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "user_id=user123&quota=2500" \
  "https://example.com/v1/chat/completions/quota/used/refresh"
```

##### Delta Used Quota
```bash
# Increase used quota
curl -X POST \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "user_id=user123&value=10" \
  "https://example.com/v1/chat/completions/quota/used/delta"

# Decrease used quota
curl -X POST \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "user_id=user123&value=-5" \
  "https://example.com/v1/chat/completions/quota/used/delta"
```

## Usage Examples

### Normal AI Request (No Quota Deduction)
```bash
curl "https://example.com/v1/chat/completions" \
  -H "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..." \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

### AI Request with Quota Deduction
```bash
curl "https://example.com/v1/chat/completions" \
  -H "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..." \
  -H "x-quota-identity: user" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

## Error Handling

### Common Error Responses

| Status Code | Error Code | Description |
|-------------|------------|-------------|
| 401 | `ai-quota.no_token` | JWT token not provided |
| 401 | `ai-quota.invalid_token` | Invalid JWT token format |
| 401 | `ai-quota.token_parse_failed` | JWT token parsing failed |
| 401 | `ai-quota.no_userid` | User ID not found in JWT token |
| 403 | `ai-quota.unauthorized` | Management API authentication failed |
| 403 | `ai-quota.noquota` | Insufficient quota |
| 400 | `ai-quota.invalid_params` | Invalid request parameters |
| 503 | `ai-quota.error` | Redis connection error |

## Important Notes

1. **JWT Format Requirements**: JWT token must contain user ID information; the plugin extracts the `id` field from token claims
2. **Redis Connection**: Ensure Redis service availability; the plugin depends on Redis for quota storage
3. **Management API Security**: Keep admin authentication keys secure to prevent unauthorized access
4. **Quota Precision**: Quota calculations are integer-based; decimal values are not supported
5. **Concurrency Safety**: The plugin supports quota management in high-concurrency scenarios

Note: Administrative operations do not require carrying JWT tokens, only need to provide the correct administrative secret key in the specified request header.
