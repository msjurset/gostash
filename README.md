# stash

A personal knowledge vault for the command line. Capture URLs, text snippets, files, images, and emails — then search across everything with full-text search.

## Features

- **Capture anything** — URLs (with automatic title/content extraction), text from stdin, local files, images, `.eml` emails
- **Full-text search** — SQLite FTS5-powered search across all stored content
- **Tags & collections** — Organize items with tags and named collections
- **Content extraction** — Automatically extracts searchable text from HTML, PDF, DOCX, images, and email messages
- **Interactive TUI** — Browse and search with a terminal UI built on [Bubbletea](https://github.com/charmbracelet/bubbletea)
- **Configurable** — TOML config file at `~/.config/stash/config.toml`
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
make deploy
```

## Usage

Running `stash` with no arguments launches the interactive TUI.

```sh
# Launch TUI
stash

# Save a URL
stash add https://example.com -T bookmark,reading

# Save text from stdin
echo "quick note" | stash add - -t "My Note"

# Save a file
stash add report.pdf -T work,reports

# Save an email
stash add message.eml -T inbox

# Search everything
stash search "database migration"

# List recent items
stash list --tag reading --limit 10

# Interactive TUI (explicit)
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
- `--type <type>` — Force item type: `link`, `snippet`, `file`, `image`, `email` (on `add`)

### TUI Keys

| Key | Action |
|-----|--------|
| `/` | Search (supports `tag:name` filter syntax) |
| `1`–`5` | Filter by type (links, snippets, files, images, emails) |
| `j`/`k` or arrows | Navigate |
| `enter` | View detail |
| `r` | Refresh |
| `q` | Quit / back |
| `ctrl+c` | Force quit |
| `ctrl+l` | Clear search |

## Configuration

Config file: `~/.config/stash/config.toml`

```toml
data_dir  = "~/.stash"
db_path   = "~/.stash/stash.db"
files_dir = "~/.stash/files"
```

Precedence: CLI flags > `STASH_DIR` env var > config file > defaults.

## Storage

Data lives in `~/.stash/` by default:

- `stash.db` — SQLite database with FTS5
- `files/` — Content-addressable file store (SHA-256)

## Shell Completions

```sh
# Install via make
make install-completion

# Or source manually
source completions/stash.bash    # bash
source completions/stash.zsh     # zsh
```

## License

[MIT](LICENSE)
