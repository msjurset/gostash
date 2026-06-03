package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/credentials"
	"github.com/msjurset/gostash/internal/gemini"
	"github.com/msjurset/gostash/internal/usage"

	"github.com/spf13/cobra"
)

var askCmd = &cobra.Command{
	Use:   "ask <question>",
	Short: "Ask a question about your stashed items (RAG)",
	Long: `Ask a question about your stashed items using Retrieval-Augmented
Generation (RAG). Stash will retrieve the most relevant items using
semantic search and use them as context for Gemini to answer your
question.

  stash ask "what are my notes on the marketing plan?"
  stash ask "how do I fix a flat tire?" --tag bicycle`,
	Args: cobra.ExactArgs(1),
	RunE: runAsk,
}

func init() {
	addSearchFilterFlags(askCmd)
	rootCmd.AddCommand(askCmd)
}

func runAsk(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	question := args[0]
	ctx := context.Background()

	// 1. Get API key
	key, err := credentials.Load(credentials.KeyGeminiAPIKey)
	if err != nil {
		return fmt.Errorf("loading Gemini key: %w", err)
	}
	if key == "" {
		return fmt.Errorf("no Gemini API key found; run `stash auth set-gemini` first")
	}

	client := gemini.New()
	ledger := usage.New(config.Dir())

	if !flagJSON {
		fmt.Printf("Thinking about: %q...\n", question)
	}

	// 2. Embed the question for semantic retrieval
	res, err := client.EmbedContent(ctx, key, question)
	if err != nil {
		return fmt.Errorf("embedding question: %w", err)
	}
	ledger.Record(res.Model, res.Tokens, 0)

	// 3. Retrieve relevant items
	filter, err := buildFilter(cmd, "")
	if err != nil {
		return err
	}
	filter.Query = question
	filter.Semantic = true
	filter.QueryVector = res.Vector
	filter.Limit = 10 // top 10 items for context

	items, err := s.SearchItems(ctx, filter)
	if err != nil {
		return fmt.Errorf("retrieving context: %w", err)
	}

	if len(items) == 0 {
		return fmt.Errorf("no relevant items found in your stash to answer this question")
	}

	// 4. Construct context for RAG
	var contextParts []string
	for i, item := range items {
		part := fmt.Sprintf("Item %d: %s\nType: %s\nNotes: %s\nContent: %s",
			i+1, item.Title, item.Type.Display(), item.Notes, truncateText(item.ExtractedText, 2000))
		contextParts = append(contextParts, part)
	}
	contextInfo := strings.Join(contextParts, "\n\n---\n\n")

	// 5. Ask Gemini
	prompt := fmt.Sprintf(`You are a personal knowledge assistant. Answer the user's question using ONLY the provided context from their "Stash" vault. 
If the answer is not in the context, say you don't know based on the stashed items.
Be concise but thorough. Cite the Item numbers in your answer.

Context:
%s

Question: %s`, contextInfo, question)

	queryRes, err := client.Query(ctx, key, "", nil, prompt)
	if queryRes.Model != "" {
		ledger.Record(queryRes.Model, queryRes.PromptTokens, queryRes.CandidatesTokens)
	}
	if err != nil {
		return fmt.Errorf("generating answer: %w", err)
	}

	if flagJSON {
		printJSON(map[string]any{
			"question": question,
			"answer":   queryRes.Answer,
			"context":  items,
		})
	} else {
		fmt.Println("\n--- Answer ---")
		fmt.Println(queryRes.Answer)
		fmt.Println("\n--- Context Sources ---")
		for i, item := range items {
			fmt.Printf("[%d] %s (%s)\n", i+1, item.Title, item.ID)
		}
	}

	return nil
}

func truncateText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
