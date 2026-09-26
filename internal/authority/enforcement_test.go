package authority_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/chester-hill-solutions/stow-s3/internal/authority"
)

// Every operation this package defines is either enforced somewhere or declared
// ungated with a reason.
//
// Four of the twelve were in the second state and nobody knew, because the set is
// closed and the constants are exported: a caller narrowing an Authority appears
// to withhold something the code never consults. The gate metrics cannot see it —
// there is no line of code to be too long or too complex — which is why it needed
// a test rather than a lint rule.
//
// The check is by source scan rather than by a hand-maintained list, so it cannot
// drift from what the code does. A hand-written "these are enforced" list would
// be a fourth copy of the same fact, which is the failure this repository has
// already hit six times.

// operationByConstName maps each exported constant's Go name to the Operation it
// denotes. A check site reads check(authority.BucketCreate), so the source scan
// finds a name, while Defined reports values like "bucket.create" — the two have
// to be reconciled or every operation looks unenforced.
func operationByConstName(t *testing.T) map[string]authority.Operation {
	t.Helper()
	fset := token.NewFileSet()
	path := "authority.go"
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	byName := map[string]authority.Operation{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range value.Names {
				lit, ok := value.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				parsed, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				byName[name.Name] = authority.Operation(parsed)
			}
		}
	}
	if len(byName) == 0 {
		t.Fatal("no operations parsed from the authority package; the scan cannot be trusted")
	}
	return byName
}

// checkSitesInFile records every check(authority.Op) call in one file.
//
// The shape is <receiver>.check(authority.Op): the receiver is the environment,
// and the operation is the argument. Split out from the walk so that neither half
// carries the other's branching — the walk plus the matcher together exceeded the
// complexity ceiling, which the ratchet caught on this file the moment it landed.
func checkSitesInFile(path string, byName map[string]authority.Operation, found map[authority.Operation]bool) error {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return err
	}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "check" {
			return true
		}
		lit, ok := call.Args[0].(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := lit.X.(*ast.Ident); ok && pkg.Name == "authority" {
			if op, known := byName[lit.Sel.Name]; known {
				found[op] = true
			}
		}
		return true
	})
	return nil
}

// enforcedOperations finds every check(authority.X) call site in the repository.
// The runtime enforces through a single predicate, so the presence of the call is
// the evidence that an operation is consulted.
func enforcedOperations(t *testing.T) map[authority.Operation]bool {
	t.Helper()
	byName := operationByConstName(t)
	found := map[authority.Operation]bool{}
	for _, root := range []string{"cmd", "internal", "pkg"} {
		err := filepath.WalkDir(filepath.Join("..", "..", root), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			return checkSitesInFile(path, byName, found)
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	return found
}

func TestEveryDefinedOperationIsEnforcedOrDeclaredUngated(t *testing.T) {
	enforced := enforcedOperations(t)

	var undeclared []string
	for _, op := range authority.Defined() {
		if enforced[op] {
			if _, declared := authority.Ungated[op]; declared {
				t.Errorf("%s is enforced but still listed in Ungated; remove it so the list keeps meaning", op)
			}
			continue
		}
		if reason, declared := authority.Ungated[op]; !declared {
			undeclared = append(undeclared, strconv.Quote(string(op)))
		} else if strings.TrimSpace(reason) == "" {
			t.Errorf("%s is declared ungated with no reason", op)
		}
	}

	if len(undeclared) > 0 {
		t.Fatalf("operations with no enforcement site and no entry in authority.Ungated: %s\n"+
			"Enforce the operation, or add it to Ungated with the reason it is not wired.\n"+
			"An operation nobody checks is a permission that is described and not granted.",
			strings.Join(undeclared, ", "))
	}
}

// The ungated list is the whole of what is known-unenforced, so it is worth
// stating its size rather than letting it drift upward unnoticed. It is a floor:
// M1.3 enforces three of these four and M4.3 removes the fifth, at which point
// this number changes and the change is a thing somebody decided.
func TestUngatedListIsTheKnownGap(t *testing.T) {
	enforced := enforcedOperations(t)

	var actual []string
	for _, op := range authority.UngatedOperations() {
		actual = append(actual, string(op))
	}

	want := []string{"environment.destroy", "environment.promote", "upstream.read", "upstream.write"}
	if strings.Join(actual, ",") != strings.Join(want, ",") {
		t.Fatalf("ungated operations = %v, want %v\n"+
			"If a check site landed, remove the entry. If enforcement is still owed, the\n"+
			"reason in authority.Ungated should say so.",
			actual, want)
	}

	for _, op := range authority.UngatedOperations() {
		if enforced[op] {
			t.Errorf("%s is listed ungated but a check site exists", op)
		}
	}
}

// An operation in Ungated that is not in Defined at all is a typo, or a constant
// that was renamed. Ungated is keyed by Operation, so a stale key would compile
// and sit there forever.
func TestUngatedNamesOnlyDefinedOperations(t *testing.T) {
	defined := map[authority.Operation]bool{}
	for _, op := range authority.Defined() {
		defined[op] = true
	}
	for op := range authority.Ungated {
		if !defined[op] {
			t.Errorf("authority.Ungated names %s, which Defined does not", op)
		}
	}
}
