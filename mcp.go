package main

// MCP sets (homelab fork). Every MCP server a Claude session loads costs it
// context on every request, and a default session loads all of them: the
// user's and the project's servers, every plugin's, and the claude.ai
// connectors of the account it is logged in with. With [mcp] strict on, a
// worker starts with --strict-mcp-config and only the servers its bead needs:
// [mcp] default (the minimum every worker gets), the bead's repository's
// [mcp.repos] list, and one per mcp:<name> label on the bead. The coordinator
// gets [mcp] coordinator. A bead whose wanted set changes while its worker
// runs (`quartermaster mcp add`) has its worker relaunched once idle, keeping
// the conversation, as a launch-mode change does.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type MCPConfig struct {
	// Strict starts every claude worker and the coordinator with only the
	// servers chosen for it; off, sessions load whatever Claude would.
	Strict bool `toml:"strict"`
	// Catalog is a JSON file of servers to choose from, {"servers": {name:
	// definition}, "descriptions": {name: text}, "defaultOn": [names]} (the
	// cws catalogue) or {"mcpServers": {...}}. Servers in ~/.claude.json, the
	// user's and the bead's repository's, are in the catalogue too.
	Catalog string `toml:"catalog"`
	// Default is the minimum set every worker gets; unset takes the
	// catalogue's defaultOn.
	Default *[]string `toml:"default"`
	// Coordinator is the coordinator's set; unset takes Default.
	Coordinator *[]string `toml:"coordinator"`
	// Repos adds servers for every bead in a repository, by its repos name.
	Repos map[string][]string `toml:"repos"`
}

const mcpLabel = "mcp:"

type mcpServer struct {
	Def  json.RawMessage
	Desc string
	From string // catalogue, user, or project
}

type mcpCatalog struct {
	Servers   map[string]mcpServer
	DefaultOn []string
}

func (c Config) mcpCatalogPath() string {
	if c.MCP.Catalog != "" {
		return expandHome(c.MCP.Catalog)
	}
	return expandHome("~/.config/cws/mcp-catalog.json")
}

func claudeJSONPath() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, ".claude.json")
	}
	return expandHome("~/.claude.json")
}

// mcpCatalogFor is every server a session in repo could be given: the
// catalogue file's, then the user's in ~/.claude.json, then repo's own
// ("local" scope), the first definition of a name winning.
func (c Config) mcpCatalogFor(repo string) (mcpCatalog, error) {
	cat := mcpCatalog{Servers: map[string]mcpServer{}}
	add := func(defs map[string]json.RawMessage, descs map[string]string, from string) {
		for n, d := range defs {
			if _, ok := cat.Servers[n]; !ok {
				cat.Servers[n] = mcpServer{Def: d, Desc: descs[n], From: from}
			}
		}
	}
	if b, err := os.ReadFile(c.mcpCatalogPath()); err == nil {
		var f struct {
			Servers      map[string]json.RawMessage `json:"servers"`
			MCPServers   map[string]json.RawMessage `json:"mcpServers"`
			Descriptions map[string]string          `json:"descriptions"`
			DefaultOn    []string                   `json:"defaultOn"`
		}
		if err := json.Unmarshal(b, &f); err != nil {
			return cat, fmt.Errorf("mcp catalogue %s: %w", c.mcpCatalogPath(), err)
		}
		add(f.Servers, f.Descriptions, "catalogue")
		add(f.MCPServers, f.Descriptions, "catalogue")
		cat.DefaultOn = f.DefaultOn
	} else if !errors.Is(err, os.ErrNotExist) {
		return cat, err
	}
	if b, err := os.ReadFile(claudeJSONPath()); err == nil {
		var f struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
			Projects   map[string]struct {
				MCPServers map[string]json.RawMessage `json:"mcpServers"`
			} `json:"projects"`
		}
		if json.Unmarshal(b, &f) == nil {
			add(f.MCPServers, nil, "user")
			if repo != "" {
				add(f.Projects[filepath.Clean(repo)].MCPServers, nil, "project")
			}
		}
	}
	return cat, nil
}

// mcpDefault is the minimum set: [mcp] default, else the catalogue's defaultOn.
func (c Config) mcpDefault(cat mcpCatalog) []string {
	if c.MCP.Default != nil {
		return *c.MCP.Default
	}
	return cat.DefaultOn
}

// mcpLabels is the servers a bead's mcp:<name> labels ask for.
func mcpLabels(b Bead) []string {
	var out []string
	for _, l := range b.Labels {
		if n, ok := strings.CutPrefix(l, mcpLabel); ok && n != "" {
			out = append(out, n)
		}
	}
	return out
}

