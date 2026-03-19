package extract

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// ArchiveEntry represents a single file/directory in an archive.
type ArchiveEntry struct {
	Name string
	Size int64
	Dir  bool
}

// ListTarGz lists entries in a .tar.gz file.
func ListTarGz(r io.Reader) ([]ArchiveEntry, error) {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer gr.Close()

	var entries []ArchiveEntry
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}
		entries = append(entries, ArchiveEntry{
			Name: hdr.Name,
			Size: hdr.Size,
			Dir:  hdr.Typeflag == tar.TypeDir,
		})
	}
	return entries, nil
}

// ListZip lists entries in a .zip file by path.
func ListZip(path string) ([]ArchiveEntry, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}
	defer zr.Close()

	var entries []ArchiveEntry
	for _, f := range zr.File {
		entries = append(entries, ArchiveEntry{
			Name: f.Name,
			Size: int64(f.UncompressedSize64),
			Dir:  f.FileInfo().IsDir(),
		})
	}
	return entries, nil
}

// ListArchiveFile lists entries from an archive file at the given path based on MIME type.
func ListArchiveFile(path, mimeType string) ([]ArchiveEntry, error) {
	switch {
	case strings.Contains(mimeType, "gzip") || strings.Contains(mimeType, "tar"):
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return ListTarGz(f)
	case strings.Contains(mimeType, "zip"):
		return ListZip(path)
	default:
		return nil, nil
	}
}

// FormatTree renders archive entries as an indented tree string.
func FormatTree(entries []ArchiveEntry) string {
	if len(entries) == 0 {
		return ""
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	// Build tree structure
	type node struct {
		name     string
		size     int64
		dir      bool
		children []*node
		childMap map[string]*node
	}

	root := &node{childMap: make(map[string]*node)}

	for _, e := range entries {
		parts := strings.Split(strings.TrimSuffix(e.Name, "/"), "/")
		cur := root
		for i, p := range parts {
			if p == "" {
				continue
			}
			child, ok := cur.childMap[p]
			if !ok {
				isDir := e.Dir || i < len(parts)-1
				child = &node{name: p, dir: isDir, childMap: make(map[string]*node)}
				cur.childMap[p] = child
				cur.children = append(cur.children, child)
			}
			if i == len(parts)-1 {
				child.size = e.Size
				child.dir = e.Dir
			}
			cur = child
		}
	}

	var buf strings.Builder
	var walk func(n *node, prefix string, last bool)
	walk = func(n *node, prefix string, last bool) {
		connector := "├── "
		if last {
			connector = "└── "
		}

		if n.name != "" {
			label := n.name
			if n.dir {
				label += "/"
			}
			fmt.Fprintf(&buf, "%s%s%s\n", prefix, connector, label)
		}

		childPrefix := prefix
		if n.name != "" {
			if last {
				childPrefix += "    "
			} else {
				childPrefix += "│   "
			}
		}

		sort.Slice(n.children, func(i, j int) bool {
			// Directories first, then alphabetical
			if n.children[i].dir != n.children[j].dir {
				return n.children[i].dir
			}
			return n.children[i].name < n.children[j].name
		})

		for i, child := range n.children {
			walk(child, childPrefix, i == len(n.children)-1)
		}
	}

	// If there's a single top-level dir, print it as root
	if len(root.children) == 1 && root.children[0].dir {
		topDir := root.children[0]
		fmt.Fprintf(&buf, "%s/\n", topDir.name)
		sort.Slice(topDir.children, func(i, j int) bool {
			if topDir.children[i].dir != topDir.children[j].dir {
				return topDir.children[i].dir
			}
			return topDir.children[i].name < topDir.children[j].name
		})
		for i, child := range topDir.children {
			walk(child, "", i == len(topDir.children)-1)
		}
	} else {
		for i, child := range root.children {
			walk(child, "", i == len(root.children)-1)
		}
	}

	return buf.String()
}
