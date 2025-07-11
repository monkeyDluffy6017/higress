package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/alibaba/higress/plugins/wasm-go/extensions/ai-quota/util"
	"github.com/alibaba/higress/plugins/wasm-go/pkg/wrapper"
	"github.com/go-jose/go-jose/v3/jwt"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/tidwall/gjson"
	"github.com/tidwall/resp"
)

const (
	pluginName = "ai-quota"
	wildcard   = "*"
)

// Provider types for AI services
const (
	ProviderTypeOpenAI   = "openai"
	ProviderTypeAzure    = "azure"
	ProviderTypeQwen     = "qwen"
	ProviderTypeMoonshot = "moonshot"
	ProviderTypeClaude   = "claude"
	ProviderTypeGemini   = "gemini"
)

// ResponseData 统一响应结构体
type ResponseData struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
}

// ModelInfo represents a model in the models list response
type ModelInfo struct {
	Id      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ModelsResponse represents the /ai-gateway/api/v1/models response
type ModelsResponse struct {
	Object string      `json:"object"`
	Data   []ModelInfo `json:"data"`
}

// sendJSONResponse 发送JSON格式的响应
func sendJSONResponse(statusCode uint32, code string, message string, success bool, data any) error {
	response := ResponseData{
		Code:    code,
		Message: message,
		Success: success,
		Data:    data,
	}
	body, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return util.SendResponse(statusCode, code, util.MimeTypeApplicationJson, string(body))
}

type ChatMode string

const (
	ChatModeCompletion ChatMode = "completion"
	ChatModeAdmin      ChatMode = "admin"
	ChatModeNone       ChatMode = "none"
)

type AdminMode string

const (
	AdminModeRefresh     AdminMode = "refresh"
	AdminModeQuery       AdminMode = "query"
	AdminModeDelta       AdminMode = "delta"
	AdminModeUsedQuery   AdminMode = "used_query"
	AdminModeUsedRefresh AdminMode = "used_refresh"
	AdminModeUsedDelta   AdminMode = "used_delta"
	AdminModeStarQuery   AdminMode = "star_query"
	AdminModeStarSet     AdminMode = "star_set"
	AdminModeNone        AdminMode = "none"
)

// AuthUser struct for parsing user info from JWT
type AuthUser struct {
	ID string `json:"universal_id"`
}

func main() {
	wrapper.SetCtx(
		pluginName,
		wrapper.ParseConfigBy(parseConfig),
		wrapper.ProcessRequestHeadersBy(onHttpRequestHeaders),
		wrapper.ProcessRequestBodyBy(onHttpRequestBody),
		wrapper.ProcessStreamingResponseBodyBy(onHttpStreamingResponseBody),
	)
}

// ProviderConfig contains provider type and model mapping configuration
type ProviderConfig struct {
	Type         string            `yaml:"type"`         // Provider type (openai, qwen, claude, etc.)
	ModelMapping map[string]string `yaml:"modelMapping"` // Model name mapping
}

type QuotaConfig struct {
	redisInfo         RedisInfo      `yaml:"redis"`
	RedisKeyPrefix    string         `yaml:"redis_key_prefix"`
	RedisUsedPrefix   string         `yaml:"redis_used_prefix"`
	RedisStarPrefix   string         `yaml:"redis_star_prefix"`
	CheckGithubStar   bool           `yaml:"check_github_star"`
	TokenHeader       string         `yaml:"token_header"`
	AdminHeader       string         `yaml:"admin_header"`
	AdminKey          string         `yaml:"admin_key"`
	AdminPath         string         `yaml:"admin_path"`
	DeductHeader      string         `yaml:"deduct_header"`
	DeductHeaderValue string         `yaml:"deduct_header_value"`
	ModelQuotaWeights map[string]int `yaml:"model_quota_weights"`
	// Provider configuration for /ai-gateway/api/v1/models endpoint
	Provider    ProviderConfig      `yaml:"provider"` // Provider configuration
	redisClient wrapper.RedisClient `yaml:"-"`
	starCache   map[string]bool     `yaml:"-"` // Simple star status cache
}

type Consumer struct {
	Name       string `yaml:"name"`
	Credential string `yaml:"credential"`
}

type RedisInfo struct {
	ServiceName string `required:"true" yaml:"service_name" json:"service_name"`
	ServicePort int    `required:"false" yaml:"service_port" json:"service_port"`
	Username    string `required:"false" yaml:"username" json:"username"`
	Password    string `required:"false" yaml:"password" json:"password"`
	Timeout     int    `required:"false" yaml:"timeout" json:"timeout"`
	Database    int    `required:"false" yaml:"database" json:"database"`
}

func parseConfig(json gjson.Result, config *QuotaConfig, log wrapper.Log) error {
	log.Debugf("parse config()")

	// admin path
	config.AdminPath = json.Get("admin_path").String()
	if config.AdminPath == "" {
		config.AdminPath = "/quota"
	}

	// token header name
	config.TokenHeader = json.Get("token_header").String()
	if config.TokenHeader == "" {
		config.TokenHeader = "authorization"
	}

	// admin header name and key
	config.AdminHeader = json.Get("admin_header").String()
	if config.AdminHeader == "" {
		config.AdminHeader = "x-admin-key"
	}

	config.AdminKey = json.Get("admin_key").String()
	if config.AdminKey == "" {
		return errors.New("missing admin_key in config")
	}

	// deduct header and value
	config.DeductHeader = json.Get("deduct_header").String()
	if config.DeductHeader == "" {
		config.DeductHeader = "x-quota-identity"
	}

	config.DeductHeaderValue = json.Get("deduct_header_value").String()
	if config.DeductHeaderValue == "" {
		config.DeductHeaderValue = "user"
	}

	// Parse model quota weights
	config.ModelQuotaWeights = make(map[string]int)
	modelWeights := json.Get("model_quota_weights")
	if modelWeights.Exists() {
		modelWeights.ForEach(func(key, value gjson.Result) bool {
			config.ModelQuotaWeights[key.String()] = int(value.Int())
			return true
		})
	}

	// Parse provider configuration
	providerConfig := json.Get("provider")
	if providerConfig.Exists() {
		// Parse provider type
		providerType := providerConfig.Get("type").String()
		if providerType == "" {
			providerType = ProviderTypeOpenAI // Default to OpenAI
		}
		config.Provider.Type = providerType

		// Parse model mapping
		config.Provider.ModelMapping = make(map[string]string)
		modelMapping := providerConfig.Get("modelMapping")
		if modelMapping.Exists() {
			modelMapping.ForEach(func(key, value gjson.Result) bool {
				config.Provider.ModelMapping[key.String()] = value.String()
				return true
			})
		}
	} else {
		// Default provider configuration
		config.Provider.Type = ProviderTypeOpenAI
		config.Provider.ModelMapping = make(map[string]string)
	}

	// Redis
	config.RedisKeyPrefix = json.Get("redis_key_prefix").String()
	if config.RedisKeyPrefix == "" {
		config.RedisKeyPrefix = "chat_quota:"
	}

	config.RedisUsedPrefix = json.Get("redis_used_prefix").String()
	if config.RedisUsedPrefix == "" {
		config.RedisUsedPrefix = "chat_quota_used:"
	}

	config.RedisStarPrefix = json.Get("redis_star_prefix").String()
	if config.RedisStarPrefix == "" {
		config.RedisStarPrefix = "chat_quota_star:"
	}

	config.CheckGithubStar = json.Get("check_github_star").Bool()

	// Initialize simple star cache
	config.starCache = make(map[string]bool)

	redisConfig := json.Get("redis")
	if !redisConfig.Exists() {
		return errors.New("missing redis in config")
	}
	serviceName := redisConfig.Get("service_name").String()
	if serviceName == "" {
		return errors.New("redis service name must not be empty")
	}
	servicePort := int(redisConfig.Get("service_port").Int())
	if servicePort == 0 {
		if strings.HasSuffix(serviceName, ".static") {
			// use default logic port which is 80 for static service
			servicePort = 80
		} else {
			servicePort = 6379
		}
	}
	username := redisConfig.Get("username").String()
	password := redisConfig.Get("password").String()
	timeout := int(redisConfig.Get("timeout").Int())
	if timeout == 0 {
		timeout = 1000
	}
	database := int(redisConfig.Get("database").Int())
	config.redisInfo.ServiceName = serviceName
	config.redisInfo.ServicePort = servicePort
	config.redisInfo.Username = username
	config.redisInfo.Password = password
	config.redisInfo.Timeout = timeout
	config.redisInfo.Database = database
	config.redisClient = wrapper.NewRedisClusterClient(wrapper.FQDNCluster{
		FQDN: serviceName,
		Port: int64(servicePort),
	})

	return config.redisClient.Init(username, password, int64(timeout), wrapper.WithDataBase(database))
}

// parseUserInfoFromToken parses user info from JWT token
func parseUserInfoFromToken(accessToken string) (*AuthUser, error) {
	// use ParseSigned method to parse JWT token without signature verification
	token, err := jwt.ParseSigned(accessToken)
	if err != nil {
		return nil, fmt.Errorf("failed to parse JWT token: %w", err)
	}

	// get unverified claims
	var customClaims map[string]interface{}
	err = token.UnsafeClaimsWithoutVerification(&customClaims)
	if err != nil {
		return nil, fmt.Errorf("failed to extract claims: %w", err)
	}

	// serialize and deserialize claims to get user info
	jsonBytes, err := json.Marshal(customClaims)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize user info: %w", err)
	}

	var userInfo AuthUser
	if err := json.Unmarshal(jsonBytes, &userInfo); err != nil {
		return nil, fmt.Errorf("failed to deserialize user info: %w", err)
	}

	return &userInfo, nil
}

