package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/server"

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

All non-/healthz paths require ` + "`Authorization: Bearer <token>`" + `.`,
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

func init() {
	serveCmd.Flags().StringP("addr", "a", ":9999", "Listen address (host:port). Bind to a specific interface with e.g. 192.168.1.10:9999.")
	serveCmd.Flags().String("advertise", "", "Hostname / IP advertised in the pairing QR (default: first non-loopback IPv4)")
	serveCmd.Flags().Bool("no-qr", false, "Don't print the pairing QR on startup")

	serveTokenCmd.Flags().Bool("rotate", false, "Generate a fresh token, invalidating all paired devices")

	servePairCmd.Flags().StringP("addr", "a", ":9999", "Address the daemon is listening on (matches `serve --addr`)")
	servePairCmd.Flags().String("advertise", "", "Override the advertised host (default: first non-loopback IPv4)")
	servePairCmd.Flags().Bool("qr", false, "Render the QR to the terminal in addition to the URI")

	serveCmd.AddCommand(serveTokenCmd)
	serveCmd.AddCommand(servePairCmd)
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

	srv := &server.Server{
		Store:        s,
		Files:        fs,
		Token:        token,
		NewItemID:    newFetchID,
		NewSnippetID: newFetchID,
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

	// Catch SIGINT / SIGTERM for graceful shutdown — in-flight
	// uploads get up to 10 seconds to finish.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-stop:
		fmt.Fprintln(cmd.OutOrStdout(), "\nshutting down…")
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
