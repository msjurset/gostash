package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/gemini"
	"github.com/msjurset/gostash/internal/identify"
	"github.com/msjurset/gostash/internal/server"
	"github.com/msjurset/gostash/internal/usage"

	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the HTTP API for the mobile companion / external clients",
	Long: `Start an HTTP server exposing the stash for capture and browse over the
network. Single bearer-token auth; the token lives at
$STASH_DIR/serve.token and is generated on first run.

A QR code is printed to the terminal on startup encoding the pairing
URI (stash-pair://<host>:<port>?token=<hex>). The Android companion
scans the QR to pair with this server. Suppress the QR with --no-qr.

Endpoints:
  POST   /capture                — capture URL / text (JSON) or file (multipart)
  GET    /items                  — list (filters: type, tag, collection, limit, offset)
  GET    /items/<id>             — single item
  GET    /items/<id>/blob        — full content bytes (images / files)
  GET    /items/<id>/thumbnail   — thumbnail bytes
  POST   /items/<id>/tags        — add tag(s) — body: {"tags":["..."]}
  DELETE /items/<id>/tags/<tag>  — remove one tag
  DELETE /items/<id>             — archive; ?hard=true hard-deletes
  GET    /search?q=…             — full-text + tag search
  GET    /tags                   — autocomplete list
  GET    /collections            — autocomplete list
  GET    /healthz                — liveness probe

All non-/healthz paths require ` + "`Authorization: Bearer <token>`" + `.
/healthz is intentionally unauthenticated so liveness probes can run
without consulting the token file.

Subcommands:
  stash serve token   — print or rotate the bearer token
  stash serve pair    — print the pairing URI / QR for the running daemon
  stash serve status  — show running PID, bind, uptime, /healthz state`,
	RunE: runServe,
}

var serveTokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Print the current bearer token (or rotate it)",
	RunE:  runServeToken,
}

var servePairCmd = &cobra.Command{
	Use:   "pair",
	Short: "Print the pairing URI / QR for the running daemon",
	Long: `Emits the same pairing payload as the startup banner, but at any
time — useful when stash serve is running as a launchd daemon
and the startup output is in a log file the user doesn't read.
The Mac app's Settings → Phone pairing tab shells out to this
subcommand to render the QR locally.

  stash serve pair           — JSON with host, port, token, uri
  stash serve pair --qr      — terminal QR + human-readable URI`,
	RunE: runServePair,
}

var serveStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the running daemon's PID / bind / uptime / health",
	Long: `Reports the live state of the stash serve daemon: whether it's
listening, which PID is bound to the configured port, how long it's
been up, whether /healthz answers, and the launchd registration state.
Useful when debugging "the phone can't reach my Mac" without curling
endpoints by hand. Exit code: 0 running and healthy, 2 not running
or unreachable.`,
	RunE: runServeStatus,
}

func init() {
	serveCmd.Flags().StringP("addr", "a", ":9999", "Listen address (host:port). Bind to a specific interface with e.g. 192.168.1.10:9999.")
	serveCmd.Flags().String("advertise", "", "Hostname / IP advertised in the pairing QR (default: first non-loopback IPv4)")
	serveCmd.Flags().Bool("no-qr", false, "Don't print the pairing QR on startup")

	serveTokenCmd.Flags().Bool("rotate", false, "Generate a fresh token, invalidating all paired devices")

	servePairCmd.Flags().StringP("addr", "a", ":9999", "Address the daemon is listening on (matches `serve --addr`)")
	servePairCmd.Flags().String("advertise", "", "Override the advertised host (default: first non-loopback IPv4)")
	servePairCmd.Flags().Bool("qr", false, "Render the QR to the terminal in addition to the URI")

	serveStatusCmd.Flags().StringP("addr", "a", ":9999", "Address the daemon is listening on (matches `serve --addr`)")
	serveStatusCmd.Flags().String("label", "com.msjurseth.stash.serve", "launchd label used to query registration state (macOS only)")
	serveStatusCmd.Flags().String("log", "", "Log path to tail for recent errors (default: ~/Library/Logs/stash-serve.log)")
	serveStatusCmd.Flags().Int("tail", 0, "Show the last N log lines (only on failure unless explicitly set)")

	serveCmd.AddCommand(serveTokenCmd)
	serveCmd.AddCommand(servePairCmd)
	serveCmd.AddCommand(serveStatusCmd)
	rootCmd.AddCommand(serveCmd)
}