func onHttpRequestHeaders(context wrapper.HttpContext, config QuotaConfig, log wrapper.Log) types.Action {
	log.Debugf("onHttpRequestHeaders()")

	rawPath := context.Path()
	path, _ := url.Parse(rawPath)

	// Handle /ai-gateway/api/v1/models request locally first
	if path.Path == "/ai-gateway/api/v1/models" {
		log.Debugf("[onHttpRequestHeaders] handling /ai-gateway/api/v1/models request locally")
		context.DontReadRequestBody()

		// Generate models response based on modelMapping configuration
		responseBody, err := config.BuildModelsResponse()
		if err != nil {
			log.Errorf("failed to build models response: %v", err)
			_ = sendJSONResponse(500, "ai-quota.build_models_failed", "Failed to build models response", false, nil)
			return types.ActionContinue
		}

		// Send HTTP response directly
		headers := [][2]string{
			{"content-type", "application/json"},
		}
		err = proxywasm.SendHttpResponse(200, headers, responseBody, -1)
		if err != nil {
			log.Errorf("failed to send response: %v", err)
			_ = sendJSONResponse(500, "ai-quota.send_models_response_failed", "Failed to send models response", false, nil)
			return types.ActionContinue
		}

		log.Debugf("[onHttpRequestHeaders] models response sent: %s", string(responseBody))
		return types.ActionContinue
	}

	chatMode, adminMode := getOperationMode(path.Path, config.AdminPath, log)
	context.SetContext("chatMode", chatMode)
	context.SetContext("adminMode", adminMode)
	log.Debugf("chatMode:%s, adminMode:%s", chatMode, adminMode)

	if chatMode == ChatModeNone {
		return types.ActionContinue
	}

	if chatMode == ChatModeAdmin {
		// for admin operations, check admin header and key
		adminKey, err := proxywasm.GetHttpRequestHeader(config.AdminHeader)
		if err != nil || adminKey != config.AdminKey {
			sendJSONResponse(http.StatusForbidden, "ai-gateway.unauthorized", "Request denied by ai quota check. Unauthorized admin operation.", false, nil)
			return types.ActionContinue
		}

		// query quota, used quota or star status
		if adminMode == AdminModeQuery || adminMode == AdminModeUsedQuery || adminMode == AdminModeStarQuery {
			return queryQuota(context, config, path, adminMode, log)
		}
		if adminMode == AdminModeRefresh || adminMode == AdminModeDelta || adminMode == AdminModeUsedRefresh || adminMode == AdminModeUsedDelta || adminMode == AdminModeStarSet {
			context.BufferRequestBody()
			return types.HeaderStopIteration
		}
		return types.ActionContinue
	}

	// for completion mode, need to get userId from token and read request body to extract model
	// get token
	tokenHeader, err := proxywasm.GetHttpRequestHeader(config.TokenHeader)
	if err != nil || tokenHeader == "" {
		sendJSONResponse(http.StatusUnauthorized, "ai-gateway.no_token", "Request denied by ai quota check. No token found.", false, nil)
		return types.ActionContinue
	}

	// extract token (remove Bearer prefix etc.)
	token := extractTokenFromHeader(tokenHeader)
	if token == "" {
		sendJSONResponse(http.StatusUnauthorized, "ai-gateway.invalid_token", "Request denied by ai quota check. Invalid token format.", false, nil)
		return types.ActionContinue
	}

	// parse token to get userId
	userInfo, err := parseUserInfoFromToken(token)
	if err != nil {
		log.Warnf("Failed to parse token: %v", err)
		sendJSONResponse(http.StatusUnauthorized, "ai-gateway.token_parse_failed", "Request denied by ai quota check. Token parse failed.", false, nil)
		return types.ActionContinue
	}

	if userInfo.ID == "" {
		sendJSONResponse(http.StatusUnauthorized, "ai-gateway.no_userid", "Request denied by ai quota check. No user ID found in token.", false, nil)
		return types.ActionContinue
	}

	context.SetContext("userId", userInfo.ID)

	// Buffer request body to extract model info
	// Note: ai-proxy plugin (priority 100) may have already buffered the request body
	// This call is safe and won't conflict with existing buffering
	context.BufferRequestBody()
	return types.HeaderStopIteration
}

