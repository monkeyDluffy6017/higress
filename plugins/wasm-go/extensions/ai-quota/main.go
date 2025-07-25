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

	"sync"

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
	AdminModeNone           AdminMode = "none"
	AdminModeRefresh        AdminMode = "refresh"
	AdminModeDelta          AdminMode = "delta"
	AdminModeQuery          AdminMode = "query"
	AdminModeUsedRefresh    AdminMode = "used_refresh"
	AdminModeUsedDelta      AdminMode = "used_delta"
	AdminModeUsedQuery      AdminMode = "used_query"
	AdminModeStarSet        AdminMode = "star_set"
	AdminModeStarQuery      AdminMode = "star_query"
	AdminModePermSet        AdminMode = "permission_set"
	AdminModePermQuery      AdminMode = "permission_query"
	AdminModeStargazerSet   AdminMode = "stargazer_set"
	AdminModeStargazerQuery AdminMode = "stargazer_query"
	AdminModeQuotaSet       AdminMode = "quota_set"
	AdminModeQuotaQuery     AdminMode = "quota_query"
)

// AuthUser struct for parsing user info from JWT
type AuthUser struct {
	ID             string `json:"universal_id"`
	EmployeeNumber string `json:"id"`
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

// ProviderConfig contains provider type and model list configuration
type ProviderConfig struct {
	Id     string   `yaml:"id"`     // Provider ID for identification
	Type   string   `yaml:"type"`   // Provider type (openai, qwen, claude, etc.)
	Models []string `yaml:"models"` // List of supported model IDs
}

// GetId returns the provider ID
func (c *ProviderConfig) GetId() string {
	return c.Id
}

// GetType returns the provider type
func (c *ProviderConfig) GetType() string {
	return c.Type
}

// GetModelList returns the list of models available for this provider
func (c *ProviderConfig) GetModelList() ([]ModelInfo, error) {
	var models []ModelInfo
	owner := c.getOwnerByProviderType()

	// Use Models array to build model list
	for _, modelId := range c.Models {
		if modelId == "" {
			continue // Skip empty model IDs
		}
		models = append(models, ModelInfo{
			Id:      modelId,
			Object:  "model",
			Created: 1686935002,
			OwnedBy: owner,
		})
	}

	return models, nil
}

// getOwnerByProviderType returns the owner name based on provider type
func (c *ProviderConfig) getOwnerByProviderType() string {
	switch c.Type {
	case ProviderTypeOpenAI:
		return "openai"
	case ProviderTypeAzure:
		return "microsoft"
	case ProviderTypeQwen:
		return "alibaba"
	case ProviderTypeMoonshot:
		return "moonshot"
	case ProviderTypeClaude:
		return "anthropic"
	case ProviderTypeGemini:
		return "google"
	default:
		return "unknown"
	}
}

// BuildModelsResponse creates a models list response for a single provider
func (c *ProviderConfig) BuildModelsResponse() ([]byte, error) {
	models, err := c.GetModelList()
	if err != nil {
		return nil, err
	}

	response := ModelsResponse{
		Object: "list",
		Data:   models,
	}

	return json.Marshal(response)
}

type QuotaManagementConfig struct {
	UserLevelEnabled  bool               `yaml:"user_level_enabled"`
	DeductHeader      string             `yaml:"deduct_header"`
	DeductHeaderValue string             `yaml:"deduct_header_value"`
	RedisKeyPrefix    string             `yaml:"redis_key_prefix"`
	RedisUsedPrefix   string             `yaml:"redis_used_prefix"`
	AdminQuotaPath    string             `yaml:"admin_quota_path"`
	RedisQuotaPrefix  string             `yaml:"redis_quota_prefix"`
	ModelQuotaWeights map[string]float64 `yaml:"model_quota_weights"`
	CacheTTLSeconds   int                `yaml:"cache_ttl_seconds"` // Cache TTL in seconds, default 60s
}

type QuotaConfig struct {
	redisInfo           RedisInfo                 `yaml:"redis"`
	StarCheckManagement StarCheckManagementConfig `yaml:"star_check_management"`
	TokenHeader         string                    `yaml:"token_header"`
	AdminHeader         string                    `yaml:"admin_header"`
	AdminKey            string                    `yaml:"admin_key"`
	AdminPath           string                    `yaml:"admin_path"`

	// Nested quota management configuration
	QuotaManagement QuotaManagementConfig `yaml:"quota_management"`

	// Provider configuration for /ai-gateway/api/v1/models endpoint
	Provider  ProviderConfig   `yaml:"provider"`  // Single provider configuration (legacy support)
	Providers []ProviderConfig `yaml:"providers"` // Multi-provider configuration (new format)

	// Permission management configuration
	RestrictedModels     []string                   `yaml:"restricted_models"`
	PermissionManagement PermissionManagementConfig `yaml:"permission_management"`

	redisClient       wrapper.RedisClient `yaml:"-"`
	starCacheManager  *StarCacheManager   `yaml:"-"` // Manager for star projects cache
	providerConfigs   []ProviderConfig    `yaml:"-"` // All configured providers for models endpoint
	permissionChecker *PermissionChecker  `yaml:"-"` // Permission checker instance
	starCheckChecker  *StarCheckChecker   `yaml:"-"` // Star check permission checker instance
	quotaChecker      *QuotaChecker       `yaml:"-"` // Quota permission checker instance
}

// StarCheckManagementConfig configuration for star check management
type StarCheckManagementConfig struct {
	Enabled              bool   `yaml:"enabled"`
	UserLevelEnabled     bool   `yaml:"user_level_enabled"`
	RedisStarPrefix      string `yaml:"redis_star_prefix"`
	AdminStargazerPath   string `yaml:"admin_stargazer_path"`
	RedisStargazerPrefix string `yaml:"redis_stargazer_prefix"`
	TargetRepo           string `yaml:"target_repo"`
	CacheTTLSeconds      int    `yaml:"cache_ttl_seconds"` // Cache TTL in seconds for StarCache, default 60s
}

// PermissionManagementConfig configuration for permission management
type PermissionManagementConfig struct {
	RedisPermissionPrefix string `yaml:"redis_permission_prefix"`
	AdminPermissionPath   string `yaml:"admin_permission_path"`
	CacheTTLSeconds       int    `yaml:"cache_ttl_seconds"` // Cache TTL in seconds for StarCache, default 60s
}

// PermissionChecker handles model permission checking
type PermissionChecker struct {
	restrictedModels []string
	memoryCache      map[string][]string // employee_number -> allowed_models
	cacheExpireTime  map[string]int64    // employee_number -> expire_timestamp
	mu               sync.RWMutex
	redisClient      wrapper.RedisClient
	redisPermPrefix  string
	cacheTTLSeconds  int64 // Cache TTL in seconds
}

// NewPermissionChecker creates a new permission checker
func NewPermissionChecker(restrictedModels []string, redisClient wrapper.RedisClient, redisPermPrefix string, cacheTTLSeconds int) *PermissionChecker {
	if cacheTTLSeconds <= 0 {
		cacheTTLSeconds = 60 // Default 60 seconds
	}
	return &PermissionChecker{
		restrictedModels: restrictedModels,
		memoryCache:      make(map[string][]string),
		cacheExpireTime:  make(map[string]int64),
		redisClient:      redisClient,
		redisPermPrefix:  redisPermPrefix,
		cacheTTLSeconds:  int64(cacheTTLSeconds),
	}
}

// CheckModelPermission checks if a user has permission to access a model (async)
func (p *PermissionChecker) CheckModelPermission(employeeNumber, modelName string, log wrapper.Log, callback func(bool)) {
	log.Debugf("[PermissionChecker.CheckModelPermission] Checking permission for employee: %s, model: %s", employeeNumber, modelName)

	// 1. Check if model is restricted
	if !p.isRestrictedModel(modelName, log) {
		log.Debugf("[PermissionChecker.CheckModelPermission] Model %s is not restricted, allowing access", modelName)
		callback(true) // Not restricted, allow access
		return
	}

	log.Debugf("[PermissionChecker.CheckModelPermission] Model %s is restricted, checking user permissions", modelName)

	// 2. Get user's allowed models (async)
	p.getUserAllowedModels(employeeNumber, func(allowedModels []string) {
		log.Debugf("[PermissionChecker.CheckModelPermission] Employee %s allowed models: %v", employeeNumber, allowedModels)

		// 3. Check if model is allowed
		isAllowed := p.isModelAllowed(modelName, allowedModels, log)
		log.Debugf("[PermissionChecker.CheckModelPermission] Final permission result for employee %s and model %s: %t", employeeNumber, modelName, isAllowed)

		callback(isAllowed)
	})
}

// isRestrictedModel checks if a model is in the restricted list
func (p *PermissionChecker) isRestrictedModel(modelName string, log wrapper.Log) bool {
	log.Debugf("[PermissionChecker.isRestrictedModel] Checking model: %s against restricted list: %v", modelName, p.restrictedModels)

	for _, restricted := range p.restrictedModels {
		if modelName == restricted {
			log.Debugf("[PermissionChecker.isRestrictedModel] Model %s IS RESTRICTED", modelName)
			return true
		}
	}

	log.Debugf("[PermissionChecker.isRestrictedModel] Model %s is NOT RESTRICTED", modelName)
	return false
}

// isModelAllowed checks if a model is in the allowed list
func (p *PermissionChecker) isModelAllowed(modelName string, allowedModels []string, log wrapper.Log) bool {
	log.Debugf("[PermissionChecker.isModelAllowed] Checking if model %s is in allowed list: %v", modelName, allowedModels)

	for _, allowed := range allowedModels {
		if modelName == allowed {
			log.Debugf("[PermissionChecker.isModelAllowed] Model %s IS ALLOWED", modelName)
			return true
		}
	}

	log.Debugf("[PermissionChecker.isModelAllowed] Model %s is NOT in allowed list", modelName)
	return false
}

// getUserAllowedModels gets user's allowed models with callback
func (p *PermissionChecker) getUserAllowedModels(employeeNumber string, callback func([]string)) {
	now := time.Now().Unix()

	// Check if cache exists and is not expired
	p.mu.RLock()
	cachedModels, hasCache := p.memoryCache[employeeNumber]
	expireTime, hasExpireTime := p.cacheExpireTime[employeeNumber]
	p.mu.RUnlock()

	// If cache exists and not expired, use it
	if hasCache && hasExpireTime && now < expireTime {
		callback(cachedModels)
		return
	}

	// Cache expired or missing, get from Redis
	p.getFromRedis(employeeNumber, func(loadedModels []string) {
		// Update cache with new expiration time
		newExpireTime := now + p.cacheTTLSeconds
		p.updateMemoryCacheWithTTL(employeeNumber, loadedModels, newExpireTime)
		callback(loadedModels)
	})
}

// getFromRedis gets allowed models from Redis (async, non-blocking)
func (p *PermissionChecker) getFromRedis(employeeNumber string, callback func([]string)) {
	key := p.redisPermPrefix + employeeNumber

	// Start async Redis operation without blocking
	p.redisClient.Get(key, func(response resp.Value) {
		if err := response.Error(); err != nil {
			// Redis error, keep cache empty
			callback(nil) // Call callback with nil on error
			return
		}

		if !response.IsNull() {
			data := response.String()
			var loadedModels []string
			if json.Unmarshal([]byte(data), &loadedModels) == nil {
				callback(loadedModels) // Call callback with loaded data
			} else {
				callback(nil) // Call callback with nil on parse error
			}
		} else {
			callback(nil) // Call callback with nil if key not found
		}
	})
}

// updateMemoryCache updates the memory cache
func (p *PermissionChecker) updateMemoryCacheWithTTL(employeeNumber string, models []string, expireTime int64) {
	p.mu.Lock()
	p.memoryCache[employeeNumber] = models
	p.cacheExpireTime[employeeNumber] = expireTime
	p.mu.Unlock()
}

// deleteMemoryCache removes the cache entry for a specific employee
func (p *PermissionChecker) deleteMemoryCache(employeeNumber string) {
	p.mu.Lock()
	delete(p.memoryCache, employeeNumber)
	delete(p.cacheExpireTime, employeeNumber)
	p.mu.Unlock()
}

// SetUserPermission sets user's allowed models in Redis and cache
func (p *PermissionChecker) SetUserPermission(employeeNumber string, models []string, callback func(error)) {
	// Clear memory cache first to force Redis reads on all instances
	p.deleteMemoryCache(employeeNumber)

	// Update Redis
	key := p.redisPermPrefix + employeeNumber
	data, _ := json.Marshal(models)

	p.redisClient.Set(key, string(data), func(response resp.Value) {
		// Call the callback with the result
		if callback != nil {
			callback(response.Error())
		}
	})
}

// StarCacheManager manages the star projects cache
type StarCacheManager struct {
	memoryCache     map[string][]string // employee_number -> starred projects
	cacheExpireTime map[string]int64    // employee_number -> expire_timestamp
	mu              sync.RWMutex
	redisClient     wrapper.RedisClient
	redisStarPrefix string
	cacheTTLSeconds int64 // Cache TTL in seconds
}

func NewStarCacheManager(redisClient wrapper.RedisClient, redisStarPrefix string, cacheTTLSeconds int) *StarCacheManager {
	if cacheTTLSeconds <= 0 {
		cacheTTLSeconds = 60 // Default 60 seconds
	}
	return &StarCacheManager{
		memoryCache:     make(map[string][]string),
		cacheExpireTime: make(map[string]int64),
		redisClient:     redisClient,
		redisStarPrefix: redisStarPrefix,
		cacheTTLSeconds: int64(cacheTTLSeconds),
	}
}

func (s *StarCacheManager) CheckStarredProjects(userId string, log wrapper.Log, callback func([]string, error)) {
	now := time.Now().Unix()

	// Check if cache exists and is not expired
	s.mu.RLock()
	cachedProjects, hasCache := s.memoryCache[userId]
	expireTime, hasExpireTime := s.cacheExpireTime[userId]
	s.mu.RUnlock()

	// If cache exists and not expired, use it
	if hasCache && hasExpireTime && now < expireTime {
		log.Debugf("Starred projects found in valid cache for employee %s: %v (expires in %d seconds)",
			userId, cachedProjects, expireTime-now)
		callback(cachedProjects, nil)
		return
	}

	// Cache expired or missing, get from Redis
	log.Debugf("Starred projects cache expired or missing for employee %s, fetching from Redis", userId)
	s.getFromRedis(userId, func(projects []string, err error) {
		if err == nil {
			// Update cache with new expiration time only on success
			newExpireTime := now + s.cacheTTLSeconds
			s.updateMemoryCacheWithTTL(userId, projects, newExpireTime)
			log.Debugf("Fetched starred projects for employee %s from Redis: %v", userId, projects)
		}
		callback(projects, err)
	})
}

func (s *StarCacheManager) updateMemoryCacheWithTTL(userId string, projects []string, expireTime int64) {
	s.mu.Lock()
	s.memoryCache[userId] = projects
	s.cacheExpireTime[userId] = expireTime
	s.mu.Unlock()
}

func (s *StarCacheManager) getFromRedis(userId string, callback func([]string, error)) {
	redisKey := s.redisStarPrefix + userId
	s.redisClient.Get(redisKey, func(response resp.Value) {
		var projects []string // Default to empty slice

		// Check for Redis errors
		if err := response.Error(); err != nil {
			callback(nil, err)
			return
		}

		// Parse starred projects from comma-separated format
		if !response.IsNull() {
			data := response.String()
			if data != "" {
				// Parse comma-separated project list
				projects = strings.Split(data, ",")
				for i, project := range projects {
					projects[i] = strings.TrimSpace(project)
				}
			}
		}

		callback(projects, nil)
	})
}

// deleteMemoryCache removes the cache entry for a specific employee
func (s *StarCacheManager) deleteMemoryCache(userId string) {
	s.mu.Lock()
	delete(s.memoryCache, userId)
	delete(s.cacheExpireTime, userId)
	s.mu.Unlock()
}

func (s *StarCacheManager) SetStarredProjects(userId string, projects []string, callback func(error)) {
	// Clear memory cache first to force Redis reads on all instances
	s.deleteMemoryCache(userId)

	// Save to Redis as comma-separated string (not JSON)
	redisKey := s.redisStarPrefix + userId
	projectsStr := strings.Join(projects, ",")

	s.redisClient.Set(redisKey, projectsStr, func(response resp.Value) {
		if err := response.Error(); err != nil {
			callback(err)
			return
		}
		callback(nil)
	})
}

// StarCheckChecker manages user-level star check permissions
type StarCheckChecker struct {
	memoryCache     map[string]bool  // employee_number -> star_check_enabled
	cacheExpireTime map[string]int64 // employee_number -> expire_timestamp
	mu              sync.RWMutex
	redisClient     wrapper.RedisClient
	redisStarPrefix string
	cacheTTLSeconds int64 // Cache TTL in seconds
}

func NewStarCheckChecker(redisClient wrapper.RedisClient, redisStarPrefix string, cacheTTLSeconds int) *StarCheckChecker {
	if cacheTTLSeconds <= 0 {
		cacheTTLSeconds = 60 // Default 60 seconds
	}
	return &StarCheckChecker{
		memoryCache:     make(map[string]bool),
		cacheExpireTime: make(map[string]int64),
		redisClient:     redisClient,
		redisStarPrefix: redisStarPrefix,
		cacheTTLSeconds: int64(cacheTTLSeconds),
	}
}

func (s *StarCheckChecker) CheckStarCheckPermission(employeeNumber string, log wrapper.Log, callback func(bool)) {
	now := time.Now().Unix()

	// Check if cache exists and is not expired
	s.mu.RLock()
	cachedValue, hasCache := s.memoryCache[employeeNumber]
	expireTime, hasExpireTime := s.cacheExpireTime[employeeNumber]
	s.mu.RUnlock()

	// If cache exists and not expired, use it
	if hasCache && hasExpireTime && now < expireTime {
		log.Debugf("Star check permission found in valid cache for employee %s: %t (expires in %d seconds)",
			employeeNumber, cachedValue, expireTime-now)
		callback(cachedValue)
		return
	}

	// Cache expired or missing, get from Redis
	log.Debugf("Star check cache expired or missing for employee %s, fetching from Redis", employeeNumber)
	s.getFromRedis(employeeNumber, func(enabled bool) {
		// Update cache with new expiration time
		newExpireTime := now + s.cacheTTLSeconds
		s.updateMemoryCacheWithTTL(employeeNumber, enabled, newExpireTime)
		callback(enabled)
	})
}

func (s *StarCheckChecker) updateMemoryCacheWithTTL(employeeNumber string, enabled bool, expireTime int64) {
	s.mu.Lock()
	s.memoryCache[employeeNumber] = enabled
	s.cacheExpireTime[employeeNumber] = expireTime
	s.mu.Unlock()
}

func (s *StarCheckChecker) getFromRedis(employeeNumber string, callback func(bool)) {
	redisKey := s.redisStarPrefix + employeeNumber
	s.redisClient.Get(redisKey, func(response resp.Value) {
		enabled := false // Default to false (disabled)

		if err := response.Error(); err == nil && !response.IsNull() {
			// Parse permission value
			permissionStr := response.String()
			if permissionStr == "true" || permissionStr == "1" {
				enabled = true
			}
		}

		callback(enabled)
	})
}

// deleteMemoryCache removes the cache entry for a specific employee
func (s *StarCheckChecker) deleteMemoryCache(employeeNumber string) {
	s.mu.Lock()
	delete(s.memoryCache, employeeNumber)
	delete(s.cacheExpireTime, employeeNumber)
	s.mu.Unlock()
}

func (s *StarCheckChecker) SetStarCheckPermission(employeeNumber string, enabled bool, callback func(error)) {
	// Clear memory cache first to force Redis reads on all instances
	s.deleteMemoryCache(employeeNumber)

	// Save to Redis
	redisKey := s.redisStarPrefix + employeeNumber
	var redisValue string
	if enabled {
		redisValue = "true"
	} else {
		redisValue = "false"
	}

	s.redisClient.Set(redisKey, redisValue, func(response resp.Value) {
		if err := response.Error(); err != nil {
			callback(err)
			return
		}
		callback(nil)
	})
}

// QuotaChecker manages user-level quota control permissions
type QuotaChecker struct {
	memoryCache      map[string]bool  // employee_number -> quota_enabled
	cacheExpireTime  map[string]int64 // employee_number -> expire_timestamp
	mu               sync.RWMutex
	redisClient      wrapper.RedisClient
	redisQuotaPrefix string
	cacheTTLSeconds  int64 // Cache TTL in seconds
}

func NewQuotaChecker(redisClient wrapper.RedisClient, redisQuotaPrefix string, cacheTTLSeconds int) *QuotaChecker {
	if cacheTTLSeconds <= 0 {
		cacheTTLSeconds = 60 // Default 60 seconds
	}
	return &QuotaChecker{
		memoryCache:      make(map[string]bool),
		cacheExpireTime:  make(map[string]int64),
		redisClient:      redisClient,
		redisQuotaPrefix: redisQuotaPrefix,
		cacheTTLSeconds:  int64(cacheTTLSeconds),
	}
}

func (q *QuotaChecker) CheckQuotaPermission(employeeNumber string, log wrapper.Log, callback func(bool)) {
	now := time.Now().Unix()

	// Check if cache exists and is not expired
	q.mu.RLock()
	cachedValue, hasCache := q.memoryCache[employeeNumber]
	expireTime, hasExpireTime := q.cacheExpireTime[employeeNumber]
	q.mu.RUnlock()

	// If cache exists and not expired, use it
	if hasCache && hasExpireTime && now < expireTime {
		log.Debugf("Quota control permission found in valid cache for employee %s: %t (expires in %d seconds)",
			employeeNumber, cachedValue, expireTime-now)
		callback(cachedValue)
		return
	}

	// Cache expired or missing, get from Redis
	log.Debugf("Cache expired or missing for employee %s, fetching from Redis", employeeNumber)
	q.getFromRedis(employeeNumber, func(enabled bool) {
		// Update cache with new expiration time
		newExpireTime := now + q.cacheTTLSeconds
		q.updateMemoryCacheWithTTL(employeeNumber, enabled, newExpireTime)
		callback(enabled)
	})
}

func (q *QuotaChecker) updateMemoryCacheWithTTL(employeeNumber string, enabled bool, expireTime int64) {
	q.mu.Lock()
	q.memoryCache[employeeNumber] = enabled
	q.cacheExpireTime[employeeNumber] = expireTime
	q.mu.Unlock()
}

func (q *QuotaChecker) getFromRedis(employeeNumber string, callback func(bool)) {
	redisKey := q.redisQuotaPrefix + employeeNumber
	q.redisClient.Get(redisKey, func(response resp.Value) {
		enabled := false // Default to false (disabled quota control)

		if err := response.Error(); err == nil && !response.IsNull() {
			// Parse permission value
			permissionStr := response.String()
			if permissionStr == "true" || permissionStr == "1" {
				enabled = true
			}
		}

		callback(enabled)
	})
}

// deleteMemoryCache removes the cache entry for a specific employee
func (q *QuotaChecker) deleteMemoryCache(employeeNumber string) {
	q.mu.Lock()
	delete(q.memoryCache, employeeNumber)
	delete(q.cacheExpireTime, employeeNumber)
	q.mu.Unlock()
}

func (q *QuotaChecker) SetQuotaPermission(employeeNumber string, enabled bool, callback func(error)) {
	// Clear memory cache first to force Redis reads on all instances
	q.deleteMemoryCache(employeeNumber)

	// Save to Redis
	redisKey := q.redisQuotaPrefix + employeeNumber
	var redisValue string
	if enabled {
		redisValue = "true"
	} else {
		redisValue = "false"
	}

	q.redisClient.Set(redisKey, redisValue, func(response resp.Value) {
		if err := response.Error(); err != nil {
			callback(err)
			return
		}
		callback(nil)
	})
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

	// Parse quota management configuration
	quotaManagement := json.Get("quota_management")
	if !quotaManagement.Exists() {
		return errors.New("missing quota_management in config")
	}

	// Parse user level enabled setting
	config.QuotaManagement.UserLevelEnabled = quotaManagement.Get("user_level_enabled").Bool()

	// deduct header and value
	config.QuotaManagement.DeductHeader = quotaManagement.Get("deduct_header").String()
	if config.QuotaManagement.DeductHeader == "" {
		config.QuotaManagement.DeductHeader = "x-quota-identity"
	}

	config.QuotaManagement.DeductHeaderValue = quotaManagement.Get("deduct_header_value").String()
	if config.QuotaManagement.DeductHeaderValue == "" {
		config.QuotaManagement.DeductHeaderValue = "user"
	}

	config.QuotaManagement.RedisKeyPrefix = quotaManagement.Get("redis_key_prefix").String()
	if config.QuotaManagement.RedisKeyPrefix == "" {
		config.QuotaManagement.RedisKeyPrefix = "chat_quota:"
	}

	config.QuotaManagement.RedisUsedPrefix = quotaManagement.Get("redis_used_prefix").String()
	if config.QuotaManagement.RedisUsedPrefix == "" {
		config.QuotaManagement.RedisUsedPrefix = "chat_quota_used:"
	}

	config.QuotaManagement.AdminQuotaPath = quotaManagement.Get("admin_quota_path").String()
	if config.QuotaManagement.AdminQuotaPath == "" {
		config.QuotaManagement.AdminQuotaPath = "/check-quota"
	}

	config.QuotaManagement.RedisQuotaPrefix = quotaManagement.Get("redis_quota_prefix").String()
	if config.QuotaManagement.RedisQuotaPrefix == "" {
		config.QuotaManagement.RedisQuotaPrefix = "quota_check:"
	}

	// Parse model quota weights
	config.QuotaManagement.ModelQuotaWeights = make(map[string]float64)
	modelWeights := quotaManagement.Get("model_quota_weights")
	if modelWeights.Exists() {
		modelWeights.ForEach(func(key, value gjson.Result) bool {
			config.QuotaManagement.ModelQuotaWeights[key.String()] = value.Float()
			return true
		})
	}

	// Parse cache TTL seconds
	config.QuotaManagement.CacheTTLSeconds = int(quotaManagement.Get("cache_ttl_seconds").Int())
	if config.QuotaManagement.CacheTTLSeconds <= 0 {
		config.QuotaManagement.CacheTTLSeconds = 60 // Default 60 seconds
	}

	// Parse star check management configuration
	starCheckManagement := json.Get("star_check_management")
	config.StarCheckManagement.Enabled = starCheckManagement.Get("enabled").Bool()
	config.StarCheckManagement.UserLevelEnabled = starCheckManagement.Get("user_level_enabled").Bool()
	config.StarCheckManagement.RedisStarPrefix = starCheckManagement.Get("redis_star_prefix").String()
	config.StarCheckManagement.AdminStargazerPath = starCheckManagement.Get("admin_stargazer_path").String()
	config.StarCheckManagement.RedisStargazerPrefix = starCheckManagement.Get("redis_stargazer_prefix").String()
	config.StarCheckManagement.TargetRepo = starCheckManagement.Get("target_repo").String()

	// Parse cache TTL seconds for StarCache
	config.StarCheckManagement.CacheTTLSeconds = int(starCheckManagement.Get("cache_ttl_seconds").Int())
	if config.StarCheckManagement.CacheTTLSeconds <= 0 {
		config.StarCheckManagement.CacheTTLSeconds = 60 // Default 60 seconds
	}

	// Set default values if not specified
	if config.StarCheckManagement.RedisStarPrefix == "" {
		config.StarCheckManagement.RedisStarPrefix = "chat_quota_star:"
	}
	if config.StarCheckManagement.AdminStargazerPath == "" {
		config.StarCheckManagement.AdminStargazerPath = "/check-star"
	}
	if config.StarCheckManagement.RedisStargazerPrefix == "" {
		config.StarCheckManagement.RedisStargazerPrefix = "star_check:"
	}

	// Initialize star projects cache with expiration time management

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

	// Parse permission management configuration BEFORE provider configuration
	config.RestrictedModels = make([]string, 0)
	if restrictedModelsJson := json.Get("restricted_models"); restrictedModelsJson.Exists() && restrictedModelsJson.IsArray() {
		log.Debugf("[parseConfig] Found restricted_models in config")
		for _, modelJson := range restrictedModelsJson.Array() {
			modelName := modelJson.String()
			if modelName != "" {
				config.RestrictedModels = append(config.RestrictedModels, modelName)
				log.Debugf("[parseConfig] Added restricted model: %s", modelName)
			}
		}
	} else {
		log.Debugf("[parseConfig] No restricted_models found in config")
	}
	log.Debugf("[parseConfig] Final restricted models list: %v", config.RestrictedModels)

	// Parse permission management configuration
	permConfig := json.Get("permission_management")
	config.PermissionManagement.RedisPermissionPrefix = permConfig.Get("redis_permission_prefix").String()
	if config.PermissionManagement.RedisPermissionPrefix == "" {
		config.PermissionManagement.RedisPermissionPrefix = "model_perm:"
	}
	log.Debugf("[parseConfig] Redis permission prefix: %s", config.PermissionManagement.RedisPermissionPrefix)

	config.PermissionManagement.AdminPermissionPath = permConfig.Get("admin_permission_path").String()
	if config.PermissionManagement.AdminPermissionPath == "" {
		config.PermissionManagement.AdminPermissionPath = "/model-permission"
	}
	log.Debugf("[parseConfig] Admin permission path: %s", config.PermissionManagement.AdminPermissionPath)

	// Parse cache TTL seconds for StarCache
	config.PermissionManagement.CacheTTLSeconds = int(permConfig.Get("cache_ttl_seconds").Int())
	if config.PermissionManagement.CacheTTLSeconds <= 0 {
		config.PermissionManagement.CacheTTLSeconds = 60 // Default 60 seconds
	}

	// Initialize permission checker
	log.Debugf("[parseConfig] Initializing permission checker with restricted models: %v", config.RestrictedModels)
	config.permissionChecker = NewPermissionChecker(
		config.RestrictedModels,
		config.redisClient,
		config.PermissionManagement.RedisPermissionPrefix,
		config.PermissionManagement.CacheTTLSeconds,
	)
	log.Debugf("[parseConfig] Permission checker initialized successfully: %t", config.permissionChecker != nil)

	config.starCacheManager = NewStarCacheManager(
		config.redisClient,
		config.StarCheckManagement.RedisStarPrefix,
		config.StarCheckManagement.CacheTTLSeconds,
	)

	// Initialize star check permission checker if user level is enabled
	if config.StarCheckManagement.UserLevelEnabled {
		log.Debugf("[parseConfig] Initializing star check permission checker")
		config.starCheckChecker = NewStarCheckChecker(
			config.redisClient,
			config.StarCheckManagement.RedisStargazerPrefix,
			config.StarCheckManagement.CacheTTLSeconds,
		)
		log.Debugf("[parseConfig] Star check permission checker initialized successfully: %t", config.starCheckChecker != nil)
	}

	// Initialize quota permission checker if user level is enabled
	if config.QuotaManagement.UserLevelEnabled {
		log.Debugf("[parseConfig] Initializing quota permission checker")
		config.quotaChecker = NewQuotaChecker(
			config.redisClient,
			config.QuotaManagement.RedisQuotaPrefix,
			config.QuotaManagement.CacheTTLSeconds,
		)
		log.Debugf("[parseConfig] Quota permission checker initialized successfully: %t", config.quotaChecker != nil)
	}

	// Parse provider configuration - support both single provider and multi-provider modes
	// Process providers array configuration first
	if providersJson := json.Get("providers"); providersJson.Exists() && providersJson.IsArray() {
		config.Providers = make([]ProviderConfig, 0)
		for _, providerJson := range providersJson.Array() {
			providerConfig := ProviderConfig{}

			// Parse provider ID (required for multi-provider)
			providerConfig.Id = providerJson.Get("id").String()
			if providerConfig.Id == "" {
				log.Warnf("Provider ID is required for multi-provider configuration, skipping provider")
				continue
			}

			// Parse provider type
			providerType := providerJson.Get("type").String()
			if providerType == "" {
				providerType = ProviderTypeOpenAI // Default to OpenAI
			}
			providerConfig.Type = providerType

			// Parse models array (new format, preferred)
			if modelsJson := providerJson.Get("models"); modelsJson.Exists() && modelsJson.IsArray() {
				providerConfig.Models = make([]string, 0)
				for _, modelJson := range modelsJson.Array() {
					modelId := modelJson.String()
					if modelId != "" {
						providerConfig.Models = append(providerConfig.Models, modelId)
					}
				}
			}

			config.Providers = append(config.Providers, providerConfig)
		}

		// Reset legacy provider config for pure multi-provider mode
		config.Provider = ProviderConfig{}
		return config.redisClient.Init(username, password, int64(timeout), wrapper.WithDataBase(database))
	}

	// Process legacy single provider configuration
	if providerConfig := json.Get("provider"); providerConfig.Exists() && providerConfig.IsObject() {
		// Legacy single provider configuration
		config.Provider.Id = "default" // Set default ID for legacy mode

		// Parse provider type
		providerType := providerConfig.Get("type").String()
		if providerType == "" {
			providerType = ProviderTypeOpenAI // Default to OpenAI
		}
		config.Provider.Type = providerType

		// Parse models array (new format, preferred)
		if modelsJson := providerConfig.Get("models"); modelsJson.Exists() && modelsJson.IsArray() {
			config.Provider.Models = make([]string, 0)
			for _, modelJson := range modelsJson.Array() {
				modelId := modelJson.String()
				if modelId != "" {
					config.Provider.Models = append(config.Provider.Models, modelId)
				}
			}
		}

		// Clear multi-provider array for legacy mode
		config.Providers = nil
		return config.redisClient.Init(username, password, int64(timeout), wrapper.WithDataBase(database))
	}

	// If no provider configuration is found, set defaults
	config.Provider.Id = "default"
	config.Provider.Type = ProviderTypeOpenAI
	config.Provider.Models = make([]string, 0)

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

	var employeeNumber string
	if properties, ok := customClaims["properties"].(map[string]interface{}); ok {
		if customID, ok := properties["oauth_Custom_id"].(string); ok {
			employeeNumber = customID
		}
	}

	if employeeNumber == "" {
		if universalID, ok := customClaims["universal_id"].(string); ok {
			employeeNumber = universalID
		}
	}

	jsonBytes, err := json.Marshal(customClaims)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize user info: %w", err)
	}

	var userInfo AuthUser
	if err := json.Unmarshal(jsonBytes, &userInfo); err != nil {
		return nil, fmt.Errorf("failed to deserialize user info: %w", err)
	}

	userInfo.EmployeeNumber = employeeNumber

	return &userInfo, nil
}

