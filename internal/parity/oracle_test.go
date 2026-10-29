package parity

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// oracle reads files of the pinned LiteRouter commit with git, so no working
// file or v1 state is ever read.
type oracle struct {
	dir, pin string
}

// pinnedOracle returns the run's oracle (the commit in ORACLE_SHA), or skips when ORACLE_DIR is unset like
// the other dedicated parity gates. The CI parity job always sets it.
func pinnedOracle(t *testing.T) oracle {
	t.Helper()
	dir := os.Getenv("ORACLE_DIR")
	if dir == "" {
		t.Skip("pinned drift gate sets ORACLE_DIR; normal unit tests need no oracle")
	}
	// The run's commit is resolved once by compat/parity/resolve-oracle.mjs and
	// handed to every gate as ORACLE_SHA, so Go and node cannot disagree. There is
	// no default: a run without one fails rather than guessing.
	sha := os.Getenv("ORACLE_SHA")
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(sha) {
		t.Fatal("ORACLE_SHA must be the 40-hex oracle commit of this run (compat/parity/resolve-oracle.mjs)")
	}
	o := oracle{dir: dir, pin: sha}
	if got := strings.TrimSpace(o.git(t, "rev-parse", o.pin+"^{commit}")); got != o.pin {
		t.Fatalf("oracle does not contain the run's commit %s", o.pin)
	}
	return o
}

func (o oracle) git(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", o.dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

func (o oracle) file(t *testing.T, path string) string {
	t.Helper()
	return o.git(t, "show", o.pin+":"+path)
}

// files lists the file names directly inside a directory of the pinned commit.
func (o oracle) files(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	for _, name := range strings.Fields(o.git(t, "ls-tree", "--name-only", o.pin, strings.TrimRight(dir, "/")+"/")) {
		names = append(names, name[strings.LastIndex(name, "/")+1:])
	}
	sort.Strings(names)
	return names
}

// lineOf returns "path:line" for the first line matching pattern, for failure
// messages that point at the oracle source.
func lineOf(source, path, pattern string) string {
	re := regexp.MustCompile(pattern)
	for i, line := range strings.Split(source, "\n") {
		if re.MatchString(line) {
			return fmt.Sprintf("%s:%d", path, i+1)
		}
	}
	return path
}

// ratchet compares the gaps found against the tracked list. A gap not tracked is
// new drift; a tracked gap no longer found is stale and must be removed, so the
// list can only shrink.
func ratchet(t *testing.T, what string, found map[string]string, tracked map[string]string) {
	t.Helper()
	var untracked, stale []string
	for key, where := range found {
		if _, ok := tracked[key]; !ok {
			untracked = append(untracked, fmt.Sprintf("%s (%s)", key, where))
		}
	}
	for key := range tracked {
		if _, ok := found[key]; !ok {
			stale = append(stale, key)
		}
	}
	sort.Strings(untracked)
	sort.Strings(stale)
	if len(untracked) > 0 {
		t.Errorf("%s: LiteRouter has these and Routeweft does not, and they are not tracked:\n  %s", what, strings.Join(untracked, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("%s: these tracked gaps are closed or no longer in LiteRouter; remove them from the tracked list:\n  %s", what, strings.Join(stale, "\n  "))
	}
}
