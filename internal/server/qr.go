package server

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// PairingURI is the URL the mobile app reads from the QR code to
// pair with this server. Custom scheme so the Android app can register
// a deep-link handler — scanning the QR with the system camera opens
// the app directly to the "Add server" screen.
//
//   stash-pair://<host>:<port>?token=<hex>
func PairingURI(host string, port int, token string) string {
	u := url.URL{
		Scheme: "stash-pair",
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
	}
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()
	return u.String()
}

// RenderQRTerm writes a low-density ASCII-art QR code to w sized for
// a typical terminal window (~37 lines tall). Two-character glyphs
// per module keep the QR readable at a normal font size — most
// terminal fonts are roughly half-width, so 2 chars wide ≈ 1 module
// tall.
func RenderQRTerm(w io.Writer, content string) error {
	qr, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		return fmt.Errorf("encode qr: %w", err)
	}
	bitmap := qr.Bitmap()
	// Pair rows so each glyph cell is half-height — gives roughly
	// square modules on most terminals.
	for y := 0; y < len(bitmap); y += 2 {
		var b strings.Builder
		for x := 0; x < len(bitmap[y]); x++ {
			top := bitmap[y][x]
			var bot bool
			if y+1 < len(bitmap) {
				bot = bitmap[y+1][x]
			}
			b.WriteString(halfBlock(top, bot))
		}
		if _, err := fmt.Fprintln(w, b.String()); err != nil {
			return err
		}
	}
	return nil
}

// halfBlock returns the Unicode block char that represents a vertical
// pair of QR modules. ANSI inverse-video isn't needed — the block
// chars carry the contrast themselves.
func halfBlock(top, bot bool) string {
	switch {
	case top && bot:
		return "█"
	case top && !bot:
		return "▀"
	case !top && bot:
		return "▄"
	default:
		return " "
	}
}

// FirstLANAddress returns the first non-loopback IPv4 address bound
// to an interface that's up. Used by `stash serve` to advertise a
// useful address in the pairing QR — the user probably doesn't want
// 127.0.0.1 even though the server binds to 0.0.0.0 by default.
// Returns "" when no eligible interface is found (caller should fall
// back to a manual --advertise flag or just `localhost`).
func FirstLANAddress() string {
	ifs, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifs {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip, ok := addr.(*net.IPNet)
			if !ok || ip.IP.IsLoopback() {
				continue
			}
			v4 := ip.IP.To4()
			if v4 == nil {
				continue
			}
			// Skip link-local (169.254.x.x). Most user networks
			// are 192.168.x.x or 10.x.x.x and those work over
			// WireGuard too once the tunnel is up.
			if v4[0] == 169 && v4[1] == 254 {
				continue
			}
			return v4.String()
		}
	}
	return ""
}