func onHttpRequestHeaders(context wrapper.HttpContext, config QuotaConfig, log wrapper.Log) types.Action {
	log.Debugf("onHttpRequestHeaders()")

	rawPath := context.Path()
	path, _ := url.Parse(rawPath)

	// Handle /ai-gateway/api/v1/models request locally first
	if path.Path == "/ai-gateway/api/v1/models" {
		log.Debugf("[onHttpRequestHeaders] handling /ai-gateway/api/v1/models request locally")
		log.Debugf("[onHttpRequestHeaders] Restricted models config: %v", config.RestrictedModels)
		context.DontReadRequestBody()

		// Get user's allowed models if token exists
		var allowedModels []string
		tokenHeader, err := proxywasm.GetHttpRequestHeader(config.TokenHeader)
		log.Debugf("[onHttpRequestHeaders] Token header retrieval - err: %v, header present: %t", err, tokenHeader != "")

		if err == nil && tokenHeader != "" {
			// Extract token
			token := extractTokenFromHeader(tokenHeader)
			if token != "" {
				// Parse token to get employee number
				userInfo, err := parseUserInfoFromToken(token)
				log.Debugf("[onHttpRequestHeaders] Token parsing result - err: %v", err)

				if err == nil && userInfo.EmployeeNumber != "" {
					log.Debugf("[onHttpRequestHeaders] Employee number extracted: %s", userInfo.EmployeeNumber)

					// Get user's allowed models (only if permissionChecker is available)
					if config.permissionChecker != nil {
						config.permissionChecker.getUserAllowedModels(userInfo.EmployeeNumber, func(models []string) {
							allowedModels = models
							log.Debugf("[onHttpRequestHeaders] user %s has allowed models: %v", userInfo.ID, allowedModels)

							// Generate filtered models response with user permissions
							responseBody, err := config.BuildFilteredModelsResponse(allowedModels, log)
							if err != nil {
								log.Errorf("failed to build models response: %v", err)
								_ = sendJSONResponse(500, "ai-quota.build_models_failed", "Failed to build models response", false, nil)
								return
							}

							// Send HTTP response directly
							headers := [][2]string{
								{"content-type", "application/json"},
							}
							err = proxywasm.SendHttpResponse(200, headers, responseBody, -1)
							if err != nil {
								log.Errorf("failed to send response: %v", err)
								_ = sendJSONResponse(500, "ai-quota.send_models_response_failed", "Failed to send models response", false, nil)
								return
							}

							log.Debugf("[onHttpRequestHeaders] models response sent: %s", string(responseBody))
						})
						return types.ActionPause // Wait for async permission loading
					} else {
						log.Debugf("[onHttpRequestHeaders] permissionChecker is nil, no user-specific filtering")
					}
				} else {
					log.Debugf("[onHttpRequestHeaders] Failed to extract employee number - userInfo: %+v, err: %v", userInfo, err)
				}
			} else {
				log.Debugf("[onHttpRequestHeaders] Empty token after extraction")
			}
		} else {
			log.Debugf("[onHttpRequestHeaders] No token header found, proceeding without user-specific filtering")
		}

		// Generate filtered models response (without user-specific permissions if no token)
		responseBody, err := config.BuildFilteredModelsResponse(allowedModels, log)
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
		// query star check permission
		if adminMode == AdminModeStargazerQuery {
			return queryStarCheckPermission(context, config, path, log)
		}
		// query quota permission
		if adminMode == AdminModeQuotaQuery {
			return queryQuotaPermission(context, config, path, log)
		}
		if adminMode == AdminModeRefresh || adminMode == AdminModeDelta || adminMode == AdminModeUsedRefresh || adminMode == AdminModeUsedDelta || adminMode == AdminModeStarSet || adminMode == AdminModeStargazerSet || adminMode == AdminModeQuotaSet {
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

	if userInfo.EmployeeNumber == "" {
		sendJSONResponse(http.StatusUnauthorized, "ai-gateway.no_userid", "Request denied by ai quota check. No employee number found in token.", false, nil)
		return types.ActionContinue
	}

	// For star check, use universal_id; for quota operations, maintain compatibility using universal_id as fallback
	employeeNumber := userInfo.EmployeeNumber
	userId := userInfo.ID

	context.SetContext("userId", userId)
	context.SetContext("employeeNumber", employeeNumber)

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
	if adminMode == AdminModePermSet {
		return setUserPermission(ctx, config, string(body), log)
	}
	if adminMode == AdminModeStargazerSet {
		return setStarCheckPermission(ctx, config, string(body), log)
	}
	if adminMode == AdminModeQuotaSet {
		return setQuotaPermission(ctx, config, string(body), log)
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
	if config.StarCheckManagement.Enabled {
		log.Debugf("GitHub star check is enabled, checking star status for user: %s", userId)

		// Check if user-level control is enabled
		if config.StarCheckManagement.UserLevelEnabled {
			employeeNumber, ok := ctx.GetContext("employeeNumber").(string)
			if !ok {
				sendJSONResponse(http.StatusUnauthorized, "ai-gateway.no_userid", "Request denied by ai quota check. No user ID found.", false, nil)
				return types.ActionContinue
			}

			log.Debugf("User-level star check control is enabled, checking user permission for user: %s", userId)

			if config.starCheckChecker != nil {
				config.starCheckChecker.CheckStarCheckPermission(employeeNumber, log, func(userStarCheckEnabled bool) {
					if !userStarCheckEnabled {
						log.Debugf("User %s has star check disabled at user level, proceeding with quota check", userId)
						// User has star check disabled, skip star check and proceed with quota logic
						processQuotaLogic(ctx, config, body, userId, log)
						return
					}

					log.Debugf("User %s has star check enabled at user level, proceeding with star check", userId)
					// User has star check enabled, proceed with normal star check logic
					performStarCheck(ctx, config, body, userId, log)
				})
				return types.ActionPause
			} else {
				log.Warnf("Star check permission checker not initialized, falling back to global star check for user: %s", userId)
				// Fallback to global star check if checker is not available
				performStarCheck(ctx, config, body, userId, log)
				return types.ActionPause
			}
		} else {
			log.Debugf("User-level star check control is disabled, using global star check for user: %s", userId)
			// User-level control is disabled, use global star check
			performStarCheck(ctx, config, body, userId, log)
			return types.ActionPause
		}
	}

	// If GitHub star check is disabled, proceed directly with quota logic
	log.Debugf("GitHub star check is disabled, proceeding with quota check")
	return processQuotaLogic(ctx, config, body, userId, log)
}

func processQuotaLogic(ctx wrapper.HttpContext, config QuotaConfig, body []byte, userId string, log wrapper.Log) types.Action {
	log.Debugf("[processQuotaLogic] Starting quota logic for user: %s", userId)
	log.Debugf("[processQuotaLogic] Request body: %s", string(body))
	log.Debugf("[processQuotaLogic] Restricted models count: %d", len(config.RestrictedModels))
	log.Debugf("[processQuotaLogic] Restricted models: %v", config.RestrictedModels)
	log.Debugf("[processQuotaLogic] PermissionChecker available: %t", config.permissionChecker != nil)

	// Extract model from request body
	modelName := gjson.GetBytes(body, "model").String()
	log.Debugf("[processQuotaLogic] Extracted model name: %s", modelName)

	// Check model permission first
	if len(config.RestrictedModels) > 0 && config.permissionChecker != nil {
		log.Debugf("[processQuotaLogic] Starting permission check for model: %s", modelName)

		// Check if the model is in restricted list
		isRestricted := config.permissionChecker.isRestrictedModel(modelName, log)
		if isRestricted {
			// Get employee number from context (more efficient than re-parsing token)
			employeeNumber, ok := ctx.GetContext("employeeNumber").(string)
			if !ok || employeeNumber == "" {
				log.Warnf("[processQuotaLogic] Employee number not found in context for restricted model %s - BLOCKING REQUEST", modelName)
				sendJSONResponse(http.StatusUnauthorized, "ai-quota.no_employee_number",
					fmt.Sprintf("Valid employee information required to access restricted model %s", modelName), false, nil)
				return types.ActionContinue
			}

			log.Debugf("[processQuotaLogic] Employee number retrieved from context: %s", employeeNumber)

			// Check if user has permission to use this model
			config.permissionChecker.CheckModelPermission(employeeNumber, modelName, log, func(hasPermission bool) {
				if !hasPermission {
					log.Warnf("[processQuotaLogic] User %s does not have permission to use restricted model %s - BLOCKING REQUEST", userId, modelName)
					sendJSONResponse(http.StatusForbidden, "ai-quota.model_permission_denied",
						fmt.Sprintf("You don't have permission to use model %s", modelName), false, nil)
					return
				}

				// Permission check passed, continue with quota logic
				log.Debugf("[processQuotaLogic] Permission check passed for user %s and model %s", userId, modelName)
				continueWithQuotaLogic(ctx, config, body, userId, modelName, log)
			})
			return types.ActionPause // Wait for async permission check
		}
	} else {
		log.Debugf("[processQuotaLogic] Skipping permission check - restrictedModels: %d, permissionChecker: %t", len(config.RestrictedModels), config.permissionChecker != nil)
	}

	// Continue with quota logic
	continueWithQuotaLogic(ctx, config, body, userId, modelName, log)
	return types.ActionPause
}

func doQuotaCheck(ctx wrapper.HttpContext, config QuotaConfig, userId string, quotaWeight float64, modelName string, log wrapper.Log) {
	// Need to deduct quota: perform full quota check and deduction
	totalKey := config.QuotaManagement.RedisKeyPrefix + userId
	usedKey := config.QuotaManagement.RedisUsedPrefix + userId

	// Use enhanced error handling with retries for critical quota operations
	retryConfig := wrapper.RetryConfig{
		MaxRetries:    2, // Limit retries for latency-sensitive operations
		InitialDelay:  50 * time.Millisecond,
		MaxDelay:      500 * time.Millisecond,
		BackoffFactor: 2.0,
		EnableJitter:  true,
	}

	config.redisClient.Get(totalKey, func(totalResponse resp.Value) {
		handleTotalQuotaResponseWithRetry(ctx, config, usedKey, totalResponse, userId, quotaWeight, modelName, log, retryConfig)
	})
}

func handleTotalQuotaResponseWithRetry(ctx wrapper.HttpContext, config QuotaConfig, usedKey string, totalResponse resp.Value, userId string, quotaWeight float64, modelName string, log wrapper.Log, retryConfig wrapper.RetryConfig) {
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
	var totalQuota float64 = 0 // Default value for users without allocated quota

	if totalQuotaStr != "" {
		var parseErr error
		totalQuota, parseErr = strconv.ParseFloat(totalQuotaStr, 64)
		if parseErr != nil {
			log.Warnf("Invalid total quota format for user %s: %s", userId, totalQuotaStr)
			totalQuota = 0 // Default to 0 on parse error
		}

		// Validate that total quota is non-negative
		if totalQuota < 0 {
			log.Warnf("Invalid total quota value for user %s: %f (cannot be negative)", userId, totalQuota)
			totalQuota = 0 // Default to 0 on parse error
		}
	} else {
		log.Debugf("No quota data found for user %s in Redis, defaulting to 0", userId)
	}

	// Get used quota
	config.redisClient.Get(usedKey, func(usedResponse resp.Value) {
		handleUsedQuotaResponseWithRetry(ctx, config, usedResponse, userId, quotaWeight, modelName, totalQuota, log)
	})
}

func handleUsedQuotaResponseWithRetry(ctx wrapper.HttpContext, config QuotaConfig, usedResponse resp.Value, userId string, quotaWeight float64, modelName string, totalQuota float64, log wrapper.Log) {
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
	var usedQuota float64 = 0 // Default used quota to 0 if no data in Redis

	if usedQuotaStr != "" {
		var parseErr error
		usedQuota, parseErr = strconv.ParseFloat(usedQuotaStr, 64)
		if parseErr != nil {
			log.Warnf("Invalid used quota format for user %s: %s, defaulting to 0", userId, usedQuotaStr)
			usedQuota = 0 // Default to 0 on parse error
		}

		// Validate that used quota is non-negative
		if usedQuota < 0 {
			log.Warnf("Invalid used quota value for user %s: %f (cannot be negative)", userId, usedQuota)
			usedQuota = 0 // Default to 0 on parse error
		}

		// Additional sanity check: used quota shouldn't exceed total quota by a large margin
		// (Allow some tolerance for concurrent operations)
		if usedQuota > totalQuota+quotaWeight {
			log.Warnf("Used quota (%f) significantly exceeds total quota (%f) for user %s. This may indicate data inconsistency.",
				usedQuota, totalQuota, userId)
		}
	} else {
		log.Debugf("No used quota data found for user %s in Redis, defaulting to 0", userId)
	}

	// Calculate remaining quota
	remainingQuota := totalQuota - usedQuota

	// Log quota status for debugging
	log.Debugf("Quota status for user %s: total=%f, used=%f, remaining=%f, required=%f",
		userId, totalQuota, usedQuota, remainingQuota, quotaWeight)

	// Check if sufficient quota is available
	if remainingQuota >= quotaWeight {
		// Check if we need to deduct quota based on header
		deductHeaderValue, err := proxywasm.GetHttpRequestHeader(config.QuotaManagement.DeductHeader)
		shouldDeduct := err == nil && deductHeaderValue == config.QuotaManagement.DeductHeaderValue

		if shouldDeduct {
			// Use Redis native INCRBYFLOAT for atomic quota deduction
			usedKey := config.QuotaManagement.RedisUsedPrefix + userId
			config.redisClient.IncrByFloat(usedKey, quotaWeight, func(response resp.Value) {
				handleQuotaDeductionResponse(ctx, response, userId, quotaWeight, modelName, remainingQuota, log)
			})
		} else {
			// No quota deduction needed: allow request to continue without Redis queries
			log.Debugf("Quota deduction not required for user %s (header: %s != %s), allowing request",
				userId, deductHeaderValue, config.QuotaManagement.DeductHeaderValue)
			proxywasm.ResumeHttpRequest()
			return
		}
	} else {
		log.Warnf("Insufficient quota for user %s: remaining=%f, required=%f", userId, remainingQuota, quotaWeight)
		sendJSONResponse(http.StatusForbidden, "quota-check.insufficient_quota",
			fmt.Sprintf("Insufficient quota. Required: %f, Available: %f", quotaWeight, remainingQuota), false, nil)
	}
}

// incrementFloatValue is now deprecated - use handleQuotaDeductionResponse with redisClient.IncrByFloat directly
// This function is kept only for compatibility with admin operations (deltaQuota, deltaUsedQuota)
func incrementFloatValue(redisClient wrapper.RedisClient, key string, delta float64, callback func(float64, error)) {
	redisClient.IncrByFloat(key, delta, func(response resp.Value) {
		// Handle Redis errors
		if err := response.Error(); err != nil {
			callback(0, fmt.Errorf("redis incrbyfloat error: %w", err))
			return
		}

		// Parse the new value returned by INCRBYFLOAT
		newValue := response.Float()
		callback(newValue, nil)
	})
}

func handleQuotaDeductionResponse(ctx wrapper.HttpContext, incrResponse resp.Value, userId string, quotaWeight float64, modelName string, remainingQuota float64, log wrapper.Log) {
	if wrapper.IsRedisErrorResponse(incrResponse) {
		redisErr := wrapper.GetRedisErrorFromResponse(incrResponse)
		log.Errorf("Failed to deduct quota for user %s: %v", userId, redisErr)
		sendJSONResponse(http.StatusInternalServerError, "quota-check.deduction_failed",
			fmt.Sprintf("Quota deduction failed: %s", redisErr.Error()), false, nil)
		return
	}

	// Validate the response from Redis operation
	newUsedQuotaFloat := incrResponse.Float()

	// Sanity check: the new used quota should be reasonable
	if newUsedQuotaFloat < quotaWeight {
		log.Errorf("Unexpected used quota after deduction for user %s: got %f, expected at least %f",
			userId, newUsedQuotaFloat, quotaWeight)
		sendJSONResponse(http.StatusInternalServerError, "quota-check.deduction_inconsistent",
			"Quota deduction resulted in inconsistent state", false, nil)
		return
	}

	// Calculate what the previous used quota should have been
	expectedPreviousUsed := newUsedQuotaFloat - quotaWeight

	// Log quota deduction details for audit and debugging
	log.Infof("Successfully deducted %f quota for user %s, model %s. Previous used: %f, New used: %f",
		quotaWeight, userId, modelName, expectedPreviousUsed, newUsedQuotaFloat)

	// Additional debug information
	log.Debugf("Quota deduction details for user %s: deducted=%f, new_used=%f, expected_previous=%f",
		userId, quotaWeight, newUsedQuotaFloat, expectedPreviousUsed)

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
	if strings.HasSuffix(path, fullAdminPath+"/star/projects/set") {
		return ChatModeAdmin, AdminModeStarSet
	}
	if strings.HasSuffix(path, fullAdminPath+"/star/projects/query") {
		return ChatModeAdmin, AdminModeStarQuery
	}
	if strings.HasSuffix(path, fullAdminPath) {
		return ChatModeAdmin, AdminModeQuery
	}

	// Check for permission management path
	if strings.HasSuffix(path, "/model-permission/set") {
		return ChatModeAdmin, AdminModePermSet
	}
	if strings.HasSuffix(path, "/model-permission") {
		return ChatModeAdmin, AdminModePermQuery
	}

	// Check for star check permission management paths
	if strings.HasSuffix(path, "/check-star/set") {
		return ChatModeAdmin, AdminModeStargazerSet
	}
	if strings.HasSuffix(path, "/check-star") {
		return ChatModeAdmin, AdminModeStargazerQuery
	}

	// Check for quota permission management paths
	if strings.HasSuffix(path, "/check-quota/set") {
		return ChatModeAdmin, AdminModeQuotaSet
	}
	if strings.HasSuffix(path, "/check-quota") {
		return ChatModeAdmin, AdminModeQuotaQuery
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
	quota, err := strconv.ParseFloat(values["quota"], 64)
	if userId == "" || err != nil {
		sendJSONResponse(http.StatusBadRequest, "ai-gateway.invalid_params", "Request denied by ai quota check. user_id can't be empty and quota must be a valid number.", false, nil)
		return types.ActionContinue
	}
	err2 := config.redisClient.Set(config.QuotaManagement.RedisKeyPrefix+userId, fmt.Sprintf("%.6f", quota), func(response resp.Value) {
		log.Debugf("Redis set key = %s quota = %f", config.QuotaManagement.RedisKeyPrefix+userId, quota)
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

	// For star query, use employee_number; for other queries, keep backward compatibility with user_id
	var employeeNumber string
	if adminMode == AdminModeStarQuery {
		employeeNumber = values["employee_number"]
		if employeeNumber == "" {
			sendJSONResponse(http.StatusBadRequest, "ai-gateway.invalid_params", "Request denied by ai quota check. employee_number can't be empty.", false, nil)
			return types.ActionContinue
		}
	} else {
		// For quota queries, maintain backward compatibility with user_id
		employeeNumber = values["user_id"]
		if employeeNumber == "" {
			sendJSONResponse(http.StatusBadRequest, "ai-gateway.invalid_params", "Request denied by ai quota check. user_id can't be empty.", false, nil)
			return types.ActionContinue
		}
	}

	// Determine which key to use based on admin mode
	var redisKey string
	var responseType string
	if adminMode == AdminModeUsedQuery {
		redisKey = config.QuotaManagement.RedisUsedPrefix + employeeNumber
		responseType = "used_quota"
	} else if adminMode == AdminModeStarQuery {
		// Check cache first for star query
		cached, _ := config.checkStarCache(employeeNumber)
		if cached {
			// Get actual projects from cache
			config.starCacheManager.mu.RLock()
			projects := config.starCacheManager.memoryCache[employeeNumber]
			config.starCacheManager.mu.RUnlock()

			log.Debugf("Star projects found in cache for employee %s: %v", employeeNumber, projects)

			data := map[string]interface{}{
				"employee_number":  employeeNumber,
				"starred_projects": strings.Join(projects, ","), // Return as comma-separated string
				"type":             "star_status",
			}
			sendJSONResponse(http.StatusOK, "ai-gateway.querystar", "query star status successful (cached)", true, data)
			return types.ActionContinue
		}

		redisKey = config.StarCheckManagement.RedisStarPrefix + employeeNumber
		responseType = "star_status"
	} else {
		redisKey = config.QuotaManagement.RedisKeyPrefix + employeeNumber
		responseType = "total_quota"
	}

	err := config.redisClient.Get(redisKey, func(response resp.Value) {
		// Check for Redis errors first
		if wrapper.IsRedisErrorResponse(response) {
			redisErr := wrapper.GetRedisErrorFromResponse(response)
			log.Errorf("Failed to query %s for employee %s: %v", responseType, employeeNumber, redisErr)
			sendJSONResponse(http.StatusServiceUnavailable, "ai-gateway.redis_error",
				fmt.Sprintf("Redis error: %s", redisErr.Error()), false, nil)
			return
		}

		if adminMode == AdminModeStarQuery {
			// Handle star projects query (comma-separated string value)
			var starredProjects []string
			if !response.IsNull() {
				starredProjectsStr := response.String()
				if starredProjectsStr != "" {
					// Parse comma-separated project list
					starredProjects = strings.Split(starredProjectsStr, ",")
					for i, project := range starredProjects {
						starredProjects[i] = strings.TrimSpace(project)
					}
				}
			} else {
				log.Debugf("No starred projects found for employee %s (key does not exist)", employeeNumber)
			}

			// Cache the starred projects
			config.setStarCache(employeeNumber, starredProjects)
			log.Debugf("Cached starred projects from Redis for employee %s: %v", employeeNumber, starredProjects)

			data := map[string]interface{}{
				"employee_number":  employeeNumber,
				"starred_projects": strings.Join(starredProjects, ","), // Return as comma-separated string
				"type":             responseType,
			}
			sendJSONResponse(http.StatusOK, "ai-gateway.querystar", "query star projects successful", true, data)
		} else {
			// Handle quota query (float value)
			var quota float64 = 0
			if !response.IsNull() {
				// Validate that the response can be converted to float
				quotaStr := response.String()
				if quotaStr != "" {
					var parseErr error
					quota, parseErr = strconv.ParseFloat(quotaStr, 64)
					if parseErr != nil {
						log.Errorf("Invalid %s format for user %s: %s", responseType, employeeNumber, quotaStr)
						sendJSONResponse(http.StatusInternalServerError, "ai-gateway.invalid_quota_format",
							fmt.Sprintf("Invalid %s format", responseType), false, nil)
						return
					}

					// Validate that quota is non-negative
					if quota < 0 {
						log.Errorf("Invalid %s value for user %s: %f (cannot be negative)", responseType, employeeNumber, quota)
						sendJSONResponse(http.StatusInternalServerError, "ai-gateway.invalid_quota_value",
							fmt.Sprintf("Invalid %s value", responseType), false, nil)
						return
					}
				}
			} else {
				log.Debugf("No %s found for user %s (key does not exist or is empty), defaulting to 0", responseType, employeeNumber)
			}

			data := map[string]interface{}{
				"user_id": employeeNumber,
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
	value, err := strconv.ParseFloat(values["value"], 64)
	if userId == "" || err != nil {
		sendJSONResponse(http.StatusBadRequest, "ai-gateway.invalid_params", "Request denied by ai quota check. user_id can't be empty and value must be a valid number.", false, nil)
		return types.ActionContinue
	}

	key := config.QuotaManagement.RedisKeyPrefix + userId
	incrementFloatValue(config.redisClient, key, value, func(newValue float64, err error) {
		if err != nil {
			log.Errorf("Redis delta operation failed for key = %s value = %f: %v", key, value, err)
			sendJSONResponse(http.StatusServiceUnavailable, "ai-gateway.error", fmt.Sprintf("redis error:%v", err), false, nil)
			return
		}
		log.Debugf("Redis delta operation successful for key = %s value = %f, new value = %f", key, value, newValue)
		sendJSONResponse(http.StatusOK, "ai-gateway.deltaquota", "delta quota successful", true, nil)
	})

	return types.ActionPause
}

func refreshUsedQuota(ctx wrapper.HttpContext, config QuotaConfig, body string, log wrapper.Log) types.Action {
	queryValues, _ := url.ParseQuery(body)
	values := make(map[string]string, len(queryValues))
	for k, v := range queryValues {
		values[k] = v[0]
	}
	userId := values["user_id"]
	quota, err := strconv.ParseFloat(values["quota"], 64)
	if userId == "" || err != nil {
		sendJSONResponse(http.StatusBadRequest, "ai-gateway.invalid_params", "Request denied by ai quota check. user_id can't be empty and quota must be a valid number.", false, nil)
		return types.ActionContinue
	}
	err2 := config.redisClient.Set(config.QuotaManagement.RedisUsedPrefix+userId, fmt.Sprintf("%.6f", quota), func(response resp.Value) {
		log.Debugf("Redis set key = %s quota = %f", config.QuotaManagement.RedisUsedPrefix+userId, quota)
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
	value, err := strconv.ParseFloat(values["value"], 64)
	if userId == "" || err != nil {
		sendJSONResponse(http.StatusBadRequest, "ai-gateway.invalid_params", "Request denied by ai quota check. user_id can't be empty and value must be a valid number.", false, nil)
		return types.ActionContinue
	}

	key := config.QuotaManagement.RedisUsedPrefix + userId
	incrementFloatValue(config.redisClient, key, value, func(newValue float64, err error) {
		if err != nil {
			log.Errorf("Redis delta used operation failed for key = %s value = %f: %v", key, value, err)
			sendJSONResponse(http.StatusServiceUnavailable, "ai-gateway.error", fmt.Sprintf("redis error:%v", err), false, nil)
			return
		}
		log.Debugf("Redis delta used operation successful for key = %s value = %f, new value = %f", key, value, newValue)
		sendJSONResponse(http.StatusOK, "ai-gateway.deltausedquota", "delta used quota successful", true, nil)
	})

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

	employeeNumber := values["employee_number"]
	starredProjects := values["starred_projects"]
	if employeeNumber == "" {
		sendJSONResponse(http.StatusBadRequest, "ai-gateway.invalid_params", "Request denied by ai quota check. employee_number can't be empty.", false, nil)
		return types.ActionContinue
	}

	// starredProjects can be empty (to clear all starred projects) or comma-separated project names
	var projectsList []string
	if starredProjects != "" {
		projectsList = strings.Split(starredProjects, ",")
		for i, project := range projectsList {
			projectsList[i] = strings.TrimSpace(project)
		}
	}

	// Use StarCacheManager to set starred projects
	config.starCacheManager.SetStarredProjects(employeeNumber, projectsList, func(err error) {
		if err != nil {
			log.Errorf("Failed to set starred projects for employee %s: %v", employeeNumber, err)
			sendJSONResponse(http.StatusServiceUnavailable, "ai-gateway.error", fmt.Sprintf("redis error:%v", err), false, nil)
			return
		}

		log.Debugf("Successfully set starred projects for employee %s: %v", employeeNumber, projectsList)

		data := map[string]interface{}{
			"employee_number":  employeeNumber,
			"starred_projects": starredProjects,
		}
		sendJSONResponse(http.StatusOK, "ai-gateway.setstar", "set star projects successful", true, data)
	})

	return types.ActionPause
}

func setUserPermission(ctx wrapper.HttpContext, config QuotaConfig, body string, log wrapper.Log) types.Action {
	queryValues, _ := url.ParseQuery(body)
	values := make(map[string]string)
	for k, v := range queryValues {
		if len(v) > 0 {
			values[k] = v[0]
		}
	}

	employeeNumber := values["employee_number"]
	modelsParam := values["models"]
	if employeeNumber == "" {
		sendJSONResponse(http.StatusBadRequest, "ai-quota.invalid_params", "Request denied by ai quota check. employee_number can't be empty.", false, nil)
		return types.ActionContinue
	}

	// Parse models list
	var models []string
	if modelsParam != "" {
		if err := json.Unmarshal([]byte(modelsParam), &models); err != nil {
			// Try to parse as comma-separated string
			models = strings.Split(modelsParam, ",")
			for i, model := range models {
				models[i] = strings.TrimSpace(model)
			}
		}
	}

	log.Debugf("Setting permission for employee %s with models: %v", employeeNumber, models)

	// Set user permission
	if config.permissionChecker != nil {
		config.permissionChecker.SetUserPermission(employeeNumber, models, func(err error) {
			if err != nil {
				log.Errorf("Failed to set user permission for employee %s: %v", employeeNumber, err)
				sendJSONResponse(http.StatusServiceUnavailable, "ai-quota.error", fmt.Sprintf("Failed to set user permission: %v", err), false, nil)
				return
			}
			data := map[string]interface{}{
				"employee_number": employeeNumber,
				"models":          models,
			}
			sendJSONResponse(http.StatusOK, "ai-quota.setpermission", "set user permission successful", true, data)
		})
	} else {
		log.Errorf("Permission checker not initialized, cannot set user permission for employee %s", employeeNumber)
		sendJSONResponse(http.StatusServiceUnavailable, "ai-quota.error", "Permission management not configured.", false, nil)
	}

	return types.ActionPause
}

func setStarCheckPermission(ctx wrapper.HttpContext, config QuotaConfig, body string, log wrapper.Log) types.Action {
	queryValues, _ := url.ParseQuery(body)
	values := make(map[string]string)
	for k, v := range queryValues {
		if len(v) > 0 {
			values[k] = v[0]
		}
	}

	employeeNumber := values["employee_number"]
	enabledParam := values["enabled"]
	if employeeNumber == "" {
		sendJSONResponse(http.StatusBadRequest, "ai-quota.invalid_params", "Request denied by ai quota check. employee_number can't be empty.", false, nil)
		return types.ActionContinue
	}

	// Parse enabled parameter
	enabled := false
	if enabledParam == "true" || enabledParam == "1" {
		enabled = true
	}

	log.Debugf("Setting star check permission for employee %s: %t", employeeNumber, enabled)

	// Set user star check permission
	if config.starCheckChecker != nil {
		config.starCheckChecker.SetStarCheckPermission(employeeNumber, enabled, func(err error) {
			if err != nil {
				log.Errorf("Failed to set star check permission for employee %s: %v", employeeNumber, err)
				sendJSONResponse(http.StatusServiceUnavailable, "ai-quota.error", fmt.Sprintf("Failed to set star check permission: %v", err), false, nil)
				return
			}
			data := map[string]interface{}{
				"employee_number": employeeNumber,
				"enabled":         enabled,
			}
			sendJSONResponse(http.StatusOK, "ai-quota.set_star_permission", "set star check permission successful", true, data)
		})
	} else {
		log.Errorf("Star check permission checker not initialized, cannot set permission for employee %s", employeeNumber)
		sendJSONResponse(http.StatusServiceUnavailable, "ai-quota.error", "Star check permission management not configured.", false, nil)
	}

	return types.ActionPause
}

func queryStarCheckPermission(ctx wrapper.HttpContext, config QuotaConfig, url *url.URL, log wrapper.Log) types.Action {
	queryValues := url.Query()
	values := make(map[string]string, len(queryValues))
	for k, v := range queryValues {
		values[k] = v[0]
	}

	employeeNumber := values["employee_number"]
	if employeeNumber == "" {
		sendJSONResponse(http.StatusBadRequest, "ai-quota.invalid_params", "Request denied by ai quota check. employee_number can't be empty.", false, nil)
		return types.ActionContinue
	}

	log.Debugf("Querying star check permission for employee %s", employeeNumber)

	if config.starCheckChecker != nil {
		config.starCheckChecker.CheckStarCheckPermission(employeeNumber, log, func(enabled bool) {
			data := map[string]interface{}{
				"employee_number": employeeNumber,
				"enabled":         enabled,
			}
			sendJSONResponse(http.StatusOK, "ai-quota.query_star_permission", "query star check permission successful", true, data)
		})
	} else {
		log.Errorf("Star check permission checker not initialized, cannot query permission for employee %s", employeeNumber)
		sendJSONResponse(http.StatusServiceUnavailable, "ai-quota.error", "Star check permission management not configured.", false, nil)
	}

	return types.ActionPause
}

// performStarCheck performs the actual star checking logic
func performStarCheck(ctx wrapper.HttpContext, config QuotaConfig, body []byte, userId string, log wrapper.Log) {
	// Use StarCacheManager to check starred projects
	config.starCacheManager.CheckStarredProjects(userId, log, func(starredProjects []string, err error) {
		if err != nil {
			// Redis error - allow request to pass through for better user experience
			log.Warnf("Redis error when checking star status for user %s: %v. Allowing request to pass through.", userId, err)
			processQuotaLogic(ctx, config, body, userId, log)
			return
		}

		// Check if target repo is in starred projects
		hasStar := false
		for _, project := range starredProjects {
			if project == config.StarCheckManagement.TargetRepo {
				hasStar = true
				break
			}
		}

		log.Debugf("User %s starred projects: %v, target repo (%s) starred: %t", userId, starredProjects, config.StarCheckManagement.TargetRepo, hasStar)

		if hasStar {
			// Star check passed, continue with quota logic
			processQuotaLogic(ctx, config, body, userId, log)
		} else {
			sendJSONResponse(http.StatusForbidden, "ai-gateway.star_required", fmt.Sprintf("Please star the project first: https://github.com/%s", strings.ReplaceAll(config.StarCheckManagement.TargetRepo, ".", "/")), false, nil)
		}
	})
}

// continueWithQuotaLogic continues with quota checking after permission validation
func setQuotaPermission(ctx wrapper.HttpContext, config QuotaConfig, body string, log wrapper.Log) types.Action {
	queryValues, _ := url.ParseQuery(body)
	values := make(map[string]string)
	for k, v := range queryValues {
		if len(v) > 0 {
			values[k] = v[0]
		}
	}

	employeeNumber := values["employee_number"]
	enabledParam := values["enabled"]
	if employeeNumber == "" {
		sendJSONResponse(http.StatusBadRequest, "ai-quota.invalid_params", "Request denied by ai quota check. employee_number can't be empty.", false, nil)
		return types.ActionContinue
	}

	// Parse enabled parameter
	enabled := false // Default to false (disabled quota control)
	if enabledParam == "true" || enabledParam == "1" {
		enabled = true
	}

	log.Debugf("Setting quota control permission for employee %s: %t", employeeNumber, enabled)

	// Set user quota control permission
	if config.quotaChecker != nil {
		config.quotaChecker.SetQuotaPermission(employeeNumber, enabled, func(err error) {
			if err != nil {
				log.Errorf("Failed to set quota control permission for employee %s: %v", employeeNumber, err)
				sendJSONResponse(http.StatusServiceUnavailable, "ai-quota.error", fmt.Sprintf("Failed to set quota control permission: %v", err), false, nil)
				return
			}
			data := map[string]interface{}{
				"employee_number": employeeNumber,
				"enabled":         enabled,
			}
			sendJSONResponse(http.StatusOK, "ai-quota.set_quota_permission", "set quota control permission successful", true, data)
		})
	} else {
		log.Errorf("Quota permission checker not initialized, cannot set permission for employee %s", employeeNumber)
		sendJSONResponse(http.StatusServiceUnavailable, "ai-quota.error", "Quota control permission management not configured.", false, nil)
	}

	return types.ActionPause
}

func queryQuotaPermission(ctx wrapper.HttpContext, config QuotaConfig, url *url.URL, log wrapper.Log) types.Action {
	queryValues := url.Query()
	values := make(map[string]string, len(queryValues))
	for k, v := range queryValues {
		values[k] = v[0]
	}

	employeeNumber := values["employee_number"]
	if employeeNumber == "" {
		sendJSONResponse(http.StatusBadRequest, "ai-quota.invalid_params", "Request denied by ai quota check. employee_number can't be empty.", false, nil)
		return types.ActionContinue
	}

	log.Debugf("Querying quota control permission for employee %s", employeeNumber)

	if config.quotaChecker != nil {
		config.quotaChecker.CheckQuotaPermission(employeeNumber, log, func(enabled bool) {
			data := map[string]interface{}{
				"employee_number": employeeNumber,
				"enabled":         enabled,
			}
			sendJSONResponse(http.StatusOK, "ai-quota.query_quota_permission", "query quota control permission successful", true, data)
		})
	} else {
		log.Errorf("Quota permission checker not initialized, cannot query permission for employee %s", employeeNumber)
		sendJSONResponse(http.StatusServiceUnavailable, "ai-quota.error", "Quota control permission management not configured.", false, nil)
	}

	return types.ActionPause
}

func continueWithQuotaLogic(ctx wrapper.HttpContext, config QuotaConfig, body []byte, userId string, modelName string, log wrapper.Log) {
	// Get quota weight for this model, default to 0 if not configured
	var quotaWeight float64 = 0
	if weight, exists := config.QuotaManagement.ModelQuotaWeights[modelName]; exists {
		quotaWeight = weight
	}

	log.Debugf("Model %s quota weight: %f", modelName, quotaWeight)

	// If quota weight is 0, no deduction needed, allow request to continue
	if quotaWeight == 0 {
		log.Debugf("Model %s has zero quota weight, skipping quota check", modelName)
		proxywasm.ResumeHttpRequest()
		return
	}

	// Check if user-level quota control is enabled
	if config.QuotaManagement.UserLevelEnabled {
		// Get employeeNumber from context for quota permission check
		employeeNumber, ok := ctx.GetContext("employeeNumber").(string)
		if !ok {
			log.Warnf("Employee number not found in context, falling back to global quota check for user: %s", userId)
			doQuotaCheck(ctx, config, userId, quotaWeight, modelName, log)
			return
		}

		log.Debugf("User-level quota control is enabled, checking user permission for user: %s", userId)

		if config.quotaChecker != nil {
			config.quotaChecker.CheckQuotaPermission(employeeNumber, log, func(userQuotaEnabled bool) {
				if !userQuotaEnabled {
					log.Debugf("User %s has quota control disabled at user level, skipping quota check", userId)
					// User has quota control disabled, skip quota check and allow request
					proxywasm.ResumeHttpRequest()
					return
				}

				log.Debugf("User %s has quota control enabled at user level, proceeding with quota check", userId)
				// User has quota control enabled, proceed with normal quota logic
				doQuotaCheck(ctx, config, userId, quotaWeight, modelName, log)
			})
			return
		} else {
			log.Warnf("Quota permission checker not initialized, falling back to global quota check for user: %s", userId)
			// Fallback to global quota check if checker is not available
			doQuotaCheck(ctx, config, userId, quotaWeight, modelName, log)
			return
		}
	} else {
		log.Debugf("User-level quota control is disabled, using global quota check for user: %s", userId)
		// User-level control is disabled, use global quota check
		doQuotaCheck(ctx, config, userId, quotaWeight, modelName, log)
	}
}

// checkStarCache checks if user starred projects are cached and if target repo is starred
func (config *QuotaConfig) checkStarCache(employeeNumber string) (bool, bool) {
	// Check if cache exists and is not expired
	now := time.Now().Unix()

	config.starCacheManager.mu.RLock()
	cachedProjects, hasCache := config.starCacheManager.memoryCache[employeeNumber]
	expireTime, hasExpireTime := config.starCacheManager.cacheExpireTime[employeeNumber]
	config.starCacheManager.mu.RUnlock()

	// If cache exists and not expired, use it
	if hasCache && hasExpireTime && now < expireTime {
		// Check if target repo is in the starred projects list
		for _, project := range cachedProjects {
			if project == config.StarCheckManagement.TargetRepo {
				return true, true
			}
		}
		return true, false // Cache exists but target repo not starred
	}
	return false, false // Not cached or expired
}

// setStarCache sets user starred projects in cache
func (config *QuotaConfig) setStarCache(employeeNumber string, starredProjects []string) {
	// Always cache the result, even if it's empty
	// Empty slice means "queried but no starred projects found in Redis"
	// Missing key means "not queried yet"
	now := time.Now().Unix()
	expireTime := now + config.starCacheManager.cacheTTLSeconds
	config.starCacheManager.updateMemoryCacheWithTTL(employeeNumber, starredProjects, expireTime)
}

// deleteStarCache removes user starred projects from cache
func (config *QuotaConfig) deleteStarCache(employeeNumber string) {
	config.starCacheManager.deleteMemoryCache(employeeNumber)
}

// BuildCombinedModelsResponse builds a models response that combines all configured providers
func (config *QuotaConfig) BuildCombinedModelsResponse() ([]byte, error) {
	// For single provider configuration
	if len(config.Providers) == 0 && config.Provider.Id != "" {
		return config.Provider.BuildModelsResponse()
	}

	// For multi-provider configuration, combine all model mappings
	if len(config.Providers) == 0 {
		return []byte(`{"object":"list","data":[]}`), nil
	}

	// Collect all unique models from all providers (first provider wins for duplicates)
	modelMap := make(map[string]ModelInfo)

	for _, providerConfig := range config.Providers {
		models, err := providerConfig.GetModelList()
		if err != nil {
			continue
		}

		// Add models that don't already exist (first provider priority)
		for _, model := range models {
			if _, exists := modelMap[model.Id]; !exists {
				modelMap[model.Id] = model
			}
		}
	}

	// Convert map to slice
	var models []ModelInfo
	for _, model := range modelMap {
		models = append(models, model)
	}

	// Build response
	response := ModelsResponse{
		Object: "list",
		Data:   models,
	}

	return json.Marshal(response)
}

// BuildFilteredModelsResponse builds a filtered models response based on user permissions
func (config *QuotaConfig) BuildFilteredModelsResponse(allowedModels []string, log wrapper.Log) ([]byte, error) {
	log.Debugf("[BuildFilteredModelsResponse] Starting model filtering process")
	log.Debugf("[BuildFilteredModelsResponse] User allowed models: %v", allowedModels)
	log.Debugf("[BuildFilteredModelsResponse] Restricted models config: %v", config.RestrictedModels)
	log.Debugf("[BuildFilteredModelsResponse] PermissionChecker available: %t", config.permissionChecker != nil)

	// First get all available models
	allModelsData, err := config.BuildCombinedModelsResponse()
	if err != nil {
		log.Errorf("[BuildFilteredModelsResponse] Failed to build combined models response: %v", err)
		return nil, err
	}

	// Parse all models
	var allModelsResponse ModelsResponse
	if err := json.Unmarshal(allModelsData, &allModelsResponse); err != nil {
		log.Errorf("[BuildFilteredModelsResponse] Failed to unmarshal models response: %v", err)
		return nil, err
	}

	log.Debugf("[BuildFilteredModelsResponse] Total available models before filtering: %d", len(allModelsResponse.Data))
	for i, model := range allModelsResponse.Data {
		log.Debugf("[BuildFilteredModelsResponse] Model[%d]: %s", i, model.Id)
	}

	// Filter models based on permissions
	var filteredModels []ModelInfo
	for _, model := range allModelsResponse.Data {
		log.Debugf("[BuildFilteredModelsResponse] Processing model: %s", model.Id)

		// Check if model is restricted (only if permissionChecker is available)
		if config.permissionChecker != nil && config.permissionChecker.isRestrictedModel(model.Id, log) {
			log.Debugf("[BuildFilteredModelsResponse] Model %s is RESTRICTED", model.Id)

			// Model is restricted, check if user has permission
			if allowedModels == nil || len(allowedModels) == 0 {
				log.Debugf("[BuildFilteredModelsResponse] No user permissions found, SKIPPING restricted model: %s", model.Id)
				continue
			}

			// Check if model is in allowed list
			if config.permissionChecker != nil && !config.permissionChecker.isModelAllowed(model.Id, allowedModels, log) {
				log.Debugf("[BuildFilteredModelsResponse] Model %s not in user's allowed list, SKIPPING", model.Id)
				continue
			}

			log.Debugf("[BuildFilteredModelsResponse] Model %s is restricted but user has permission, INCLUDING", model.Id)
		} else {
			log.Debugf("[BuildFilteredModelsResponse] Model %s is NOT RESTRICTED, INCLUDING", model.Id)
		}

		// Model is not restricted or user has permission, include it
		filteredModels = append(filteredModels, model)
		log.Debugf("[BuildFilteredModelsResponse] Added model to final list: %s", model.Id)
	}

	log.Debugf("[BuildFilteredModelsResponse] Final filtered models count: %d", len(filteredModels))
	for i, model := range filteredModels {
		log.Debugf("[BuildFilteredModelsResponse] Final[%d]: %s", i, model.Id)
	}

	// Build filtered response
	response := ModelsResponse{
		Object: "list",
		Data:   filteredModels,
	}

	responseData, err := json.Marshal(response)
	if err != nil {
		log.Errorf("[BuildFilteredModelsResponse] Failed to marshal final response: %v", err)
		return nil, err
	}

	log.Debugf("[BuildFilteredModelsResponse] Final response: %s", string(responseData))
	return responseData, nil
}
