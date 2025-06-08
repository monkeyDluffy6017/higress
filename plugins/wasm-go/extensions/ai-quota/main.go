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

type ChatMode string

const (
	ChatModeCompletion ChatMode = "completion"
	ChatModeAdmin      ChatMode = "admin"
	ChatModeNone       ChatMode = "none"
)

type AdminMode string

const (
	AdminModeRefresh      AdminMode = "refresh"
	AdminModeQuery        AdminMode = "query"
	AdminModeDelta        AdminMode = "delta"
	AdminModeUsedQuery    AdminMode = "used_query"
	AdminModeUsedRefresh  AdminMode = "used_refresh"
	AdminModeUsedDelta    AdminMode = "used_delta"
	AdminModeNone         AdminMode = "none"
)

// AuthUser struct for parsing user info from JWT
type AuthUser struct {
	ID string `json:"id"`
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
	TokenHeader       string            `yaml:"token_header"`
	AdminHeader       string            `yaml:"admin_header"`
	AdminKey          string            `yaml:"admin_key"`
	AdminPath         string            `yaml:"admin_path"`
	DeductHeader      string            `yaml:"deduct_header"`
	DeductHeaderValue string            `yaml:"deduct_header_value"`
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
		config.DeductHeader = "x-quota-deduct"
	}

	config.DeductHeaderValue = json.Get("deduct_header_value").String()
	if config.DeductHeaderValue == "" {
		config.DeductHeaderValue = "true"
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
			util.SendResponse(http.StatusForbidden, "ai-quota.unauthorized", "text/plain", "Request denied by ai quota check. Unauthorized admin operation.")
			return types.ActionContinue
		}

		// query quota or used quota
		if adminMode == AdminModeQuery || adminMode == AdminModeUsedQuery {
			return queryQuota(context, config, path, adminMode, log)
		}
		if adminMode == AdminModeRefresh || adminMode == AdminModeDelta || adminMode == AdminModeUsedRefresh || adminMode == AdminModeUsedDelta {
			context.BufferRequestBody()
			return types.HeaderStopIteration
		}
		return types.ActionContinue
	}

	// for completion mode, need to get userId from token
	// there is no need to read request body when it is on chat completion mode
	context.DontReadRequestBody()

	// get token
	tokenHeader, err := proxywasm.GetHttpRequestHeader(config.TokenHeader)
	if err != nil || tokenHeader == "" {
		util.SendResponse(http.StatusUnauthorized, "ai-quota.no_token", "text/plain", "Request denied by ai quota check. No token found.")
		return types.ActionContinue
	}

	// extract token (remove Bearer prefix etc.)
	token := extractTokenFromHeader(tokenHeader)
	if token == "" {
		util.SendResponse(http.StatusUnauthorized, "ai-quota.invalid_token", "text/plain", "Request denied by ai quota check. Invalid token format.")
		return types.ActionContinue
	}

	// parse token to get userId
	userInfo, err := parseUserInfoFromToken(token)
	if err != nil {
		log.Warnf("Failed to parse token: %v", err)
		util.SendResponse(http.StatusUnauthorized, "ai-quota.token_parse_failed", "text/plain", "Request denied by ai quota check. Token parse failed.")
		return types.ActionContinue
	}

	if userInfo.ID == "" {
		util.SendResponse(http.StatusUnauthorized, "ai-quota.no_userid", "text/plain", "Request denied by ai quota check. No user ID found in token.")
		return types.ActionContinue
	}

	context.SetContext("userId", userInfo.ID)

	// check quota here: get both total and used quota
	totalKey := config.RedisKeyPrefix + userInfo.ID
	usedKey := config.RedisUsedPrefix + userInfo.ID

	// First get total quota
	config.redisClient.Get(totalKey, func(totalResponse resp.Value) {
		if err := totalResponse.Error(); err != nil {
			util.SendResponse(http.StatusForbidden, "ai-quota.noquota", "text/plain", "Request denied by ai quota check, No quota available")
			return
		}

		totalQuota := 0
		if !totalResponse.IsNull() {
			totalQuota = totalResponse.Integer()
		}

		if totalQuota <= 0 {
			util.SendResponse(http.StatusForbidden, "ai-quota.noquota", "text/plain", "Request denied by ai quota check, No quota available")
			return
		}

		// Then get used quota
		config.redisClient.Get(usedKey, func(usedResponse resp.Value) {
			usedQuota := 0
			if err := usedResponse.Error(); err == nil && !usedResponse.IsNull() {
				usedQuota = usedResponse.Integer()
			}

			remainingQuota := totalQuota - usedQuota
			log.Debugf("get userId:%s totalQuota:%d usedQuota:%d remainingQuota:%d", userInfo.ID, totalQuota, usedQuota, remainingQuota)

			if remainingQuota <= 0 {
				util.SendResponse(http.StatusForbidden, "ai-quota.noquota", "text/plain", "Request denied by ai quota check, No quota left")
				return
			}

			// Check if we need to deduct quota based on header
			deductHeaderValue, err := proxywasm.GetHttpRequestHeader(config.DeductHeader)
			if err == nil && deductHeaderValue == config.DeductHeaderValue {
				// Increment used quota by 1
				config.redisClient.IncrBy(usedKey, 1, nil)
			}

			proxywasm.ResumeHttpRequest()
		})
	})
	return types.HeaderStopAllIterationAndWatermark
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
	if chatMode == ChatModeNone || chatMode == ChatModeCompletion {
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

	return types.ActionContinue
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
		util.SendResponse(http.StatusBadRequest, "ai-quota.invalid_params", "text/plain", "Request denied by ai quota check. user_id can't be empty and quota must be integer.")
		return types.ActionContinue
	}
	err2 := config.redisClient.Set(config.RedisKeyPrefix+userId, quota, func(response resp.Value) {
		log.Debugf("Redis set key = %s quota = %d", config.RedisKeyPrefix+userId, quota)
		if err := response.Error(); err != nil {
			util.SendResponse(http.StatusServiceUnavailable, "ai-quota.error", "text/plain", fmt.Sprintf("redis error:%v", err))
			return
		}
		util.SendResponse(http.StatusOK, "ai-quota.refreshquota", "text/plain", "refresh quota successful")
	})

	if err2 != nil {
		util.SendResponse(http.StatusServiceUnavailable, "ai-quota.error", "text/plain", fmt.Sprintf("redis error:%v", err))
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
		util.SendResponse(http.StatusBadRequest, "ai-quota.invalid_params", "text/plain", "Request denied by ai quota check. user_id can't be empty.")
		return types.ActionContinue
	}
	userId := values["user_id"]

	// Determine which key to use based on admin mode
	var redisKey string
	var responseType string
	if adminMode == AdminModeUsedQuery {
		redisKey = config.RedisUsedPrefix + userId
		responseType = "used_quota"
	} else {
		redisKey = config.RedisKeyPrefix + userId
		responseType = "total_quota"
	}

	err := config.redisClient.Get(redisKey, func(response resp.Value) {
		quota := 0
		if err := response.Error(); err != nil {
			util.SendResponse(http.StatusServiceUnavailable, "ai-quota.error", "text/plain", fmt.Sprintf("redis error:%v", err))
			return
		} else if response.IsNull() {
			quota = 0
		} else {
			quota = response.Integer()
		}
		result := struct {
			UserID string `json:"user_id"`
			Quota  int    `json:"quota"`
			Type   string `json:"type"`
		}{
			UserID: userId,
			Quota:  quota,
			Type:   responseType,
		}
		body, _ := json.Marshal(result)
		util.SendResponse(http.StatusOK, "ai-quota.queryquota", "application/json", string(body))
	})
	if err != nil {
		util.SendResponse(http.StatusServiceUnavailable, "ai-quota.error", "text/plain", fmt.Sprintf("redis error:%v", err))
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
		util.SendResponse(http.StatusBadRequest, "ai-quota.invalid_params", "text/plain", "Request denied by ai quota check. user_id can't be empty and value must be integer.")
		return types.ActionContinue
	}

	if value >= 0 {
		err := config.redisClient.IncrBy(config.RedisKeyPrefix+userId, value, func(response resp.Value) {
			log.Debugf("Redis Incr key = %s value = %d", config.RedisKeyPrefix+userId, value)
			if err := response.Error(); err != nil {
				util.SendResponse(http.StatusServiceUnavailable, "ai-quota.error", "text/plain", fmt.Sprintf("redis error:%v", err))
				return
			}
			util.SendResponse(http.StatusOK, "ai-quota.deltaquota", "text/plain", "delta quota successful")
		})
		if err != nil {
			util.SendResponse(http.StatusServiceUnavailable, "ai-quota.error", "text/plain", fmt.Sprintf("redis error:%v", err))
			return types.ActionContinue
		}
	} else {
		err := config.redisClient.DecrBy(config.RedisKeyPrefix+userId, 0-value, func(response resp.Value) {
			log.Debugf("Redis Decr key = %s value = %d", config.RedisKeyPrefix+userId, 0-value)
			if err := response.Error(); err != nil {
				util.SendResponse(http.StatusServiceUnavailable, "ai-quota.error", "text/plain", fmt.Sprintf("redis error:%v", err))
				return
			}
			util.SendResponse(http.StatusOK, "ai-quota.deltaquota", "text/plain", "delta quota successful")
		})
		if err != nil {
			util.SendResponse(http.StatusServiceUnavailable, "ai-quota.error", "text/plain", fmt.Sprintf("redis error:%v", err))
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
		util.SendResponse(http.StatusBadRequest, "ai-quota.invalid_params", "text/plain", "Request denied by ai quota check. user_id can't be empty and quota must be integer.")
		return types.ActionContinue
	}
	err2 := config.redisClient.Set(config.RedisUsedPrefix+userId, quota, func(response resp.Value) {
		log.Debugf("Redis set key = %s quota = %d", config.RedisUsedPrefix+userId, quota)
		if err := response.Error(); err != nil {
			util.SendResponse(http.StatusServiceUnavailable, "ai-quota.error", "text/plain", fmt.Sprintf("redis error:%v", err))
			return
		}
		util.SendResponse(http.StatusOK, "ai-quota.refreshusedquota", "text/plain", "refresh used quota successful")
	})

	if err2 != nil {
		util.SendResponse(http.StatusServiceUnavailable, "ai-quota.error", "text/plain", fmt.Sprintf("redis error:%v", err))
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
		util.SendResponse(http.StatusBadRequest, "ai-quota.invalid_params", "text/plain", "Request denied by ai quota check. user_id can't be empty and value must be integer.")
		return types.ActionContinue
	}

	if value >= 0 {
		err := config.redisClient.IncrBy(config.RedisUsedPrefix+userId, value, func(response resp.Value) {
			log.Debugf("Redis Incr key = %s value = %d", config.RedisUsedPrefix+userId, value)
			if err := response.Error(); err != nil {
				util.SendResponse(http.StatusServiceUnavailable, "ai-quota.error", "text/plain", fmt.Sprintf("redis error:%v", err))
				return
			}
			util.SendResponse(http.StatusOK, "ai-quota.deltausedquota", "text/plain", "delta used quota successful")
		})
		if err != nil {
			util.SendResponse(http.StatusServiceUnavailable, "ai-quota.error", "text/plain", fmt.Sprintf("redis error:%v", err))
			return types.ActionContinue
		}
	} else {
		err := config.redisClient.DecrBy(config.RedisUsedPrefix+userId, 0-value, func(response resp.Value) {
			log.Debugf("Redis Decr key = %s value = %d", config.RedisUsedPrefix+userId, 0-value)
			if err := response.Error(); err != nil {
				util.SendResponse(http.StatusServiceUnavailable, "ai-quota.error", "text/plain", fmt.Sprintf("redis error:%v", err))
				return
			}
			util.SendResponse(http.StatusOK, "ai-quota.deltausedquota", "text/plain", "delta used quota successful")
		})
		if err != nil {
			util.SendResponse(http.StatusServiceUnavailable, "ai-quota.error", "text/plain", fmt.Sprintf("redis error:%v", err))
			return types.ActionContinue
		}
	}

	return types.ActionPause
}
