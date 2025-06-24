package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

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
)

// ResponseData 统一响应结构体
type ResponseData struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
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

type QuotaConfig struct {
	redisInfo         RedisInfo         `yaml:"redis"`
	RedisKeyPrefix    string            `yaml:"redis_key_prefix"`
	RedisUsedPrefix   string            `yaml:"redis_used_prefix"`
	RedisStarPrefix   string            `yaml:"redis_star_prefix"`
	CheckGithubStar   bool              `yaml:"check_github_star"`
	TokenHeader       string            `yaml:"token_header"`
	AdminHeader       string            `yaml:"admin_header"`
	AdminKey          string            `yaml:"admin_key"`
	AdminPath         string            `yaml:"admin_path"`
	DeductHeader      string            `yaml:"deduct_header"`
	DeductHeaderValue string            `yaml:"deduct_header_value"`
	ModelQuotaWeights map[string]int    `yaml:"model_quota_weights"`
	credential2Name   map[string]string `yaml:"-"`
	redisClient       wrapper.RedisClient
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
		return types.ActionContinue
	}

	// Get user ID from context
	userId, ok := ctx.GetContext("userId").(string)
	if !ok {
		sendJSONResponse(http.StatusUnauthorized, "ai-gateway.no_userid", "Request denied by ai quota check. No user ID found.", false, nil)
		return types.ActionContinue
	}

	// Check GitHub star status if enabled
	if config.CheckGithubStar {
		starKey := config.RedisStarPrefix + userId
		config.redisClient.Get(starKey, func(starResponse resp.Value) {
			if err := starResponse.Error(); err != nil || starResponse.IsNull() || starResponse.String() != "true" {
				sendJSONResponse(http.StatusForbidden, "ai-gateway.star_required", "Please star the project first: https://github.com/zgsm-ai/zgsm", false, nil)
				return
			}
			// Continue with quota check
			doQuotaCheck(ctx, config, userId, quotaWeight, modelName, log)
		})
		return types.ActionPause
	}

	// Check and deduct quota directly if GitHub star check is disabled
	doQuotaCheck(ctx, config, userId, quotaWeight, modelName, log)
	return types.ActionPause
}

func doQuotaCheck(ctx wrapper.HttpContext, config QuotaConfig, userId string, quotaWeight int, modelName string, log wrapper.Log) {
	totalKey := config.RedisKeyPrefix + userId
	usedKey := config.RedisUsedPrefix + userId

	// First get total quota
	config.redisClient.Get(totalKey, func(totalResponse resp.Value) {
		if err := totalResponse.Error(); err != nil {
			sendJSONResponse(http.StatusForbidden, "ai-gateway.noquota", "Request denied by ai quota check, No quota available", false, nil)
			return
		}

		totalQuota := 0
		if !totalResponse.IsNull() {
			totalQuota = totalResponse.Integer()
		}

		if totalQuota <= 0 {
			sendJSONResponse(http.StatusForbidden, "ai-gateway.noquota", "Request denied by ai quota check, No quota available", false, nil)
			return
		}

		// Then get used quota
		config.redisClient.Get(usedKey, func(usedResponse resp.Value) {
			usedQuota := 0
			if err := usedResponse.Error(); err == nil && !usedResponse.IsNull() {
				usedQuota = usedResponse.Integer()
			}

			remainingQuota := totalQuota - usedQuota
			log.Debugf("User %s: totalQuota:%d usedQuota:%d remainingQuota:%d requiredQuota:%d", userId, totalQuota, usedQuota, remainingQuota, quotaWeight)

			if remainingQuota < quotaWeight {
				sendJSONResponse(http.StatusForbidden, "ai-gateway.noquota", fmt.Sprintf("Request denied by ai quota check, insufficient quota. Required: %d, Remaining: %d", quotaWeight, remainingQuota), false, nil)
				return
			}

			// Check if we need to deduct quota based on header
			deductHeaderValue, err := proxywasm.GetHttpRequestHeader(config.DeductHeader)
			if err == nil && deductHeaderValue == config.DeductHeaderValue {
				// Increment used quota by the model's quota weight
				config.redisClient.IncrBy(usedKey, quotaWeight, func(response resp.Value) {
					if err := response.Error(); err != nil {
						log.Errorf("Failed to deduct quota: %v", err)
					} else {
						log.Debugf("Successfully deducted %d quota for user %s, model %s", quotaWeight, userId, modelName)
					}
					proxywasm.ResumeHttpRequest()
				})
			} else {
				proxywasm.ResumeHttpRequest()
			}
		})
	})
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
		redisKey = config.RedisStarPrefix + userId
		responseType = "star_status"
	} else {
		redisKey = config.RedisKeyPrefix + userId
		responseType = "total_quota"
	}

	err := config.redisClient.Get(redisKey, func(response resp.Value) {
		if err := response.Error(); err != nil {
			sendJSONResponse(http.StatusServiceUnavailable, "ai-gateway.error", fmt.Sprintf("redis error:%v", err), false, nil)
			return
		}

		if adminMode == AdminModeStarQuery {
			// Handle star status query (string value)
			starValue := "false"
			if !response.IsNull() {
				starValue = response.String()
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
				quota = response.Integer()
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