// mcpWant is the set b's worker gets, sorted: the default, its repository's,
// and its labels'. unknown are the names no catalogue defines.
func (c Config) mcpWant(b Bead, cat mcpCatalog) (want, unknown []string) {
	set := map[string]bool{}
	for _, n := range c.mcpDefault(cat) {
		set[n] = true
	}
	if name, ok := c.repoName(b); ok && name != "" {
		for _, n := range c.MCP.Repos[name] {
			set[n] = true
		}
	}
	for _, n := range mcpLabels(b) {
		set[n] = true
	}
	for _, n := range slices.Sorted(maps.Keys(set)) {
		if _, ok := cat.Servers[n]; ok {
			want = append(want, n)
		} else {
			unknown = append(unknown, n)
		}
	}
	return want, unknown
}

func mcpConfigPath(agent string) string {
	return filepath.Join(stateDir(), "mcp", agent+".json")
}

// writeMCPConfig writes agent's --mcp-config file with names' definitions.
// It may hold tokens, so only the user reads it.
func writeMCPConfig(agent string, names []string, cat mcpCatalog) (string, error) {
	servers := map[string]json.RawMessage{}
	for _, n := range names {
		servers[n] = cat.Servers[n].Def
	}
	b, err := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
	if err != nil {
		return "", err
	}
	p := mcpConfigPath(agent)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return "", err
	}
	return p, os.Rename(tmp, p)
}

// mcpArgs is what a claude worker for b starts with under [mcp] strict:
// --strict-mcp-config and a config of its set. nil when strict is off, for
// other kinds, and for remote hosts, whose files aren't here.
func (c Config) mcpArgs(kind, host string, b Bead) ([]string, []string, error) {
	if !c.MCP.Strict || kind != "claude" || (host != "" && host != localHost) {
		return nil, nil, nil
	}
	cat, err := c.mcpCatalogFor(c.repoFor(b))
	if err != nil {
		return nil, nil, err
	}
	want, unknown := c.mcpWant(b, cat)
	if len(unknown) > 0 {
		return nil, nil, fmt.Errorf("%s wants MCP servers no catalogue defines: %s (quartermaster mcp list; quartermaster mcp remove %s %s)",
			b.ID, strings.Join(unknown, ", "), b.ID, strings.Join(unknown, " "))
	}
	p, err := writeMCPConfig(agentName(b.ID), want, cat)
	if err != nil {
		return nil, nil, err
	}
	return []string{"--strict-mcp-config", "--mcp-config", p}, want, nil
}

// coordinatorMCPArgs is mcpArgs for the coordinator: [mcp] coordinator, else
// the default.
func (c Config) coordinatorMCPArgs(kind string) ([]string, error) {
	if !c.MCP.Strict || kind != "claude" {
		return nil, nil
	}
	cat, err := c.mcpCatalogFor("")
	if err != nil {
		return nil, err
	}
	names := c.mcpDefault(cat)
	if c.MCP.Coordinator != nil {
		names = *c.MCP.Coordinator
	}
	for _, n := range names {
		if _, ok := cat.Servers[n]; !ok {
			return nil, fmt.Errorf("[mcp] coordinator names %s, which no catalogue defines (quartermaster mcp list)", n)
		}
	}
	p, err := writeMCPConfig(c.CoordinatorName, names, cat)
	if err != nil {
		return nil, err
	}
	return []string{"--strict-mcp-config", "--mcp-config", p}, nil
}

