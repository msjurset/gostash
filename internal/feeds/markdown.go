package feeds

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

// HTMLToMarkdown turns a chunk of RSS/Atom HTML into Markdown that
// renders cleanly in the Mac app's `NotesView` and `stash show`'s
// glamour pipeline. Targets the subset of HTML feeds actually emit:
// `<p>`, `<a>`, `<strong>`, `<em>`, `<code>`, `<pre>`, `<blockquote>`,
// `<ul>/<ol>/<li>`, `<h1..h6>`, `<br>`, `<hr>`, `<img>`. Unknown tags
// degrade to their text content. Best-effort — feed authors do all
// sorts of weird things and we don't try to handle every edge case.
//
// Returns the original string unchanged if the input contains no HTML
// tags at all, so notes the user wrote in Markdown pass through.
func HTMLToMarkdown(s string) string {
	if !looksLikeHTML(s) {
		return s
	}
	doc, err := html.Parse(strings.NewReader("<body>" + s + "</body>"))
	if err != nil {
		return s
	}
	var b strings.Builder
	body := findBody(doc)
	if body == nil {
		return s
	}
	render(&b, body, &renderCtx{})
	return strings.TrimSpace(collapseBlankLines(b.String()))
}

func looksLikeHTML(s string) bool {
	if strings.Contains(s, "<p>") || strings.Contains(s, "<p ") ||
		strings.Contains(s, "<br") || strings.Contains(s, "<a ") ||
		strings.Contains(s, "<blockquote") || strings.Contains(s, "<ul") ||
		strings.Contains(s, "<ol") || strings.Contains(s, "<h1") ||
		strings.Contains(s, "<h2") || strings.Contains(s, "<h3") ||
		strings.Contains(s, "<code") || strings.Contains(s, "<pre") ||
		strings.Contains(s, "<em") || strings.Contains(s, "<strong") {
		return true
	}
	return false
}

func findBody(n *html.Node) *html.Node {
	if n.Type == html.ElementNode && n.Data == "body" {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if b := findBody(c); b != nil {
			return b
		}
	}
	return nil
}

// renderCtx tracks list-nesting state so nested <ul>/<ol> indent and
// number correctly. Blockquote handling lives in the blockquote case
// itself (sub-buffer + line prefix) and doesn't need state here.
type renderCtx struct {
	listDepth   int
	listOrdered []bool
	listIndex   []int
}

func render(b *strings.Builder, n *html.Node, ctx *renderCtx) {
	switch n.Type {
	case html.TextNode:
		// Collapse runs of whitespace to a single space — HTML
		// treats whitespace as insignificant between tags but the
		// text node still contains it. Preserve trailing/leading
		// spaces because they may matter mid-paragraph.
		emitText(b, n.Data, ctx)
		return
	case html.ElementNode:
		switch n.Data {
		case "p":
			ensureBlankLine(b)
			renderChildren(b, n, ctx)
			ensureBlankLine(b)
		case "br":
			b.WriteString("  \n") // Markdown line break
		case "a":
			href := attr(n, "href")
			text := childText(n, ctx)
			if href == "" {
				b.WriteString(text)
			} else if text == "" || text == href {
				b.WriteString("<" + href + ">")
			} else {
				fmt.Fprintf(b, "[%s](%s)", text, href)
			}
		case "strong", "b":
			b.WriteString("**")
			renderChildren(b, n, ctx)
			b.WriteString("**")
		case "em", "i":
			b.WriteString("*")
			renderChildren(b, n, ctx)
			b.WriteString("*")
		case "code":
			// Inline code unless wrapped in <pre>; <pre><code> path
			// handled by the pre case below.
			b.WriteString("`")
			b.WriteString(strings.ReplaceAll(childText(n, ctx), "`", "\\`"))
			b.WriteString("`")
		case "pre":
			ensureBlankLine(b)
			b.WriteString("```\n")
			b.WriteString(strings.TrimRight(childText(n, ctx), "\n"))
			b.WriteString("\n```")
			ensureBlankLine(b)
		case "blockquote":
			ensureBlankLine(b)
			// Render contents into a sub-buffer, then prefix every
			// line with "> ". This keeps nested blocks (paragraphs,
			// code, lists) working naturally and gives valid
			// Markdown that survives glamour's renderer.
			var sub strings.Builder
			renderChildren(&sub, n, ctx)
			for _, line := range strings.Split(strings.TrimSpace(collapseBlankLines(sub.String())), "\n") {
				if line == "" {
					b.WriteString(">\n")
				} else {
					b.WriteString("> " + line + "\n")
				}
			}
			ensureBlankLine(b)
		case "ul":
			renderList(b, n, ctx, false)
		case "ol":
			renderList(b, n, ctx, true)
		case "li":
			// Standalone <li> outside a list — render as bullet.
			b.WriteString("- ")
			renderChildren(b, n, ctx)
			b.WriteString("\n")
		case "h1":
			renderHeading(b, n, ctx, 1)
		case "h2":
			renderHeading(b, n, ctx, 2)
		case "h3":
			renderHeading(b, n, ctx, 3)
		case "h4":
			renderHeading(b, n, ctx, 4)
		case "h5":
			renderHeading(b, n, ctx, 5)
		case "h6":
			renderHeading(b, n, ctx, 6)
		case "hr":
			ensureBlankLine(b)
			b.WriteString("---")
			ensureBlankLine(b)
		case "img":
			src := attr(n, "src")
			alt := attr(n, "alt")
			if src != "" {
				fmt.Fprintf(b, "![%s](%s)", alt, src)
			}
		case "script", "style":
			// Drop entirely.
			return
		default:
			// Unknown / passthrough: just emit children.
			renderChildren(b, n, ctx)
		}
	default:
		renderChildren(b, n, ctx)
	}
}

