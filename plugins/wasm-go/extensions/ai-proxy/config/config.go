package config

import (
	"encoding/json"

	"github.com/alibaba/higress/plugins/wasm-go/extensions/ai-proxy/provider"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/tidwall/gjson"
)

// @Name ai-proxy
// @Category custom
// @Phase UNSPECIFIED_PHASE
// @Priority 0
// @Title zh-CN AI代理
// @Description zh-CN 通过AI助手提供智能对话服务
// @IconUrl https://img.alicdn.com/imgextra/i1/O1CN018iKKih1iVx287RltL_!!6000000004419-2-tps-42-42.png
// @Version 0.1.0
//
// @Contact.name CH3CHO
// @Contact.url https://github.com/CH3CHO
// @Contact.email ch3cho@qq.com
//
// @Example
// { "provider": { "type": "qwen", "apiToken": "YOUR_DASHSCOPE_API_TOKEN", "modelMapping": { "*": "qwen-turbo" } } }
// @End
type PluginConfig struct {
	// @Title zh-CN AI服务提供商配置
	// @Description zh-CN AI服务提供商配置，包含API接口、模型和知识库文件等信息
	providerConfigs []provider.ProviderConfig `required:"true" yaml:"providers"`

	activeProviderConfig *provider.ProviderConfig `yaml:"-"`
	activeProvider       provider.Provider        `yaml:"-"`
}

func (c *PluginConfig) FromJson(json gjson.Result) {
	// Reset configuration to avoid state pollution
	c.providerConfigs = make([]provider.ProviderConfig, 0)
	c.activeProviderConfig = nil
	c.activeProvider = nil

	proxywasm.LogInfof("[ai-proxy] Start parsing configuration: %s", json.Raw)
	proxywasm.LogInfof("[ai-proxy] Initial state - providerConfigs: %d, activeProviderConfig: %v, activeProvider: %v",
		len(c.providerConfigs), c.activeProviderConfig != nil, c.activeProvider != nil)

	if providersJson := json.Get("providers"); providersJson.Exists() && providersJson.IsArray() {
		proxywasm.LogInfof("[ai-proxy] Found 'providers' array configuration")
		proxywasm.LogInfof("[ai-proxy] Providers array content: %s", providersJson.Raw)

		for idx, providerJson := range providersJson.Array() {
			proxywasm.LogInfof("[ai-proxy] Processing provider[%d]: %s", idx, providerJson.Raw)
			providerConfig := provider.ProviderConfig{}
			providerConfig.FromJson(providerJson)
			c.providerConfigs = append(c.providerConfigs, providerConfig)
			proxywasm.LogInfof("[ai-proxy] Added provider[%d], current providerConfigs length: %d", idx, len(c.providerConfigs))
		}

		proxywasm.LogInfof("[ai-proxy] Completed processing providers array. Total providers: %d", len(c.providerConfigs))
		proxywasm.LogInfof("[ai-proxy] Multi-provider mode: activeProviderConfig will be selected dynamically")
		// For multi-provider configuration, we don't set activeProviderConfig
		// Instead, we'll select provider dynamically based on model name
		return
	}

	if providerJson := json.Get("provider"); providerJson.Exists() && providerJson.IsObject() {
		proxywasm.LogInfof("[ai-proxy] Found single 'provider' object configuration")
		proxywasm.LogInfof("[ai-proxy] Provider object content: %s", providerJson.Raw)

		// Legacy single provider configuration
		providerConfig := provider.ProviderConfig{}
		providerConfig.FromJson(providerJson)
		c.providerConfigs = []provider.ProviderConfig{providerConfig}
		c.activeProviderConfig = &c.providerConfigs[0]

		proxywasm.LogInfof("[ai-proxy] Single provider mode: activeProviderConfig has been set")
		proxywasm.LogInfof("[ai-proxy] Provider type: %s", c.activeProviderConfig.GetType())
		return
	}

	proxywasm.LogWarnf("[ai-proxy] No valid provider configuration found in JSON: %s", json.Raw)
}

func (c *PluginConfig) Validate() error {
	if c.activeProviderConfig == nil {
		return nil
	}
	if err := c.activeProviderConfig.Validate(); err != nil {
		return err
	}
	return nil
}

func (c *PluginConfig) Complete() error {
	if c.activeProviderConfig == nil {
		c.activeProvider = nil
		return nil
	}

	var err error

	c.activeProvider, err = provider.CreateProvider(*c.activeProviderConfig)
	if err != nil {
		return err
	}

	providerConfig := c.GetProviderConfig()
	return providerConfig.SetApiTokensFailover(c.activeProvider)
}

func (c *PluginConfig) GetProvider() provider.Provider {
	return c.activeProvider
}

func (c *PluginConfig) GetProviderConfig() *provider.ProviderConfig {
	return c.activeProviderConfig
}

// GetProviderConfigs returns all provider configurations
func (c *PluginConfig) GetProviderConfigs() []provider.ProviderConfig {
	return c.providerConfigs
}

// GetProviderForModel returns the provider that should handle the given model
// It searches through providers in order and returns the first one that has a mapping for the model
func (c *PluginConfig) GetProviderForModel(modelName string) (*provider.ProviderConfig, provider.Provider) {
	// For legacy single provider configuration
	if c.activeProviderConfig != nil {
		return c.activeProviderConfig, c.activeProvider
	}

	// For multi-provider configuration, find the first provider that can handle this model
	for i := range c.providerConfigs {
		providerConfig := &c.providerConfigs[i]
		if providerConfig.CanHandleModel(modelName) {
			// Create provider instance if not exists
			if p, err := provider.CreateProvider(*providerConfig); err == nil {
				return providerConfig, p
			}
		}
	}

	// If no specific provider found, use the first one as fallback
	if len(c.providerConfigs) > 0 {
		providerConfig := &c.providerConfigs[0]
		if p, err := provider.CreateProvider(*providerConfig); err == nil {
			return providerConfig, p
		}
	}

	return nil, nil
}

// BuildCombinedModelsResponse builds a models response that combines all configured providers
func (c *PluginConfig) BuildCombinedModelsResponse() ([]byte, error) {
	// For legacy single provider configuration
	if c.activeProviderConfig != nil {
		return c.activeProviderConfig.BuildModelsResponse()
	}

	// For multi-provider configuration, combine all model mappings
	if len(c.providerConfigs) == 0 {
		return []byte(`{"object":"list","data":[]}`), nil
	}

	// Collect all unique models from all providers (first provider wins for duplicates)
	modelMap := make(map[string]provider.ModelInfo)

	for _, providerConfig := range c.providerConfigs {
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
	var models []provider.ModelInfo
	for _, model := range modelMap {
		models = append(models, model)
	}

	// Build response
	response := provider.ModelsResponse{
		Object: "list",
		Data:   models,
	}

	return json.Marshal(response)
}
