// Command quality records and checks ratcheted Go maintainability metrics.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const baselineVersion = 1

type violation struct {
	Rule     string `json:"rule"`
	Identity string `json:"identity"`
	Message  string `json:"message"`
}

type report struct {
	Version    int            `json:"version"`
	Violations []violation    `json:"violations"`
	Counts     map[string]int `json:"counts"`
}

func main() {
	var writeBaseline bool
	var baselinePath string
	flag.BoolVar(&writeBaseline, "write-baseline", false, "write the current quality baseline")
	flag.StringVar(&baselinePath, "baseline", filepath.Join("scripts", "baselines", "go-quality.json"), "baseline path")
	flag.Parse()

	current, err := scan()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	if writeBaseline {
		data, err := json.MarshalIndent(current, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := os.MkdirAll(filepath.Dir(baselinePath), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := os.WriteFile(baselinePath, append(data, '\n'), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Printf("Wrote %s (%d violations)\n", baselinePath, len(current.Violations))
		return
	}

	baselineBytes, err := os.ReadFile(baselinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "quality baseline %s is missing: %v\n", baselinePath, err)
		os.Exit(2)
	}
	var baseline report
	if err := json.Unmarshal(baselineBytes, &baseline); err != nil {
		fmt.Fprintf(os.Stderr, "decode quality baseline: %v\n", err)
		os.Exit(2)
	}
	if baseline.Version != baselineVersion {
		fmt.Fprintf(os.Stderr, "unsupported quality baseline version %d\n", baseline.Version)
		os.Exit(2)
	}

	if previous, ok := previousBaseline(baselinePath); ok {
		if problems := countIncreases(current, previous); len(problems) > 0 {
			for _, problem := range problems {
				fmt.Fprintln(os.Stderr, problem)
			}
			os.Exit(1)
		}
	}

	if problems := ratchetProblems(current, baseline); len(problems) > 0 {
		fmt.Fprintln(os.Stderr, "Go quality ratchet violation")
		for _, problem := range problems {
			fmt.Fprintln(os.Stderr, problem)
		}
		fmt.Fprintln(os.Stderr, "Fix the violation; regenerate the baseline only after intentional debt reduction.")
		os.Exit(1)
	}
	fmt.Printf("Go quality ratchet OK (%d baseline entries)\n", len(current.Violations))
}

// countIncreases reports the rules whose violation count rose against a previous
// report. A rule that current no longer has is not an increase, and one that
// previous never had counts as an increase from zero.
//
// This is the check that makes a baseline impossible to grow quietly: identities
// are matched exactly, so a violation can be renamed to shed its entry, but the
// per-rule totals still catch it.
func countIncreases(current, previous report) []string {
	var problems []string
	for rule, count := range current.Counts {
		if count > previous.Counts[rule] {
			problems = append(problems, fmt.Sprintf("quality baseline increased: %s (%d > %d)", rule, count, previous.Counts[rule]))
		}
	}
	sort.Strings(problems)
	return problems
}

// ratchetProblems reports every identity that is new since the baseline and
// every baseline entry that is now stale. Both are failures: a new identity is
// unapproved debt, and a stale one is debt that was paid and not recorded, which
// is what forces the baseline down after a fix.
func ratchetProblems(current, baseline report) []string {
	allowed := make(map[string]struct{}, len(baseline.Violations))
	for _, item := range baseline.Violations {
		allowed[item.Identity] = struct{}{}
	}
	actual := make(map[string]struct{}, len(current.Violations))
	var problems []string
	for _, item := range current.Violations {
		actual[item.Identity] = struct{}{}
		if _, ok := allowed[item.Identity]; !ok {
			problems = append(problems, fmt.Sprintf("new: %s (%s)", item.Identity, item.Message))
		}
	}
	for _, item := range baseline.Violations {
		if _, ok := actual[item.Identity]; !ok {
			problems = append(problems, "stale: "+item.Identity)
		}
	}
	sort.Strings(problems)
	return problems
}

func previousBaseline(path string) (report, bool) {
	ref := strings.TrimSpace(os.Getenv("STOW_BASELINE_REF"))
	if ref == "" {
		ref = "HEAD^"
	}
	if _, err := exec.Command("git", "rev-parse", "--verify", ref).Output(); err != nil {
		fmt.Fprintf(os.Stderr, "cannot resolve ratchet base %s; check out full history or set STOW_BASELINE_REF\\n", ref)
		os.Exit(2)
	}
	command := exec.Command("git", "show", ref+":"+filepath.ToSlash(path))
	data, err := command.Output()
	if err != nil {
		return report{}, false
	}
	var previous report
	if json.Unmarshal(data, &previous) != nil || previous.Version != baselineVersion {
		return report{}, false
	}
	return previous, true
}

// scan is where the tree comes from, so a test can point the same analysis at a
// fixture instead of at the repository. A fixture inside the repository would be
// scanned by the gate itself and would need a baseline entry of its own, which is
// a debt recorded in order to test the thing that records debt.
//
// The root list is shared with the file-size ratchet through
// config/scan-roots.json. They each had their own and they disagreed: tools/ and
// scripts/ were in neither, so the code that checks the code was itself unchecked.
func scan() (report, error) {
	roots, err := scanRootsFromConfig()
	if err != nil {
		return report{}, err
	}
	return scanRoots(roots)
}

// scanRootsFromConfig reads the Go roots from config/scan-roots.json. A missing or
// malformed file is an error rather than a fallback: falling back to a built-in
// list would restore the two-copies problem this exists to remove, quietly.
func scanRootsFromConfig() ([]string, error) {
	path := filepath.Join(repoRoot(), "config", "scan-roots.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read scan roots: %w", err)
	}
	var config struct {
		Go []string `json:"go"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if len(config.Go) == 0 {
		return nil, fmt.Errorf("%s lists no go roots", path)
	}
	return config.Go, nil
}

// repoRoot is the directory containing go.mod, found by walking up from the
// working directory so the tool works from anywhere in the tree.
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

func scanRoots(roots []string) (report, error) {
	result := report{Version: baselineVersion, Counts: map[string]int{}}
	seen := map[string]int{}
	for _, root := range roots {
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			return scanFile(path, &result, seen)
		}); err != nil {
			return report{}, err
		}
	}
	sort.Slice(result.Violations, func(i, j int) bool {
		return result.Violations[i].Identity < result.Violations[j].Identity
	})
	return result, nil
}

// checker accumulates the violations found in one file.
//
// It is a type rather than a set of closures inside scanFile because a closure's
// branches count towards the enclosing function's complexity, and scanFile was
// over the ceiling for exactly that reason: it was holding four closures whose
// logic had nothing to do with walking a file.
type checker struct {
	path   string
	fset   *token.FileSet
	result *report
	seen   map[string]int
}

func (c *checker) add(rule, identity, message string) {
	occurrenceKey := rule + "\x00" + identity
	c.seen[occurrenceKey]++
	identity = fmt.Sprintf("%s#%d", identity, c.seen[occurrenceKey])
	c.result.Violations = append(c.result.Violations, violation{Rule: rule, Identity: identity, Message: message})
	c.result.Counts[rule]++
}

func (c *checker) checkFunction(name string, body *ast.BlockStmt, params *ast.FieldList, start int) {
	if body == nil {
		return
	}
	end := c.fset.Position(body.End()).Line
	lines := end - start + 1
	complexity := cyclomaticComplexity(body)
	paramCount := parameterCount(params)
	base := fmt.Sprintf("%s:%s", filepath.ToSlash(c.path), name)
	if lines > 200 {
		c.add("function-lines", base, fmt.Sprintf("function %s has %d lines (maximum 200)", name, lines))
	}
	if complexity > 15 {
		c.add("complexity", base, fmt.Sprintf("function %s has complexity %d (maximum 15)", name, complexity))
	}
	if paramCount > 5 {
		c.add("max-params", base, fmt.Sprintf("function %s has %d parameters (maximum 5)", name, paramCount))
	}
}

// checkLiterals reports the function literals nested inside a function. They are
// real functions with their own length, complexity and parameter count, and a
// closure-heavy function can otherwise hide all three behind its parent's numbers.
func (c *checker) checkLiterals(fn *ast.FuncDecl) {
	literalNumber := 0
	var walk func(ast.Node)
	walk = func(node ast.Node) {
		ast.Inspect(node, func(child ast.Node) bool {
			literal, isLiteral := child.(*ast.FuncLit)
			if !isLiteral {
				return true
			}
			literalNumber++
			name := fmt.Sprintf("%s/func-literal-%d", fn.Name.Name, literalNumber)
			c.checkFunction(name, literal.Body, literal.Type.Params, c.fset.Position(literal.Pos()).Line)
			walk(literal.Body)
			return false
		})
	}
	walk(fn.Body)
}

// checkEscapes reports the two type and safety escape hatches: `any` and `panic`.
func (c *checker) checkEscapes(file *ast.File) {
	location := filepath.ToSlash(c.path)
	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.Ident:
			if typed.Name == "any" {
				c.add("any", location, "use of any")
			}
		case *ast.CallExpr:
			if ident, ok := typed.Fun.(*ast.Ident); ok && ident.Name == "panic" {
				c.add("panic", location, "panic call")
			}
		}
		return true
	})
}

func scanFile(path string, result *report, seen map[string]int) error {
	source, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, source, 0)
	if err != nil {
		return err
	}

	c := &checker{path: path, fset: fset, result: result, seen: seen}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		c.checkFunction(fn.Name.Name, fn.Body, fn.Type.Params, fset.Position(fn.Pos()).Line)
		c.checkLiterals(fn)
	}
	c.checkEscapes(file)
	return nil
}

func cyclomaticComplexity(body *ast.BlockStmt) int {
	complexity := 1
	ast.Inspect(body, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
			complexity++
		case *ast.CaseClause:
			if typed.List != nil {
				complexity++
			}
		case *ast.CommClause:
			if typed.Comm != nil {
				complexity++
			}
		case *ast.BinaryExpr:
			if typed.Op == token.LAND || typed.Op == token.LOR {
				complexity++
			}
		}
		return true
	})
	return complexity
}

func parameterCount(params *ast.FieldList) int {
	if params == nil {
		return 0
	}
	count := 0
	for _, field := range params.List {
		if len(field.Names) == 0 {
			count++
			continue
		}
		count += len(field.Names)
	}
	return count
}
