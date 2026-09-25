package runtime_test

import (
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

var forbiddenPlatformImports = map[string]struct{}{
	"os":            {},
	"path/filepath": {},
	"syscall":       {},
	"log":           {},
}

func TestRuntimeDependencyBoundary(t *testing.T) {
	root := moduleRoot(t)
	deps := goList(t, root, "-deps", "./internal/runtime")

	// The filesystem backend and its filesystem-only standard packages must not
	// be linked into the embedded runtime.
	for _, dep := range deps {
		if dep == "github.com/chester-hill-solutions/stow/internal/storage/fs" || dep == "path/filepath" || dep == "log" {
			t.Fatalf("runtime dependency closure includes %q:\n%s", dep, strings.Join(deps, "\n"))
		}
	}

	// Check the packages' own imports as well as the module dependency closure.
	// Go's standard-library context/time closure can contain os and syscall on
	// some Go releases, so those transitive packages are not treated as
	// filesystem dependencies here; a direct import still fails the check.
	for _, packagePath := range []string{"./internal/runtime", "./internal/storage"} {
		imports := goList(t, root, "-f", "{{join .Imports \"\\n\"}}", packagePath)
		for _, imported := range imports {
			if _, forbidden := forbiddenPlatformImports[imported]; forbidden {
				t.Errorf("%s directly imports platform package %q", packagePath, imported)
			}
		}
	}

	// Keep the non-standard portion visible in test output: this is the part
	// of `go list -deps ./internal/runtime` owned by this module.
	nonStandard := goList(t, root, "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "./internal/runtime")
	for _, dep := range nonStandard {
		if _, forbidden := forbiddenPlatformImports[dep]; forbidden {
			t.Errorf("non-standard runtime dependency %q is platform-specific", dep)
		}
	}
	t.Logf("non-standard runtime dependencies: %s", strings.Join(nonStandard, ", "))
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func goList(t *testing.T, root string, args ...string) []string {
	t.Helper()
	commandArgs := append([]string{"list"}, args...)
	cmd := exec.Command("go", commandArgs...)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(commandArgs, " "), err, output)
	}
	var values []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			values = append(values, line)
		}
	}
	return values
}
