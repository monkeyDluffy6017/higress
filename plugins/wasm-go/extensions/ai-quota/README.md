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
- **Model List Display**: Support for displaying available model lists via `/ai-gateway/api/v1/models` endpoint with configurable providers

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
| `star_check_management` | object   | Optional           | -                   | GitHub star checking configuration            |
| `token_header`         | string    | Optional           | authorization       | Request header name storing JWT token         |
| `admin_header`         | string    | Optional           | x-admin-key         | Request header name for admin verification    |
| `admin_key`            | string    | Required           | -                   | Secret key for admin operation verification   |
| `admin_path`           | string    | Optional           | /quota              | Prefix for quota management request paths     |
| `deduct_header`        | string    | Optional           | x-quota-identity    | Header name triggering quota deduction        |
| `deduct_header_value`  | string    | Optional           | true                | Header value triggering quota deduction       |
| `provider`             | object    | Optional           | -                   | Single provider configuration for model lists |
| `providers`            | array     | Optional           | -                   | Multi-provider configuration for model lists  |
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

### Star Check Management Configuration

| Name        | Data Type | Required | Default Value | Description                                      |
|-------------|-----------|----------|---------------|--------------------------------------------------|
| `enabled`   | boolean   | No       | false         | Whether to enable GitHub star checking          |
| `user_level_enabled` | boolean | No | false    | Whether to enable individual user-level control |
| `redis_star_prefix` | string | No | chat_quota_star: | Redis key prefix for GitHub star projects (employee_number -> starred projects) |
| `admin_stargazer_path` | string | No | /check-star | Path prefix for star check permission management APIs |
| `redis_stargazer_prefix` | string | No | star_check: | Redis key prefix for star check permissions (employee_number -> enabled status) |
| `target_repo` | string  | No       | -             | Target repository for star checking (e.g., "zgsm-ai.zgsm") |

## Configuration Example

### Basic Configuration
```yaml
redis_key_prefix: "chat_quota:"
redis_used_prefix: "chat_quota_used:"
star_check_management:
  enabled: false
  user_level_enabled: false
  redis_star_prefix: "chat_quota_star:"
  admin_stargazer_path: "/check-star"
  redis_stargazer_prefix: "star_check:"
  target_repo: "zgsm-ai.zgsm"
token_header: "authorization"
admin_header: "x-admin-key"
admin_key: "your-admin-secret"
admin_path: "/quota"
deduct_header: "x-quota-identity"
deduct_header_value: "user"
# Single provider configuration for model list display
provider:
  type: "openai"
  models:
    - "gpt-4"
    - "gpt-3.5-turbo"
    - "text-embedding-3-large"
redis:
  service_name: redis-service.default.svc.cluster.local
  service_port: 6379
  timeout: 2000
```

### Configuration with GitHub Star Check Enabled
```yaml
redis_key_prefix: "chat_quota:"
redis_used_prefix: "chat_quota_used:"
star_check_management:
  enabled: true
  user_level_enabled: true
  redis_star_prefix: "chat_quota_star:"
  admin_stargazer_path: "/check-star"
  redis_stargazer_prefix: "star_check:"
  target_repo: "zgsm-ai.zgsm"
token_header: "authorization"
admin_header: "x-admin-key"
admin_key: "your-admin-secret"
admin_path: "/quota"
deduct_header: "x-quota-identity"
deduct_header_value: "user"
# Multi-provider configuration for model list display
providers:
  - id: openai-provider
    type: openai
    models:
      - "gpt-4"
      - "gpt-3.5-turbo"
  - id: deepseek-provider
    type: deepseek
    models:
      - "deepseek-r1"
      - "deepseek-chat"
redis:
  service_name: "local-redis.static"
  service_port: 80
  timeout: 2000
```

**Note**: When `check_github_star` is set to `true`, users must star the GitHub project before using AI services. The system will check if the value of the Redis key `chat_quota_star:{user_id}` is "true".

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

### Model List API

**Path**: `/ai-gateway/api/v1/models`

**Method**: GET

**Description**: Returns a combined list of available models from all configured providers. This endpoint does not require authentication and is handled locally by the plugin.

**Response Example**:
```json
{
  "object": "list",
  "data": [
    {
      "id": "gpt-4",
      "object": "model",
      "created": 1686935002,
      "owned_by": "openai"
    },
    {
      "id": "deepseek-r1",
      "object": "model",
      "created": 1686935002,
      "owned_by": "unknown"
    }
  ]
}
```

**Notes**:
- In multi-provider mode, if multiple providers define the same model name, the first provider's configuration takes precedence
- The `owned_by` field is automatically set based on the provider type (openai → "openai", qwen → "alibaba", etc.)
- This endpoint is handled locally and does not forward requests to upstream services

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

#### GitHub Star Projects Management

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

##### Set GitHub Star Projects
```bash
# Set starred projects for a user (using employee number)
curl -X POST \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "employee_number=emp123&starred_projects=zgsm-ai.zgsm,microsoft/vscode,openai/gpt-4" \
  "https://example.com/v1/chat/completions/quota/star/projects/set"

# Clear all starred projects for a user
curl -X POST \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "employee_number=emp123&starred_projects=" \
  "https://example.com/v1/chat/completions/quota/star/projects/set"
```

