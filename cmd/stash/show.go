package main

import (
	"context"

	"github.com/spf13/cobra"
)

var showCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show details of a stashed item",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		item, err := s.GetItem(context.Background(), args[0])
		if err != nil {
			return err
		}

		printItem(item)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(showCmd)
}