func runServe(cmd *cobra.Command, _ []string) error {
	addr, _ := cmd.Flags().GetString("addr")
	advertise, _ := cmd.Flags().GetString("advertise")
	noQR, _ := cmd.Flags().GetBool("no-qr")

	stashDir := config.Dir()
	token, err := server.LoadOrCreateToken(stashDir)
	if err != nil {
		return err
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	fs := openFileStore()

	port, err := portFromAddr(addr)
	if err != nil {
		return err
	}
	if advertise == "" {
		advertise = server.FirstLANAddress()
		if advertise == "" {
			advertise = "localhost"
		}
	}

	// Usage ledger — persists per-call Gemini spend from the
	// identify worker so Mac + Android UIs can read /gemini-usage
	// and fold daemon spend into their per-device cost views.
	usageLedger := usage.New(stashDir)

	srv := &server.Server{
		Store:           s,
		Files:           fs,
		Token:           token,
		NewItemID:       newFetchID,
		NewSnippetID:    newFetchID,
		UsageLedgerPath: usageLedger.Path(),
		UsageRecorder:   usageLedger,
	}
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	if !noQR {
		printPairingBanner(cmd.OutOrStdout(), advertise, port, token)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "stash serve listening on %s (paired token at %s)\n",
			addr, server.TokenPath(stashDir))
	}

	// Graceful shutdown coordination. SIGINT/SIGTERM cancels ctx;
	// background workers (identify worker, etc.) observe the
	// cancellation, stop picking up new jobs, finish any in-flight
	// work, and signal completion via the WaitGroup. We then close
	// the HTTP server (its own 10s drain budget covers phone
	// uploads), and exit.
	//
	// Timing budget: the plist's ExitTimeOut=60s caps how long
	// launchd will wait before SIGKILL. A long Gemini identify
	// call is ~15-30s, an in-flight phone upload ~10s, so the 60s
	// budget covers a worst-case overlap with margin.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var workers sync.WaitGroup

	// Identify worker — polls for items tagged `needs-identify`
	// and runs Gemini on them. Idles when no API key is cached;
	// pauses cleanly on key rejection until `stash auth
	// refresh-gemini` runs. See internal/identify for the full
	// defensive-behavior contract.
	identifyWorker := identify.New(s, fs, gemini.New(), identify.Options{
		Recorder: usageLedger,
	})
	workers.Add(1)
	go func() {
		defer workers.Done()
		identifyWorker.Run(ctx)
	}()

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		fmt.Fprintln(cmd.OutOrStdout(), "\nshutting down…")
		// Phase 1: wait for any background workers to drain
		// in-flight jobs. Cap the wait so a wedged worker can't
		// hold the process open past launchd's ExitTimeOut.
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer drainCancel()
		drained := make(chan struct{})
		go func() {
			workers.Wait()
			close(drained)
		}()
		select {
		case <-drained:
		case <-drainCtx.Done():
			fmt.Fprintln(cmd.OutOrStdout(), "worker drain timed out; forcing close")
		}
		// Phase 2: HTTP graceful shutdown (10s internal budget).
		return server.ShutdownWithGrace(context.Background(), httpSrv)
	}
}

