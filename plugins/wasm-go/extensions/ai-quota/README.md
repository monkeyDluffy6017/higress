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
- **Model Mapping Support**: Support for model name mapping and `/ai-gateway/api/v1/models` API endpoint
- **Multiple AI Providers**: Support for model list generation from various AI service providers

## How It Works

### Quota Calculation Logic
```
Remaining Quota = Total Quota - Used Quota
```

### Redis Key Structure
- `{redis_key_prefix}{user_id}` - Stores user's total quota
- `{redis_used_prefix}{user_id}` - Stores user's used quota
- `{redis_star_prefix}{user_id}` - Stores user's GitHub star status (when check_github_star is enabled)

### Quota Deduction Mechanism
When a request contains specified headers and values, the system increments the user's used quota by 1. This mechanism allows flexible control over when quotas are deducted.

## Configuration Description

| Name                   | Data Type | Required Conditions | Default Value       | Description                                    |
|------------------------|-----------|---------------------|---------------------|------------------------------------------------|
| `redis_key_prefix`     | string    | Optional           | chat_quota:         | Redis key prefix for total quota              |
| `redis_used_prefix`    | string    | Optional           | chat_quota_used:    | Redis key prefix for used quota               |
| `redis_star_prefix`    | string    | Optional           | chat_quota_star:    | Redis key prefix for GitHub star status       |
| `check_github_star`    | boolean   | Optional           | false               | Whether to enable GitHub star checking        |
| `token_header`         | string    | Optional           | authorization       | Request header name storing JWT token         |
| `admin_header`         | string    | Optional           | x-admin-key         | Request header name for admin verification    |
| `admin_key`            | string    | Required           | -                   | Secret key for admin operation verification   |
| `admin_path`           | string    | Optional           | /quota              | Prefix for quota management request paths     |
| `deduct_header`        | string    | Optional           | x-quota-identity    | Header name triggering quota deduction        |
| `deduct_header_value`  | string    | Optional           | true                | Header value triggering quota deduction       |
| `model_quota_weights`  | object    | Optional           | {}                  | Model quota weight configuration              |
| `provider`             | object    | Optional           | {type: "openai", modelMapping: {}} | Provider configuration for model mapping |
| `provider.type`        | string    | Optional           | openai              | AI service provider type: openai, azure, qwen, moonshot, claude, gemini |
| `provider.modelMapping`| object    | Optional           | {}                  | Model name mapping table for mapping request model names to target AI provider models |
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
redis_star_prefix: "chat_quota_star:"
check_github_star: false
token_header: "authorization"
admin_header: "x-admin-key"
admin_key: "your-admin-secret"
admin_path: "/quota"
deduct_header: "x-quota-identity"
deduct_header_value: "user"
model_quota_weights:
  'gpt-3.5-turbo': 1
  'gpt-4': 2
  'gpt-4-turbo': 3
  'gpt-4o': 4
redis:
  service_name: redis-service.default.svc.cluster.local
  service_port: 6379
  timeout: 2000
```

### Configuration with GitHub Star Check Enabled
```yaml
redis_key_prefix: "chat_quota:"
redis_used_prefix: "chat_quota_used:"
redis_star_prefix: "chat_quota_star:"
check_github_star: true
token_header: "authorization"
admin_header: "x-admin-key"
admin_key: "your-admin-secret"
admin_path: "/quota"
deduct_header: "x-quota-identity"
deduct_header_value: "user"
redis:
  service_name: "local-redis.static"
  service_port: 80
  timeout: 2000
```

**Note**: When `check_github_star` is set to `true`, users must star the GitHub project before using AI services. The system will check if the value of the Redis key `chat_quota_star:{user_id}` is "true".

### Configuration with Model Mapping

#### Qwen (Alibaba) Provider Configuration
```yaml
redis_key_prefix: "chat_quota:"
redis_used_prefix: "chat_quota_used:"
token_header: "authorization"
admin_header: "x-admin-key"
admin_key: "your-admin-secret"
admin_path: "/quota"
deduct_header: "x-quota-identity"
deduct_header_value: "user"
model_quota_weights:
  'gpt-3.5-turbo': 1
  'gpt-4': 5
  'gpt-4-turbo': 10
  'gpt-4o': 15
# Provider Configuration with Model Mapping
provider:
  type: "qwen"
  modelMapping:
    'gpt-3.5-turbo': "qwen-plus"
    'gpt-4': "qwen-max"
    'gpt-4-turbo': "qwen-max"
    'gpt-4o': "qwen-max"
    'text-embedding-v1': "text-embedding-v1"
    'gpt-4-*': "qwen-max"  # Prefix matching, won't appear in model list
    '*': "qwen-turbo"      # Wildcard default model, won't appear in model list
redis:
  service_name: redis-service.default.svc.cluster.local
  service_port: 6379
  timeout: 2000
```

#### OpenAI Provider Configuration
```yaml
redis_key_prefix: "chat_quota:"
redis_used_prefix: "chat_quota_used:"
token_header: "authorization"
admin_header: "x-admin-key"
admin_key: "your-admin-secret"
admin_path: "/quota"
# OpenAI provider configuration
provider:
  type: "openai"
  modelMapping:
    'gpt-3.5-turbo': "gpt-3.5-turbo"
    'gpt-4': "gpt-4"
    'gpt-4-turbo': "gpt-4-turbo"
    'gpt-4o': "gpt-4o"
    '*': "gpt-3.5-turbo"  # Default model