// extractTokenFromHeader extracts token from header
func extractTokenFromHeader(header string) string {
	// remove Bearer prefix
	if strings.HasPrefix(header, "Bearer ") {
		return strings.TrimSpace(header[7:])
	}
	// if no Bearer prefix, return directly
	return strings.TrimSpace(header)
}

func onHttpRequestBody(ctx wrapper.HttpContext, config QuotaConfig, body []byte, log wrapper.Log) types.Action {
	log.Debugf("onHttpRequestBody()")
	chatMode, ok := ctx.GetContext("chatMode").(ChatMode)
	if !ok {
		return types.ActionContinue
	}

	if chatMode == ChatModeCompletion {
		// Handle quota check and deduction for completion requests
		return handleCompletionQuota(ctx, config, body, log)
	}

	if chatMode == ChatModeNone {
		return types.ActionContinue
	}

	adminMode, ok := ctx.GetContext("adminMode").(AdminMode)
	if !ok {
		return types.ActionContinue
	}

	if adminMode == AdminModeRefresh {
		return refreshQuota(ctx, config, string(body), log)
	}
	if adminMode == AdminModeDelta {
		return deltaQuota(ctx, config, string(body), log)
	}
	if adminMode == AdminModeUsedRefresh {
		return refreshUsedQuota(ctx, config, string(body), log)
	}
	if adminMode == AdminModeUsedDelta {
		return deltaUsedQuota(ctx, config, string(body), log)
	}
	if adminMode == AdminModeStarSet {
		return setStarStatus(ctx, config, string(body), log)
	}

	return types.ActionContinue
}

func handleCompletionQuota(ctx wrapper.HttpContext, config QuotaConfig, body []byte, log wrapper.Log) types.Action {
	// Get user ID from context first
	userId, ok := ctx.GetContext("userId").(string)
	if !ok {
		sendJSONResponse(http.StatusUnauthorized, "ai-gateway.no_userid", "Request denied by ai quota check. No user ID found.", false, nil)
		return types.ActionContinue
	}

	// Check GitHub star status first if enabled
	if config.CheckGithubStar {
		log.Debugf("GitHub star check is enabled, checking star status for user: %s", userId)

		// First check local cache
		if cached, hasStar := config.checkStarCache(userId); cached {
			log.Debugf("Star status found in cache for user %s: %t", userId, hasStar)
			if hasStar {
				log.Debugf("User %s has starred the project (cached), proceeding with quota check", userId)
				// Star check passed, continue with quota logic
				processQuotaLogic(ctx, config, body, userId, log)
			} else {
				log.Debugf("User %s has not starred the project (cached)", userId)
				sendJSONResponse(http.StatusForbidden, "ai-gateway.star_required", "Please star the project first: https://github.com/zgsm-ai/zgsm", false, nil)
			}
			return types.ActionPause
		}

		// Cache miss, check Redis
		log.Debugf("Star status not in cache, checking Redis for user: %s", userId)
		starKey := config.RedisStarPrefix + userId
		config.redisClient.Get(starKey, func(starResponse resp.Value) {
			// Check if there's a Redis error
			if err := starResponse.Error(); err != nil {
				log.Warnf("Redis error when checking star status for user %s: %v. Allowing request to pass through.", userId, err)
				// Redis error - allow request to pass through for better user experience
				processQuotaLogic(ctx, config, body, userId, log)
				return
			}

			// No Redis error, check the actual value
			hasStar := false
			if !starResponse.IsNull() && starResponse.String() == "true" {
				log.Debugf("User %s has starred the project (from Redis)", userId)
				hasStar = true
			} else {
				log.Debugf("User %s has not starred the project (confirmed from Redis)", userId)
			}

			// Only cache true status
			if hasStar {
				config.setStarCache(userId, hasStar)
				log.Debugf("Cached star status for user %s: %t", userId, hasStar)
				// Star check passed, continue with quota logic
				processQuotaLogic(ctx, config, body, userId, log)
			} else {
				log.Debugf("User %s has not starred, not caching false status", userId)
				sendJSONResponse(http.StatusForbidden, "ai-gateway.star_required", "Please star the project first: https://github.com/zgsm-ai/zgsm", false, nil)
			}
		})
		return types.ActionPause
	}

	// If GitHub star check is disabled, proceed directly with quota logic
	log.Debugf("GitHub star check is disabled, proceeding with quota check")
	return processQuotaLogic(ctx, config, body, userId, log)
}