func runServePair(cmd *cobra.Command, _ []string) error {
	stashDir := config.Dir()
	token, err := server.LoadOrCreateToken(stashDir)
	if err != nil {
		return err
	}
	addr, _ := cmd.Flags().GetString("addr")
	advertise, _ := cmd.Flags().GetString("advertise")
	port, err := portFromAddr(addr)
	if err != nil {
		return err
	}
	if advertise == "" {
		advertise = server.FirstLANAddress()
		if advertise == "" {
			advertise = "localhost"
		}
	}
	uri := server.PairingURI(advertise, port, token)
	if flagJSON {
		printJSON(map[string]any{
			"host":  advertise,
			"port":  port,
			"token": token,
			"uri":   uri,
		})
		return nil
	}
	showQR, _ := cmd.Flags().GetBool("qr")
	if showQR {
		_ = server.RenderQRTerm(cmd.OutOrStdout(), uri)
		fmt.Fprintln(cmd.OutOrStdout())
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Server:  %s:%d\n", advertise, port)
	fmt.Fprintf(cmd.OutOrStdout(), "Token:   %s\n", token)
	fmt.Fprintf(cmd.OutOrStdout(), "URI:     %s\n", uri)
	return nil
}

func runServeToken(cmd *cobra.Command, _ []string) error {
	stashDir := config.Dir()
	rotate, _ := cmd.Flags().GetBool("rotate")
	var (
		tok string
		err error
	)
	if rotate {
		tok, err = server.RotateToken(stashDir)
	} else {
		tok, err = server.LoadOrCreateToken(stashDir)
	}
	if err != nil {
		return err
	}
	if flagJSON {
		printJSON(map[string]string{"token": tok, "path": server.TokenPath(stashDir)})
		return nil
	}
	if rotate {
		fmt.Fprintln(cmd.OutOrStdout(), "✓ Token rotated. Re-pair every paired device.")
	}
	fmt.Println(tok)
	return nil
}

func printPairingBanner(w interface {
	Write([]byte) (int, error)
}, host string, port int, token string) {
	uri := server.PairingURI(host, port, token)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Stash server listening — pair the mobile app with this QR:\n\n")
	_ = server.RenderQRTerm(w, uri)
	fmt.Fprintf(w, "\nOr enter manually:\n")
	fmt.Fprintf(w, "  Server:  %s:%d\n", host, port)
	fmt.Fprintf(w, "  Token:   %s\n", token)
	fmt.Fprintf(w, "  URI:     %s\n\n", uri)
}

func portFromAddr(addr string) (int, error) {
	// Accept ":9999", "host:9999", "127.0.0.1:9999".
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			n, err := strconv.Atoi(addr[i+1:])
			if err != nil {
				return 0, fmt.Errorf("port in %q: %w", addr, err)
			}
			return n, nil
		}
	}
	return 0, fmt.Errorf("addr %q has no port", addr)
}

// ServeStatus is the JSON shape emitted by `stash serve status --json`.
// Fields are best-effort: when a probe fails, the related fields are
// zero / empty and `Healthz.Error` carries the explanation.
type ServeStatus struct {
	Running     bool          `json:"running"`
	Port        int           `json:"port"`
	PID         int           `json:"pid,omitempty"`
	UptimeSecs  int64         `json:"uptime_seconds,omitempty"`
	TokenPath   string        `json:"token_path"`
	TokenMasked string        `json:"token_masked,omitempty"`
	Healthz     HealthzResult `json:"healthz"`
	Launchd     LaunchdState  `json:"launchd"`
	LogPath     string        `json:"log_path"`
	LogTail     []string      `json:"log_tail,omitempty"`
}