// launchedMCP is the servers a pane's foreground process was started with:
// the names in the file after its --mcp-config, and whether it was strict.
func launchedMCP(argv [][]string) (names []string, strict bool) {
	for _, a := range argv {
		if !slices.Contains(a, "--strict-mcp-config") {
			continue
		}
		strict = true
		for i := 0; i+1 < len(a); i++ {
			if a[i] != "--mcp-config" {
				continue
			}
			b, err := os.ReadFile(a[i+1])
			if err != nil {
				continue
			}
			var f struct {
				MCPServers map[string]json.RawMessage `json:"mcpServers"`
			}
			if json.Unmarshal(b, &f) == nil {
				names = append(names, slices.Collect(maps.Keys(f.MCPServers))...)
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names), strict
}

// mcpCatalogs memoises mcpCatalogFor by repository for one ticker pass, so
// ~/.claude.json is read once per repository, not once per worker.
type mcpCatalogs map[string]mcpCatalog

func (m mcpCatalogs) get(c Config, repo string) (mcpCatalog, error) {
	if cat, ok := m[repo]; ok {
		return cat, nil
	}
	cat, err := c.mcpCatalogFor(repo)
	if err == nil {
		m[repo] = cat
	}
	return cat, err
}

// mcpMismatch reports whether a worker for b runs with other MCP servers
// than it should: started without strict, or with another set.
func (c Config) mcpMismatch(b Bead, argv [][]string, cats mcpCatalogs) (bool, error) {
	if !c.MCP.Strict {
		return false, nil
	}
	if cats == nil {
		cats = mcpCatalogs{}
	}
	cat, err := cats.get(c, c.repoFor(b))
	if err != nil {
		return false, err
	}
	want, unknown := c.mcpWant(b, cat)
	if len(unknown) > 0 {
		return false, fmt.Errorf("unknown MCP servers %s", strings.Join(unknown, ", "))
	}
	got, strict := launchedMCP(argv)
	return !strict || !slices.Equal(got, want), nil
}

// mcpBrief tells a worker what it has and how to get more.
func mcpBrief(id string, have []string, cat mcpCatalog) string {
	var others []string
	for _, n := range slices.Sorted(maps.Keys(cat.Servers)) {
		if !slices.Contains(have, n) {
			others = append(others, n)
		}
	}
	has := "none"
	if len(have) > 0 {
		has = strings.Join(have, ", ")
	}
	s := fmt.Sprintf("\nMCP servers: this session has only %s, to keep context small.", has)
	if len(others) > 0 {
		s += fmt.Sprintf(" Others available: %s (`quartermaster mcp list` describes them).", strings.Join(others, ", "))
	}
	s += fmt.Sprintf(" If the task needs one, run `quartermaster mcp add %s <name>` and note why with `bd note`, then end your turn: quartermaster restarts you with it once you are idle, keeping this conversation. Don't add servers with `claude mcp` or `ws-mcp`; this session ignores them.\n", id)
	return s
}

// mcpGuide is the coordinator guide's section on MCP sets, "" when strict
// is off.
func mcpGuide(cfg Config) string {
	if !cfg.MCP.Strict {
		return ""
	}
	return `
## MCP servers

Workers start with only the MCP servers their bead needs, since each one costs
context on every request: the minimum set, the repository's ([mcp.repos]), and
one per ` + "`mcp:<name>`" + ` label. Before dispatching, decide what the task needs
and no more: ` + "`quartermaster mcp list`" + ` shows the catalogue with descriptions,
` + "`quartermaster dispatch <bead> --mcp a,b`" + ` or ` + "`quartermaster mcp add <bead> a b`" + `
adds them, and ` + "`quartermaster mcp list <bead>`" + ` shows what a bead gets. Most
code work needs nothing beyond the minimum; add a browser server for UI checks,
a forge or cloud server only when the task acts there. A running worker that
gets a new server is restarted with it once idle, keeping its conversation.
`
}

// mcpCmd is `quartermaster mcp [list [bead]] | add|remove <bead> <name>...`.
func mcpCmd(cfg Config, bd Beads, pos []string, w io.Writer) error {
	sub := "list"
	if len(pos) > 0 {
		sub, pos = pos[0], pos[1:]
	}
	switch sub {
	case "list", "ls":
		var b *Bead
		repo := cfg.Repo
		if len(pos) > 0 {
			got, err := bd.Show(pos[0])
			if err != nil {
				return err
			}
			b, repo = &got, cfg.repoFor(got)
		}
		cat, err := cfg.mcpCatalogFor(repo)
		if err != nil {
			return err
		}
		def := cfg.mcpDefault(cat)
		var want []string
		if b != nil {
			var unknown []string
			want, unknown = cfg.mcpWant(*b, cat)
			for _, n := range unknown {
				fmt.Fprintf(w, "  %-18s %-9s %s\n", n, "MISSING", "labelled but no catalogue defines it")
			}
		}
		for _, n := range slices.Sorted(maps.Keys(cat.Servers)) {
			s := cat.Servers[n]
			mark := ""
			switch {
			case b != nil && slices.Contains(want, n):
				mark = "on"
			case b == nil && slices.Contains(def, n):
				mark = "default"
			}
			fmt.Fprintf(w, "  %-18s %-9s %-10s %s\n", n, mark, s.From, s.Desc)
		}
		if !cfg.MCP.Strict {
			fmt.Fprintln(w, "([mcp] strict is off: sessions load every server Claude finds, not these sets)")
		}
		return nil
	case "add", "remove", "rm":
		if len(pos) < 2 {
			return fmt.Errorf("usage: quartermaster mcp %s <bead> <name>...", sub)
		}
		b, err := bd.Show(pos[0])
		if err != nil {
			return err
		}
		return setMCPLabels(cfg, bd, b, pos[1:], sub == "add", w)
	}
	return fmt.Errorf("usage: quartermaster mcp [list [<bead>]] | add|remove <bead> <name>...")
}

// setMCPLabels adds or removes mcp:<name> labels, refusing names no
// catalogue defines on add.
func setMCPLabels(cfg Config, bd Beads, b Bead, names []string, on bool, w io.Writer) error {
	if on {
		cat, err := cfg.mcpCatalogFor(cfg.repoFor(b))
		if err != nil {
			return err
		}
		for _, n := range names {
			if _, ok := cat.Servers[n]; !ok {
				return fmt.Errorf("no MCP server %q in the catalogue (quartermaster mcp list)", n)
			}
		}
	}
	for _, n := range names {
		if b.HasLabel(mcpLabel+n) == on {
			continue
		}
		if err := bd.SetLabel(b.ID, mcpLabel+n, on); err != nil {
			return err
		}
	}
	verb := "added"
	if !on {
		verb = "removed"
	}
	fmt.Fprintf(w, "%s %s MCP %s; a running worker restarts with its new set once idle\n", b.ID, verb, strings.Join(names, ", "))
	return nil
}
