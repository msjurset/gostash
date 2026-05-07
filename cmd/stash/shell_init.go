package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// shellInitCmd prints a copy-pasteable shell snippet integrating
// `stash find` into the user's interactive shell. Two pieces:
//
//   - `sf` — a function wrapper that passes flags through to
//     `stash find`. Saves typing on the common case
//     (`sf --tag video` vs `stash find --tag video`).
//   - Alt-S keybind — launches `stash find` from any prompt with no
//     pre-typed command.
//
// Print-only; never modifies the user's dotfiles. The user picks
// where to drop the snippet (for autoload-style setups it'll usually
// split across two files; for monolithic .zshrc/.bashrc it goes in
// one place). `source <(stash shell-init)` works in zsh/bash for
// session-only adoption.
var shellInitCmd = &cobra.Command{
	Use:   "shell-init",
	Short: "Print shell function + keybind for `stash find` integration",
	Long: `Print a shell snippet that adds:

  sf [filter-flags]    Function wrapper for ` + "`stash find`" + ` (saves typing).
  Alt-S                Keybind that launches ` + "`stash find`" + ` from any prompt.

The snippet is shell-aware. Auto-detects from $SHELL when --shell is
omitted. Supported: zsh, bash.

Recommended use:

  # zsh — paste into ~/.zshrc, or source it directly:
  source <(stash shell-init zsh)

  # bash — paste into ~/.bashrc, or:
  eval "$(stash shell-init bash)"

The snippet is intentionally short so it's easy to read and adapt
(rebind Alt-S, change the function name, etc.).`,
	RunE: runShellInit,
}

func init() {
	shellInitCmd.Flags().String("shell", "", "Target shell: zsh, bash (defaults to $SHELL detection)")
	rootCmd.AddCommand(shellInitCmd)
}

func runShellInit(cmd *cobra.Command, args []string) error {
	shell, _ := cmd.Flags().GetString("shell")
	if shell == "" {
		shell = detectShell()
	}
	shell = strings.ToLower(shell)

	switch shell {
	case "zsh":
		fmt.Print(zshShellInit)
	case "bash":
		fmt.Print(bashShellInit)
	default:
		return fmt.Errorf("unsupported shell %q (supported: zsh, bash)", shell)
	}
	return nil
}

// detectShell returns "zsh" / "bash" / etc. derived from $SHELL.
// Defaults to "zsh" since the project's primary user runs zsh and
// the snippet falls back gracefully there.
func detectShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return filepath.Base(s)
	}
	return "zsh"
}

const zshShellInit = `# stash find shell integration — paste into ~/.zshrc, or:
#   source <(stash shell-init zsh)
#
# Three pieces: function wrapper (sf), completion glue, Alt-S keybind.
# Adapt to taste — change the function name, rebind the key, etc.

# sf — pass-through to ` + "`stash find`" + `, accepts the same filter flags.
sf() { stash find "$@" }

# Tab-completion for sf — rewrites the words array so 'sf ...' looks
# like 'stash find ...' and dispatches to the autoloaded _stash
# compdef. Without this, sf has no completions because zsh
# associates completion by command name. Note the 0-indexed slice
# (':1' drops one element) despite the rest of zsh being 1-indexed.
_sf() {
    words=(stash find "${words[@]:1}")
    (( CURRENT += 1 ))
    _stash
}
compdef _sf sf

# Alt-S launches ` + "`stash find`" + ` from any prompt.
# (Ctrl-G is commonly taken by fzf-git; Alt-S is mnemonic for stash.)
stash-find-widget() {
    BUFFER='stash find'
    zle accept-line
}
zle -N stash-find-widget
bindkey '^[s' stash-find-widget
`

const bashShellInit = `# stash find shell integration — paste into ~/.bashrc, or:
#   eval "$(stash shell-init bash)"
#
# Two pieces: a function wrapper (sf) and an Alt-S keybind. Adapt
# either to taste — change the function name, rebind to a different
# key, etc.

# sf — pass-through to ` + "`stash find`" + `, accepts the same filter flags.
sf() { stash find "$@"; }

# Alt-S launches ` + "`stash find`" + ` from any prompt.
# bash's ` + "`bind -x`" + ` runs the command but doesn't redraw the prompt
# afterward; that's normal — the prompt comes back on next input.
bind -x '"\es": stash find'
`
