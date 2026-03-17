package main

import (
	"context"
	"fmt"

	"github.com/msjurset/gostash/internal/model"

	"github.com/spf13/cobra"
)

var collectionCmd = &cobra.Command{
	Use:     "collection",
	Aliases: []string{"col"},
	Short:   "Manage collections",
}

var colListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all collections",
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		cols, err := s.ListCollections(context.Background())
		if err != nil {
			return err
		}
		printCollections(cols)
		return nil
	},
}

var colCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new collection",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		desc, _ := cmd.Flags().GetString("description")
		col, err := s.CreateCollection(context.Background(), args[0], desc)
		if err != nil {
			return err
		}

		if flagJSON {
			printJSON(col)
		} else {
			fmt.Printf("Created collection %q\n", col.Name)
		}
		return nil
	},
}

var colDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a collection (items are kept)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		if err := s.DeleteCollection(context.Background(), args[0]); err != nil {
			return err
		}

		if flagJSON {
			printJSON(map[string]string{"deleted": args[0]})
		} else {
			fmt.Printf("Deleted collection %q\n", args[0])
		}
		return nil
	},
}

var colShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show items in a collection",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		ctx := context.Background()
		col, err := s.GetCollection(ctx, args[0])
		if err != nil {
			return err
		}

		items, err := s.ListCollectionItems(ctx, col.Name, model.ItemFilter{})
		if err != nil {
			return err
		}

		if !flagJSON {
			fmt.Printf("Collection: %s\n", col.Name)
			if col.Description != "" {
				fmt.Printf("Description: %s\n", col.Description)
			}
			fmt.Println()
		}
		printItems(items)
		return nil
	},
}

func init() {
	colCreateCmd.Flags().StringP("description", "d", "", "Collection description")
	collectionCmd.AddCommand(colListCmd)
	collectionCmd.AddCommand(colCreateCmd)
	collectionCmd.AddCommand(colDeleteCmd)
	collectionCmd.AddCommand(colShowCmd)
	rootCmd.AddCommand(collectionCmd)
}