redis:
  service_name: redis-service.default.svc.cluster.local
  service_port: 6379
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
2. If `check_github_star` is enabled, check user's GitHub star status (`{redis_star_prefix}{user_id}` must be "true")
3. Check user's remaining quota (total - used)
4. Allow request to proceed if remaining quota > 0
5. Increment used quota by 1 if deduction trigger header is present

**GitHub Star Check**:
- When `check_github_star` is set to `true`, the system will first check if the user has starred the GitHub project
- If the value of `{redis_star_prefix}{user_id}` in Redis is not "true", a 403 error will be returned, prompting the user to star https://github.com/zgsm-ai/zgsm project
- Only after passing the GitHub star check will the system proceed with quota check and deduction

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
  "code": "ai-gateway.queryquota",
  "message": "query quota successful",
  "success": true,
  "data": {
    "user_id": "user123",
    "quota": 10000,
    "type": "total_quota"
  }
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
  "code": "ai-gateway.queryquota",
  "message": "query quota successful",
  "success": true,
  "data": {
    "user_id": "user123",
    "quota": 2500,
    "type": "used_quota"
  }
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

### Model List Endpoint

#### Get Available Models

**Path**: `/ai-gateway/api/v1/models`

**Method**: GET

**Description**: This endpoint returns a list of available models based on `modelMapping` configuration, compatible with OpenAI API format.

**Request Example**:
```bash
curl -X GET "http://your-domain/ai-gateway/api/v1/models" \
  -H "Content-Type: application/json"
```

**Response Examples**:

**With specific model mapping configured**:
```json
{
  "object": "list",
  "data": [
    {
      "id": "gpt-3.5-turbo",
      "object": "model",
      "created": 1686935002,
      "owned_by": "alibaba"
    },
    {
      "id": "gpt-4",
      "object": "model",
      "created": 1686935002,
      "owned_by": "alibaba"
    },
    {
      "id": "gpt-4-turbo",
      "object": "model",
      "created": 1686935002,
      "owned_by": "alibaba"
    }
  ]
}
```

**Without modelMapping configured**:
```json
{
  "object": "list",
  "data": []
}
```

**Owner Field Mapping**:

| Provider Type | owned_by Value |
|---------------|----------------|
| openai | openai |
| azure | openai-internal |
| qwen | alibaba |
| moonshot | moonshot |
| claude | anthropic |
| gemini | google |
| others | provider type name |

**Important Notes**:
1. **Empty string mappings are skipped**: If a model is mapped to an empty string (e.g., `"*": ""`), it will be skipped and not returned in the model list.
2. **Empty modelMapping**: If `modelMapping` is not configured or is empty, this endpoint will return an empty model list.
3. **Wildcards and prefix matching**: Wildcard `*` and prefix matching patterns (e.g., `gpt-4-*`) will not appear in the model list.

#### GitHub Star Status Management

##### Query GitHub Star Status
```bash
curl -H "x-admin-key: your-admin-secret" \
  "https://example.com/v1/chat/completions/quota/star?user_id=user123"
```

**Response Example**:
```json
{
  "code": "ai-gateway.querystar",
  "message": "query star status successful",
  "success": true,
  "data": {
    "user_id": "user123",
    "star_value": "true",
    "type": "star_status"
  }
}
```

##### Set GitHub Star Status
```bash
# Set as starred
curl -X POST \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "user_id=user123&star_value=true" \
  "https://example.com/v1/chat/completions/quota/star/set"

# Set as not starred
curl -X POST \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "user_id=user123&star_value=false" \
  "https://example.com/v1/chat/completions/quota/star/set"
```

**Parameter Description**:
- `user_id`: User ID (required)
- `star_value`: Star status, must be "true" or "false" (required)

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
| 401 | `ai-gateway.no_token` | JWT token not provided |
| 401 | `ai-gateway.invalid_token` | Invalid JWT token format |
| 401 | `ai-gateway.token_parse_failed` | JWT token parsing failed |
| 401 | `ai-gateway.no_userid` | User ID not found in JWT token |
| 403 | `ai-gateway.unauthorized` | Management API authentication failed |
| 403 | `ai-gateway.star_required` | Need to star the GitHub project first |
| 403 | `ai-gateway.noquota` | Insufficient quota |
| 400 | `ai-gateway.invalid_params` | Invalid request parameters |
| 503 | `ai-gateway.error` | Redis connection error |

**Error Response Example**:
```json
{
  "code": "ai-gateway.noquota",
  "message": "Request denied by ai quota check, insufficient quota. Required: 1, Remaining: 0",
  "success": false
}
```

**Success Response Example**:
```json
{
  "code": "ai-gateway.refreshquota",
  "message": "refresh quota successful",
  "success": true
}
```

## Important Notes

1. **JWT Format Requirements**: JWT token must contain user ID information; the plugin extracts the `id` field from token claims
2. **Redis Connection**: Ensure Redis service availability; the plugin depends on Redis for quota storage
3. **Management API Security**: Keep admin authentication keys secure to prevent unauthorized access
4. **Quota Precision**: Quota calculations are integer-based; decimal values are not supported
5. **Concurrency Safety**: The plugin supports quota management in high-concurrency scenarios

Note: Administrative operations do not require carrying JWT tokens, only need to provide the correct administrative secret key in the specified request header.