type HealthzResult struct {
	OK        bool   `json:"ok"`
	LatencyMs int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

type LaunchdState struct {
	Label    string `json:"label"`
	Loaded   bool   `json:"loaded"`
	LastExit int    `json:"last_exit"`
}

func runServeStatus(cmd *cobra.Command, _ []string) error {
	addr, _ := cmd.Flags().GetString("addr")
	label, _ := cmd.Flags().GetString("label")
	logPath, _ := cmd.Flags().GetString("log")
	tail, _ := cmd.Flags().GetInt("tail")

	port, err := portFromAddr(addr)
	if err != nil {
		return err
	}
	if logPath == "" {
		home, _ := os.UserHomeDir()
		logPath = filepath.Join(home, "Library", "Logs", "stash-serve.log")
	}

	stashDir := config.Dir()
	tokenPath := server.TokenPath(stashDir)
	tokenMasked := readMaskedToken(tokenPath)

	status := ServeStatus{
		Port:        port,
		TokenPath:   tokenPath,
		TokenMasked: tokenMasked,
		LogPath:     logPath,
		Launchd:     queryLaunchdState(label),
	}

	// Healthz probe is the source of truth for "is the daemon
	// actually serving" — a process can be bound to the port but
	// stuck before the listener returns 200s. The PID lookup is
	// secondary diagnostic info.
	status.Healthz = probeHealthz(port)
	status.Running = status.Healthz.OK

	if pid := findListenPID(port); pid > 0 {
		status.PID = pid
		if uptime, ok := processUptimeSeconds(pid); ok {
			status.UptimeSecs = uptime
		}
		// If the port has a listener but /healthz didn't answer,
		// the daemon is technically "up" — flag that distinct
		// state by reporting Running = true with Healthz.OK false.
		if !status.Running {
			status.Running = true
		}
	}

	// On failure, include the log tail by default so the user has
	// something to diagnose with. On success, only include it when
	// --tail N is set explicitly.
	if tail > 0 || (!status.Healthz.OK && tail == 0) {
		n := tail
		if n == 0 {
			n = 10
		}
		status.LogTail = tailFile(logPath, n)
	}

	if flagJSON {
		printJSON(status)
		if !status.Running {
			os.Exit(2)
		}
		return nil
	}

	printServeStatusHuman(cmd.OutOrStdout(), status)
	if !status.Running {
		os.Exit(2)
	}
	return nil
}

func printServeStatusHuman(w io.Writer, s ServeStatus) {
	if s.Running && s.Healthz.OK {
		fmt.Fprintln(w, "stash serve — running")
	} else if s.Running {
		fmt.Fprintln(w, "stash serve — process up but /healthz failed")
	} else {
		fmt.Fprintln(w, "stash serve — not running")
	}
	fmt.Fprintln(w)
	if s.PID > 0 {
		fmt.Fprintf(w, "  PID:        %d\n", s.PID)
	}
	fmt.Fprintf(w, "  Port:       %d\n", s.Port)
	if s.UptimeSecs > 0 {
		fmt.Fprintf(w, "  Uptime:     %s\n", humanDurationShort(s.UptimeSecs))
	}
	if s.TokenMasked != "" {
		fmt.Fprintf(w, "  Token:      %s (%s)\n", s.TokenMasked, s.TokenPath)
	} else {
		fmt.Fprintf(w, "  Token path: %s (no token file yet)\n", s.TokenPath)
	}
	if s.Healthz.OK {
		fmt.Fprintf(w, "  Healthz:    ok (%dms)\n", s.Healthz.LatencyMs)
	} else {
		errMsg := s.Healthz.Error
		if errMsg == "" {
			errMsg = "unreachable"
		}
		fmt.Fprintf(w, "  Healthz:    FAIL — %s\n", errMsg)
	}
	loadedStr := "not loaded"
	if s.Launchd.Loaded {
		loadedStr = "loaded"
		if s.Launchd.LastExit != 0 {
			loadedStr = fmt.Sprintf("loaded, last exit=%d", s.Launchd.LastExit)
		}
	}
	fmt.Fprintf(w, "  Launchd:    %s — %s\n", s.Launchd.Label, loadedStr)
	fmt.Fprintf(w, "  Log:        %s\n", s.LogPath)
	if len(s.LogTail) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  Recent log lines:\n")
		for _, line := range s.LogTail {
			fmt.Fprintf(w, "    %s\n", line)
		}
	}
}

// readMaskedToken reads the token file and returns a redacted form
// (`abcd…wxyz`) so the user can verify they're paired with the right
// instance without splattering the secret across `status` output that
// might end up in screenshots / paste-buffers.
func readMaskedToken(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	tok := strings.TrimSpace(string(b))
	if len(tok) < 12 {
		return tok
	}
	return tok[:4] + "…" + tok[len(tok)-4:]
}

