package extract

import (
	"os"
	"runtime"
	"strings"
)

// init prepends well-known Homebrew (/opt/homebrew/bin, /usr/local/bin) and
// MacPorts (/opt/local/bin) directories to PATH if they aren't already
// listed. This is a no-op when invoked from a normal interactive shell
// (which already has them) but matters for macOS .app bundles, which
// inherit a stripped-down PATH from launchd and otherwise can't find
// pdftoppm / tesseract.
func init() {
	candidates := homebrewPathCandidates()
	if len(candidates) == 0 {
		return
	}
	current := os.Getenv("PATH")
	parts := splitPath(current)
	known := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		known[p] = struct{}{}
	}
	added := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if _, ok := known[c]; ok {
			continue
		}
		if _, err := os.Stat(c); err != nil {
			continue
		}
		added = append(added, c)
	}
	if len(added) == 0 {
		return
	}
	if current != "" {
		os.Setenv("PATH", current+pathListSeparator()+strings.Join(added, pathListSeparator()))
	} else {
		os.Setenv("PATH", strings.Join(added, pathListSeparator()))
	}
}

func homebrewPathCandidates() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin"}
	case "linux":
		return []string{"/usr/local/bin", "/home/linuxbrew/.linuxbrew/bin"}
	}
	return nil
}

func pathListSeparator() string {
	if runtime.GOOS == "windows" {
		return ";"
	}
	return ":"
}

func splitPath(p string) []string {
	if p == "" {
		return nil
	}
	return strings.Split(p, pathListSeparator())
}
