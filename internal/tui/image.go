package tui

import (
	"os"
	"os/exec"
	"strings"
)

// renderAsciiImage shells out to asciizer to produce ANSI art from an image file.
// width is the desired output width in characters.
func renderAsciiImage(filePath string, width int) (string, error) {
	asciizer, err := exec.LookPath("asciizer")
	if err != nil {
		return "", err
	}

	cmd := exec.Command(asciizer, "-color", "-stdout", "-w", itoa(width), filePath)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return strings.TrimRight(string(out), "\n"), nil
}

func itoa(i int) string {
	if i <= 0 {
		return "80"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}
