package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/msjurset/gostash/internal/audit"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/credentials"
	"github.com/msjurset/gostash/internal/gemini"
	"github.com/msjurset/gostash/internal/usage"
	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/fetch"
	"io"
	"os"
	"time"
	"github.com/spf13/cobra"
)

var editCmd = &cobra.Command{
	Use:   "edit <id>",
	Short: "Edit a stashed item",
	Args:  cobra.ExactArgs(1),
	RunE:  runEdit,
}

func init() {
	editCmd.Flags().StringP("title", "t", "", "New title")
	editCmd.Flags().StringP("note", "n", "", "New note")
	editCmd.Flags().StringP("extracted-text", "e", "", "Set extracted text")
	editCmd.Flags().StringP("url", "u", "", "New URL (for link items — fix dead links by pointing them at the new address)")
	editCmd.Flags().StringSlice("add-tag", nil, "Add tags (repeatable)")
	editCmd.Flags().StringSlice("remove-tag", nil, "Remove tags (repeatable)")
	editCmd.Flags().StringP("collection", "c", "", "Add to collection")
	editCmd.Flags().String("location", "", "Set geolocation as 'lat,lon' (decimal degrees); sets source=manual")
	editCmd.Flags().Bool("clear-location", false, "Remove the item's stored location")
	editCmd.Flags().String("speaker-map", "", "Speaker ID map as JSON (e.g. '{\"1\":\"Mark\"}')")
	editCmd.Flags().Bool("ask-ai", false, "Ask a follow-up question of the AI (use with --ask-question)")
	editCmd.Flags().String("ask-question", "", "The follow-up question to ask the AI")
	editCmd.Flags().Bool("unarchive", false, "Restore the item from the archive")
	rootCmd.AddCommand(editCmd)

	rootCmd.AddCommand(&cobra.Command{
		Use:   "reindex",
		Short: "Rebuild the FTS5 search index",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()
			return s.RebuildFTS(context.Background())
		},
	})

	rootCmd.AddCommand(&cobra.Command{
		Use:   "clean-orphans",
		Short: "Delete unreferenced files from the store",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()

			fs := openFileStore()
			hashes, err := s.AllReferencedHashes(context.Background())
			if err != nil {
				return err
			}
			referenced := make(map[string]bool)
			for _, h := range hashes {
				referenced[h] = true
			}

			all, err := fs.ListAll()
			if err != nil {
				return err
			}

			var count int
			for _, h := range all {
				if !referenced[h] {
					if err := fs.Delete(h); err == nil {
						count++
					}
				}
			}

			if flagJSON {
				printJSON(map[string]any{"status": "ok", "orphans_deleted": count})
			} else {
				fmt.Printf("Deleted %d orphaned file(s).\n", count)
			}
			return nil
		},
	})

	for _, kind := range []string{"fix", "summary", "tags"} {
		k := kind
		rootCmd.AddCommand(&cobra.Command{
			Use:    "ai-" + k,
			Hidden: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				text, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				apiKey, err := credentials.Load(credentials.KeyGeminiAPIKey)
				if err != nil {
					return err
				}
				gClient := gemini.New()
				var res gemini.QueryResult
				switch k {
				case "fix":
					res, err = gClient.Fix(context.Background(), apiKey, string(text))
				case "summary":
					res, err = gClient.Summary(context.Background(), apiKey, string(text))
				case "tags":
					res, err = gClient.SuggestTags(context.Background(), apiKey, string(text))
				}
				if err != nil {
					return err
				}
				usageLedger := usage.New(config.Dir())
				usageLedger.Record(res.Model, res.PromptTokens, res.CandidatesTokens)
				if flagJSON {
					printJSON(map[string]string{"result": res.Answer})
				} else {
					fmt.Print(res.Answer)
				}
				return nil
			},
		})
	}
}

