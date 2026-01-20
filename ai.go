package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/google/generative-ai-go/genai"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/googleai"
	"github.com/tmc/langchaingo/llms/openai"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// GenerateCommand uses an LLM to convert a natural language prompt into a bash command.
func GenerateCommand(ctx context.Context, prompt string, onToken func(string)) (string, error) {
	cfg, _ := LoadConfig() // Ignore error, treat as empty config

	var llm llms.Model
	var err error

	// Priority: Env Vars > Config File

	googleKey := os.Getenv("GOOGLE_API_KEY")
	if googleKey == "" && cfg != nil {
		googleKey = cfg.GoogleAPIKey
	}

	openaiKey := os.Getenv("OPENAI_API_KEY")
	if openaiKey == "" && cfg != nil {
		openaiKey = cfg.OpenAIAPIKey
	}

	// Check for Google API Key first
	if googleKey != "" {
		model := "gemini-2.0-flash"
		if cfg != nil && cfg.GoogleModel != "" {
			model = cfg.GoogleModel
		}
		// Use a model confirmed to be available via API listing
		llm, err = googleai.New(ctx, googleai.WithAPIKey(googleKey), googleai.WithDefaultModel(model))
		if err != nil {
			return "", fmt.Errorf("failed to create GoogleAI client: %w", err)
		}
	} else if openaiKey != "" {
		model := "gpt-4o"
		if cfg != nil && cfg.OpenAIModel != "" {
			model = cfg.OpenAIModel
		}
		llm, err = openai.New(openai.WithToken(openaiKey), openai.WithModel(model))
		if err != nil {
			return "", fmt.Errorf("failed to create OpenAI client: %w", err)
		}
	} else {
		// Return specific error type/string to trigger UI flow
		return "", fmt.Errorf("MISSING_API_KEY")
	}

	content := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman,
			`You are a helpful assistant that converts natural language requests into a single bash command. 
Output ONLY the command. Do not include markdown code blocks, explanations, or quotes.
Request: `+prompt+`
Command:`),
	}

	completion, err := llm.GenerateContent(ctx, content,
		llms.WithTemperature(0.0),
		llms.WithMaxTokens(256),
		llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
			if onToken != nil {
				onToken(string(chunk))
			}
			return nil
		}),
	)
	if err != nil {
		return "", fmt.Errorf("AI generation failed: %w", err)
	}

	if len(completion.Choices) == 0 {
		return "", fmt.Errorf("no response from AI")
	}

	return completion.Choices[0].Content, nil
}

// ListModels returns a list of available model names for the given provider and key.
func ListModels(provider, key string) ([]string, error) {
	if provider == "google" {
		ctx := context.Background()
		client, err := genai.NewClient(ctx, option.WithAPIKey(key))
		if err != nil {
			return nil, err
		}
		defer client.Close()

		var models []string
		iter := client.ListModels(ctx)
		for {
			m, err := iter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				return nil, err
			}
			// Only include generation models
			if strings.Contains(m.Name, "gemini") {
				// Name comes as "models/gemini-pro", strip prefix if needed or keep it
				// langchaingo usually expects just "gemini-pro" but "models/" might be needed for pure API
				// Let's strip "models/" for display
				name := strings.TrimPrefix(m.Name, "models/")
				models = append(models, name)
			}
		}
		return models, nil
	} else if provider == "openai" {
		// Simple HTTP request for OpenAI
		req, err := http.NewRequest("GET", "https://api.openai.com/v1/models", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+key)

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("OpenAI API returned status: %s", resp.Status)
		}

		var result struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, err
		}

		var models []string
		for _, m := range result.Data {
			if strings.HasPrefix(m.ID, "gpt") {
				models = append(models, m.ID)
			}
		}
		sort.Strings(models)
		return models, nil
	}
	return nil, fmt.Errorf("unknown provider")
}