**Parameter Description**:
- `employee_number`: Employee number (required, extracted from JWT token's EmployeeNumber field)
- `starred_projects`: Comma-separated list of starred project repositories (optional, empty means clear all)

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

## Configuration

This plugin supports the following configuration:

```yaml
redis:
  service_name: redis-service
  service_port: 6379
  username: ""
  password: "your_password"
  timeout: 2000
  database: 0

redis_key_prefix: "chat_quota:"
redis_used_prefix: "chat_quota_used:"
star_check_management:
  enabled: true
  redis_star_prefix: "github_star:"
  target_repo: "zgsm-ai.zgsm"

# New: Model Permission Management
restricted_models:
  - "gpt-4"
  - "gpt-4-32k"
  - "claude-3-opus"
  - "deepseek-v3"

permission_management:
  redis_permission_prefix: "model_perm:"
  admin_permission_path: "/model-permission"

token_header: "authorization"
admin_header: "x-admin-key"
admin_key: "your-admin-secret"
admin_path: "/quota"
deduct_header: "x-quota-deduct"
deduct_header_value: "true"

model_quota_weights:
  "gpt-4": 10
  "gpt-3.5-turbo": 1
  "claude-3-5-sonnet-latest": 5

provider:
  id: "openai"
  type: "openai"
  models:
    - "gpt-4"
    - "gpt-3.5-turbo"
```

## Features

### Core Quota Management
- **Quota tracking**: Real-time quota consumption tracking
- **Deduction control**: Smart quota deduction with header-based control
- **Multi-model support**: Different quota weights for different models
- **Star requirement**: Optional GitHub star requirement for access

### Model Permission Management (New)
- **Restricted models**: Define models that require special permissions
- **User permissions**: Grant specific users access to restricted models
- **Memory caching**: In-memory permission cache for high performance
- **JWT integration**: Extract user information from JWT tokens

## Permission Management

### User Permission Management

The plugin now supports fine-grained model access control based on user permissions.

#### Setting User Permissions

```bash
curl -X POST "http://your-gateway/model-permission/set" \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "employee_number=85054712&models=[\"gpt-4\",\"claude-3-opus\"]"
```

#### How It Works

1. **Token Extraction**: The plugin extracts user information from JWT tokens in request headers
2. **Employee Number**: Parses employee number from the user's full name (format: "Username (EmployeeNumber)")
3. **Permission Check**: For restricted models, checks if the user has permission
4. **Caching**: Permissions are cached in memory for performance
5. **Redis Storage**: Permission data is stored in Redis with prefix `model_perm:`

### /ai-gateway/api/v1/models Endpoint

When handling `/ai-gateway/api/v1/models` requests:

- **Without Token**: Returns only unrestricted models
- **With Valid Token**: Returns unrestricted models + user's allowed restricted models
- **Permission-based Filtering**: Automatically filters the model list based on user permissions

## Administrative Interface

### Quota Management (Existing)

- `GET /v1/chat/completions/quota?user_id={user_id}`: Query user quota
- `POST /v1/chat/completions/quota/refresh`: Refresh user quota
- `POST /v1/chat/completions/quota/delta`: Modify user quota

### Permission Management (New)

- `POST /model-permission/set`: Set user model permissions
  - Parameters: `employee_number`, `models` (JSON array)
- `GET /model-permission?employee_number={employee_number}`: Query user permissions

### Star Check Permission Management (New)

When user-level star check control is enabled (`star_check_management.user_level_enabled: true`), you can use these APIs to manage individual user star check settings:

- `POST /check-star/set`: Set user star check permission
  - Parameters: `employee_number`, `enabled` (true/false)
- `GET /check-star?employee_number={employee_number}`: Query user star check permission

#### Setting User Star Check Permission

```bash
curl -X POST "https://example.com/check-star/set" \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "employee_number=85054712&enabled=true"
```

#### Querying User Star Check Permission

```bash
curl -X GET "https://example.com/check-star?employee_number=85054712" \
  -H "x-admin-key: your-admin-secret"
```

#### Star Check Workflow

1. **Global Check**: First checks `star_check_management.enabled`
2. **User-Level Control**: If `user_level_enabled` is true, checks user's individual setting
3. **Actual Star Check**: Only performs GitHub star checking when user's star check is enabled

## Configuration Details

### restricted_models
List of models that require special permissions. Users without explicit permission cannot access these models.

### permission_management
- `redis_permission_prefix`: Redis key prefix for storing permissions (default: "model_perm:")
- `admin_permission_path`: URL path for permission management API (default: "/model-permission")

## JWT Token Format

The plugin expects JWT tokens with user information in this format:

```json
{
  "universal_id": "user-uuid",
  "username": "johndoe",
  "employeeNumber": "85054712",
  "fullName": "John Doe (85054712)"
}
```

## Security Features

- **Admin Authentication**: All administrative operations require valid admin keys
- **Token Validation**: JWT tokens are parsed and validated
- **Permission Hierarchy**: Clear separation between unrestricted and restricted models
- **Audit Trail**: All permission operations are logged

## Error Handling

The plugin returns standardized error responses:

```json
{
  "code": "ai-quota.model_permission_denied",
  "message": "You don't have permission to use model gpt-4",
  "success": false
}
```