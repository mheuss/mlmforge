package testutil

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// FindFloatIdents returns "file:line: Owner floatNN" for each float32 or
// float64 identifier in the non-test Go files of dir, skipping owners named in
// exceptions.
func FindFloatIdents(dir string, exceptions map[string]bool) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		report := func(owner string, n ast.Node) {
			if exceptions[owner] {
				return
			}
			ast.Inspect(n, func(node ast.Node) bool {
				if id, ok := node.(*ast.Ident); ok && (id.Name == "float32" || id.Name == "float64") {
					found = append(found, fmt.Sprintf("%s:%d: %s %s", name, fset.Position(id.Pos()).Line, owner, id.Name))
				}
				return true
			})
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				report(funcOwner(d), d)
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						reportType(s, report)
					case *ast.ValueSpec:
						for i, n := range s.Names {
							if s.Type != nil {
								report(n.Name, s.Type)
							}
							if len(s.Values) == len(s.Names) {
								report(n.Name, s.Values[i])
								continue
							}
							for _, v := range s.Values {
								report(n.Name, v)
							}
						}
					}
				}
			}
		}
	}
	return found, nil
}

// reportType reports the floats in a type declaration under the owner each
// one belongs to.
func reportType(s *ast.TypeSpec, report func(string, ast.Node)) {
	if s.TypeParams != nil {
		report(s.Name.Name, s.TypeParams)
	}
	var members *ast.FieldList
	switch t := s.Type.(type) {
	case *ast.StructType:
		members = t.Fields
	case *ast.InterfaceType:
		members = t.Methods
	default:
		report(s.Name.Name, s.Type)
		return
	}
	for _, m := range members.List {
		if len(m.Names) == 0 {
			owner := s.Name.Name
			if name := embeddedName(m.Type); name != "" {
				owner += "." + name
			}
			report(owner, m.Type)
			continue
		}
		for _, n := range m.Names {
			report(s.Name.Name+"."+n.Name, m.Type)
		}
	}
}

// embeddedName returns the field name Go gives an embedded field of type t.
func embeddedName(t ast.Expr) string {
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	switch g := t.(type) {
	case *ast.IndexExpr:
		t = g.X
	case *ast.IndexListExpr:
		t = g.X
	}
	switch n := t.(type) {
	case *ast.Ident:
		return n.Name
	case *ast.SelectorExpr:
		return n.Sel.Name
	}
	return ""
}

// funcOwner names a function, or a method as Type.Method.
func funcOwner(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return d.Name.Name
	}
	t := d.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	switch g := t.(type) {
	case *ast.IndexExpr:
		t = g.X
	case *ast.IndexListExpr:
		t = g.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + d.Name.Name
	}
	return d.Name.Name
}