func processQuotaLogic(ctx wrapper.HttpContext, config QuotaConfig, body []byte, userId string, log wrapper.Log) types.Action {
	// Extract model from request body
	modelName := gjson.GetBytes(body, "model").String()
	log.Debugf("Extracted model name: %s", modelName)

	// Get quota weight for this model, default to 0 if not configured
	quotaWeight := 0
	if weight, exists := config.ModelQuotaWeights[modelName]; exists {
		quotaWeight = weight
	}

	log.Debugf("Model %s quota weight: %d", modelName, quotaWeight)

	// If quota weight is 0, no deduction needed, allow request to continue
	if quotaWeight == 0 {
		log.Debugf("Model %s has zero quota weight, skipping quota check", modelName)
		proxywasm.ResumeHttpRequest()
		return types.ActionContinue
	}

	// Check and deduct quota
	doQuotaCheck(ctx, config, userId, quotaWeight, modelName, log)
	return types.ActionPause
}

func doQuotaCheck(ctx wrapper.HttpContext, config QuotaConfig, userId string, quotaWeight int, modelName string, log wrapper.Log) {
	totalKey := config.RedisKeyPrefix + userId
	usedKey := config.RedisUsedPrefix + userId

	// Check if we need to deduct quota based on header
	deductHeaderValue, err := proxywasm.GetHttpRequestHeader(config.DeductHeader)
	shouldDeduct := err == nil && deductHeaderValue == config.DeductHeaderValue

	// Use enhanced error handling with retries for critical quota operations
	retryConfig := wrapper.RetryConfig{
		MaxRetries:    2, // Limit retries for latency-sensitive operations
		InitialDelay:  50 * time.Millisecond,
		MaxDelay:      500 * time.Millisecond,
		BackoffFactor: 2.0,
		EnableJitter:  true,
	}

	if shouldDeduct {
		// For now, use regular get operations until AtomicQuotaCheck is implemented
		config.redisClient.Get(totalKey, func(totalResponse resp.Value) {
			handleTotalQuotaResponseWithRetry(ctx, config, usedKey, totalResponse, userId, quotaWeight, modelName, log, retryConfig)
		})
	} else {
		// Use regular GET for quota checking
		config.redisClient.Get(totalKey, func(totalResponse resp.Value) {
			handleTotalQuotaResponseWithRetry(ctx, config, usedKey, totalResponse, userId, quotaWeight, modelName, log, retryConfig)
		})
	}
}

func handleTotalQuotaResponseWithRetry(ctx wrapper.HttpContext, config QuotaConfig, usedKey string, totalResponse resp.Value, userId string, quotaWeight int, modelName string, log wrapper.Log, retryConfig wrapper.RetryConfig) {
	if wrapper.IsRedisErrorResponse(totalResponse) {
		redisErr := wrapper.GetRedisErrorFromResponse(totalResponse)
		log.Errorf("Failed to get total quota for user %s: %v", userId, redisErr)

		// Check if it's a retryable error
		if wrapper.IsRetryableError(redisErr) {
			log.Warnf("Retryable error encountered, quota check will be retried for user %s", userId)
		}

		sendJSONResponse(http.StatusForbidden, "quota-check.total_quota_error",
			fmt.Sprintf("Failed to retrieve total quota: %s", redisErr.Error()), false, nil)
		return
	}

	// Handle the case where total quota key doesn't exist or is empty - default to 0
	totalQuotaStr := totalResponse.String()
	totalQuota := 0 // Default value for users without allocated quota
	var parseErr error

	if totalQuotaStr != "" {
		totalQuota, parseErr = strconv.Atoi(totalQuotaStr)
		if parseErr != nil {
			log.Errorf("Invalid total quota format for user %s: %s", userId, totalQuotaStr)
			sendJSONResponse(http.StatusInternalServerError, "quota-check.invalid_total_quota",
				"Invalid total quota format", false, nil)
			return
		}

		// Validate that total quota is non-negative
		if totalQuota < 0 {
			log.Errorf("Invalid total quota value for user %s: %d (cannot be negative)", userId, totalQuota)
			sendJSONResponse(http.StatusInternalServerError, "quota-check.invalid_total_quota",
				"Invalid total quota value", false, nil)
			return
		}
	} else {
		// Key doesn't exist or is empty, log for monitoring
		log.Infof("No total quota found for user %s (key does not exist or is empty), defaulting to 0", userId)
	}

	// Get used quota
	config.redisClient.Get(usedKey, func(usedResponse resp.Value) {
		handleUsedQuotaResponseWithRetry(ctx, config, usedResponse, userId, quotaWeight, modelName, totalQuota, log)
	})
}

