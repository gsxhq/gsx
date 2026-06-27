package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
)

// TestWatchSession_MultiModuleEditBoth extends the multi-module scenario: after
// a cold-start spanning two independent modules, edit a package in each module
// and verify that each regenerates via its OWN Module with correct output.
//
// Before the multi-module watch fix all dirs were loaded against a single
// resolver rooted at the first module, so editing a package in the second
// module would either fail to type-check or produce stale output. This test
// ensures there is no cross-module bleed: editing beta must not corrupt alpha's
// output and vice versa.
func TestWatchSession_MultiModuleEditBoth(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}

	parent := t.TempDir()

	// alpha: single-package module with a simple leaf component.
	aDir := filepath.Join(parent, "alpha")
	writeModule(t, aDir, "alphamod")
	writeFile(t, aDir, "hi.gsx", "package alpha\n\ncomponent Hi(name string) {\n\t<p>{name}</p>\n}\n")

	// beta: two-package module where views imports comp.
	// This proves that the Module for beta correctly resolves betamod/comp from
	// within beta/views — which would fail if the session used alpha's Module.
	bRoot := filepath.Join(parent, "beta")
	writeModule(t, bRoot, "betamod")
	compDir := filepath.Join(bRoot, "comp")
	viewsDir := filepath.Join(bRoot, "views")
	writeFile(t, compDir, "card.gsx", "package comp\n\ncomponent Card(title string) {\n\t<div class=\"card\">{title}</div>\n}\n")
	writeFile(t, viewsDir, "page.gsx", "package views\n\nimport \"betamod/comp\"\n\ncomponent Page() {\n\t<comp.Card title=\"hi\"/>\n}\n")

	cfg := watchConfig{paths: []string{parent}, cls: attrclass.Builtin()}
	sess, startup, err := newWatchSession(cfg)
	if err != nil {
		t.Fatalf("newWatchSession: %v", err)
	}
	for _, r := range startup {
		if r.Err != nil {
			t.Fatalf("startup regen %s: %v diags=%v", r.Dir, r.Err, r.Diags)
		}
	}

	// --- Edit alpha ---
	writeFile(t, aDir, "hi.gsx", "package alpha\n\ncomponent Hi(name string) {\n\t<em>{name}</em>\n}\n")

	alphaM, alphaErr := sess.moduleForDir(aDir)
	if alphaErr != nil {
		t.Fatalf("moduleForDir(alpha): %v", alphaErr)
	}
	alphaM.Invalidate(aDir)
	for _, dep := range alphaM.Dependents(aDir) {
		if r := sess.regenDir(dep); !r.OK {
			t.Fatalf("regenDir(alpha dep %s): err=%v diags=%v", dep, r.Err, r.Diags)
		}
	}

	// alpha hi.x.go must reflect the <em> change. The codegen writes the closing
	// tag as a single static string S("</em>"), so we assert on that literal.
	aXgo, _ := os.ReadFile(filepath.Join(aDir, "hi.x.go"))
	if !strings.Contains(string(aXgo), "</em>") {
		t.Errorf("alpha hi.x.go not updated to use <em> after edit:\n%s", aXgo)
	}

	// --- Edit beta/comp ---
	writeFile(t, compDir, "card.gsx", "package comp\n\ncomponent Card(title string) {\n\t<span class=\"card\">{title}</span>\n}\n")

	betaM, betaErr := sess.moduleForDir(compDir)
	if betaErr != nil {
		t.Fatalf("moduleForDir(beta/comp): %v", betaErr)
	}
	betaM.Invalidate(compDir)
	for _, dep := range betaM.Dependents(compDir) {
		if r := sess.regenDir(dep); !r.OK {
			t.Fatalf("regenDir(beta dep %s): err=%v diags=%v", dep, r.Err, r.Diags)
		}
	}

	// beta/comp card.x.go must reflect the <span> change.
	bCompXgo, _ := os.ReadFile(filepath.Join(compDir, "card.x.go"))
	if !strings.Contains(string(bCompXgo), "<span") {
		t.Errorf("beta/comp card.x.go not updated to <span> after edit:\n%s", bCompXgo)
	}

	// beta/views page.x.go must still reference comp.Card (cross-package ref intact).
	bViewsXgo, _ := os.ReadFile(filepath.Join(viewsDir, "page.x.go"))
	if !strings.Contains(string(bViewsXgo), "comp.Card") {
		t.Errorf("beta/views page.x.go missing comp.Card reference after beta/comp edit:\n%s", bViewsXgo)
	}

	// Cross-module no-bleed: after editing beta, alpha's hi.x.go must still show
	// the <em> update from above — beta's reopen/regen must not overwrite it.
	aXgoFinal, _ := os.ReadFile(filepath.Join(aDir, "hi.x.go"))
	if !strings.Contains(string(aXgoFinal), "</em>") {
		t.Errorf("alpha hi.x.go was corrupted by beta edit (cross-module bleed):\n%s", aXgoFinal)
	}
}
