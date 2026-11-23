package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testGitea = "gitea.example.com"

// withGitea follows testGitea as Gitea for one test, through a fake gt that
// answers `gt api <path> --host <host>` from files under the returned dir,
// named by the path with / and ? turned into _. An absent file is a 404.
func withGitea(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
[ "$1" = api ] || exit 2
f="` + dir + `/api/$(printf '%s' "$2" | tr '/?' '__')"
echo "$2" >> "` + dir + `/calls"
if [ -f "$f" ]; then cat "$f"; else echo "Error: not found (HTTP 404 GET /api/v1/$2)" >&2; exit 1; fi
`
	if err := os.WriteFile(filepath.Join(bin, "gt"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	setGiteaHosts([]string{"https://" + testGitea + "/"}, "gt")
	t.Cleanup(func() {
		setGiteaHosts(nil, "gt")
		remotes.Clear()
		giteaLogins.Clear()
	})
	return dir
}

func giteaAnswer(t *testing.T, dir, path, body string) {
	t.Helper()
	p := filepath.Join(dir, "api", strings.NewReplacer("/", "_", "?", "_").Replace(path))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitRepoWithOrigin(t *testing.T, origin string) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", origin}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

func TestGiteaURLsCountAsPRsAndRuns(t *testing.T) {
	withGitea(t)
	notes := "PR: https://github.com/o/r/pull/3\nPR: https://" + testGitea + "/simon/messages/pulls/806.\n" +
		"api: https://" + testGitea + "/api/v1/repos/simon/messages/pulls/807/merge\nelsewhere: https://other.example/a/b/pulls/9"
	got := NotedPRs(notes)
	want := []string{"https://github.com/o/r/pull/3", "https://" + testGitea + "/simon/messages/pulls/806"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("NotedPRs = %q, want %q", got, want)
	}
	if n := prNumber(want[1]); n != 806 {
		t.Fatalf("prNumber = %d", n)
	}
	if s := prName(PR{URL: want[1]}); s != "simon/messages#806" {
		t.Fatalf("prName = %q", s)
	}
	runs := NotedRuns("run https://" + testGitea + "/simon/porto/actions/runs/23674, done")
	if len(runs) != 1 || runRepo(runs[0], "simon/porto") != "https://"+testGitea+"/simon/porto" {
		t.Fatalf("runs = %q", runs)
	}
	if runRepo("https://github.com/o/r/actions/runs/1", "o/r") != "o/r" {
		t.Fatal("a GitHub run keeps owner/repo")
	}
}

func TestGitHubOnlyWithoutGiteaHosts(t *testing.T) {
	setGiteaHosts(nil, "")
	if got := NotedPRs("https://" + testGitea + "/simon/messages/pulls/806"); len(got) != 0 {
		t.Fatalf("no gitea_hosts should follow no Gitea PRs, got %q", got)
	}
	if _, _, ok := giteaRepo("/nonexistent"); ok {
		t.Fatal("no gitea_hosts: no repo is on Gitea")
	}
}

func TestParseRemote(t *testing.T) {
	for in, want := range map[string]string{
		"https://simon@gitea.example.com/simon/budget.git": "gitea.example.com simon/budget",
		"https://gitea.example.com/simon/ci":               "gitea.example.com simon/ci",
		"ssh://git@gitea.example.com:2222/simon/x.git":     "gitea.example.com simon/x",
		"git@gitea.example.com:simon/y.git":                "gitea.example.com simon/y",
		"https://github.com/simonellistonball/homelab.git": "github.com simonellistonball/homelab",
		"/some/local/path":                                 " ",
	} {
		h, s := parseRemote(in)
		if h+" "+s != want {
			t.Errorf("parseRemote(%q) = %q %q, want %q", in, h, s, want)
		}
	}
}

func TestGiteaMineAndView(t *testing.T) {
	dir := withGitea(t)
	repo := gitRepoWithOrigin(t, "https://simon@"+testGitea+"/simon/messages.git")
	giteaAnswer(t, dir, "user", `{"login":"simon"}`)
	giteaAnswer(t, dir, "repos/simon/messages/pulls?limit=50&page=1&poster=simon&state=all", `[
	 {"number":810,"title":"open one","html_url":"https://`+testGitea+`/simon/messages/pulls/810","state":"open","mergeable":true,
	  "head":{"ref":"bead/hub-80l8-e2e","label":"bead/hub-80l8-e2e","sha":"abc"},"base":{"ref":"main"}},
	 {"number":836,"title":"merged one","html_url":"https://`+testGitea+`/simon/messages/pulls/836","state":"closed","merged":true,
	  "merged_at":"2026-11-23T00:58:08Z","head":{"ref":"refs/pull/836/head","label":"bead/hub-82i5f-flaky","sha":"def"},"base":{"ref":"main"}}]`)
	prs, err := GH{repo: repo}.Mine()
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 2 || prs[0].State != "OPEN" || prs[0].Head != "bead/hub-80l8-e2e" ||
		prs[1].State != "MERGED" || prs[1].Head != "bead/hub-82i5f-flaky" || prs[1].MergedAt.IsZero() {
		t.Fatalf("Mine = %+v", prs)
	}

	giteaAnswer(t, dir, "repos/simon/messages/pulls/810", `{"number":810,"html_url":"https://`+testGitea+`/simon/messages/pulls/810","state":"open","mergeable":true,
	 "head":{"ref":"bead/hub-80l8-e2e","sha":"abc"},"base":{"ref":"main"}}`)
	giteaAnswer(t, dir, "repos/simon/messages/pulls/810/reviews?limit=50", `[]`)
	giteaAnswer(t, dir, "repos/simon/messages/commits/abc/status?limit=50", `{"state":"pending","statuses":[
	 {"context":"ci / typecheck (pull_request)","status":"success"},
	 {"context":"ci / docker image (pull_request)","status":"skipped"},
	 {"context":"ci / end-to-end (playwright 1/2) (pull_request)","status":"pending"}]}`)
	giteaAnswer(t, dir, "repos/simon/messages/branch_protections", `[{"rule_name":"main","enable_status_check":true,
	 "status_check_contexts":["ci / typecheck (pull_request)","ci / end-to-end (playwright*) (pull_request)"]}]`)
	pr, err := GH{}.View("https://"+testGitea+"/simon/messages/pulls/810", true)
	if err != nil {
		t.Fatal(err)
	}
	c := summarizeChecks(pr.Checks)
	if c.Pending != 1 || c.Passing != 2 || len(c.Failing) != 0 || pr.MergeState != "BLOCKED" || mergeable(&pr, c) {
		t.Fatalf("pending required check: checks %+v, merge state %q", c, pr.MergeState)
	}
	if err := (GH{}).MarkBotReviews(&pr); err != nil {
		t.Fatalf("MarkBotReviews must not call GitHub for a Gitea PR: %v", err)
	}
}

func TestGiteaRunView(t *testing.T) {
	dir := withGitea(t)
	giteaAnswer(t, dir, "repos/simon/porto/actions/runs/23674", `{"id":23674,"status":"completed","conclusion":"failure",
	 "path":"deploy.yaml@refs/heads/main","html_url":"https://`+testGitea+`/simon/porto/actions/runs/23674","display_title":"deploy","created_at":"2026-11-20T10:00:00Z"}`)
	u := "https://" + testGitea + "/simon/porto/actions/runs/23674"
	got := nextRuns(nil, []Bead{{ID: "hub-1", Notes: "deploy: " + u}}, GH{}.RunView, t.Logf)
	r := got["hub-1"]
	if len(r) != 1 || r[0].Conclusion != "failure" || r[0].Workflow != "deploy.yaml" || r[0].Repo != "https://"+testGitea+"/simon/porto" || !runFailed(r[0]) {
		t.Fatalf("runs = %+v", got)
	}
	gone := nextRuns(nil, []Bead{{ID: "hub-1", Notes: "https://" + testGitea + "/simon/porto/actions/runs/1"}}, GH{}.RunView, t.Logf)
	if g := gone["hub-1"]; len(g) != 1 || g[0].Conclusion != runGone {
		t.Fatalf("a 404 run is gone: %+v", gone)
	}
}

func TestGiteaLookupMixedWithGitHub(t *testing.T) {
	dir := withGitea(t)
	u := "https://" + testGitea + "/simon/radar/pulls/12"
	giteaAnswer(t, dir, "repos/simon/radar/pulls/12", `{"number":12,"html_url":"`+u+`","state":"closed","merged":true,"merged_at":"2026-11-01T00:00:00Z","head":{"ref":"x"}}`)
	prs, err := GH{}.Lookup([]string{u})
	if err != nil || prs[u].State != "MERGED" {
		t.Fatalf("Lookup = %+v, %v", prs, err)
	}
	digest, n, err := GH{}.DigestPRs([]string{u})
	if err != nil || n != 1 || digest[u].State != "MERGED" {
		t.Fatalf("DigestPRs = %+v, %d, %v", digest, n, err)
	}
}

func TestFillDetail(t *testing.T) {
	open := giteaPull{State: "open", Mergeable: true}
	open.Head.SHA = "h1"
	ok := giteaStatus{State: "success", Statuses: []struct {
		Context string `json:"context"`
		Status  string `json:"status"`
	}{{"build", "success"}, {"lint", "failure"}}}
	approve := func(who, state string, official bool) giteaReview {
		return giteaReview{State: state, User: giteaUser{who}, Official: official, CommitID: "h1"}
	}

	var pr PR
	fillDetail(&pr, open, nil, ok, nil)
	if pr.MergeState != "UNSTABLE" || pr.ReviewDecision != "" {
		t.Fatalf("unprotected with a failing check: %q %q", pr.MergeState, pr.ReviewDecision)
	}
	fillDetail(&pr, open, nil, ok, &giteaProtection{RuleName: "main", EnableStatusCheck: true, StatusCheckContexts: []string{"lint"}})
	if pr.MergeState != "BLOCKED" {
		t.Fatalf("required check failing: %q", pr.MergeState)
	}
	fillDetail(&pr, open, nil, ok, &giteaProtection{RuleName: "main", EnableStatusCheck: true, StatusCheckContexts: []string{"bu*"}})
	if pr.MergeState != "UNSTABLE" {
		t.Fatalf("only a non-required check failing: %q", pr.MergeState)
	}
	two := &giteaProtection{RuleName: "main", RequiredApprovals: 1}
	fillDetail(&pr, open, []giteaReview{approve("ann", "APPROVED", false)}, giteaStatus{}, two)
	if pr.ReviewDecision != "REVIEW_REQUIRED" || pr.MergeState != "BLOCKED" {
		t.Fatalf("unofficial approval: %q %q", pr.ReviewDecision, pr.MergeState)
	}
	fillDetail(&pr, open, []giteaReview{approve("ann", "APPROVED", true)}, giteaStatus{}, two)
	if pr.ReviewDecision != "APPROVED" || pr.MergeState != "CLEAN" || len(pr.Reviews) != 1 || pr.Reviews[0].Commit.OID != "h1" {
		t.Fatalf("official approval: %q %q %+v", pr.ReviewDecision, pr.MergeState, pr.Reviews)
	}
	fillDetail(&pr, open, []giteaReview{approve("ann", "APPROVED", true), approve("bob", "REQUEST_CHANGES", true)}, giteaStatus{}, nil)
	if pr.ReviewDecision != "CHANGES_REQUESTED" {
		t.Fatalf("changes requested: %q", pr.ReviewDecision)
	}
	conflicted := open
	conflicted.Mergeable = false
	fillDetail(&pr, conflicted, nil, giteaStatus{}, nil)
	if pr.MergeState != "DIRTY" {
		t.Fatalf("conflict: %q", pr.MergeState)
	}
}

func TestOpenThreads(t *testing.T) {
	r := &giteaUser{"ann"}
	n := openThreads([]giteaReviewComment{
		{Path: "a.go", OriginalPosition: 3, Position: 3},              // open
		{Path: "a.go", OriginalPosition: 3, Position: 3},              // a reply in the same thread
		{Path: "b.go", OriginalPosition: 5, Position: 5, Resolver: r}, // resolved
		{Path: "b.go", OriginalPosition: 5, Position: 5},              // its reply
		{Path: "c.go", OriginalPosition: 9, Position: 0},              // outdated
	})
	if n != 1 {
		t.Fatalf("openThreads = %d, want 1", n)
	}
}

func TestGiteaCommands(t *testing.T) {
	setGiteaHosts([]string{testGitea}, "gt")
	t.Cleanup(func() { setGiteaHosts(nil, "gt") })
	b := brief(Bead{ID: "hub-1", Title: "t"}, "bead/hub-1-t", "/w", "", "", true)
	g := giteaCommands(b)
	for _, want := range []string{
		"`gt pr merge <url> --auto --squash`",
		"`gt api -X DELETE repos/{owner}/{repo}/pulls/<number>/merge`",
		"`gt api -X POST repos/{owner}/{repo}/pulls/<number>/merge -f Do=squash -f head_commit_id=<sha>`",
		"`gt pr edit <url> --base <base>`",
	} {
		if !strings.Contains(g, want) {
			t.Errorf("brief lacks %s", want)
		}
	}
	if strings.Contains(g, "`gh ") {
		t.Errorf("brief still names gh:\n%s", g)
	}
	run := giteaCommands("Investigate with `gh run view 5 -R https://" + testGitea + "/simon/x --log-failed`, fix")
	if !strings.Contains(run, "`gt run view 5 -R https://"+testGitea+"/simon/x --log`") {
		t.Errorf("run prompt: %s", run)
	}
	ready := giteaCommands("then merge it with `gh pr merge 7 --match-head-commit abc123` only if")
	if !strings.Contains(ready, "`gt api -X POST repos/{owner}/{repo}/pulls/7/merge -f Do=squash -f head_commit_id=abc123`") {
		t.Errorf("ready prompt: %s", ready)
	}
}