func handleUsedQuotaResponseWithRetry(ctx wrapper.HttpContext, config QuotaConfig, usedResponse resp.Value, userId string, quotaWeight int, modelName string, totalQuota int, log wrapper.Log) {
	if wrapper.IsRedisErrorResponse(usedResponse) {
		redisErr := wrapper.GetRedisErrorFromResponse(usedResponse)
		log.Errorf("Failed to get used quota for user %s: %v", userId, redisErr)

		// Check if it's a retryable error
		if wrapper.IsRetryableError(redisErr) {
			log.Warnf("Retryable error encountered, used quota check will be retried for user %s", userId)
		}

		sendJSONResponse(http.StatusForbidden, "quota-check.used_quota_error",
			fmt.Sprintf("Failed to retrieve used quota: %s", redisErr.Error()), false, nil)
		return
	}

	// Handle the case where used quota key doesn't exist or is empty - default to 0
	usedQuotaStr := usedResponse.String()
	usedQuota := 0 // Default value for new users

	if usedQuotaStr != "" {
		var parseErr error
		usedQuota, parseErr = strconv.Atoi(usedQuotaStr)
		if parseErr != nil {
			log.Errorf("Invalid used quota format for user %s: %s", userId, usedQuotaStr)
			sendJSONResponse(http.StatusInternalServerError, "quota-check.invalid_used_quota",
				"Invalid used quota format", false, nil)
			return
		}

		// Validate that used quota is non-negative
		if usedQuota < 0 {
			log.Errorf("Invalid used quota value for user %s: %d (cannot be negative)", userId, usedQuota)
			sendJSONResponse(http.StatusInternalServerError, "quota-check.invalid_used_quota",
				"Invalid used quota value", false, nil)
			return
		}

		// Additional sanity check: used quota shouldn't exceed total quota by a large margin
		// (Allow some tolerance for concurrent operations)
		if usedQuota > totalQuota+quotaWeight {
			log.Warnf("Used quota (%d) significantly exceeds total quota (%d) for user %s. This may indicate data inconsistency.",
				usedQuota, totalQuota, userId)
		}
	} else {
		// Key doesn't exist or is empty, log for monitoring
		log.Infof("No used quota found for user %s (key does not exist or is empty), defaulting to 0", userId)
	}

	// Calculate remaining quota
	remainingQuota := totalQuota - usedQuota

	// Log quota status for debugging
	log.Debugf("Quota status for user %s: total=%d, used=%d, remaining=%d, required=%d",
		userId, totalQuota, usedQuota, remainingQuota, quotaWeight)

	// Check if sufficient quota is available
	if remainingQuota >= quotaWeight {
		// Use regular IncrBy for quota deduction
		usedKey := config.RedisUsedPrefix + userId
		config.redisClient.IncrBy(usedKey, quotaWeight, func(incrResponse resp.Value) {
			handleQuotaDeductionResponse(ctx, incrResponse, userId, quotaWeight, modelName, remainingQuota, log)
		})
	} else {
		log.Warnf("Insufficient quota for user %s: remaining=%d, required=%d", userId, remainingQuota, quotaWeight)
		sendJSONResponse(http.StatusForbidden, "quota-check.insufficient_quota",
			fmt.Sprintf("Insufficient quota. Required: %d, Available: %d", quotaWeight, remainingQuota), false, nil)
	}
}

func handleQuotaDeductionResponse(ctx wrapper.HttpContext, incrResponse resp.Value, userId string, quotaWeight int, modelName string, remainingQuota int, log wrapper.Log) {
	if wrapper.IsRedisErrorResponse(incrResponse) {
		redisErr := wrapper.GetRedisErrorFromResponse(incrResponse)
		log.Errorf("Failed to deduct quota for user %s: %v", userId, redisErr)
		sendJSONResponse(http.StatusInternalServerError, "quota-check.deduction_failed",
			fmt.Sprintf("Quota deduction failed: %s", redisErr.Error()), false, nil)
		return
	}

	// Validate the response from Redis IncrBy operation
	newUsedQuota := incrResponse.Integer()

	// Sanity check: the new used quota should be reasonable
	if newUsedQuota < quotaWeight {
		log.Errorf("Unexpected used quota after deduction for user %s: got %d, expected at least %d",
			userId, newUsedQuota, quotaWeight)
		sendJSONResponse(http.StatusInternalServerError, "quota-check.deduction_inconsistent",
			"Quota deduction resulted in inconsistent state", false, nil)
		return
	}

	// Calculate what the previous used quota should have been
	expectedPreviousUsed := newUsedQuota - quotaWeight

	// Log quota deduction details for audit and debugging
	log.Infof("Successfully deducted %d quota for user %s, model %s. Previous used: %d, New used: %d",
		quotaWeight, userId, modelName, expectedPreviousUsed, newUsedQuota)

	// Additional debug information
	log.Debugf("Quota deduction details for user %s: deducted=%d, new_used=%d, expected_previous=%d",
		userId, quotaWeight, newUsedQuota, expectedPreviousUsed)

	proxywasm.ResumeHttpRequest()
}

func onHttpStreamingResponseBody(ctx wrapper.HttpContext, config QuotaConfig, data []byte, endOfStream bool, log wrapper.Log) []byte {
	chatMode, ok := ctx.GetContext("chatMode").(ChatMode)
	if !ok {
		return data
	}
	if chatMode == ChatModeNone || chatMode == ChatModeAdmin {
		return data
	}

	// chat completion mode - no longer need to deduct quota here as it's handled in request headers
	return data
}

