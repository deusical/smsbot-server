package bot

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
)

const (
	compatibleSettingKey   = "provider_compatible"
	compatibleSettingScope = "provider-compatible"
	defaultCompatibleURL   = "https://api.openai.com/v1"
)

type compatibleProviderSecret struct {
	BaseURL string `json:"base_url"`
	Key     string `json:"key"`
}

// CompatibleProviderStatus is safe for the dashboard: the key is never returned.
type CompatibleProviderStatus struct {
	BaseURL    string `json:"base_url"`
	Configured bool   `json:"configured"`
}

func normalizeCompatibleBaseURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 2048 || strings.ContainsAny(value, "\r\n\\") {
		return "", errors.New("invalid_provider_url")
	}
	u, err := url.Parse(value)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("invalid_provider_url")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", errors.New("invalid_provider_url")
	}
	loopback := host == "localhost"
	if ip := net.ParseIP(host); ip != nil {
		loopback = ip.IsLoopback()
	}
	if u.Scheme == "http" && !loopback {
		return "", errors.New("provider_url_requires_https")
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return "", errors.New("invalid_provider_url")
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/v1"
	}
	u.RawPath = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func (e *Engine) compatibleProviderConfig() (compatibleProviderSecret, error) {
	config := compatibleProviderSecret{BaseURL: defaultCompatibleURL}
	b, err := e.Store.GetSecretSetting(compatibleSettingKey, compatibleSettingScope)
	if err != nil {
		return config, err
	}
	if len(b) == 0 {
		return config, nil
	}
	if err = json.Unmarshal(b, &config); err != nil {
		return config, errors.New("provider_configuration_invalid")
	}
	config.BaseURL, err = normalizeCompatibleBaseURL(config.BaseURL)
	if err != nil {
		return config, err
	}
	return config, nil
}

func (e *Engine) CompatibleProviderStatus() (CompatibleProviderStatus, error) {
	config, err := e.compatibleProviderConfig()
	if err != nil {
		return CompatibleProviderStatus{}, err
	}
	return CompatibleProviderStatus{BaseURL: config.BaseURL, Configured: config.Key != ""}, nil
}

func (e *Engine) SetCompatibleProvider(baseURL, key string, clearKey bool) error {
	baseURL, err := normalizeCompatibleBaseURL(baseURL)
	if err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	if len(key) > 4096 || strings.ContainsAny(key, "\r\n") {
		return errors.New("invalid_provider_key")
	}
	if clearKey && key != "" {
		return errors.New("invalid_provider_key")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	current, err := e.compatibleProviderConfig()
	if err != nil {
		return err
	}
	if clearKey {
		current.Key = ""
	} else if key != "" {
		current.Key = key
	}
	current.BaseURL = baseURL
	b, err := json.Marshal(current)
	if err != nil {
		return err
	}
	return e.Store.SetSecretSetting(compatibleSettingKey, compatibleSettingScope, b)
}

func (e *Engine) CompatibleProviderConfigured() bool {
	config, err := e.compatibleProviderConfig()
	return err == nil && config.Key != ""
}

func (e *Engine) ProviderConfiguredFor(model Model) bool {
	if _, offline := e.Provider.(SimulatedProvider); offline {
		return true
	}
	if model.Provider == compatibleProviderID {
		if _, production := e.Provider.(*OpenRouter); !production {
			key, err := e.Store.GetSecretSetting("provider_key", "provider-key")
			return err == nil && len(key) > 0
		}
		return e.CompatibleProviderConfigured()
	}
	return e.ProviderConfigured()
}

func (e *Engine) providerForModel(model Model) (Provider, string, error) {
	if _, offline := e.Provider.(SimulatedProvider); offline {
		return e.Provider, "fixture", nil
	}
	if model.Provider == compatibleProviderID {
		if _, production := e.Provider.(*OpenRouter); !production {
			key, err := e.Store.GetSecretSetting("provider_key", "provider-key")
			return e.Provider, string(key), err
		}
		config, err := e.compatibleProviderConfig()
		if err != nil {
			return nil, "", err
		}
		if config.Key == "" {
			return nil, "", errors.New("provider_key_missing")
		}
		return NewOpenAICompatible(config.BaseURL, config.Key), config.Key, nil
	}
	key, err := e.Store.GetSecretSetting("provider_key", "provider-key")
	return e.Provider, string(key), err
}

func (e *Engine) RefreshCompatibleModels(ctx context.Context) ([]Model, error) {
	if _, offline := e.Provider.(SimulatedProvider); offline {
		return nil, errors.New("fixture_catalog_is_offline")
	}
	config, err := e.compatibleProviderConfig()
	if err != nil {
		return nil, err
	}
	if config.Key == "" {
		return nil, errors.New("provider_key_missing")
	}
	models, err := NewOpenAICompatible(config.BaseURL, config.Key).Models(ctx)
	if err != nil {
		return nil, err
	}
	return e.replaceProviderModels(compatibleProviderID, models)
}
