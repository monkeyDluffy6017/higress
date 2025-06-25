---
title: AI 配额管理
keywords: [ AI网关, AI配额 ]
description: AI 配额管理插件配置参考
---

## 功能说明

`ai-quota` 插件实现基于用户身份的AI配额管理，支持JWT token身份验证和精确的配额控制。插件采用双Redis Key架构设计，分别存储配额总数和已使用量，能够精确跟踪和控制用户的配额使用情况。

插件从请求头中获取JWT token，解码后提取用户ID作为配额限制的key。管理操作需要通过指定的请求头和密钥进行验证。

## 运行属性

插件执行阶段：`默认阶段`
插件执行优先级：`750`

## 核心特性

- **双Redis Key架构**：分别存储配额总数和已使用量，计算剩余配额
- **JWT身份验证**：从JWT token中提取用户身份信息
- **灵活的配额扣减机制**：基于请求头触发配额扣减
- **完整的管理接口**：支持配额总数和已使用量的查询、刷新、增减操作
- **Redis集群支持**：兼容Redis单机和集群模式
- **GitHub关注检查**：可选的GitHub项目关注状态验证

## 工作原理

### 配额计算逻辑
```
剩余配额 = 配额总数 - 已使用量
```

### Redis Key结构
- `{redis_key_prefix}{user_id}` - 存储用户的配额总数
- `{redis_used_prefix}{user_id}` - 存储用户的已使用量
- `{redis_star_prefix}{user_id}` - 存储用户的GitHub关注状态（当启用check_github_star时）

### 配额扣减机制
插件从请求体中提取模型名称，根据 `model_quota_weights` 配置确定扣减额度：
- 如果模型在 `model_quota_weights` 中配置了权重值，则按权重扣减配额
- 如果模型未在 `model_quota_weights` 中配置，则扣减额度为 0（不扣减配额）
- 只有当请求包含指定的请求头和值时，才会真正扣减配额

## 配置说明

| 名称                    | 数据类型   | 填写要求 | 默认值                 | 描述                           |
|------------------------|-----------|----------|------------------------|--------------------------------|
| `redis_key_prefix`     | string    | 选填     | chat_quota:            | 配额总数的redis key前缀         |
| `redis_used_prefix`    | string    | 选填     | chat_quota_used:       | 已使用量的redis key前缀         |
| `redis_star_prefix`    | string    | 选填     | chat_quota_star:       | GitHub关注状态的redis key前缀   |
| `check_github_star`    | boolean   | 选填     | false                  | 是否启用GitHub关注检查          |
| `token_header`         | string    | 选填     | authorization          | 存储JWT token的请求头名称       |
| `admin_header`         | string    | 选填     | x-admin-key            | 管理操作验证用的请求头名称       |
| `admin_key`            | string    | 必填     | -                      | 管理操作验证用的密钥            |
| `admin_path`           | string    | 选填     | /quota                 | 管理quota请求path前缀           |
| `deduct_header`        | string    | 选填     | x-quota-identity       | 扣减配额的触发请求头名称        |
| `deduct_header_value`  | string    | 选填     | true                   | 扣减配额的触发请求头值          |
| `model_quota_weights`  | object    | 选填     | {}                     | 模型配额权重配置，指定每个模型的扣减额度 |
| `redis`                | object    | 是       | -                      | redis相关配置                  |

`redis`中每一项的配置字段说明

| 配置项       | 类型   | 必填 | 默认值                                                     | 说明                                                                                         |
| ------------ | ------ | ---- | ---------------------------------------------------------- | ---------------------------                                                                  |
| service_name | string | 必填 | -                                                          | redis服务名，带服务类型的完整 FQDN 名称，如my-redis.dns，redis.my-ns.svc.cluster.local |
| service_port | int    | 选填 | 静态服务默认值80；其他服务默认值6379                             | redis服务端口                                                                               |
| username     | string | 选填 | -                                                          | redis 用户名                                                                                 |
| password     | string | 选填 | -                                                          | redis 密码                                                                                   |
| timeout      | int    | 选填 | 1000                                                       | redis连接超时时间，单位毫秒                                                                     |
| database     | int    | 选填 | 0                                                          | 使用的数据库 ID，例如，配置为1，对应`SELECT 1`                                                    |

