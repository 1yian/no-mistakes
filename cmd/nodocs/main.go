package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

var directive = regexp.MustCompile(`^//([a-z0-9]+:[a-z0-9]|line |export |extern )`)

func main() {
	write := flag.Bool("w", false, "remove doc comments instead of reporting them")
	flag.Parse()

	files, err := goFiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nodocs: %v\n", err)
		os.Exit(2)
	}

	failed := false
	for _, path := range files {
		bad, err := process(path, *write)
		if err != nil {
			fmt.Fprintf(os.Stderr, "nodocs: %s: %v\n", path, err)
			os.Exit(2)
		}
		if bad && !*write {
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}

func goFiles() ([]string, error) {
	out, err := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*.go").Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	var files []string
	for _, name := range strings.Split(string(out), "\x00") {
		if name == "" {
			continue
		}
		if _, err := os.Stat(name); err == nil {
			files = append(files, name)
		}
	}
	return files, nil
}

func docGroups(fset *token.FileSet, file *ast.File) []*ast.CommentGroup {
	var groups []*ast.CommentGroup
	add := func(g *ast.CommentGroup) {
		if g != nil {
			groups = append(groups, g)
		}
	}
	add(file.Doc)
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.GenDecl:
			add(x.Doc)
		case *ast.FuncDecl:
			add(x.Doc)
		case *ast.TypeSpec:
			add(x.Doc)
		case *ast.ValueSpec:
			add(x.Doc)
		case *ast.ImportSpec:
			add(x.Doc)
		case *ast.Field:
			add(x.Doc)
		}
		return true
	})
	return groups
}

func process(path string, write bool) (bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return false, err
	}

	var doomed []*ast.Comment
	for _, g := range docGroups(fset, file) {
		for _, c := range g.List {
			if !directive.MatchString(c.Text) {
				doomed = append(doomed, c)
			}
		}
	}
	if len(doomed) == 0 {
		return false, nil
	}
	sort.Slice(doomed, func(i, j int) bool { return doomed[i].Pos() < doomed[j].Pos() })

	if !write {
		for _, c := range doomed {
			fmt.Printf("%s: doc comment; docstrings are not allowed\n", fset.Position(c.Pos()))
		}
		return true, nil
	}

	var out bytes.Buffer
	cursor := 0
	for _, c := range doomed {
		start := fset.Position(c.Pos()).Offset
		end := fset.Position(c.End()).Offset
		lineStart := bytes.LastIndexByte(src[:start], '\n') + 1
		lineEnd := len(src)
		if i := bytes.IndexByte(src[end:], '\n'); i >= 0 {
			lineEnd = end + i + 1
		}
		if len(bytes.TrimSpace(src[lineStart:start])) != 0 || len(bytes.TrimSpace(src[end:lineEnd])) != 0 {
			return true, fmt.Errorf("%s: doc comment shares a line with code", fset.Position(c.Pos()))
		}
		out.Write(src[cursor:lineStart])
		cursor = lineEnd
	}
	out.Write(src[cursor:])

	formatted, err := format.Source(out.Bytes())
	if err != nil {
		return true, err
	}
	return true, os.WriteFile(path, formatted, 0o644)
}
