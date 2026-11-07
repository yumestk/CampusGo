package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const prefix = "github.com/yumestk/CampusGo/"

func standard(path string) bool {
	return !strings.Contains(strings.Split(path, "/")[0], ".") && path != "C"
}
func importViolation(file, path string) bool {
	switch {
	case strings.HasPrefix(file, "internal/domain/"):
		return !map[string]bool{"fmt": true, "math": true, "math/big": true, "strings": true, "strconv": true, "time": true, "errors": true, "unicode": true, "unicode/utf8": true}[path]
	case strings.HasPrefix(file, "internal/application/"):
		return strings.HasPrefix(path, "database/") || (!standard(path) && path != "github.com/google/uuid" && !strings.HasPrefix(path, prefix+"internal/domain/") && path != prefix+"internal/contracts" && path != prefix+"internal/failure")
	case strings.HasPrefix(file, "internal/fundmq/") || strings.HasPrefix(file, "internal/timeoutmq/"):
		return strings.HasPrefix(path, prefix) && path != prefix+"internal/contracts"
	}
	return false
}

var writes = regexp.MustCompile(`(?i)\b(?:INSERT(?:\s+IGNORE)?\s+INTO|UPDATE|DELETE\s+FROM|REPLACE\s+INTO)\s+` + "`?" + `(wallet_account|wallet_ledger|escrow_order|credit_score|credit_event)\b`)

func writeViolation(file, statement string) bool {
	for _, m := range writes.FindAllStringSubmatch(statement, -1) {
		table := strings.ToLower(m[1])
		owner := "internal/adapter/mysql/wallet_posting.go"
		if strings.HasPrefix(table, "credit_") {
			owner = "internal/adapter/mysql/credit.go"
		}
		// SeedOpening is the explicit idempotent opening-capital exception; migration
		// files and controlled test SQL are outside production Go source scanning.
		if file != owner && !(file == "internal/wallet/wallet.go" && strings.HasPrefix(table, "wallet_")) {
			return true
		}
	}
	return false
}

func TestProductionBoundaries(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(here), "..", ".."))
	// The graph also checks transitive project dependencies: importing a neutral
	// facade must not smuggle an SQL driver or SDK back into the application.
	graph := map[string][]string{}
	domainFiles, applicationFiles := 0, 0
	for _, base := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, base), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			pkg := prefix + filepath.ToSlash(filepath.Dir(rel))
			if strings.HasPrefix(rel, "internal/domain/") {
				domainFiles++
			}
			if strings.HasPrefix(rel, "internal/application/") {
				applicationFiles++
			}
			for _, i := range f.Imports {
				p, _ := strconv.Unquote(i.Path.Value)
				graph[pkg] = append(graph[pkg], p)
				if importViolation(rel, p) {
					t.Errorf("%s: forbidden import %s", rel, p)
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if strings.HasPrefix(rel, "internal/domain/") {
					if sel, ok := n.(*ast.SelectorExpr); ok {
						if id, ok := sel.X.(*ast.Ident); ok && id.Name == "time" && sel.Sel.Name == "Now" {
							t.Errorf("%s: domain reads wall clock instead of passed facts", rel)
						}
					}
				}
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					v, _ := strconv.Unquote(lit.Value)
					if writeViolation(rel, v) {
						t.Errorf("%s: SQL writes outside wallet/credit owner", rel)
					}
				}
				// Controllers may use declared SQL read projections. Writer construction
				// and calls must remain in cmd/application, never inside a route handler.
				if strings.HasPrefix(rel, "internal/transport/") {
					if sel, ok := n.(*ast.SelectorExpr); ok {
						switch sel.Sel.Name {
						case "TaskStore", "PublishStore", "FundStore", "CreditStore", "ApplyPublish", "ApplyConfirm", "ApplyFulfill", "CommitGrab", "RegisterGrab", "FinishGrab", "ReplayMessage", "ReplayRepair":
							t.Errorf("%s: transport bypasses application via %s", rel, sel.Sel.Name)
						}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if domainFiles < 4 || applicationFiles < 5 {
		t.Fatal("boundary scan did not find expected source")
	}
	seen := map[string]bool{}
	var visit func(string)
	visit = func(pkg string) {
		if seen[pkg] {
			return
		}
		seen[pkg] = true
		for _, dep := range graph[pkg] {
			if strings.HasPrefix(dep, prefix) {
				if !strings.HasPrefix(dep, prefix+"internal/domain/") && dep != prefix+"internal/contracts" && dep != prefix+"internal/failure" && dep != prefix+"internal/application" {
					t.Errorf("application transitively depends on %s", dep)
				}
				visit(dep)
			} else if strings.HasPrefix(dep, "database/") || (!standard(dep) && dep != "github.com/google/uuid") {
				t.Errorf("application transitively depends on SDK %s", dep)
			}
		}
	}
	visit(prefix + "internal/application")
}

func TestBoundaryRulesRejectViolations(t *testing.T) {
	for _, c := range []struct{ file, path string }{
		{"internal/domain/errand/task.go", "database/sql"},
		{"internal/domain/wallet/posting.go", prefix + "internal/domain/errand"},
		{"internal/application/settle.go", prefix + "internal/adapter/mysql"},
		{"internal/application/settle.go", "database/sql"},
		{"internal/application/settle.go", "github.com/redis/go-redis/v9"},
		{"internal/fundmq/client.go", prefix + "internal/application"},
	} {
		if !importViolation(c.file, c.path) {
			t.Errorf("accepted forbidden dependency %+v", c)
		}
	}
	if !writeViolation("internal/application/settle.go", "UPDATE wallet_account SET available=0") || !writeViolation("internal/adapter/mysql/funds.go", "INSERT INTO credit_event(user_id) VALUES (?)") {
		t.Fatal("writer ownership check did not reject violation")
	}
	if writeViolation("internal/adapter/mysql/credit.go", "INSERT INTO credit_event(user_id) VALUES (?)") {
		t.Fatal("credit owner rejected")
	}
}
