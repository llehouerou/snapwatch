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