func getOperationMode(path string, adminPath string, log wrapper.Log) (ChatMode, AdminMode) {
	fullAdminPath := "/v1/chat/completions" + adminPath
	if strings.HasSuffix(path, fullAdminPath+"/refresh") {
		return ChatModeAdmin, AdminModeRefresh
	}
	if strings.HasSuffix(path, fullAdminPath+"/delta") {
		return ChatModeAdmin, AdminModeDelta
	}
	if strings.HasSuffix(path, fullAdminPath+"/used/refresh") {
		return ChatModeAdmin, AdminModeUsedRefresh
	}
	if strings.HasSuffix(path, fullAdminPath+"/used/delta") {
		return ChatModeAdmin, AdminModeUsedDelta
	}
	if strings.HasSuffix(path, fullAdminPath+"/used") {
		return ChatModeAdmin, AdminModeUsedQuery
	}
	if strings.HasSuffix(path, fullAdminPath+"/star/set") {
		return ChatModeAdmin, AdminModeStarSet
	}
	if strings.HasSuffix(path, fullAdminPath+"/star") {
		return ChatModeAdmin, AdminModeStarQuery
	}
	if strings.HasSuffix(path, fullAdminPath) {
		return ChatModeAdmin, AdminModeQuery
	}
	if strings.HasSuffix(path, "/v1/chat/completions") {
		return ChatModeCompletion, AdminModeNone
	}
	return ChatModeNone, AdminModeNone
}

func refreshQuota(ctx wrapper.HttpContext, config QuotaConfig, body string, log wrapper.Log) types.Action {
	queryValues, _ := url.ParseQuery(body)
	values := make(map[string]string, len(queryValues))
	for k, v := range queryValues {
		values[k] = v[0]
	}
	userId := values["user_id"]
	quota, err := strconv.Atoi(values["quota"])
	if userId == "" || err != nil {
		sendJSONResponse(http.StatusBadRequest, "ai-gateway.invalid_params", "Request denied by ai quota check. user_id can't be empty and quota must be integer.", false, nil)
		return types.ActionContinue
	}
	err2 := config.redisClient.Set(config.RedisKeyPrefix+userId, quota, func(response resp.Value) {
		log.Debugf("Redis set key = %s quota = %d", config.RedisKeyPrefix+userId, quota)
		if err := response.Error(); err != nil {
			sendJSONResponse(http.StatusServiceUnavailable, "ai-gateway.error", fmt.Sprintf("redis error:%v", err), false, nil)
			return
		}
		sendJSONResponse(http.StatusOK, "ai-gateway.refreshquota", "refresh quota successful", true, nil)
	})

	if err2 != nil {
		sendJSONResponse(http.StatusServiceUnavailable, "ai-gateway.error", fmt.Sprintf("redis error:%v", err), false, nil)
		return types.ActionContinue
	}

	return types.ActionPause
}

func queryQuota(ctx wrapper.HttpContext, config QuotaConfig, url *url.URL, adminMode AdminMode, log wrapper.Log) types.Action {
	// check url
	queryValues := url.Query()
	values := make(map[string]string, len(queryValues))
	for k, v := range queryValues {
		values[k] = v[0]
	}
	if values["user_id"] == "" {
		sendJSONResponse(http.StatusBadRequest, "ai-gateway.invalid_params", "Request denied by ai quota check. user_id can't be empty.", false, nil)
		return types.ActionContinue
	}
	userId := values["user_id"]

	// Determine which key to use based on admin mode
	var redisKey string
	var responseType string
	if adminMode == AdminModeUsedQuery {
		redisKey = config.RedisUsedPrefix + userId
		responseType = "used_quota"
	} else if adminMode == AdminModeStarQuery {
		// Check cache first for star query
		if cached, hasStar := config.checkStarCache(userId); cached {
			log.Debugf("Star status found in cache for user %s: %t", userId, hasStar)
			starValue := "false"
			if hasStar {
				starValue = "true"
			}
			data := map[string]string{
				"user_id":    userId,
				"star_value": starValue,
				"type":       "star_status",
			}
			sendJSONResponse(http.StatusOK, "ai-gateway.querystar", "query star status successful (cached)", true, data)
			return types.ActionContinue
		}

		redisKey = config.RedisStarPrefix + userId
		responseType = "star_status"
	} else {
		redisKey = config.RedisKeyPrefix + userId
		responseType = "total_quota"
	}

	err := config.redisClient.Get(redisKey, func(response resp.Value) {
		// Check for Redis errors first
		if wrapper.IsRedisErrorResponse(response) {
			redisErr := wrapper.GetRedisErrorFromResponse(response)
			log.Errorf("Failed to query %s for user %s: %v", responseType, userId, redisErr)
			sendJSONResponse(http.StatusServiceUnavailable, "ai-gateway.redis_error",
				fmt.Sprintf("Redis error: %s", redisErr.Error()), false, nil)
			return
		}

		if adminMode == AdminModeStarQuery {
			// Handle star status query (string value)
			starValue := "false"
			if !response.IsNull() {
				starValueFromRedis := response.String()
				// Validate star value format
				if starValueFromRedis == "true" || starValueFromRedis == "false" {
					starValue = starValueFromRedis
				} else {
					log.Warnf("Invalid star status value for user %s: %s, defaulting to false", userId, starValueFromRedis)
				}
			} else {
				log.Debugf("No star status found for user %s (key does not exist), defaulting to false", userId)
			}

			// Only cache true status
			hasStar := starValue == "true"
			if hasStar {
				config.setStarCache(userId, hasStar)
				log.Debugf("Cached star status from Redis for user %s: %t", userId, hasStar)
			} else {
				log.Debugf("User %s has not starred, not caching false status", userId)
			}

			data := map[string]string{
				"user_id":    userId,
				"star_value": starValue,
				"type":       responseType,
			}
			sendJSONResponse(http.StatusOK, "ai-gateway.querystar", "query star status successful", true, data)
		} else {
			// Handle quota query (integer value)
			quota := 0
			if !response.IsNull() {
				// Validate that the response can be converted to integer
				quotaStr := response.String()
				if quotaStr != "" {
					var parseErr error
					quota, parseErr = strconv.Atoi(quotaStr)
					if parseErr != nil {
						log.Errorf("Invalid %s format for user %s: %s", responseType, userId, quotaStr)
						sendJSONResponse(http.StatusInternalServerError, "ai-gateway.invalid_quota_format",
							fmt.Sprintf("Invalid %s format", responseType), false, nil)
						return
					}

					// Validate that quota is non-negative
					if quota < 0 {
						log.Errorf("Invalid %s value for user %s: %d (cannot be negative)", responseType, userId, quota)
						sendJSONResponse(http.StatusInternalServerError, "ai-gateway.invalid_quota_value",
							fmt.Sprintf("Invalid %s value", responseType), false, nil)
						return
					}
				}
			} else {
				log.Debugf("No %s found for user %s (key does not exist or is empty), defaulting to 0", responseType, userId)
			}

			data := map[string]interface{}{
				"user_id": userId,
				"quota":   quota,
				"type":    responseType,
			}
			sendJSONResponse(http.StatusOK, "ai-gateway.queryquota", "query quota successful", true, data)
		}
	})
	if err != nil {
		sendJSONResponse(http.StatusServiceUnavailable, "ai-gateway.error", fmt.Sprintf("redis error:%v", err), false, nil)
		return types.ActionContinue
	}
	return types.ActionPause
}

