package config

import (
	"encoding/json"

	"github.com/alibaba/higress/plugins/wasm-go/extensions/ai-proxy/provider"
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
	// Process providers array configuration first
	if providersJson := json.Get("providers"); providersJson.Exists() && providersJson.IsArray() {
		c.providerConfigs = make([]provider.ProviderConfig, 0)
		for _, providerJson := range providersJson.Array() {
			providerConfig := provider.ProviderConfig{}
			providerConfig.FromJson(providerJson)
			c.providerConfigs = append(c.providerConfigs, providerConfig)
		}
	}

	// Process legacy single provider configuration
	if providerJson := json.Get("provider"); providerJson.Exists() && providerJson.IsObject() {
		// Legacy single provider configuration
		providerConfig := provider.ProviderConfig{}
		providerConfig.FromJson(providerJson)
		c.providerConfigs = []provider.ProviderConfig{providerConfig}
		c.activeProviderConfig = &c.providerConfigs[0]
		// Legacy configuration is used and the active provider is determined.
		// We don't need to continue with the new configuration style.
		return
	}

	// Reset active provider config
	c.activeProviderConfig = nil

	// Process activeProviderId to select from configured providers
	activeProviderId := json.Get("activeProviderId").String()
	if activeProviderId != "" {
		for i := range c.providerConfigs {
			if c.providerConfigs[i].GetId() == activeProviderId {
				c.activeProviderConfig = &c.providerConfigs[i]
				break
			}
		}
	}
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
	// Reset active provider
	c.activeProvider = nil

	if c.activeProviderConfig == nil {
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
