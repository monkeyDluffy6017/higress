package config

import (
	"github.com/alibaba/higress/plugins/wasm-go/extensions/ai-proxy/provider"
	"github.com/higress-group/wasm-go/pkg/wrapper"
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

	// internal indexes for request-scoped provider override
	providerIdToConfig   map[string]*provider.ProviderConfig `yaml:"-"`
	providerIdToInstance map[string]provider.Provider        `yaml:"-"`
}

func (c *PluginConfig) FromJson(json gjson.Result) {
	// reset indexes
	c.providerIdToConfig = make(map[string]*provider.ProviderConfig)
	c.providerIdToInstance = make(map[string]provider.Provider)

	if providersJson := json.Get("providers"); providersJson.Exists() && providersJson.IsArray() {
		c.providerConfigs = make([]provider.ProviderConfig, 0)
		for _, providerJson := range providersJson.Array() {
			providerConfig := provider.ProviderConfig{}
			providerConfig.FromJson(providerJson)
			c.providerConfigs = append(c.providerConfigs, providerConfig)
		}
	}

	if providerJson := json.Get("provider"); providerJson.Exists() && providerJson.IsObject() {
		// TODO: For legacy config support. To be removed later.
		providerConfig := provider.ProviderConfig{}
		providerConfig.FromJson(providerJson)
		c.providerConfigs = []provider.ProviderConfig{providerConfig}
		c.activeProviderConfig = &providerConfig
		// build indexes for legacy config
		for i := range c.providerConfigs {
			pc := &c.providerConfigs[i]
			if pc.GetId() != "" {
				c.providerIdToConfig[pc.GetId()] = pc
			}
		}
		// Legacy configuration is used and the active provider is determined.
		// We don't need to continue with the new configuration style.
		return
	}

	c.activeProviderConfig = nil

	activeProviderId := json.Get("activeProviderId").String()
	if activeProviderId != "" {
		for _, providerConfig := range c.providerConfigs {
			if providerConfig.GetId() == activeProviderId {
				c.activeProviderConfig = &providerConfig
				break
			}
		}
	}

	// build indexes for providers
	for i := range c.providerConfigs {
		pc := &c.providerConfigs[i]
		if pc.GetId() != "" {
			c.providerIdToConfig[pc.GetId()] = pc
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

// ===== Request-scoped provider override support =====

const (
	ctxKeyChosenProviderId = "ai-proxy.chosenProviderId"
)

// GetProviderById returns (instance, config, ok). It lazily creates provider instance if needed.
func (c *PluginConfig) GetProviderById(id string) (provider.Provider, *provider.ProviderConfig, bool) {
	if id == "" {
		return nil, nil, false
	}
	pc, ok := c.providerIdToConfig[id]
	if !ok || pc == nil {
		return nil, nil, false
	}
	if p, ok := c.providerIdToInstance[id]; ok && p != nil {
		return p, pc, true
	}
	// lazily create
	p, err := provider.CreateProvider(*pc)
	if err != nil {
		return nil, nil, false
	}
	c.providerIdToInstance[id] = p
	// initialize token failover settings for this instance
	_ = pc.SetApiTokensFailover(p)
	return p, pc, true
}

// SetChosenProviderForRequest sets the chosen provider id into the request context if exists in config.
func (c *PluginConfig) SetChosenProviderForRequest(ctx wrapper.HttpContext, id string) bool {
	if _, _, ok := c.GetProviderById(id); !ok {
		return false
	}
	ctx.SetContext(ctxKeyChosenProviderId, id)
	return true
}

// GetProviderForRequest returns the request-scoped provider if set, otherwise the active provider.
func (c *PluginConfig) GetProviderForRequest(ctx wrapper.HttpContext) provider.Provider {
	if v := ctx.GetContext(ctxKeyChosenProviderId); v != nil {
		if id, _ := v.(string); id != "" {
			if p, _, ok := c.GetProviderById(id); ok {
				return p
			}
		}
	}
	return c.activeProvider
}

// GetProviderConfigForRequest returns the request-scoped provider config if set, otherwise the active provider config.
func (c *PluginConfig) GetProviderConfigForRequest(ctx wrapper.HttpContext) *provider.ProviderConfig {
	if v := ctx.GetContext(ctxKeyChosenProviderId); v != nil {
		if id, _ := v.(string); id != "" {
			if _, pc, ok := c.GetProviderById(id); ok {
				return pc
			}
		}
	}
	return c.activeProviderConfig
}