func deltaQuota(ctx wrapper.HttpContext, config QuotaConfig, body string, log wrapper.Log) types.Action {
	queryValues, _ := url.ParseQuery(body)
	values := make(map[string]string, len(queryValues))
	for k, v := range queryValues {
		values[k] = v[0]
	}
	userId := values["user_id"]
	value, err := strconv.Atoi(values["value"])
	if userId == "" || err != nil {
		sendJSONResponse(http.StatusBadRequest, "ai-gateway.invalid_params", "Request denied by ai quota check. user_id can't be empty and value must be integer.", false, nil)
		return types.ActionContinue
	}

	if value >= 0 {
		err := config.redisClient.IncrBy(config.RedisKeyPrefix+userId, value, func(response resp.Value) {
			log.Debugf("Redis Incr key = %s value = %d", config.RedisKeyPrefix+userId, value)
			if err := response.Error(); err != nil {
				sendJSONResponse(http.StatusServiceUnavailable, "ai-gateway.error", fmt.Sprintf("redis error:%v", err), false, nil)
				return
			}
			sendJSONResponse(http.StatusOK, "ai-gateway.deltaquota", "delta quota successful", true, nil)
		})
		if err != nil {
			sendJSONResponse(http.StatusServiceUnavailable, "ai-gateway.error", fmt.Sprintf("redis error:%v", err), false, nil)
			return types.ActionContinue
		}
	} else {
		err := config.redisClient.DecrBy(config.RedisKeyPrefix+userId, 0-value, func(response resp.Value) {
			log.Debugf("Redis Decr key = %s value = %d", config.RedisKeyPrefix+userId, 0-value)
			if err := response.Error(); err != nil {
				sendJSONResponse(http.StatusServiceUnavailable, "ai-gateway.error", fmt.Sprintf("redis error:%v", err), false, nil)
				return
			}
			sendJSONResponse(http.StatusOK, "ai-gateway.deltaquota", "delta quota successful", true, nil)
		})
		if err != nil {
			sendJSONResponse(http.StatusServiceUnavailable, "ai-gateway.error", fmt.Sprintf("redis error:%v", err), false, nil)
			return types.ActionContinue
		}
	}

	return types.ActionPause
}

func refreshUsedQuota(ctx wrapper.HttpContext, config QuotaConfig, body string, log wrapper.Log) types.Action {
	queryValues, _ := url.ParseQuery(body)
	values := make(map[string]string, len(queryValues))
	for k, v := range queryValues {
		values[k] = v[0]
	}
	userId := values["user_id"]
	quota, err := strconv.Atoi(values["quota"])
	if userId == "" || err != nil {
		sendJSONResponse(http.StatusBadRequest, "ai-gateway.invalid_params", "Request denied by ai quota check. user_id can't be empty and quota must be integer.", false, nil)
		return types.ActionContinue
	}
	err2 := config.redisClient.Set(config.RedisUsedPrefix+userId, quota, func(response resp.Value) {
		log.Debugf("Redis set key = %s quota = %d", config.RedisUsedPrefix+userId, quota)
		if err := response.Error(); err != nil {
			sendJSONResponse(http.StatusServiceUnavailable, "ai-gateway.error", fmt.Sprintf("redis error:%v", err), false, nil)
			return
		}
		sendJSONResponse(http.StatusOK, "ai-gateway.refreshusedquota", "refresh used quota successful", true, nil)
	})

	if err2 != nil {
		sendJSONResponse(http.StatusServiceUnavailable, "ai-gateway.error", fmt.Sprintf("redis error:%v", err), false, nil)
		return types.ActionContinue
	}

	return types.ActionPause
}

