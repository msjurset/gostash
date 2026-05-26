package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/credentials"
	"github.com/spf13/cobra"
)

// `stash auth` — manages cached credentials. Today the only secret
// we cache is the Gemini API key (for the daemon's identify worker
// to call Google without prompting TouchID per call); future paid
// integrations would slot in alongside without restructuring.
//
// Storage shape: system keychain under service="stash". The running
// stash binary is in the keychain ACL so `stash serve` (launched by
// launchd, no GUI context) can read silently. Re-running set-* with
// a different op:// reference replaces the cached value AND
// refreshes the ACL — important when brew or a manual move puts the
// binary at a new path.
var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage cached credentials (1Password → system keychain)",
	Long: "Resolve secrets from 1Password and cache them in the system\n" +
		"keychain so background daemons (stash serve, identify worker)\n" +
		"can read them without GUI interaction.\n\n" +
		"Secrets live in 1Password; the keychain is a cache. Re-run\n" +
		"`set-*` any time to refresh.",
}

var authSetGeminiCmd = &cobra.Command{
	Use:   "set-gemini <op://reference>",
	Short: "Resolve a Gemini API key from 1Password and cache it",
	Long: "Reads a 1Password reference (e.g. op://Personal/Gemini/credential),\n" +
		"resolves it via `op read` (TouchID prompt happens here, in your\n" +
		"interactive shell), and stores the resolved key in the system\n" +
		"keychain. The launchd-spawned `stash serve` reads it from the\n" +
		"keychain — never from disk in plaintext.\n\n" +
		"Re-run with a new reference to rotate. Re-run with the same\n" +
		"reference to refresh after a key value change in 1Password.",
	Args: cobra.ExactArgs(1),
	RunE: runAuthSetGemini,
}

var authShowGeminiCmd = &cobra.Command{
	Use:   "show-gemini",
	Short: "Show whether a Gemini key is cached (does not print the key)",
	RunE:  runAuthShowGemini,
}

var authClearGeminiCmd = &cobra.Command{
	Use:   "clear-gemini",
	Short: "Remove the cached Gemini key from the system keychain",
	RunE:  runAuthClearGemini,
}

var authRefreshGeminiCmd = &cobra.Command{
	Use:   "refresh-gemini",
	Short: "Re-resolve the cached Gemini key from its saved op:// reference",
	Long: "Reads the 1Password reference saved by a prior `stash auth\n" +
		"set-gemini` from the stash config, re-runs `op read`, and writes\n" +
		"the fresh value back into the system keychain. The point of\n" +
		"refresh (vs. setting via the explicit reference each time) is\n" +
		"that the deploy step can run this with no arguments to re-prime\n" +
		"the Keychain ACL for the newly-installed binary — the user's\n" +
		"only interaction is the single TouchID prompt that `op read`\n" +
		"raises.\n\n" +
		"Fails with a clear message if no reference is saved yet — that\n" +
		"means you need a first-time `stash auth set-gemini op://…`.",
	RunE: runAuthRefreshGemini,
}

func init() {
	authCmd.AddCommand(authSetGeminiCmd)
	authCmd.AddCommand(authShowGeminiCmd)
	authCmd.AddCommand(authClearGeminiCmd)
	authCmd.AddCommand(authRefreshGeminiCmd)
	rootCmd.AddCommand(authCmd)
}

func runAuthSetGemini(cmd *cobra.Command, args []string) error {
	ref := strings.TrimSpace(args[0])
	if !strings.HasPrefix(strings.ToLower(ref), "op://") {
		return fmt.Errorf("expected an op:// reference, got %q", ref)
	}
	val, err := credentials.ResolveAndCache(credentials.KeyGeminiAPIKey, ref)
	if err != nil {
		return err
	}
	// Persist the reference (not the secret) so `refresh-gemini`
	// can re-resolve without arguments. The deploy hook calls
	// refresh on every install, which would otherwise require
	// remembering and re-typing the op:// path each time.
	cfg := config.Get()
	cfg.GeminiOpRef = ref
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	config.Reload()

	// Confirmation without echoing the secret. Length + tail is
	// enough to verify the user got the right item out of 1Password
	// (catches the classic "I had two keys named Gemini" mistake).
	tail := ""
	if len(val) >= 4 {
		tail = val[len(val)-4:]
	}
	fmt.Printf("Cached Gemini API key in keychain (length=%d, ends …%s).\n", len(val), tail)
	fmt.Printf("Reference saved to config: %s\n", ref)
	fmt.Println("`stash serve` will pick this up on its next identify cycle.")
	fmt.Println("Future `stash auth refresh-gemini` will re-resolve this reference.")
	return nil
}

func runAuthRefreshGemini(cmd *cobra.Command, args []string) error {
	cfg := config.Get()
	ref := strings.TrimSpace(cfg.GeminiOpRef)
	if ref == "" {
		return errors.New("no Gemini op:// reference saved yet — run `stash auth set-gemini op://…` first")
	}
	val, err := credentials.ResolveAndCache(credentials.KeyGeminiAPIKey, ref)
	if err != nil {
		return err
	}
	tail := ""
	if len(val) >= 4 {
		tail = val[len(val)-4:]
	}
	fmt.Printf("Refreshed Gemini key (length=%d, ends …%s) from %s.\n", len(val), tail, ref)
	return nil
}

func runAuthShowGemini(cmd *cobra.Command, args []string) error {
	val, err := credentials.Load(credentials.KeyGeminiAPIKey)
	if err != nil {
		return err
	}
	if val == "" {
		fmt.Println("No Gemini API key cached. Run: stash auth set-gemini op://…")
		return nil
	}
	tail := ""
	if len(val) >= 4 {
		tail = val[len(val)-4:]
	}
	fmt.Printf("Gemini API key cached (length=%d, ends …%s).\n", len(val), tail)
	return nil
}

func runAuthClearGemini(cmd *cobra.Command, args []string) error {
	if err := credentials.Delete(credentials.KeyGeminiAPIKey); err != nil {
		return err
	}
	fmt.Println("Cleared cached Gemini API key.")
	return nil
}
