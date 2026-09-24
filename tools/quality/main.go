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
		for rule, count := range current.Counts {
			if count > previous.Counts[rule] {
				fmt.Fprintf(os.Stderr, "quality baseline increased: %s (%d > %d)\n", rule, count, previous.Counts[rule])
				os.Exit(1)
			}
		}
	}

	allowed := make(map[string]struct{}, len(baseline.Violations))
	for _, item := range baseline.Violations {
		allowed[item.Identity] = struct{}{}
	}
	actual := make(map[string]struct{}, len(current.Violations))
	var newItems, staleItems []string
	for _, item := range current.Violations {
		actual[item.Identity] = struct{}{}
		if _, ok := allowed[item.Identity]; !ok {
			newItems = append(newItems, fmt.Sprintf("new: %s (%s)", item.Identity, item.Message))
		}
	}
	for _, item := range baseline.Violations {
		if _, ok := actual[item.Identity]; !ok {
			staleItems = append(staleItems, "stale: "+item.Identity)
		}
	}
	if len(newItems) > 0 || len(staleItems) > 0 {
		fmt.Fprintln(os.Stderr, "Go quality ratchet violation")
		for _, item := range newItems {
			fmt.Fprintln(os.Stderr, item)
		}
		for _, item := range staleItems {
			fmt.Fprintln(os.Stderr, item)
		}
		fmt.Fprintln(os.Stderr, "Fix the violation; regenerate the baseline only after intentional debt reduction.")
		os.Exit(1)
	}
	fmt.Printf("Go quality ratchet OK (%d baseline entries)\n", len(current.Violations))
}

func previousBaseline(path string) (report, bool) {
	command := exec.Command("git", "show", "HEAD:"+filepath.ToSlash(path))
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

func scan() (report, error) {
	result := report{Version: baselineVersion, Counts: map[string]int{}}
	seen := map[string]int{}
	for _, root := range []string{"cmd", "internal", "conformance"} {
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
	add := func(rule, identity, message string) {
		seen[rule]++
		identity = fmt.Sprintf("%s#%d", identity, seen[rule])
		result.Violations = append(result.Violations, violation{Rule: rule, Identity: identity, Message: message})
		result.Counts[rule]++
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		name := fn.Name.Name
		start := fset.Position(fn.Pos()).Line
		end := fset.Position(fn.Body.End()).Line
		lines := end - start + 1
		complexity := cyclomaticComplexity(fn.Body)
		params := parameterCount(fn.Type.Params)
		base := fmt.Sprintf("%s:%s", filepath.ToSlash(path), name)
		if lines > 200 {
			add("function-lines", base, fmt.Sprintf("function %s has %d lines (maximum 200)", name, lines))
		}
		if complexity > 15 {
			add("complexity", base, fmt.Sprintf("function %s has complexity %d (maximum 15)", name, complexity))
		}
		if params > 5 {
			add("max-params", base, fmt.Sprintf("function %s has %d parameters (maximum 5)", name, params))
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.Ident:
			if typed.Name == "any" {
				add("any", filepath.ToSlash(path), "use of any")
			}
		case *ast.CallExpr:
			if ident, ok := typed.Fun.(*ast.Ident); ok && ident.Name == "panic" {
				add("panic", filepath.ToSlash(path), "panic call")
			}
		}
		return true
	})
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
