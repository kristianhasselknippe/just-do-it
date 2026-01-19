package main

import (
	"context"
	"fmt"
	"os"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/openai"
)

// GenerateCommand uses an LLM to convert a natural language prompt into a bash command.
func GenerateCommand(prompt string) (string, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY is not set. Please set it to use AI features.")
	}

	llm, err := openai.New(openai.WithToken(apiKey))
	if err != nil {
		return "", fmt.Errorf("failed to create LLM client: %w", err)
	}

	ctx := context.Background()

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