func renderChildren(b *strings.Builder, n *html.Node, ctx *renderCtx) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		render(b, c, ctx)
	}
}

func renderHeading(b *strings.Builder, n *html.Node, ctx *renderCtx, level int) {
	ensureBlankLine(b)
	b.WriteString(strings.Repeat("#", level) + " ")
	renderChildren(b, n, ctx)
	ensureBlankLine(b)
}

func renderList(b *strings.Builder, n *html.Node, ctx *renderCtx, ordered bool) {
	ctx.listDepth++
	ctx.listOrdered = append(ctx.listOrdered, ordered)
	ctx.listIndex = append(ctx.listIndex, 0)
	ensureBlankLine(b)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.Data != "li" {
			continue
		}
		indent := strings.Repeat("  ", ctx.listDepth-1)
		ctx.listIndex[ctx.listDepth-1]++
		marker := "- "
		if ordered {
			marker = fmt.Sprintf("%d. ", ctx.listIndex[ctx.listDepth-1])
		}
		b.WriteString(indent + marker)
		renderChildren(b, c, ctx)
		b.WriteString("\n")
	}
	ensureBlankLine(b)
	ctx.listDepth--
	ctx.listOrdered = ctx.listOrdered[:len(ctx.listOrdered)-1]
	ctx.listIndex = ctx.listIndex[:len(ctx.listIndex)-1]
}

// childText returns the concatenated text content of n with HTML
// children processed (so a link inside emphasis still renders the
// link in Markdown). Used for `<a>` etc. where we want the inner
// formatted text as the link label.
func childText(n *html.Node, ctx *renderCtx) string {
	var b strings.Builder
	renderChildren(&b, n, ctx)
	return strings.TrimSpace(b.String())
}

// emitText writes a text fragment, normalizing whitespace. Blockquote
// prefixing happens at the blockquote element via a sub-builder
// (see the blockquote case in render).
func emitText(b *strings.Builder, text string, ctx *renderCtx) {
	b.WriteString(normalizeWhitespace(text))
}

// normalizeWhitespace collapses runs of spaces/tabs to a single space.
// Preserves single newlines so block-level handlers can rely on them
// being where the source put them.
func normalizeWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		switch r {
		case ' ', '\t':
			if !prevSpace {
				b.WriteRune(' ')
				prevSpace = true
			}
		case '\n':
			b.WriteRune('\n')
			prevSpace = false
		default:
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return b.String()
}

// ensureBlankLine guarantees there's exactly one blank line before
// the next block-level element. Called by `<p>`, headings, lists,
// blockquote, `<hr>`, and `<pre>` to keep paragraphs visually
// separated in the output.
func ensureBlankLine(b *strings.Builder) {
	s := b.String()
	if s == "" {
		return
	}
	switch {
	case strings.HasSuffix(s, "\n\n"):
		// already blank-line separated
	case strings.HasSuffix(s, "\n"):
		b.WriteString("\n")
	default:
		b.WriteString("\n\n")
	}
}

// collapseBlankLines reduces 3+ consecutive newlines down to 2 so the
// output doesn't accumulate extra blanks where nested block elements
// each called ensureBlankLine on the same boundary.
func collapseBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
