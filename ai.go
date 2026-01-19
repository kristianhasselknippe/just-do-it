package main

import (
	"context"
	"fmt"
	"os"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/googleai"
	"github.com/tmc/langchaingo/llms/openai"
)

// GenerateCommand uses an LLM to convert a natural language prompt into a bash command.
func GenerateCommand(prompt string) (string, error) {
	cfg, _ := LoadConfig() // Ignore error, treat as empty config

	ctx := context.Background()
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
		// Use a model confirmed to be available via API listing
		llm, err = googleai.New(ctx, googleai.WithAPIKey(googleKey), googleai.WithDefaultModel("gemini-2.0-flash"))
		if err != nil {
			return "", fmt.Errorf("failed to create GoogleAI client: %w", err)
		}
	} else if openaiKey != "" {
		llm, err = openai.New(openai.WithToken(openaiKey))
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
	)
	if err != nil {
		return "", fmt.Errorf("AI generation failed: %w", err)
	}

	if len(completion.Choices) == 0 {
		return "", fmt.Errorf("no response from AI")
	}

	return completion.Choices[0].Content, nil
}
