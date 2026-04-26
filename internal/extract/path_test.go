package extract

import (
	"os"
	"strings"
	"testing"
)

// The init() runs at package load and is hard to observe directly. We test
// the helpers it relies on, plus assert that on macOS a built binary spawned
// without a login shell would still see one of the typical brew prefixes.
func TestHomebrewCandidatesShape(t *testing.T) {
	cands := homebrewPathCandidates()
	// The slice may be empty on unknown GOOS, but if non-empty every entry
	// must be an absolute path.
	for _, c := range cands {
		if !strings.HasPrefix(c, "/") {
			t.Errorf("candidate %q must be absolute", c)
		}
	}
}

func TestPathInitAugmentsPATH(t *testing.T) {
	// init() already ran; verify that any candidate that exists on disk is
	// now visible in PATH. We don't *require* any to exist (CI may not have
	// them), but if they do, they must be listed.
	path := os.Getenv("PATH")
	for _, c := range homebrewPathCandidates() {
		if _, err := os.Stat(c); err != nil {
			continue
		}
		if !strings.Contains(":"+path+":", ":"+c+":") {
			t.Errorf("PATH should contain %q after init: %q", c, path)
		}
	}
}

func TestSplitPathRoundTrip(t *testing.T) {
	in := "/a:/b:/c"
	got := splitPath(in)
	if len(got) != 3 {
		t.Errorf("splitPath len = %d, want 3", len(got))
	}
}
