package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/gemini"
	"golang.org/x/term"
)

var version = "dev"

func main() {
	for {
		err := rootCmd.Execute()
		if err == nil {
			break
		}

		var failoverErr *gemini.ErrFailoverApprovalRequired
		if errors.As(err, &failoverErr) {
			cfg := config.Get()
			dur := cfg.PaidApprovalDurationHours
			if dur <= 0 {
				dur = 24
			}

			if flagJSON {
				fmt.Fprintf(os.Stdout, `{"error":"failover_approval_required","operation":%q,"duration":%d}`+"\n", failoverErr.Operation, dur)
				os.Exit(1)
			}

			if !term.IsTerminal(int(os.Stdin.Fd())) {
				fmt.Fprintf(os.Stderr, "Error: failover approval required for %q, but running non-interactively.\n", failoverErr.Operation)
				os.Exit(1)
			}

			fmt.Fprintf(os.Stderr, "\n[Quota Exhausted] Your free tier quota is depleted.\n")
			fmt.Fprintf(os.Stderr, "You have a paid API key configured. Do you want to use it for the %q operation?\n", failoverErr.Operation)
			fmt.Fprintf(os.Stderr, "This approval will last for %d hours for this operation.\n", dur)
			fmt.Fprintf(os.Stderr, "Proceed? [y/N]: ")

			reader := bufio.NewReader(os.Stdin)
			ans, _ := reader.ReadString('\n')
			ans = strings.TrimSpace(strings.ToLower(ans))

			if ans == "y" || ans == "yes" {
				st, storeErr := openStore()
				if storeErr == nil {
					expires := time.Now().UTC().Add(time.Duration(dur) * time.Hour)
					_ = st.ApproveFailover(context.Background(), failoverErr.Operation, expires)
					st.Close()
					fmt.Fprintf(os.Stderr, "Approved. Resuming operation...\n\n")
					continue // Re-run the command
				} else {
					fmt.Fprintf(os.Stderr, "Error opening store to save approval: %v\n", storeErr)
				}
			}
			fmt.Fprintf(os.Stderr, "Operation aborted.\n")
			os.Exit(1)
		}

		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

