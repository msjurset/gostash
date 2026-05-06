package main

import (
	"context"
	"fmt"
	"os"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/rules"
	"github.com/msjurset/gostash/internal/store"
)

// RuleApplyContext carries the user's explicit-input flags into the rules
// engine so they can take precedence over rule output for the same field.
// Empty strings mean "user didn't supply this; let rules win".
type RuleApplyContext struct {
	UserTitle      string
	UserNote       string
	UserCollection string
}

// ApplyRulesToItem runs the configured rules over `item` and folds the
// result into the item's fields. Returns the rule result so the caller
// can fire post-save effects (link_to, notify) and short-circuit on skip.
//
// Rule pre-save effects mutate `item` in place:
//   - tag additions are appended (deduped against existing)
//   - collection assignment uses the rule's value when user didn't pass -c
//   - title / set_note replace when user didn't pass -t / -n
//   - append_note always appends
//
// Skip is detected via result.Skipped. Caller is responsible for the audit
// log + early return (see runAdd / handleStashURL for the canonical flow).
//
// Failure to load rules is logged but never fatal — capture should never
// fail on rule misconfiguration.
func ApplyRulesToItem(item *model.Item, userInput RuleApplyContext) rules.Result {
	rs, err := rules.Load(rules.DefaultPath(config.Dir()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: rules failed to load: %v\n", err)
		return rules.Result{}
	}
	result := rs.Apply(item)
	for _, e := range result.Errors {
		fmt.Fprintf(os.Stderr, "warning: rules: %v\n", e)
	}
	if result.Skipped {
		return result
	}

	for _, tag := range result.Tags {
		if !hasTag(item.Tags, tag) {
			item.Tags = append(item.Tags, model.Tag{Name: tag})
		}
	}
	if userInput.UserCollection == "" && result.Collection != "" {
		item.Collections = append(item.Collections, model.Collection{Name: result.Collection})
	}
	if userInput.UserTitle == "" && result.Title != "" {
		item.Title = result.Title
	}
	if userInput.UserNote == "" && result.Note != "" {
		item.Notes = result.Note
	}
	if result.AppendedNote != "" {
		if item.Notes == "" {
			item.Notes = result.AppendedNote
		} else {
			item.Notes = item.Notes + "\n" + result.AppendedNote
		}
	}
	return result
}

// EnsureRuleCollections ensures every collection a rule wants to assign
// exists in the store before the item is saved. Auto-created collections
// have no description. Errors are logged but don't fail the operation —
// the subsequent CreateItem will fail with a clearer message.
func EnsureRuleCollections(ctx context.Context, s store.Store, result rules.Result) {
	if result.Collection == "" {
		return
	}
	if _, err := s.GetCollection(ctx, result.Collection); err != nil {
		if _, err := s.CreateCollection(ctx, result.Collection, ""); err != nil {
			fmt.Fprintf(os.Stderr, "warning: rules could not create collection %q: %v\n", result.Collection, err)
		}
	}
}

// FirePostSaveRuleEffects runs the link_to and notify actions that need
// the item's persisted state. Call after a successful CreateItem.
func FirePostSaveRuleEffects(ctx context.Context, s store.Store, item *model.Item, result rules.Result) {
	for _, link := range result.Links {
		applyLinkAction(ctx, s, item, link)
	}
	for _, msg := range result.Notifies {
		fireNotification(item, msg)
	}
}

// fireNotification dispatches a desktop notification for `message` using
// the platform-appropriate backend. The optional `item` is used to derive
// a click target — for URL items the link itself, for files the source
// path. Failures are silent so a missed notification never blocks a stash
// add.
func fireNotification(item *model.Item, message string) {
	if message == "" {
		return
	}
	link := notificationClickTarget(item)
	if err := notifyDesktop("Stash", message, link); err != nil {
		fmt.Fprintf(os.Stderr, "warning: notify: %v\n", err)
	}
}

// notificationClickTarget returns a URL or filesystem path the user should
// jump to when clicking the notification. Returns "" for items where the
// natural target isn't obvious (snippets, emails) — the notification then
// renders without a click action.
func notificationClickTarget(item *model.Item) string {
	if item == nil {
		return ""
	}
	if item.URL != "" {
		return item.URL
	}
	if item.Type == model.TypeFile || item.Type == model.TypeImage {
		// Prefer the source path so clicking opens the user's original
		// file. The store path (~/.stash/files/<hash>) would also work
		// but is opaque to the user.
		if item.SourcePath != "" {
			if _, err := os.Stat(item.SourcePath); err == nil {
				return "file://" + item.SourcePath
			}
		}
	}
	return ""
}

// logSkipped writes a `skip` event to the rules log so the user can audit
// which captures got dropped. Used to be a separate skip.log; the rules
// engine migrates pre-existing skip.log files into the unified format on
// first append, so this is now just a thin call to AppendEvent.
//
// Failure to write the log is reported to stderr but never escalates —
// rule events should never block a stash add.
func logSkipped(item *model.Item, result rules.Result) {
	logRuleEvent(rules.Event{
		Type:   rules.EventSkip,
		Rules:  []string{result.SkippedBy},
		Title:  item.Title,
		Source: sourceFor(item),
	})
}

// logRuleFire writes a `fire` event to the rules log when at least one
// rule matched a captured item. Called after the item is persisted so
// `ItemID` is meaningful.
func logRuleFire(item *model.Item, result rules.Result) {
	if len(result.MatchedRules) == 0 {
		return
	}
	logRuleEvent(rules.Event{
		Type:    rules.EventFire,
		Rules:   result.MatchedRules,
		ItemID:  item.ID,
		Title:   item.Title,
		Source:  sourceFor(item),
		Effects: rules.FormatEffects(result),
	})
}

// logRuleRetro writes a `retro` event for each item changed by `stash
// rules apply`. Retroactive runs don't fire skip / notify (those are
// capture-time only), so the recorded effects are tags / collection /
// title / notes only.
func logRuleRetro(item *model.Item, result rules.Result) {
	if len(result.MatchedRules) == 0 {
		return
	}
	logRuleEvent(rules.Event{
		Type:    rules.EventRetro,
		Rules:   result.MatchedRules,
		ItemID:  item.ID,
		Title:   item.Title,
		Source:  sourceFor(item),
		Effects: rules.FormatEffects(result),
	})
}

func logRuleEvent(ev rules.Event) {
	path := rules.DefaultLogPath(config.Dir())
	if err := rules.AppendEvent(path, ev); err != nil {
		fmt.Fprintf(os.Stderr, "warning: rules.log: %v\n", err)
	}
}

// sourceFor returns the most useful "where did this come from" string
// for log entries — URL for link items, source path for files, a
// stable placeholder for snippets.
func sourceFor(item *model.Item) string {
	if item.URL != "" {
		return item.URL
	}
	if item.SourcePath != "" {
		return item.SourcePath
	}
	return "(snippet)"
}

// applyLinkAction creates undirected links from the new item to all items
// matched by the LinkSpec. Bounded at 50 targets to prevent runaway
// fanout (e.g. tag rules that match hundreds of items). Errors are logged
// but don't fail the add.
func applyLinkAction(ctx context.Context, s store.Store, source *model.Item, link rules.LinkSpec) {
	const maxTargets = 50
	var targetIDs []string

	switch {
	case link.ID != "":
		// Pre-validate that the target exists; LinkItems would error
		// anyway but we'd rather warn cleanly.
		if target, err := s.GetItem(ctx, link.ID); err == nil {
			if target.ID != source.ID {
				targetIDs = append(targetIDs, target.ID)
			}
		} else {
			fmt.Fprintf(os.Stderr, "warning: link_to.id %q not found\n", link.ID)
			return
		}
	case link.Tag != "":
		items, err := s.ListItems(ctx, model.ItemFilter{Tags: []string{link.Tag}, Limit: maxTargets + 1})
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: link_to.tag %q list: %v\n", link.Tag, err)
			return
		}
		for _, it := range items {
			if it.ID == source.ID {
				continue
			}
			targetIDs = append(targetIDs, it.ID)
			if len(targetIDs) >= maxTargets {
				break
			}
		}
	default:
		return
	}

	for _, id := range targetIDs {
		if err := s.LinkItems(ctx, source.ID, id, "", false); err != nil {
			fmt.Fprintf(os.Stderr, "warning: link %s ↔ %s: %v\n", source.ID, id, err)
		}
	}
}
