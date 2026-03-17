# stash

A personal knowledge vault for the command line. Capture URLs, text snippets, files, and images — then search across everything with full-text search.

## Features

- **Capture anything** — URLs (with automatic title/content extraction), text from stdin, local files, images
- **Full-text search** — SQLite FTS5-powered search across all stored content
- **Tags & collections** — Organize items with tags and named collections
- **Content extraction** — Automatically extracts searchable text from HTML, PDF, DOCX, and images
- **Interactive TUI** — Browse and search with a terminal UI built on [Bubbletea](https://github.com/charmbracelet/bubbletea)
- **JSON output** — Script-friendly `--json` flag on all commands
- **Shell completions** — Bash and Zsh

## Install

### From source

```sh
go install github.com/msjurset/gostash/cmd/stash@latest
```

### Build locally

```sh
git clone https://github.com/msjurset/gostash.git
cd gostash
make install
```

## Usage

```sh
# Save a URL
stash add https://example.com -T bookmark,reading

# Save text from stdin
echo "quick note" | stash add - -t "My Note"

# Save a file
stash add report.pdf -T work,reports

# Search everything
stash search "database migration"

# List recent items
stash list --tag reading --limit 10

# Interactive TUI
stash ui
```

### Commands

| Command | Description |
|---------|-------------|
| `add <url\|file\|->` | Capture a URL, file, or stdin snippet |
| `list` | List stashed items with optional filters |
| `search <query>` | Full-text search across all content |
| `show <id>` | Display item details |
| `edit <id>` | Edit title, notes, or tags |
| `delete <id>` | Remove an item |
| `open <id>` | Open in default application |
| `tag list` | List all tags |
| `tag rename <old> <new>` | Rename a tag |
| `collection list` | List collections |
| `collection create <name>` | Create a collection |
| `collection show <name>` | Show items in a collection |
| `collection delete <name>` | Delete a collection |
| `ui` | Launch interactive TUI |

### Flags

- `--json` — Output as JSON
- `--db <path>` — Custom database path
- `-T <tags>` — Comma-separated tags (on `add`)
- `-t <title>` — Custom title (on `add`)
- `-n <note>` — Add a note (on `add`/`edit`)
- `-c <collection>` — Add to collection (on `add`)

## Storage

All data lives in `~/.stash/` (override with `STASH_DIR`):

- `stash.db` — SQLite database with FTS5
- `files/` — Content-addressable file store (SHA-256)

## Shell Completions

```sh
# Install system-wide
sudo make install-completion

# Or source manually
source completions/stash.bash    # bash
source completions/stash.zsh     # zsh
```

## License

[MIT](LICENSE)
