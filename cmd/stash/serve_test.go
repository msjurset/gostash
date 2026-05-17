package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParsePSEtime(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantOK  bool
		comment string
	}{
		{"00:08", 8, true, "8 seconds"},
		{"01:23", 83, true, "1m 23s"},
		{"51:19", 51*60 + 19, true, "51m 19s"},
		{"01:02:34", 3600 + 2*60 + 34, true, "1h 2m 34s"},
		{"12:00:00", 12 * 3600, true, "12 hours flat"},
		{"5-01:02:34", 5*86400 + 3600 + 2*60 + 34, true, "5d 1h 2m 34s"},
		{"10-00:00:00", 10 * 86400, true, "10 days flat"},
		{"", 0, false, "empty"},
		{"bogus", 0, false, "non-numeric"},
		{"1:2:3:4", 0, false, "too many segments"},
		{"5", 0, false, "single segment is not a valid etime"},
	}
	for _, tc := range cases {
		t.Run(tc.comment, func(t *testing.T) {
			got, ok := parsePSEtime(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("parsePSEtime(%q): ok = %v, want %v", tc.in, ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Errorf("parsePSEtime(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestHumanDurationShort(t *testing.T) {
	cases := []struct {
		secs int64
		want string
	}{
		{0, "0s"},
		{12, "12s"},
		{59, "59s"},
		{60, "1m"},
		{90, "1m"},
		{3599, "59m"},
		{3600, "1h 0m"},
		{3661, "1h 1m"},
		{2*3600 + 34*60, "2h 34m"},
		{86399, "23h 59m"},
		{86400, "1d 0h"},
		{5*86400 + 4*3600, "5d 4h"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			got := humanDurationShort(tc.secs)
			if got != tc.want {
				t.Errorf("humanDurationShort(%d) = %q, want %q", tc.secs, got, tc.want)
			}
		})
	}
}

func TestReadMaskedToken(t *testing.T) {
	dir := t.TempDir()

	// Short tokens shouldn't be masked — better to surface the
	// real value than to misleadingly hide nothing.
	short := filepath.Join(dir, "short.token")
	if err := os.WriteFile(short, []byte("abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readMaskedToken(short); got != "abc" {
		t.Errorf("short token: got %q, want %q", got, "abc")
	}

	// Standard 64-hex-char token gets masked to 4…4 form.
	full := filepath.Join(dir, "full.token")
	tok := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(full, []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := "0123…cdef"
	if got := readMaskedToken(full); got != want {
		t.Errorf("full token: got %q, want %q", got, want)
	}

	// Missing file → empty string.
	if got := readMaskedToken(filepath.Join(dir, "nope.token")); got != "" {
		t.Errorf("missing token file: got %q, want empty", got)
	}
}

func TestTailFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	contents := "a\nb\nc\nd\ne\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	// n larger than file: return everything.
	got := tailFile(path, 99)
	if want := []string{"a", "b", "c", "d", "e"}; !equalStrSlices(got, want) {
		t.Errorf("tail >=len: got %v, want %v", got, want)
	}

	// Standard tail.
	got = tailFile(path, 2)
	if want := []string{"d", "e"}; !equalStrSlices(got, want) {
		t.Errorf("tail 2: got %v, want %v", got, want)
	}

	// n=0 returns nil.
	if got := tailFile(path, 0); got != nil {
		t.Errorf("tail 0: got %v, want nil", got)
	}

	// Missing file returns nil.
	if got := tailFile(filepath.Join(dir, "nope"), 5); got != nil {
		t.Errorf("missing file: got %v, want nil", got)
	}
}

func equalStrSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
