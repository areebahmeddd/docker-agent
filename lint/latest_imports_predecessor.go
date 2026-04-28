package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/dgageot/rubocop-go/cop"
)

// LatestImportsPredecessor enforces that files under pkg/config/latest/ that
// import a historical config version package (pkg/config/vN) only ever
// import the immediate predecessor: the highest vN under pkg/config/.
//
// The existing Lint/ConfigVersionImport cop only verifies that *numbered*
// versions follow the v0 → v1 → v2 → … chain. It accepts any vN inside
// pkg/config/latest/. This cop closes that gap and ensures pkg/config/latest
// also obeys the chain (i.e. latest imports the highest vN, never an older
// version), which is required for the upgrade pipeline to reach the latest
// schema in a single hop.
type LatestImportsPredecessor struct{}

func (*LatestImportsPredecessor) Name() string { return "Lint/LatestImportsPredecessor" }
func (*LatestImportsPredecessor) Description() string {
	return "pkg/config/latest must only import its immediate predecessor (highest vN)"
}
func (*LatestImportsPredecessor) Severity() cop.Severity { return cop.Error }

var versionedImportRe = regexp.MustCompile(`pkg/config/v(\d+)$`)

func (c *LatestImportsPredecessor) Check(fset *token.FileSet, file *ast.File) []cop.Offense {
	if len(file.Imports) == 0 {
		return nil
	}
	filename := fset.Position(file.Package).Filename
	normalized := filepath.ToSlash(filename)
	if !isLatestPath(normalized) {
		return nil
	}

	highest, ok := highestSiblingVersion(filename)
	if !ok {
		// Cannot determine highest sibling vN; skip silently.
		return nil
	}

	var offenses []cop.Offense
	for _, imp := range file.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		m := versionedImportRe.FindStringSubmatch(importPath)
		if m == nil {
			continue
		}
		got, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if got == highest {
			continue
		}
		offenses = append(offenses, cop.NewOffense(c, fset, imp.Path.Pos(), imp.Path.End(),
			fmt.Sprintf("pkg/config/latest must import its predecessor v%d, not v%d", highest, got)))
	}
	return offenses
}

var (
	highestCacheMu sync.Mutex
	highestCache   = map[string]int{}
)

// highestSiblingVersion returns the largest N such that pkg/config/vN/ exists
// among the siblings of the file's directory.
func highestSiblingVersion(filename string) (int, bool) {
	abs, err := filepath.Abs(filename)
	if err != nil {
		return 0, false
	}
	configDir := filepath.Dir(filepath.Dir(abs)) // strip /<pkg>/<file.go>

	highestCacheMu.Lock()
	defer highestCacheMu.Unlock()
	if v, ok := highestCache[configDir]; ok {
		return v, v >= 0
	}

	entries, err := os.ReadDir(configDir)
	if err != nil {
		highestCache[configDir] = -1
		return 0, false
	}

	highest := -1
	re := regexp.MustCompile(`^v(\d+)$`)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m := re.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if n > highest {
			highest = n
		}
	}

	highestCache[configDir] = highest
	return highest, highest >= 0
}