func deltaUsedQuota(ctx wrapper.HttpContext, config QuotaConfig, body string, log wrapper.Log) types.Action {
	queryValues, _ := url.ParseQuery(body)
	values := make(map[string]string, len(queryValues))
	for k, v := range queryValues {
		values[k] = v[0]
	}
	userId := values["user_id"]
	value, err := strconv.Atoi(values["value"])
	if userId == "" || err != nil {
		sendJSONResponse(http.StatusBadRequest, "ai-gateway.invalid_params", "Request denied by ai quota check. user_id can't be empty and value must be integer.", false, nil)
		return types.ActionContinue
	}

	if value >= 0 {
		err := config.redisClient.IncrBy(config.RedisUsedPrefix+userId, value, func(response resp.Value) {
			log.Debugf("Redis Incr key = %s value = %d", config.RedisUsedPrefix+userId, value)
			if err := response.Error(); err != nil {
				sendJSONResponse(http.StatusServiceUnavailable, "ai-gateway.error", fmt.Sprintf("redis error:%v", err), false, nil)
				return
			}
			sendJSONResponse(http.StatusOK, "ai-gateway.deltausedquota", "delta used quota successful", true, nil)
		})
		if err != nil {
			sendJSONResponse(http.StatusServiceUnavailable, "ai-gateway.error", fmt.Sprintf("redis error:%v", err), false, nil)
			return types.ActionContinue
		}
	} else {
		err := config.redisClient.DecrBy(config.RedisUsedPrefix+userId, 0-value, func(response resp.Value) {
			log.Debugf("Redis Decr key = %s value = %d", config.RedisUsedPrefix+userId, 0-value)
			if err := response.Error(); err != nil {
				sendJSONResponse(http.StatusServiceUnavailable, "ai-gateway.error", fmt.Sprintf("redis error:%v", err), false, nil)
				return
			}
			sendJSONResponse(http.StatusOK, "ai-gateway.deltausedquota", "delta used quota successful", true, nil)
		})
		if err != nil {
			sendJSONResponse(http.StatusServiceUnavailable, "ai-gateway.error", fmt.Sprintf("redis error:%v", err), false, nil)
			return types.ActionContinue
		}
	}

	return types.ActionPause
}

func setStarStatus(ctx wrapper.HttpContext, config QuotaConfig, body string, log wrapper.Log) types.Action {
	queryValues, _ := url.ParseQuery(body)
	values := make(map[string]string)
	for k, v := range queryValues {
		if len(v) > 0 {
			values[k] = v[0]
		}
	}
	userId := values["user_id"]
	starValue := values["star_value"]
	if userId == "" || starValue == "" {
		sendJSONResponse(http.StatusBadRequest, "ai-gateway.invalid_params", "Request denied by ai quota check. user_id and star_value can't be empty.", false, nil)
		return types.ActionContinue
	}

	// Validate star_value should be "true" or "false"
	if starValue != "true" && starValue != "false" {
		sendJSONResponse(http.StatusBadRequest, "ai-gateway.invalid_params", "Request denied by ai quota check. star_value must be 'true' or 'false'.", false, nil)
		return types.ActionContinue
	}

	redisKey := config.RedisStarPrefix + userId

	// Delete from local cache before setting to ensure fresh read
	config.deleteStarCache(userId)
	log.Debugf("Deleted star cache for user %s before setting", userId)

	err := config.redisClient.Set(redisKey, starValue, func(response resp.Value) {
		log.Debugf("Redis set key = %s star_value = %s", redisKey, starValue)
		if err := response.Error(); err != nil {
			sendJSONResponse(http.StatusServiceUnavailable, "ai-gateway.error", fmt.Sprintf("redis error:%v", err), false, nil)
			return
		}

		sendJSONResponse(http.StatusOK, "ai-gateway.setstar", "set star status successful", true, nil)
	})

	if err != nil {
		sendJSONResponse(http.StatusServiceUnavailable, "ai-gateway.error", fmt.Sprintf("redis error:%v", err), false, nil)
		return types.ActionContinue
	}

	return types.ActionPause
}

// checkStarCache checks if user star status is cached
func (config *QuotaConfig) checkStarCache(userId string) (bool, bool) {
	hasStar, exists := config.starCache[userId]
	// Only return cache hit if the user has starred (true)
	// If user hasn't starred, we should always check Redis
	if exists && hasStar {
		return true, true
	}
	return false, false
}

// setStarCache sets user star status in cache (only cache true status)
func (config *QuotaConfig) setStarCache(userId string, hasStar bool) {
	if hasStar {
		config.starCache[userId] = hasStar
	} else {
		// Don't cache false status, delete if exists
		delete(config.starCache, userId)
	}
}

// deleteStarCache removes user star status from cache
func (config *QuotaConfig) deleteStarCache(userId string) {
	delete(config.starCache, userId)
}

// BuildModelsResponse creates an OpenAI-compatible models list response based on modelMapping
func (config *QuotaConfig) BuildModelsResponse() ([]byte, error) {
	// Initialize with empty slice instead of nil slice to ensure JSON serialization returns [] instead of null
	models := make([]ModelInfo, 0)

	// If modelMapping is empty, return an empty models list
	if len(config.Provider.ModelMapping) == 0 {
		response := ModelsResponse{
			Object: "list",
			Data:   models, // Use the same empty slice for consistency
		}
		return json.Marshal(response)
	}

	// Extract model names from modelMapping keys
	for modelName, modelValue := range config.Provider.ModelMapping {
		// Skip wildcard entries
		if modelName == wildcard {
			continue
		}

		// Skip prefix matching patterns (ending with *)
		if strings.HasSuffix(modelName, wildcard) {
			continue
		}

		// Skip models mapped to empty strings (which means "keep original model name" but causes issues)
		// When a model is mapped to empty string, it should be treated as not configured properly
		if modelValue == "" {
			continue
		}

		// Determine the owner based on provider type
		owner := config.getOwnerByProvider()

		models = append(models, ModelInfo{
			Id:      modelName,
			Object:  "model",
			Created: 1686935002, // Fixed timestamp as requested
			OwnedBy: owner,
		})
	}

	// Always return the same models slice (empty or with content)
	// This ensures consistent JSON response: [] instead of null
	response := ModelsResponse{
		Object: "list",
		Data:   models,
	}

	return json.Marshal(response)
}

// getOwnerByProvider returns the owner name based on provider type
func (config *QuotaConfig) getOwnerByProvider() string {
	switch config.Provider.Type {
	case ProviderTypeOpenAI:
		return "openai"
	case ProviderTypeAzure:
		return "openai-internal"
	case ProviderTypeQwen:
		return "alibaba"
	case ProviderTypeMoonshot:
		return "moonshot"
	case ProviderTypeClaude:
		return "anthropic"
	case ProviderTypeGemini:
		return "google"
	default:
		return config.Provider.Type // Use provider type as owner for unknown types
	}
}
