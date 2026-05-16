package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/msjurset/gostash/internal/store"
	"github.com/spf13/cobra"
)

var searchHistoryCmd = &cobra.Command{
	Use:     "search-history",
	Aliases: []string{"history"},
	Short:   "Manage the click-log driving Recent / Frequent search views",
	Long: `Inspect and mutate the search-history log written when the user
clicks a result from the Mac app's Quick Search panel or the Chrome
extension popup. Entries are rolled up by query for the Recent and
Frequent browse views; raw rows aren't displayed individually.`,
}

var searchHistoryListCmd = &cobra.Command{
	Use:   "list",
	Short: "List committed queries grouped by recency or frequency",
	RunE:  runSearchHistoryList,
}

var searchHistoryRecordCmd = &cobra.Command{
	Use:   "record <query>",
	Short: "Record one committed-query event",
	Args:  cobra.ExactArgs(1),
	RunE:  runSearchHistoryRecord,
}

var searchHistoryClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Delete every search-history row",
	RunE:  runSearchHistoryClear,
}

var searchHistoryDeleteCmd = &cobra.Command{
	Use:   "delete <query>",
	Short: "Delete all rows for one committed query",
	Args:  cobra.ExactArgs(1),
	RunE:  runSearchHistoryDelete,
}

func init() {
	searchHistoryListCmd.Flags().String("sort", "recent", "Sort order (recent | frequent)")
	searchHistoryListCmd.Flags().IntP("limit", "l", 30, "Maximum rollup rows (0 = unlimited)")

	searchHistoryRecordCmd.Flags().String("item-id", "", "Optional ID of the item the user clicked")

	searchHistoryCmd.AddCommand(searchHistoryListCmd)
	searchHistoryCmd.AddCommand(searchHistoryRecordCmd)
	searchHistoryCmd.AddCommand(searchHistoryClearCmd)
	searchHistoryCmd.AddCommand(searchHistoryDeleteCmd)
	rootCmd.AddCommand(searchHistoryCmd)
}

func runSearchHistoryList(cmd *cobra.Command, _ []string) error {
	sortStr, _ := cmd.Flags().GetString("sort")
	limit, _ := cmd.Flags().GetInt("limit")

	sortBy := store.SearchHistoryRecent
	switch sortStr {
	case "recent", "":
		sortBy = store.SearchHistoryRecent
	case "frequent":
		sortBy = store.SearchHistoryFrequent
	default:
		return fmt.Errorf("invalid --sort %q (want recent or frequent)", sortStr)
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	entries, err := s.ListSearchHistory(context.Background(), sortBy, limit)
	if err != nil {
		return err
	}

	if flagJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}

	if len(entries) == 0 {
		fmt.Println("No search history.")
		return nil
	}
	for _, e := range entries {
		stamp := e.LastUsedAt.Local().Format("2006-01-02 15:04")
		fmt.Printf("%s  %4dx  %s\n", stamp, e.Count, e.Query)
	}
	return nil
}

func runSearchHistoryRecord(cmd *cobra.Command, args []string) error {
	query := args[0]
	itemID, _ := cmd.Flags().GetString("item-id")

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	return s.RecordSearchClick(context.Background(), query, itemID)
}

func runSearchHistoryClear(_ *cobra.Command, _ []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	return s.ClearSearchHistory(context.Background())
}

func runSearchHistoryDelete(_ *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	return s.DeleteSearchHistoryEntry(context.Background(), args[0])
}