## 配置示例

### 基本配置
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
deduct_header_value: "true"
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

### 启用GitHub关注检查的配置
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
model_quota_weights:
  'deepseek-chat': 1
redis:
  service_name: "local-redis.static"
  service_port: 80
  timeout: 2000
```

**说明**: 当 `check_github_star` 设置为 `true` 时，用户必须先关注 GitHub 项目才能使用AI服务。系统会检查Redis中键为 `chat_quota_star:{user_id}` 的值是否为 "true"。

### 模型权重配置说明

`model_quota_weights` 配置项用于指定不同模型的配额扣减权重：

- **键**: 模型名称（如 'gpt-3.5-turbo', 'gpt-4' 等）
- **值**: 扣减权重（正整数）

示例配置说明：
- `gpt-3.5-turbo` 每次调用扣减 1 个配额
- `gpt-4` 每次调用扣减 2 个配额
- `gpt-4-turbo` 每次调用扣减 3 个配额
- `gpt-4o` 每次调用扣减 4 个配额
- 未配置的模型（如 `claude-3`）扣减 0 个配额（不限制）

## 使用示例

以下是请求不同模型时的配额扣减行为：

```bash
# 请求 gpt-3.5-turbo 模型，扣减 1 个配额
curl -X POST https://example.com/v1/chat/completions \
  -H "Authorization: Bearer <jwt-token>" \
  -H "x-quota-identity: user" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "Hello"}]
  }'

# 请求 gpt-4 模型，扣减 2 个配额
curl -X POST https://example.com/v1/chat/completions \
  -H "Authorization: Bearer <jwt-token>" \
  -H "x-quota-identity: user" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "Hello"}]
  }'

# 请求未配置的模型，不扣减配额
curl -X POST https://example.com/v1/chat/completions \
  -H "Authorization: Bearer <jwt-token>" \
  -H "x-quota-identity: user" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

## JWT Token 格式

插件期望从指定的请求头中获取JWT token，token解码后应包含用户ID信息。token格式：

```json
{
  "id": "user123",
  "other_claims": "..."
}
```

插件会从token的`id`字段提取用户ID作为配额限制的key。

## API接口

### 用户配额检查

**路径**: `/v1/chat/completions`

**方法**: POST

**请求头**:
- `Authorization`: JWT token，用于用户身份验证
- `x-quota-identity`: 可选，值为"user"时触发配额扣减

**行为**:
1. 从JWT token中提取用户ID
2. 如果启用了 `check_github_star`，检查用户的GitHub关注状态（`{redis_star_prefix}{user_id}` 必须为 "true"）
3. 从请求体中提取模型名称
4. 根据 `model_quota_weights` 配置确定所需配额
5. 检查用户的剩余配额是否足够（总数 - 已使用量 >= 所需配额）
6. 如果配额足够且包含扣减触发头，则按模型权重扣减配额
7. 如果模型未配置权重，则不扣减配额直接放行

**GitHub关注检查**:
- 当 `check_github_star` 设置为 `true` 时，会首先检查用户是否关注了GitHub项目
- 如果Redis中 `{redis_star_prefix}{user_id}` 的值不是 "true"，将返回403错误，提示用户需要关注 https://github.com/zgsm-ai/zgsm 项目
- 只有通过GitHub关注检查后，才会继续进行配额检查和扣减

### 管理接口

所有管理接口都需要在请求头中包含管理员认证信息：
```
x-admin-key: your-admin-secret-key
```

#### 配额总数管理

