package billing

import (
	"encoding/json"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The guard of the invoice's lifecycle, as checks (internal/AGENTS.md, "规则只在一处"): the invoice's state and
// formalization are decided in internal/invoices/lifecycle.go and written by its Transition, and nothing else quietly
// decides or writes them. The checks read the code by its types, not by its variables' names.

const module = "github.com/brickKit/erp-billing/v3"

// invoiceStateReaders are the places outside internal/invoices/lifecycle.go that read the invoice's state or
// formalization (in Go or in SQL), by file and function, each with what it reads it for. A new state or phase means
// deciding, for each of them, what it does with an invoice in that state.
var invoiceStateReaders = map[string]reader{
	"internal/recoverycheck/recoverycheck.go:ClaimsCheck": {SQL, "协议 05 的认领核对：开具而正式化未完成的列为差异；" +
		"还没结束的（草稿、已确认、正式化未完成）或最近改过的账单所在的订单，按阶段（invoices.PhaseOfStored）算来源应有的认领"},
}

// reader is a registered reader of the invoice's state: whether it reads it in Go or in SQL, and what for.
type reader struct {
	in  string
	why string
}

const (
	Go  = "go"
	SQL = "sql"
)

type pkg struct {
	path  string
	dir   string
	files []*ast.File
	info  *types.Info
}

// load type-checks the packages under internal/ against the compiled export data of their dependencies.
func load(t *testing.T) (*token.FileSet, []pkg) {
	t.Helper()
	cmd := exec.Command("go", "list", "-export", "-deps", "-json=ImportPath,Dir,GoFiles,Export", "./internal/...")
	cmd.Dir = ".."
	out, err := cmd.Output()
	require.NoError(t, err)
	type listed struct {
		ImportPath, Dir, Export string
		GoFiles                 []string
	}
	exports := map[string]string{}
	var mine []listed
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var l listed
		require.NoError(t, dec.Decode(&l))
		exports[l.ImportPath] = l.Export
		if strings.HasPrefix(l.ImportPath, module+"/internal/") {
			mine = append(mine, l)
		}
	}
	require.NotEmpty(t, mine)
	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) { return os.Open(exports[path]) })
	var pkgs []pkg
	for _, l := range mine {
		p := pkg{path: l.ImportPath, dir: l.Dir, info: &types.Info{Selections: map[*ast.SelectorExpr]*types.Selection{}}}
		for _, f := range l.GoFiles {
			file, err := parser.ParseFile(fset, filepath.Join(l.Dir, f), nil, 0)
			require.NoError(t, err)
			p.files = append(p.files, file)
		}
		_, err := (&types.Config{Importer: imp}).Check(l.ImportPath, fset, p.files, p.info)
		require.NoError(t, err, l.ImportPath)
		pkgs = append(pkgs, p)
	}
	return fset, pkgs
}

func rel(fset *token.FileSet, pos token.Pos) string {
	p := fset.Position(pos)
	wd, _ := os.Getwd()
	r, _ := filepath.Rel(filepath.Dir(wd), p.Filename)
	return r + ":" + strconv.Itoa(p.Line)
}

func in(p pkg, name string) bool { return p.path == module+"/internal/"+name }

// theLifecycle is the one file that decides on the invoice's state without registering.
const theLifecycle = "internal/invoices/lifecycle.go"

func lifecycleFile(fset *token.FileSet, pos token.Pos) bool {
	return strings.HasPrefix(rel(fset, pos), theLifecycle+":")
}

// where is "file:function" of pos: the function declared around it, or the top-level declaration's first name.
func where(fset *token.FileSet, f *ast.File, pos token.Pos) string {
	file := strings.SplitN(rel(fset, pos), ":", 2)[0]
	for _, d := range f.Decls {
		if pos < d.Pos() || pos > d.End() {
			continue
		}
		switch d := d.(type) {
		case *ast.FuncDecl:
			return file + ":" + d.Name.Name
		case *ast.GenDecl:
			for _, s := range d.Specs {
				if v, ok := s.(*ast.ValueSpec); ok && pos >= v.Pos() && pos <= v.End() {
					return file + ":" + v.Names[0].Name
				}
			}
		}
	}
	return file + ":?"
}

