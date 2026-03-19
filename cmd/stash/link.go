package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

var (
	linkLabel    string
	linkDirected bool
)

var linkCmd = &cobra.Command{
	Use:   "link <id1> <id2>",
	Short: "Create a link between two items",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		ctx := context.Background()

		item1, err := s.GetItem(ctx, args[0])
		if err != nil {
			return fmt.Errorf("resolve first item: %w", err)
		}
		item2, err := s.GetItem(ctx, args[1])
		if err != nil {
			return fmt.Errorf("resolve second item: %w", err)
		}

		if err := s.LinkItems(ctx, item1.ID, item2.ID, linkLabel, linkDirected); err != nil {
			return err
		}

		if flagJSON {
			printJSON(map[string]string{
				"linked": item1.ID,
				"to":     item2.ID,
				"label":  linkLabel,
			})
		} else {
			arrow := "<->"
			if linkDirected {
				arrow = "->"
			}
			fmt.Printf("Linked %s %s %s\n", shortID(item1.ID), arrow, shortID(item2.ID))
			if linkLabel != "" {
				fmt.Printf("Label: %s\n", linkLabel)
			}
		}
		return nil
	},
}

var unlinkCmd = &cobra.Command{
	Use:   "unlink <id1> <id2>",
	Short: "Remove a link between two items",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		ctx := context.Background()

		item1, err := s.GetItem(ctx, args[0])
		if err != nil {
			return fmt.Errorf("resolve first item: %w", err)
		}
		item2, err := s.GetItem(ctx, args[1])
		if err != nil {
			return fmt.Errorf("resolve second item: %w", err)
		}

		if err := s.UnlinkItems(ctx, item1.ID, item2.ID); err != nil {
			return err
		}

		if flagJSON {
			printJSON(map[string]string{
				"unlinked": item1.ID,
				"from":     item2.ID,
			})
		} else {
			fmt.Printf("Unlinked %s and %s\n", shortID(item1.ID), shortID(item2.ID))
		}
		return nil
	},
}

func init() {
	linkCmd.Flags().StringVarP(&linkLabel, "label", "l", "", "Label for the link")
	linkCmd.Flags().BoolVar(&linkDirected, "directed", false, "Create a directed link (id1 -> id2)")
	rootCmd.AddCommand(linkCmd)
	rootCmd.AddCommand(unlinkCmd)
}