##### 查询配额总数
```bash
curl -H "x-admin-key: your-admin-secret" \
  "https://example.com/v1/chat/completions/quota?user_id=user123"
```

**响应示例**:
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

##### 刷新配额总数
```bash
curl -X POST \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "user_id=user123&quota=1000" \
  "https://example.com/v1/chat/completions/quota/refresh"
```

##### 增减配额总数
```bash
# 增加配额
curl -X POST \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "user_id=user123&value=100" \
  "https://example.com/v1/chat/completions/quota/delta"

# 减少配额
curl -X POST \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "user_id=user123&value=-50" \
  "https://example.com/v1/chat/completions/quota/delta"
```

#### 已使用量管理

##### 查询已使用量
```bash
curl -H "x-admin-key: your-admin-secret" \
  "https://example.com/v1/chat/completions/quota/used?user_id=user123"
```

**响应示例**:
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

##### 刷新已使用量
```bash
curl -X POST \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "user_id=user123&quota=2500" \
  "https://example.com/v1/chat/completions/quota/used/refresh"
```

##### 增减已使用量
```bash
# 增加已使用量
curl -X POST \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "user_id=user123&value=10" \
  "https://example.com/v1/chat/completions/quota/used/delta"

# 减少已使用量
curl -X POST \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "user_id=user123&value=-5" \
  "https://example.com/v1/chat/completions/quota/used/delta"
```

#### GitHub关注状态管理

##### 查询GitHub关注状态
```bash
curl -H "x-admin-key: your-admin-secret" \
  "https://example.com/v1/chat/completions/quota/star?user_id=user123"
```

**响应示例**:
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

##### 设置GitHub关注状态
```bash
# 设置为已关注
curl -X POST \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "user_id=user123&star_value=true" \
  "https://example.com/v1/chat/completions/quota/star/set"

# 设置为未关注
curl -X POST \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "user_id=user123&star_value=false" \
  "https://example.com/v1/chat/completions/quota/star/set"
```

**参数说明**:
- `user_id`: 用户ID（必填）
- `star_value`: 关注状态，只能是 "true" 或 "false"（必填）

## 使用示例

### 正常的AI请求（不扣减配额）
```bash
curl "https://example.com/v1/chat/completions" \
  -H "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..." \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

### 扣减配额的AI请求
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

## 错误处理

### 常见错误响应

| 状态码 | 错误代码 | 说明 |
|--------|----------|------|
| 401 | `ai-gateway.no_token` | 未提供JWT token |
| 401 | `ai-gateway.invalid_token` | JWT token格式无效 |
| 401 | `ai-gateway.token_parse_failed` | JWT token解析失败 |
| 401 | `ai-gateway.no_userid` | JWT token中未找到用户ID |
| 403 | `ai-gateway.unauthorized` | 管理接口认证失败 |
| 403 | `ai-gateway.star_required` | 需要先关注GitHub项目 |
| 403 | `ai-gateway.noquota` | 配额不足 |
| 400 | `ai-gateway.invalid_params` | 请求参数无效 |
| 503 | `ai-gateway.error` | Redis连接错误 |

**错误响应示例**:
```json
{
  "code": "ai-gateway.noquota",
  "message": "Request denied by ai quota check, insufficient quota. Required: 1, Remaining: 0",
  "success": false
}
```

**成功响应示例**:
```json
{
  "code": "ai-gateway.refreshquota",
  "message": "refresh quota successful",
  "success": true
}
```

## 注意事项

1. **JWT格式要求**: JWT token必须包含用户ID信息，插件会从token的claims中提取`id`字段
2. **Redis连接**: 确保Redis服务可用，插件依赖Redis存储配额信息
3. **管理接口安全**: 管理接口的认证密钥需要妥善保管，避免泄露
4. **配额精度**: 配额计算基于整数，不支持小数
5. **并发安全**: 插件支持高并发场景下的配额管理

注意：管理操作不需要携带JWT token，只需要在指定的请求头中提供正确的管理密钥即可。

