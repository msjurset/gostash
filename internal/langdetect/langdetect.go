package langdetect

import (
	"strings"
)

// Detect returns the most likely programming language for the given content,
// or an empty string if uncertain.
func Detect(content string) string {
	if content == "" {
		return ""
	}

	lines := strings.SplitN(content, "\n", 20)
	first := ""
	if len(lines) > 0 {
		first = strings.TrimSpace(lines[0])
	}

	// 1. Shebang detection
	if strings.HasPrefix(first, "#!") {
		return detectShebang(first)
	}

	// 2. Structural markers
	if lang := detectStructure(content, lines); lang != "" {
		return lang
	}

	// 3. Keyword scoring
	return detectKeywords(content)
}

func detectShebang(line string) string {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "python"):
		return "python"
	case strings.Contains(lower, "node"):
		return "javascript"
	case strings.Contains(lower, "ruby"):
		return "ruby"
	case strings.Contains(lower, "perl"):
		return "perl"
	case strings.Contains(lower, "bash"), strings.Contains(lower, "/sh"):
		return "shell"
	case strings.Contains(lower, "zsh"):
		return "shell"
	case strings.Contains(lower, "php"):
		return "php"
	default:
		return "shell"
	}
}

func detectStructure(content string, lines []string) string {
	first := ""
	if len(lines) > 0 {
		first = strings.TrimSpace(lines[0])
	}

	lower := strings.ToLower(content)

	// Go
	if strings.HasPrefix(first, "package ") {
		return "go"
	}

	// HTML/XML
	if strings.HasPrefix(first, "<!DOCTYPE") || strings.HasPrefix(first, "<html") || strings.HasPrefix(first, "<?xml") {
		return "html"
	}

	// YAML frontmatter
	if first == "---" {
		return "yaml"
	}

	// JSON
	if (strings.HasPrefix(first, "{") || strings.HasPrefix(first, "[")) && isBalanced(content) {
		return "json"
	}

	// SQL
	sqlKeywords := []string{"select ", "insert ", "update ", "delete ", "create table", "alter table", "drop table"}
	for _, kw := range sqlKeywords {
		if strings.HasPrefix(lower, kw) || strings.Contains(lower, "\n"+kw) {
			return "sql"
		}
	}

	// Python
	if hasPythonStructure(content, lines) {
		return "python"
	}

	// Rust
	if strings.Contains(content, "fn main()") || (strings.Contains(content, "fn ") && strings.Contains(content, "-> ")) {
		return "rust"
	}

	// JavaScript/TypeScript
	if strings.Contains(content, "import React") || strings.Contains(content, "from 'react'") {
		return "jsx"
	}
	if strings.Contains(content, "interface ") && strings.Contains(content, ": string") {
		return "typescript"
	}
	if hasJSStructure(content) {
		return "javascript"
	}

	// CSS
	if strings.Contains(content, "{") && (strings.Contains(lower, "color:") || strings.Contains(lower, "margin:") || strings.Contains(lower, "display:")) {
		return "css"
	}

	// Dockerfile
	if strings.HasPrefix(first, "FROM ") && (strings.Contains(content, "RUN ") || strings.Contains(content, "COPY ")) {
		return "dockerfile"
	}

	// Makefile
	if strings.Contains(content, ".PHONY") || (strings.Contains(content, ":\n\t") && !strings.Contains(content, "{")) {
		return "makefile"
	}

	return ""
}

func hasPythonStructure(content string, lines []string) bool {
	markers := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "def ") && strings.Contains(trimmed, "):") {
			markers++
		}
		if strings.HasPrefix(trimmed, "class ") && strings.HasSuffix(trimmed, ":") {
			markers++
		}
		if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "from ") {
			markers++
		}
	}
	return markers >= 2
}

func hasJSStructure(content string) bool {
	markers := 0
	if strings.Contains(content, "const ") || strings.Contains(content, "let ") {
		markers++
	}
	if strings.Contains(content, "=> ") || strings.Contains(content, "function ") {
		markers++
	}
	if strings.Contains(content, "console.log") || strings.Contains(content, "require(") || strings.Contains(content, "import ") {
		markers++
	}
	return markers >= 2
}

func isBalanced(s string) bool {
	depth := 0
	for _, c := range s {
		switch c {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		}
		if depth < 0 {
			return false
		}
	}
	return depth == 0
}

var langKeywords = map[string][]string{
	"python":     {"def ", "self.", "print(", "elif ", "__init__", "lambda ", "None", "True", "False"},
	"go":         {"func ", "fmt.", "err != nil", "package ", ":= ", "go func", "defer ", "chan "},
	"javascript": {"const ", "let ", "var ", "function ", "=> ", "console.", "require(", "async "},
	"typescript": {"interface ", ": string", ": number", ": boolean", "readonly ", "enum ", "type "},
	"ruby":       {"def ", "end\n", "puts ", "require ", "attr_", "do |", "class ", ".each "},
	"java":       {"public ", "private ", "protected ", "class ", "void ", "System.out", "throws "},
	"c":          {"#include ", "int main", "printf(", "malloc(", "sizeof(", "NULL", "void "},
	"cpp":        {"#include ", "std::", "cout ", "nullptr", "template<", "class ", "namespace "},
	"rust":       {"fn ", "let mut ", "impl ", "pub fn", "match ", "Some(", "None", "println!"},
	"shell":      {"echo ", "if [", "fi\n", "done\n", "then\n", "export ", "#!/"},
	"php":        {"<?php", "$this->", "function ", "echo ", "->", "namespace "},
	"swift":      {"func ", "var ", "let ", "guard ", "import ", "struct ", "protocol "},
	"kotlin":     {"fun ", "val ", "var ", "class ", "override ", "companion "},
	"sql":        {"SELECT ", "FROM ", "WHERE ", "INSERT ", "UPDATE ", "JOIN ", "GROUP BY"},
	"css":        {"color:", "margin:", "padding:", "display:", "background:", "font-"},
}

func detectKeywords(content string) string {
	best := ""
	bestScore := 0

	for lang, keywords := range langKeywords {
		score := 0
		for _, kw := range keywords {
			if strings.Contains(content, kw) {
				score++
			}
		}
		if score > bestScore && score >= 3 {
			bestScore = score
			best = lang
		}
	}
	return best
}