func TestGiteaThreadPromptsNameGt(t *testing.T) {
	th := thread(bead(StatusInProgress), agent("idle", 1), openPR("build"))
	th.Gitea = true
	prev := Snapshot{Group: GroupReview, PRNumber: 7, PRState: "OPEN"}
	o := transition(th, prev, false, t0)
	if len(o.Prompts) != 1 || !strings.Contains(o.Prompts[0], "`gt pr checks 7`") {
		t.Fatalf("Gitea thread prompt: %+v", o.Prompts)
	}
	th.Gitea = false
	if o := transition(th, prev, false, t0); !strings.Contains(o.Prompts[0], "`gh pr checks 7`") {
		t.Fatalf("GitHub thread prompt: %+v", o.Prompts)
	}
}

func TestGiteaReviewsNotByGiteaLogin(t *testing.T) {
	withGitea(t)
	pr := PR{URL: "https://" + testGitea + "/simon/x/pulls/1", State: "OPEN"}
	var rv Review
	rv.Author.Login = "simon"
	pr.Reviews = []Review{rv}
	st := TickerState{Login: "simonellistonball", GiteaLogins: map[string]string{testGitea: "simon"}, PRs: map[string]PR{"hub-1": pr}}
	th := threadFor(Config{}, st, Bead{ID: "hub-1"}, nil)
	if th.Reviews != 0 {
		t.Fatalf("the user's own Gitea review counted as feedback: %d", th.Reviews)
	}
}


func TestGiteaGates(t *testing.T) {
	dir := withGitea(t)
	u := "https://" + testGitea + "/simon/x/pulls/4"
	giteaAnswer(t, dir, "repos/simon/x/pulls/4", `{"number":4,"html_url":"`+u+`","state":"open","mergeable":true,"head":{"ref":"b","sha":"h4"},"base":{"ref":"main"}}`)
	giteaAnswer(t, dir, "repos/simon/x/pulls/4/reviews?limit=50", `[{"id":9,"state":"COMMENT","comments_count":2,"user":{"login":"ann"}},{"id":10,"state":"APPROVED","comments_count":0,"user":{"login":"ann"}}]`)
	giteaAnswer(t, dir, "repos/simon/x/pulls/4/reviews/9/comments", `[{"path":"a.go","position":3,"original_position":3},{"path":"b.go","position":0,"original_position":7}]`)
	now := time.Now()
	gates, err := GH{}.Gates([]PR{{URL: u, HeadSHA: "h4"}}, now)
	g, ok := gates[u]
	if err != nil || !ok || g.Head != "h4" || g.OpenThreads != 1 || !g.At.Equal(now) {
		t.Fatalf("Gates = %+v, %v", gates, err)
	}
}
