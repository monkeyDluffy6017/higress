---
title: AI 配额管理
keywords: [ AI网关, AI配额 ]
description: AI 配额管理插件配置参考
---

## 功能说明

`ai-quota` 插件实现根据用户 ID 进行 AI Token 配额限制，支持配额管理能力，包括查询配额、刷新配额、增减配额。

插件从请求头中获取 JWT token，解码后提取用户 ID 作为配额限制的 key。管理操作需要通过指定的请求头和密钥进行验证。

## 运行属性

插件执行阶段：`默认阶段`
插件执行优先级：`750`

## 配置说明

| 名称                  | 数据类型            | 填写要求     | 默认值           | 描述                                         |
|---------------------|-----------------|----------|---------------|--------------------------------------------|
| `redis_key_prefix`  | string          | 选填       | chat_quota:   | quota redis key 前缀                        |
| `token_header`      | string          | 选填       | authorization | 存储 JWT token 的请求头名称                       |
| `admin_header`      | string          | 选填       | x-admin-key   | 管理操作验证用的请求头名称                            |
| `admin_key`         | string          | 必填       | -             | 管理操作验证用的密钥                               |
| `admin_path`        | string          | 选填       | /quota        | 管理 quota 请求 path 前缀                       |
| `redis`             | object          | 是        | -             | redis相关配置                                  |

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
token_header: "authorization"
admin_header: "x-admin-key"
admin_key: "your-admin-secret"
admin_path: /quota
redis:
  service_name: redis-service.default.svc.cluster.local
  service_port: 6379
  timeout: 2000
```

## JWT Token 格式

插件期望从指定的请求头中获取 JWT token，token 解码后应包含用户 ID 信息。token 格式：

```json
{
  "id": "user123",
  "other_claims": "..."
}
```

插件会从 token 的 `id` 字段提取用户 ID 作为配额限制的 key。

## 管理操作

### 查询配额
如果插件在路由 `example.com/v1/chat/completions` 上生效，则可以通过以下方式查询配额：

```bash
curl -H "x-admin-key: your-admin-secret" \
  "https://example.com/v1/chat/completions/quota?user_id=user123"
```

### 刷新配额
设置指定用户的配额：

```bash
curl -X POST \
  -H "x-admin-key: your-admin-secret" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "user_id=user123&quota=1000" \
  "https://example.com/v1/chat/completions/quota/refresh"
```

### 增减配额
增加或减少指定用户的配额：

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

注意：管理操作不需要携带 JWT token，只需要在指定的请求头中提供正确的管理密钥即可。

