package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/msjurset/gostash/internal/config"

	"github.com/spf13/cobra"
)

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show stash statistics",
	RunE:  runStats,
}

func init() {
	rootCmd.AddCommand(statsCmd)
}

func runStats(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	st, err := s.Stats(context.Background())
	if err != nil {
		return err
	}

	// Add disk usage (DB + files dir)
	dbSize := fileSize(config.DBPath())
	filesSize := dirSize(config.FilesDir())
	diskTotal := dbSize + filesSize

	if flagJSON {
		printJSON(map[string]any{
			"items":      st,
			"disk_db":    dbSize,
			"disk_files": filesSize,
			"disk_total": diskTotal,
		})
		return nil
	}

	fmt.Printf("Items:       %d\n", st.TotalItems)
	for _, t := range []string{"url", "snippet", "file", "image", "email"} {
		if c, ok := st.TypeCounts[t]; ok {
			fmt.Printf("  %-10s %d\n", t, c)
		}
	}

	fmt.Printf("Tags:        %d\n", st.TagCount)
	fmt.Printf("Collections: %d\n", st.CollCount)
	fmt.Printf("Links:       %d\n", st.LinkCount)
	fmt.Println()

	fmt.Printf("Storage:     %s total\n", humanSize(diskTotal))
	fmt.Printf("  Database:  %s\n", humanSize(dbSize))
	fmt.Printf("  Files:     %s\n", humanSize(filesSize))

	if st.OldestItem != nil && st.NewestItem != nil {
		fmt.Println()
		fmt.Printf("Oldest:      %s\n", st.OldestItem.Format(time.DateOnly))
		fmt.Printf("Newest:      %s\n", st.NewestItem.Format(time.DateOnly))
	}

	if len(st.TopTags) > 0 {
		fmt.Println()
		fmt.Println("Top tags:")
		for _, t := range st.TopTags {
			fmt.Printf("  %-20s %d\n", t.Name, t.Count)
		}
	}

	if len(st.MonthCounts) > 0 {
		fmt.Println()
		fmt.Println("Growth (by month):")
		// Print in chronological order
		for i := len(st.MonthCounts) - 1; i >= 0; i-- {
			mc := st.MonthCounts[i]
			bar := ""
			for j := 0; j < mc.Count && j < 40; j++ {
				bar += "█"
			}
			if mc.Count > 40 {
				bar += "…"
			}
			fmt.Printf("  %s  %s %d\n", mc.Month, bar, mc.Count)
		}
	}

	return nil
}

func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func dirSize(path string) int64 {
	var total int64
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}
