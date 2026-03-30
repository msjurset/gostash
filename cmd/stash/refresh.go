package main

import (
	"context"
	"fmt"

	"github.com/msjurset/gostash/internal/fetch"
	"github.com/msjurset/gostash/internal/model"

	"github.com/spf13/cobra"
)

var refreshCmd = &cobra.Command{
	Use:   "refresh <id>",
	Short: "Re-fetch content for a URL item",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		ctx := context.Background()
		item, err := s.GetItem(ctx, args[0])
		if err != nil {
			return err
		}

		if item.Type != model.TypeURL {
			return fmt.Errorf("item is not a URL (type: %s)", item.Type.Display())
		}
		if item.URL == "" {
			return fmt.Errorf("item has no URL")
		}

		result, err := fetch.URL(item.URL)
		if err != nil {
			return fmt.Errorf("fetch failed: %w", err)
		}

		if result.ExtractedText == "" {
			if flagJSON {
				printJSON(map[string]string{"status": "no_content"})
			} else {
				fmt.Println("No text content available from this URL.")
			}
			return nil
		}

		item.ExtractedText = result.ExtractedText
		item.MimeType = result.MimeType
		if result.Title != "" {
			item.Title = result.Title
		}

		if err := s.UpdateItem(ctx, item); err != nil {
			return fmt.Errorf("save: %w", err)
		}

		if flagJSON {
			printJSON(item)
		} else {
			fmt.Printf("Refreshed [%s] %s\n", shortID(item.ID), item.Title)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(refreshCmd)
}
