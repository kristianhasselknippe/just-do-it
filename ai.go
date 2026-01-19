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
	ctx := context.Background()
	var llm llms.Model
	var err error

	// Check for Google API Key first
	if apiKey := os.Getenv("GOOGLE_API_KEY"); apiKey != "" {
		llm, err = googleai.New(ctx, googleai.WithAPIKey(apiKey), googleai.WithDefaultModel("gemini-pro"))
		if err != nil {
			return "", fmt.Errorf("failed to create GoogleAI client: %w", err)
		}
	} else if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
		llm, err = openai.New(openai.WithToken(apiKey))
		if err != nil {
			return "", fmt.Errorf("failed to create OpenAI client: %w", err)
		}
	} else {
		return "", fmt.Errorf("neither GOOGLE_API_KEY nor OPENAI_API_KEY is set. Please set one to use AI features.")
	}

	content := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, `You are a helpful assistant that converts natural language requests into a single bash command. 
Output ONLY the command. Do not include markdown code blocks, explanations, or quotes.`),
		llms.TextParts(llms.ChatMessageTypeHuman, "Request: "+prompt+"\nCommand:"),
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
