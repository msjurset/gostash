package main

import (
	"fmt"
	"os"

	"github.com/msjurset/gostash/internal/audit"
	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/model"
)

// logTagAudit appends a tag mutation event to $STASH_DIR/tags.log.
// Best-effort: failures log to stderr but never propagate, since a
// flaky log writer must not block a successful tag operation.
//
// Item is required so we can snapshot URL/type/domain at log time —
// historical events stay correct even if the user later edits the URL.
func logTagAudit(item *model.Item, action audit.TagAction, tag, source string) {
	if item == nil {
		return
	}
	ev := audit.TagEvent{
		Action:   action,
		Tag:      tag,
		ItemID:   item.ID,
		ItemType: string(item.Type),
		ItemURL:  item.URL,
		Source:   source,
	}
	path := audit.DefaultTagsLogPath(config.Dir())
	if err := audit.AppendTagEvent(path, ev); err != nil {
		fmt.Fprintf(os.Stderr, "warning: tags.log: %v\n", err)
	}
}
