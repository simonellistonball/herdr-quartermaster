package main

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// mcpFixture writes a cws-style catalogue and a ~/.claude.json with a user
// server and a project server for repo, and points quartermaster's state at a
// scratch dir.
func mcpFixture(t *testing.T, repo string) Config {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("QUARTERMASTER_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	catalog := `{"servers":{"codegraph":{"command":"codegraph"},"playwright":{"command":"npx","args":["@playwright/mcp"]},"gitea":{"command":"docker"}},
	  "descriptions":{"playwright":"browser automation"},"defaultOn":["codegraph"]}`
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	claude := `{"mcpServers":{"aws-mcp":{"command":"uvx"},"codegraph":{"command":"other"}},
	  "projects":{"` + repo + `":{"mcpServers":{"porto":{"type":"http","url":"https://porto"}}}}}`
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(claude), 0o644); err != nil {
		t.Fatal(err)
	}
	return Config{
		Repo:  "/r/budget",
		Repos: map[string]string{"porto": repo, "radar": "/r/radar"},
		MCP: MCPConfig{
			Strict:  true,
			Catalog: filepath.Join(dir, "catalog.json"),
			Repos:   map[string][]string{"radar": {"playwright"}},
		},
		CoordinatorName: "quartermaster",
	}
}

func TestMCPCatalogMergesSources(t *testing.T) {
	cfg := mcpFixture(t, "/r/porto")
	cat, err := cfg.mcpCatalogFor("/r/porto")
	if err != nil {
		t.Fatal(err)
	}
	names := slices.Sorted(func(yield func(string) bool) {
		for n := range cat.Servers {
			if !yield(n) {
				return
			}
		}
	})
	if strings.Join(names, " ") != "aws-mcp codegraph gitea playwright porto" {
		t.Fatalf("catalogue = %v", names)
	}
	if !strings.Contains(string(cat.Servers["codegraph"].Def), `"codegraph"`) || cat.Servers["codegraph"].From != "catalogue" {
		t.Fatalf("the catalogue's definition should win: %+v", cat.Servers["codegraph"])
	}
	if cat.Servers["aws-mcp"].From != "user" || cat.Servers["porto"].From != "project" || cat.Servers["playwright"].Desc != "browser automation" {
		t.Fatalf("sources: %+v", cat.Servers)
	}
	other, _ := cfg.mcpCatalogFor("/r/budget")
	if _, ok := other.Servers["porto"]; ok {
		t.Fatal("a project's server belongs to that project only")
	}
}

func TestMCPWant(t *testing.T) {
	cfg := mcpFixture(t, "/r/porto")
	cat, _ := cfg.mcpCatalogFor("/r/radar")
	want, unknown := cfg.mcpWant(Bead{ID: "hub-1"}, cat)
	if strings.Join(want, ",") != "codegraph" || len(unknown) != 0 {
		t.Fatalf("minimum = %v %v", want, unknown)
	}
	want, _ = cfg.mcpWant(Bead{ID: "hub-2", Labels: []string{"repo:radar", "mcp:gitea"}}, cat)
	if strings.Join(want, ",") != "codegraph,gitea,playwright" {
		t.Fatalf("repo + label = %v", want)
	}
	_, unknown = cfg.mcpWant(Bead{ID: "hub-3", Labels: []string{"mcp:nope"}}, cat)
	if strings.Join(unknown, ",") != "nope" {
		t.Fatalf("unknown = %v", unknown)
	}
	none := []string{}
	cfg.MCP.Default = &none
	want, _ = cfg.mcpWant(Bead{ID: "hub-1"}, cat)
	if len(want) != 0 {
		t.Fatalf("default = [] means nothing: %v", want)
	}
}

func TestMCPArgs(t *testing.T) {
	cfg := mcpFixture(t, "/r/porto")
	b := Bead{ID: "hub-7", Labels: []string{"repo:porto", "mcp:porto"}}
	args, have, err := cfg.mcpArgs("claude", localHost, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 3 || args[0] != "--strict-mcp-config" || args[1] != "--mcp-config" || strings.Join(have, ",") != "codegraph,porto" {
		t.Fatalf("args %v have %v", args, have)
	}
	fi, err := os.Stat(args[2])
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("config file %v %v", fi, err)
	}
	var f struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	raw, _ := os.ReadFile(args[2])
	if json.Unmarshal(raw, &f) != nil || len(f.MCPServers) != 2 || !strings.Contains(string(f.MCPServers["porto"]), "https://porto") {
		t.Fatalf("config = %s", raw)
	}
	for _, c := range []struct {
		kind, host string
		strict     bool
	}{{"codex", localHost, true}, {"claude", "server", true}, {"claude", localHost, false}} {
		cfg.MCP.Strict = c.strict
		if a, _, err := cfg.mcpArgs(c.kind, c.host, b); a != nil || err != nil {
			t.Errorf("%+v: want no MCP args, got %v %v", c, a, err)
		}
	}
	cfg.MCP.Strict = true
	if _, _, err := cfg.mcpArgs("claude", localHost, Bead{ID: "hub-8", Labels: []string{"mcp:nope"}}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown server should refuse: %v", err)
	}
}

func TestMCPMismatch(t *testing.T) {
	cfg := mcpFixture(t, "/r/porto")
	b := Bead{ID: "hub-7"}
	args, _, err := cfg.mcpArgs("claude", localHost, b)
	if err != nil {
		t.Fatal(err)
	}
	argv := [][]string{{"zsh"}, append([]string{"node", "/usr/bin/claude", "--continue"}, args...)}
	if diff, err := cfg.mcpMismatch(b, argv, nil); err != nil || diff {
		t.Fatalf("launched with its set: diff=%v err=%v", diff, err)
	}
	b.Labels = []string{"mcp:playwright"}
	if diff, _ := cfg.mcpMismatch(b, argv, nil); !diff {
		t.Fatal("a new label should mismatch")
	}
	if diff, _ := cfg.mcpMismatch(Bead{ID: "hub-7"}, [][]string{{"node", "/usr/bin/claude"}}, nil); !diff {
		t.Fatal("a worker started without strict should mismatch")
	}
	cfg.MCP.Strict = false
	if diff, _ := cfg.mcpMismatch(b, [][]string{{"claude"}}, nil); diff {
		t.Fatal("strict off never mismatches")
	}
}

func TestMCPBriefAndGuide(t *testing.T) {
	cfg := mcpFixture(t, "/r/porto")
	cat, _ := cfg.mcpCatalogFor("/r/budget")
	s := mcpBrief("hub-1", []string{"codegraph"}, cat)
	for _, want := range []string{"has only codegraph", "aws-mcp, gitea, playwright", "`quartermaster mcp add hub-1 <name>`"} {
		if !strings.Contains(s, want) {
			t.Errorf("brief lacks %q:\n%s", want, s)
		}
	}
	if !strings.Contains(mcpBrief("hub-1", nil, cat), "has only none") {
		t.Error("an empty set says none")
	}
	if !strings.Contains(mcpGuide(cfg), "dispatch <bead> --mcp a,b") {
		t.Error("guide lacks --mcp")
	}
	cfg.MCP.Strict = false
	if mcpGuide(cfg) != "" {
		t.Error("no guide section with strict off")
	}
}

func TestCoordinatorMCPArgs(t *testing.T) {
	cfg := mcpFixture(t, "/r/porto")
	none := []string{}
	cfg.MCP.Coordinator = &none
	args, err := cfg.coordinatorMCPArgs("claude")
	if err != nil || len(args) != 3 {
		t.Fatalf("%v %v", args, err)
	}
	raw, _ := os.ReadFile(args[2])
	if strings.TrimSpace(string(raw)) != "{\n  \"mcpServers\": {}\n}" {
		t.Fatalf("coordinator config = %s", raw)
	}
	bad := []string{"nope"}
	cfg.MCP.Coordinator = &bad
	if _, err := cfg.coordinatorMCPArgs("claude"); err == nil {
		t.Fatal("an unknown coordinator server should refuse")
	}
}

func TestSetMCPLabelsRefusesUnknown(t *testing.T) {
	cfg := mcpFixture(t, "/r/porto")
	var sb strings.Builder
	if err := setMCPLabels(cfg, Beads{}, Bead{ID: "hub-1"}, []string{"nope"}, true, &sb); err == nil {
		t.Fatal("adding an unknown server should refuse before calling bd")
	}
}

// Under [mcp] strict, a ready worker started without strict, or with another
// set than its bead wants, is due a relaunch, which resumes it with its set.
func TestRelaunchOnMCPChange(t *testing.T) {
	fakeTools(t)
	cfg := mcpFixture(t, "/r/porto")
	base := defaultConfig()
	base.MCP, base.Repos, base.Repo = cfg.MCP, cfg.Repos, cfg.Repo
	tk := Ticker{cfg: base, herdr: newHerdr(), log: log.New(io.Discard, "", 0)}
	idle := Agent{Name: "b-1", Kind: "claude", PaneID: "w1:p1", Status: "idle"}
	th := Thread{Bead: Bead{ID: "b-1", Status: StatusInProgress, Labels: []string{"autonomy:4"}}, Agent: &idle}

	t.Setenv("FAKE_PROCS", `[{"argv":["claude"]}]`)
	if due := tk.launchMismatch([]Thread{th}); !due["b-1"] {
		t.Fatal("a worker started without strict is not due a relaunch")
	}
	args, _, err := base.mcpArgs("claude", localHost, th.Bead)
	if err != nil {
		t.Fatal(err)
	}
	procs, _ := json.Marshal([]map[string][]string{{"argv": append([]string{"claude"}, args...)}})
	t.Setenv("FAKE_PROCS", string(procs))
	if due := tk.launchMismatch([]Thread{th}); len(due) != 0 {
		t.Fatalf("a worker with its set is due: %v", due)
	}
	th.Bead.Labels = append(th.Bead.Labels, "mcp:playwright")
	if due := tk.launchMismatch([]Thread{th}); !due["b-1"] {
		t.Fatal("a worker whose bead gained a server is not due a relaunch")
	}
	codex := Agent{Name: "b-1", Kind: "codex", PaneID: "w1:p1", Status: "idle"}
	t.Setenv("FAKE_PROCS", `[{"argv":["codex"]}]`)
	if due := tk.launchMismatch([]Thread{{Bead: th.Bead, Agent: &codex}}); len(due) != 0 {
		t.Fatalf("MCP sets are claude-only: %v", due)
	}
}
