package archtest

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const module = "jungle-gaming-backend"

// rule restringe os imports diretos dos pacotes sob um prefixo. Imports
// diretos bastam: se o domínio só importa o domínio e a biblioteca padrão,
// nenhuma dependência proibida entra de forma indireta.
type rule struct {
	pkgPrefix string                // pacotes afetados (relativo à raiz)
	allowed   func(imp string) bool // nil = tudo permitido, exceto denied
	denied    []string              // prefixos de import proibidos
}

var forbiddenInfra = []string{
	"net/http", "database/sql",
	"go.uber.org/fx", "github.com/jackc/", "github.com/aws/",
	module + "/internal/adapters", module + "/internal/bootstrap", module + "/cmd",
}

var rules = []rule{
	{
		// Domínio: só biblioteca padrão e o próprio domínio (enunciado §4).
		pkgPrefix: "internal/domain",
		allowed: func(imp string) bool {
			return isStdlib(imp) || strings.HasPrefix(imp, module+"/internal/domain")
		},
		denied: forbiddenInfra,
	},
	{
		// Casos de uso: domínio e portas; nenhum detalhe de transporte ou banco.
		pkgPrefix: "internal/application",
		denied:    forbiddenInfra,
	},
	{
		// Adaptadores não conhecem a composição nem uns aos outros via bootstrap.
		pkgPrefix: "internal/adapters",
		denied:    []string{"go.uber.org/fx", module + "/internal/bootstrap", module + "/cmd"},
	},
	{
		pkgPrefix: "internal/config",
		denied:    []string{"go.uber.org/fx", module + "/internal/"},
	},
}

func TestLayerBoundaries(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range f.Imports {
			imp, _ := strconv.Unquote(spec.Path.Value)
			for _, r := range rules {
				if !strings.HasPrefix(rel, r.pkgPrefix+"/") {
					continue
				}
				if r.allowed != nil && !r.allowed(imp) {
					t.Errorf("%s: import %q not allowed in %s", rel, imp, r.pkgPrefix)
				}
				for _, d := range r.denied {
					if imp == d || strings.HasPrefix(imp, d+"/") || (strings.HasSuffix(d, "/") && strings.HasPrefix(imp, d)) {
						t.Errorf("%s: import %q is forbidden in %s", rel, imp, r.pkgPrefix)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Fx só pode aparecer na composição (internal/bootstrap) e no main.
func TestFxOnlyInBootstrap(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "vendor") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/bootstrap/") || strings.HasPrefix(rel, "cmd/") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range f.Imports {
			if imp, _ := strconv.Unquote(spec.Path.Value); strings.HasPrefix(imp, "go.uber.org/fx") {
				t.Errorf("%s imports %s; Fx belongs only to internal/bootstrap", rel, imp)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func isStdlib(imp string) bool {
	first, _, _ := strings.Cut(imp, "/")
	return !strings.Contains(first, ".") && first != module
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