func runEdit(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx := context.Background()
	id := args[0]

	item, err := s.GetItem(ctx, id)
	if err != nil {
		return err
	}

	if cmd.Flags().Changed("title") {
		item.Title, _ = cmd.Flags().GetString("title")
	}
	if cmd.Flags().Changed("note") {
		item.Notes, _ = cmd.Flags().GetString("note")
	}
	if cmd.Flags().Changed("extracted-text") {
		item.ExtractedText, _ = cmd.Flags().GetString("extracted-text")
	}
	if cmd.Flags().Changed("url") {
		item.URL, _ = cmd.Flags().GetString("url")
	}
	if cmd.Flags().Changed("clear-location") {
		clear, _ := cmd.Flags().GetBool("clear-location")
		if clear {
			item.Location = nil
		}
	}
	if cmd.Flags().Changed("location") {
		raw, _ := cmd.Flags().GetString("location")
		loc, err := parseLocationFlag(raw)
		if err != nil {
			return err
		}
		item.Location = loc
	}
	if cmd.Flags().Changed("speaker-map") {
		raw, _ := cmd.Flags().GetString("speaker-map")
		var m map[string]string
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return fmt.Errorf("--speaker-map: invalid JSON: %w", err)
		}
		item.SpeakerMap = m
	}

	if ask, _ := cmd.Flags().GetBool("ask-ai"); ask {
		question, _ := cmd.Flags().GetString("ask-question")
		if strings.TrimSpace(question) == "" {
			return fmt.Errorf("--ask-ai requires --ask-question")
		}

		apiKey, err := credentials.Load(credentials.KeyGeminiAPIKey)
		if err != nil {
			return fmt.Errorf("gemini key: %w", err)
		}

		gClient := gemini.New()
		var b strings.Builder
		b.WriteString(fmt.Sprintf("Title: %s\n", item.Title))
		if item.URL != "" {
			b.WriteString(fmt.Sprintf("URL: %s\n", item.URL))
		}
		if item.Notes != "" {
			b.WriteString(fmt.Sprintf("Notes: %s\n", item.Notes))
		}
		if item.ExtractedText != "" {
			b.WriteString(fmt.Sprintf("Extracted Text: %s\n", item.ExtractedText))
		}
		
		if item.URL != "" {
			qLower := strings.ToLower(question)
			if strings.Contains(qLower, "summar") || strings.Contains(qLower, "crawl") || strings.Contains(qLower, "company") || strings.Contains(qLower, "product") || strings.Contains(qLower, "what does") || strings.Contains(qLower, "who is") {
				extra, err := fetch.CrawlRelatedContext(item.URL)
				if err == nil && extra != "" {
					b.WriteString(fmt.Sprintf("\n--- Additional Crawled Context ---\n%s\n", extra))
				}
			}
		}
		
		contextInfo := b.String()
		var media []gemini.Media

		// Include primary media if any
		if (item.Type == model.TypeImage || strings.HasPrefix(item.MimeType, "video/")) && item.StorePath != "" {
			fs := openFileStore()
			if rc, err := fs.Open(item.StorePath); err == nil {
				if data, err := io.ReadAll(rc); err == nil {
					media = append(media, gemini.Media{
						Data:     data,
						MimeType: item.MimeType,
					})
				}
				rc.Close()
			}
		}

		// Include attached files if they are images or videos
		if files, err := s.ListItemFiles(ctx, id); err == nil {
			fs := openFileStore()
			for _, f := range files {
				if strings.HasPrefix(f.MimeType, "image/") || strings.HasPrefix(f.MimeType, "video/") {
					if rc, err := fs.Open(f.ContentHash); err == nil {
						if data, err := io.ReadAll(rc); err == nil {
							media = append(media, gemini.Media{
								Data:     data,
								MimeType: f.MimeType,
							})
						}
						rc.Close()
					}
				}
			}
		}

		res, err := gClient.Query(ctx, apiKey, contextInfo, media, question)
		if err != nil {
			return fmt.Errorf("gemini query: %w", err)
		}


		// Record usage for accounting/analytics
		usageLedger := usage.New(config.Dir())
		usageLedger.Record(res.Model, res.PromptTokens, res.CandidatesTokens)

		now := time.Now().Format("2006-01-02 15:04")
		sep := "\n\n--- Follow-up: " + now + " ---\n"
		item.Notes += sep + question + "\n\n" + res.Answer
	}

	if cmd.Flags().Changed("unarchive") {
		unarch, _ := cmd.Flags().GetBool("unarchive")
		if unarch {
			item.Archived = false
		}
	}

	if err := s.UpdateItem(ctx, item); err != nil {
		return err
	}

	// Handle tag additions
	if addTags, _ := cmd.Flags().GetStringSlice("add-tag"); len(addTags) > 0 {
		for _, t := range addTags {
			if err := s.AddTag(ctx, id, t); err != nil {
				return fmt.Errorf("add tag %q: %w", t, err)
			}
			logTagAudit(item, audit.ActionAdd, t, "edit")
		}

		// Trigger rules after tagging
		full, err := s.GetItem(ctx, id)
		if err == nil {
			res := ApplyRulesToItem(s, full, RuleApplyContext{})
			if res.Title != "" || res.HasNoteUpdate() {
				if err := s.UpdateItem(ctx, full); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to update item from rules: %v\n", err)
				}
			}
			FirePostSaveRuleEffects(ctx, s, full, res)
		}
	}

	// Handle tag removals
	if rmTags, _ := cmd.Flags().GetStringSlice("remove-tag"); len(rmTags) > 0 {
		for _, t := range rmTags {
			if err := s.RemoveTag(ctx, id, t); err != nil {
				return fmt.Errorf("remove tag %q: %w", t, err)
			}
			logTagAudit(item, audit.ActionRemove, t, "edit")
		}
	}

	// Handle collection
	if col, _ := cmd.Flags().GetString("collection"); col != "" {
		if err := s.AddToCollection(ctx, id, col); err != nil {
			return fmt.Errorf("add to collection: %w", err)
		}
	}

	// Re-fetch to show updated state
	item, err = s.GetItem(ctx, id)
	if err != nil {
		return err
	}

	if flagJSON {
		printJSON(item)
	} else {
		fmt.Printf("Updated [%s] %s\n", shortID(item.ID), item.Title)
	}
	return nil
}

// parseLocationFlag accepts "lat,lon" (decimal degrees) and returns
// a *Location with Source="manual". Leading/trailing whitespace and
// a surrounding pair of parens are tolerated so users can paste
// "(33.7547, -84.6322)" or "33.7547,-84.6322". Out-of-range or
// non-numeric components are surfaced as errors so a typo doesn't
// silently corrupt the record.
func parseLocationFlag(raw string) (*model.Location, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "(")
	s = strings.TrimSuffix(s, ")")
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return nil, fmt.Errorf("--location: expected 'lat,lon', got %q", raw)
	}
	lat, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return nil, fmt.Errorf("--location: bad latitude %q: %w", parts[0], err)
	}
	lon, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return nil, fmt.Errorf("--location: bad longitude %q: %w", parts[1], err)
	}
	if lat < -90 || lat > 90 {
		return nil, fmt.Errorf("--location: latitude %v out of range [-90, 90]", lat)
	}
	if lon < -180 || lon > 180 {
		return nil, fmt.Errorf("--location: longitude %v out of range [-180, 180]", lon)
	}
	return &model.Location{Lat: lat, Lon: lon, Source: "manual"}, nil
}