// fileOf is the parsed file holding pos.
func fileOf(p pkg, pos token.Pos) *ast.File {
	for _, f := range p.files {
		if pos >= f.Pos() && pos <= f.End() {
			return f
		}
	}
	return nil
}

// guarded are core.Invoice's fields the lifecycle owns.
var guarded = map[string]bool{"State": true, "Formalization": true}

// TestOnlyTheLifecycleReadsTheInvoiceState: outside internal/invoices/lifecycle.go (and internal/core, which reads
// the rows), nothing reads core.Invoice's State or Formalization but the registered readers: every decision on an
// invoice asks invoices.Decide.
func TestOnlyTheLifecycleReadsTheInvoiceState(t *testing.T) {
	fset, pkgs := load(t)
	found := 0
	readers := map[string]bool{}
	for _, p := range pkgs {
		for sel, s := range p.info.Selections {
			if s.Kind() != types.FieldVal || !guarded[s.Obj().Name()] {
				continue
			}
			recv := s.Recv()
			if ptr, ok := recv.(*types.Pointer); ok {
				recv = ptr.Elem()
			}
			named, ok := recv.(*types.Named)
			if !ok || named.Obj().Pkg().Path() != module+"/internal/core" || named.Obj().Name() != "Invoice" {
				continue
			}
			found++
			if in(p, "core") || lifecycleFile(fset, sel.Pos()) {
				continue
			}
			w := where(fset, fileOf(p, sel.Pos()), sel.Pos())
			readers[w] = true
			if invoiceStateReaders[w].in != Go {
				t.Errorf("%s (%s) reads the invoice's state outside "+theLifecycle+": ask invoices.Decide, or register "+
					"it in invoiceStateReaders with what it reads it for", rel(fset, sel.Pos()), w)
			}
		}
	}
	assert.NotZero(t, found, "the check finds the reads in internal/invoices (else it checks nothing)")
	for w, r := range invoiceStateReaders {
		if r.in == Go {
			assert.True(t, readers[w], "%s is registered but no longer reads the invoice's state: unregister it", w)
		}
	}
}

var (
	writesInvoiceState = regexp.MustCompile(`(?is)UPDATE\s+invoice\b.*\bSET\b.*\b(state|formalization)\s*=`)
	stateCondition     = regexp.MustCompile(`(?is)\b(\w+\.)?(state|formalization)\s*(=|<>|!=|IN\b|NOT\s+IN\b)`)
	invoiceTable       = regexp.MustCompile(`\binvoice\b`)
)

// TestTheInvoiceStateHasItsOwner: the SQL that writes the invoice's state or formalization is in
// internal/invoices/lifecycle.go (Transition), and the SQL elsewhere whose conditions read them is registered in
// invoiceStateReaders.
func TestTheInvoiceStateHasItsOwner(t *testing.T) {
	fset, pkgs := load(t)
	writes := 0
	readers := map[string]bool{}
	for _, p := range pkgs {
		for _, f := range p.files {
			f := f
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				sql, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				at := rel(fset, lit.Pos())
				w := where(fset, f, lit.Pos())
				if writesInvoiceState.MatchString(sql) {
					writes++
					if !lifecycleFile(fset, lit.Pos()) {
						t.Errorf("%s writes the invoice's state outside %s: use invoices.Transition", at, theLifecycle)
					}
					return true
				}
				if stateCondition.MatchString(sql) && invoiceTable.MatchString(sql) && !lifecycleFile(fset, lit.Pos()) {
					readers[w] = true
					if invoiceStateReaders[w].in != SQL {
						t.Errorf("%s (%s) reads the invoice's state in SQL outside "+theLifecycle+": register it in "+
							"invoiceStateReaders with what it reads it for", at, w)
					}
				}
				return true
			})
		}
	}
	assert.NotZero(t, writes, "the check finds the writes in internal/invoices")
	for w, r := range invoiceStateReaders {
		if r.in == SQL {
			assert.True(t, readers[w], "%s is registered but no longer reads the invoice's state: unregister it", w)
		}
	}
}
