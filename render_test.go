package main

import (
	"strings"
	"testing"
)

const sample = `diff --git a/old.go b/new.go
similarity index 80%
rename from old.go
rename to new.go
index 1111111..2222222 100644
--- a/old.go
+++ b/new.go
@@ -1,4 +1,4 @@
 package main
 
-func a() {}
+func b() {}
 // end
`

func TestRenderDiff(t *testing.T) {
	files, err := RenderDiff(sample)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d", len(files))
	}
	f := files[0]
	if f.Path != "new.go" || f.OldPath != "old.go" {
		t.Errorf("paths: %q -> %q", f.OldPath, f.Path)
	}
	if f.Plus != 1 || f.Minus != 1 {
		t.Errorf("stats: +%d -%d", f.Plus, f.Minus)
	}
	var mod *Row
	for i := range f.Rows {
		if f.Rows[i].Kind == "mod" {
			mod = &f.Rows[i]
		}
	}
	if mod == nil {
		t.Fatalf("no mod row in %+v", f.Rows)
	}
	if mod.OldNum != 3 || mod.NewNum != 3 {
		t.Errorf("line numbers: %d / %d", mod.OldNum, mod.NewNum)
	}
	if !strings.Contains(string(mod.Old), "a") || strings.Contains(string(mod.Old), "b()") {
		t.Errorf("old column: %s", mod.Old)
	}
	if !strings.Contains(string(mod.New), "b") || strings.Contains(string(mod.New), "a()") {
		t.Errorf("new column: %s", mod.New)
	}
	if !strings.Contains(string(mod.New), "<span") {
		t.Errorf("expected chroma markup in %s", mod.New)
	}
	// context rows appear on both sides with both numbers
	if r := f.Rows[1]; r.Kind != "ctx" || r.OldNum != 1 || r.NewNum != 1 {
		t.Errorf("first ctx row: %+v", r)
	}
}

func TestInlineDiff(t *testing.T) {
	hl := highlighter("x.go")
	old, new := inlineDiff("return a+b", "return a + b")
	if old != (span{8, 9}) || new != (span{8, 11}) {
		t.Fatalf("spans: %+v %+v", old, new)
	}
	if got := string(hl("return a + b", new)); !strings.Contains(got, "<mark>") || !strings.Contains(got, "</mark>") ||
		strings.Contains(got, "\n") || !strings.HasSuffix(got, "b</span>") {
		t.Errorf("marked html: %s", got)
	}
	if a, b := inlineDiff("foo", "bar"); a != (span{}) || b != (span{}) {
		t.Errorf("whole-line change should not be marked: %+v %+v", a, b)
	}
	if a, _ := inlineDiff("é", "éa"); a != (span{2, 2}) {
		t.Errorf("utf8 boundary: %+v", a)
	}
}
