package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/adrg/xdg"
)

type Config struct {
	GoogleAPIKey string `json:"google_api_key,omitempty"`
	OpenAIAPIKey string `json:"openai_api_key,omitempty"`
	GoogleModel  string `json:"google_model,omitempty"`
	OpenAIModel  string `json:"openai_model,omitempty"`
}

func GetConfigPath() (string, error) {
	return xdg.ConfigFile("just-ui/config.json")
}

func LoadConfig() (*Config, error) {
	path, err := GetConfigPath()
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return &Config{}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func SaveConfig(cfg *Config) error {
	path, err := GetConfigPath()
	if err != nil {
		return err
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0600)
}