// findListenPID returns the PID of the process listening on the given
// TCP port via `lsof`. Returns 0 if no process is listening or lsof is
// unavailable.
func findListenPID(port int) int {
	out, err := exec.Command(
		"lsof",
		"-iTCP:"+strconv.Itoa(port),
		"-sTCP:LISTEN",
		"-t", "-n", "-P",
	).Output()
	if err != nil {
		return 0
	}
	// `lsof -t` emits one PID per line. Take the first.
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && pid > 0 {
			return pid
		}
	}
	return 0
}

// processUptimeSeconds returns how long the process has been running,
// derived from `ps -o etime=`. Returns (0, false) on any failure —
// uptime is diagnostic, not load-bearing. macOS ps doesn't support
// the BSD-style `etimes` integer-seconds field, so the elapsed time
// arrives as `[[DD-]HH:]MM:SS` and is parsed here.
func processUptimeSeconds(pid int) (int64, bool) {
	out, err := exec.Command("ps", "-o", "etime=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, false
	}
	return parsePSEtime(strings.TrimSpace(string(out)))
}

// parsePSEtime parses the `ps etime` elapsed-time format used by
// macOS and most BSD/Linux variants: `[[DD-]HH:]MM:SS`. Returns the
// total elapsed seconds.
func parsePSEtime(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	var days int64
	if idx := strings.Index(s, "-"); idx >= 0 {
		d, err := strconv.ParseInt(s[:idx], 10, 64)
		if err != nil {
			return 0, false
		}
		days = d
		s = s[idx+1:]
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var hours, mins, secs int64
	if len(parts) == 3 {
		h, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return 0, false
		}
		hours = h
		parts = parts[1:]
	}
	m, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, false
	}
	mins = m
	sc, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, false
	}
	secs = sc
	return days*86400 + hours*3600 + mins*60 + secs, true
}

// queryLaunchdState shells out to `launchctl list <label>` and reports
// whether the service is registered. The exit-code semantics:
//
//	0  → label is loaded; stdout has the plist dict
//	113 (macOS) → label is unknown / not loaded
//
// Any non-zero is treated as "not loaded" so the report stays honest
// even when launchctl semantics shift between releases.
func queryLaunchdState(label string) LaunchdState {
	state := LaunchdState{Label: label}
	out, err := exec.Command("launchctl", "list", label).Output()
	if err != nil {
		return state
	}
	state.Loaded = true
	// Parse `LastExitStatus = N;` if present.
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "\"LastExitStatus\"") {
			parts := strings.Split(line, "=")
			if len(parts) == 2 {
				raw := strings.TrimSuffix(strings.TrimSpace(parts[1]), ";")
				if n, err := strconv.Atoi(raw); err == nil {
					state.LastExit = n
				}
			}
		}
	}
	return state
}

// probeHealthz does a short-timeout GET against /healthz. The endpoint
// is intentionally unauthenticated on the server side so this works
// without consulting the token file.
func probeHealthz(port int) HealthzResult {
	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	client := &http.Client{Timeout: 2 * time.Second}
	start := time.Now()
	resp, err := client.Get(url)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return HealthzResult{OK: false, Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return HealthzResult{
			OK:        false,
			LatencyMs: latency,
			Error:     fmt.Sprintf("HTTP %d", resp.StatusCode),
		}
	}
	return HealthzResult{OK: true, LatencyMs: latency}
}

// tailFile returns the last n lines of the named file, or nil on any
// error. Used by status to surface recent log entries without paging
// in the whole file.
func tailFile(path string, n int) []string {
	if n <= 0 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}

// humanDurationShort renders a duration in seconds as a compact
// "2h 34m" or "12d 4h" — coarsest two units that fit. Sub-minute
// durations show as "Ns". Distinct from the coarser
// `humanDuration(time.Duration)` used by resurface (which rounds to
// hours and skips minutes/seconds — not useful for a freshly-started
// daemon that's been up 14s).
func humanDurationShort(secs int64) string {
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	days := secs / 86400
	hours := (secs % 86400) / 3600
	mins := (secs % 3600) / 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}
