package main

// Gitea backend (homelab fork). quartermaster follows PRs and Actions runs
// through gh and GitHub's GraphQL API; a repository whose origin is on one of
// the configured gitea_hosts is followed through Gitea's REST API instead, by
// way of `gt api`, the Gitea CLI, which holds the login the way gh does for
// GitHub. The GH methods hand a Gitea repository or URL to this file and keep
// their GitHub path unchanged; everything above them sees the same PR, Run
// and DigestPR shapes whichever forge a repository is on.
//
// What Gitea has no equivalent for is approximated:
//   - reviewDecision and mergeStateStatus are computed from the PR's reviews,
//     its head's commit statuses and the base branch's protection rule;
//   - review threads are review comments grouped by file and line, resolved
//     when any comment in the group is, outdated when Gitea no longer places
//     them in the diff;
//   - auto-merge (Gitea's scheduled merge) is not read, so AutoMerge stays nil;
//   - verify (stale-bead checks) stays GitHub-only and skips Gitea PRs.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// giteaHosts are the hosts followed as Gitea, from the config's gitea_hosts.
var giteaHosts []string

// giteaCLI is the Gitea CLI quartermaster runs, from the config's gitea_cli.
var giteaCLI = "gt"

const ghPRPattern = `https://(?:%s)/([\w.-]+/[\w.-]+)/pulls?/(\d+)`
const ghRunPattern = `https://(?:%s)/([\w.-]+/[\w.-]+)/actions/runs/(\d+)`

// setGiteaHosts makes PR and run URLs on hosts count alongside GitHub's.
// prURL and runURL keep their submatches (owner/repo, number) for every host.
func setGiteaHosts(hosts []string, cli string) {
	giteaHosts = nil
	alt := []string{`github\.com`}
	for _, h := range hosts {
		if h = normalizeHost(h); h != "" && !slices.Contains(giteaHosts, h) {
			giteaHosts = append(giteaHosts, h)
			alt = append(alt, regexp.QuoteMeta(h))
		}
	}
	if cli != "" {
		giteaCLI = cli
	}
	if len(giteaHosts) == 0 {
		prURL = regexp.MustCompile(`https://github\.com/([\w.-]+/[\w.-]+)/pull/(\d+)`)
		runURL = regexp.MustCompile(`https://github\.com/([\w.-]+/[\w.-]+)/actions/runs/(\d+)`)
		return
	}
	prURL = regexp.MustCompile(fmt.Sprintf(ghPRPattern, strings.Join(alt, "|")))
	runURL = regexp.MustCompile(fmt.Sprintf(ghRunPattern, strings.Join(alt, "|")))
}

// normalizeHost is a host from a host or URL: GITEA_HOST is a URL.
func normalizeHost(h string) string {
	h = strings.TrimSpace(h)
	if u, err := url.Parse(h); err == nil && u.Host != "" {
		return strings.ToLower(u.Host)
	}
	return strings.ToLower(strings.TrimRight(h, "/"))
}

// giteaHostsFromEnv is GITEA_HOST, the workspace's Gitea, when the config
// names none.
func giteaHostsFromEnv() []string {
	if h := normalizeHost(os.Getenv("GITEA_HOST")); h != "" {
		return []string{h}
	}
	return nil
}

// giteaURL splits a Gitea PR or run URL into host and owner/repo, or false for
// a GitHub URL or anything else.
func giteaURL(u string) (host, slug string, ok bool) {
	if len(giteaHosts) == 0 {
		return "", "", false
	}
	rest, found := strings.CutPrefix(u, "https://")
	if !found {
		return "", "", false
	}
	host, path, _ := strings.Cut(rest, "/")
	host = strings.ToLower(host)
	if !slices.Contains(giteaHosts, host) {
		return "", "", false
	}
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 {
		return "", "", false
	}
	return host, parts[0] + "/" + parts[1], true
}

func isGiteaURL(u string) bool {
	_, _, ok := giteaURL(u)
	return ok
}

// remoteRef is a repository's forge: its host and owner/repo.
type remoteRef struct{ host, slug string }

var remotes sync.Map // repo dir -> remoteRef ("" host when not on Gitea)

// giteaRepo is the Gitea host and owner/repo of a checkout's origin, or false
// when origin isn't on a Gitea host. Remotes are read once per process.
func giteaRepo(dir string) (string, string, bool) {
	if len(giteaHosts) == 0 || dir == "" {
		return "", "", false
	}
	if r, ok := remotes.Load(dir); ok {
		ref := r.(remoteRef)
		return ref.host, ref.slug, ref.host != ""
	}
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	ref := remoteRef{}
	if err == nil {
		if h, s := parseRemote(strings.TrimSpace(string(out))); slices.Contains(giteaHosts, h) {
			ref = remoteRef{h, s}
		}
	}
	remotes.Store(dir, ref)
	return ref.host, ref.slug, ref.host != ""
}

// parseRemote is the host and owner/repo of a git remote URL: https (with or
// without a user), ssh://, or scp-like user@host:owner/repo.
func parseRemote(remote string) (host, slug string) {
	remote = strings.TrimSuffix(strings.TrimSuffix(remote, "/"), ".git")
	if u, err := url.Parse(remote); err == nil && u.Host != "" {
		host, slug = u.Hostname(), strings.Trim(u.Path, "/")
	} else if at, rest, ok := strings.Cut(remote, ":"); ok && !strings.Contains(at, "/") {
		_, host, _ = strings.Cut(at, "@")
		if host == "" {
			host = at
		}
		slug = strings.Trim(rest, "/")
	}
	if strings.Count(slug, "/") != 1 {
		return "", ""
	}
	return strings.ToLower(host), slug
}

// Gitea talks to one Gitea host through `gt api`.
type Gitea struct{ host string }

// errGitea404 marks a 404, as gh's "HTTP 404" does for GitHub.
var errGitea404 = errors.New("HTTP 404")

func (g Gitea) api(path string, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, giteaCLI, "api", path, "--host", g.host)
	cmd.Dir = os.TempDir() // never a checkout: the host and path say everything
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "HTTP 404") {
			return fmt.Errorf("%s api %s: %w: %s", giteaCLI, path, errGitea404, msg)
		}
		return fmt.Errorf("%s api %s: %v: %s", giteaCLI, path, err, msg)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(stdout.Bytes(), out)
}

var giteaLogins sync.Map // host -> login

// Login is the user gt is logged in as on the host, "" when it can't say.
func (g Gitea) Login() string {
	if l, ok := giteaLogins.Load(g.host); ok {
		return l.(string)
	}
	var u giteaUser
	if err := g.api("user", &u); err != nil || u.Login == "" {
		return ""
	}
	giteaLogins.Store(g.host, u.Login)
	return u.Login
}

type giteaUser struct {
	Login string `json:"login"`
}

type giteaPull struct {
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	HTMLURL   string     `json:"html_url"`
	State     string     `json:"state"` // open, closed
	Draft     bool       `json:"draft"`
	Merged    bool       `json:"merged"`
	MergedAt  *time.Time `json:"merged_at"`
	Mergeable bool       `json:"mergeable"`
	Head      struct {
		Ref   string `json:"ref"`
		Label string `json:"label"`
		SHA   string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	User giteaUser `json:"user"`
}

// branch is the PR's head branch. Once the branch is deleted (as after a
// merge) Gitea reports ref as refs/pull/N/head, and only the label keeps the
// name; a fork's label is owner:branch.
func (p giteaPull) branch() string {
	if !strings.HasPrefix(p.Head.Ref, "refs/") {
		return p.Head.Ref
	}
	l := p.Head.Label
	if _, b, ok := strings.Cut(l, ":"); ok {
		l = b
	}
	return l
}

func (p giteaPull) state() string {
	switch {
	case p.State == "open":
		return "OPEN"
	case p.Merged:
		return "MERGED"
	}
	return "CLOSED"
}

func (p giteaPull) pr() PR {
	pr := PR{
		Number:  p.Number,
		Title:   p.Title,
		URL:     p.HTMLURL,
		Head:    p.branch(),
		State:   p.state(),
		IsDraft: p.Draft,
		HeadSHA: p.Head.SHA,
	}
	if p.MergedAt != nil {
		pr.MergedAt = *p.MergedAt
	}
	return pr
}

// giteaPage is Gitea's default MAX_RESPONSE_ITEMS: a larger limit is cut to it.
const giteaPage = 50

// pulls lists up to max PRs of the repo in state (open, closed, all), by
// poster when one is given, newest first.
func (g Gitea) pulls(slug, state, poster string, max int) ([]giteaPull, error) {
	var all []giteaPull
	for page := 1; len(all) < max; page++ {
		q := url.Values{"state": {state}, "limit": {strconv.Itoa(giteaPage)}, "page": {strconv.Itoa(page)}}
		if poster != "" {
			q.Set("poster", poster)
		}
		var prs []giteaPull
		if err := g.api(fmt.Sprintf("repos/%s/pulls?%s", slug, q.Encode()), &prs); err != nil {
			return all, err
		}
		all = append(all, prs...)
		if len(prs) < giteaPage {
			break
		}
	}
	return all[:min(len(all), max)], nil
}

func (g Gitea) pull(slug string, n int) (giteaPull, error) {
	var p giteaPull
	return p, g.api(fmt.Sprintf("repos/%s/pulls/%d", slug, n), &p)
}

// mine is the user's PRs, as gh pr list --author @me.
func (g Gitea) mine(slug, state string, max int) ([]giteaPull, error) {
	login := g.Login()
	if login == "" {
		return nil, fmt.Errorf("%s api user: not logged in to %s", giteaCLI, g.host)
	}
	return g.pulls(slug, state, login, max)
}

type giteaReview struct {
	ID          int64     `json:"id"`
	State       string    `json:"state"` // APPROVED, REQUEST_CHANGES, COMMENT, PENDING, REQUEST_REVIEW
	User        giteaUser `json:"user"`
	CommitID    string    `json:"commit_id"`
	SubmittedAt time.Time `json:"submitted_at"`
	Stale       bool      `json:"stale"`
	Dismissed   bool      `json:"dismissed"`
	Official    bool      `json:"official"`
	Comments    int       `json:"comments_count"`
}

type giteaStatus struct {
	State    string `json:"state"`
	Statuses []struct {
		Context string `json:"context"`
		Status  string `json:"status"` // pending, success, error, failure, warning, skipped
	} `json:"statuses"`
}

type giteaProtection struct {
	RuleName               string   `json:"rule_name"`
	BranchName             string   `json:"branch_name"`
	EnableStatusCheck      bool     `json:"enable_status_check"`
	StatusCheckContexts    []string `json:"status_check_contexts"`
	RequiredApprovals      int      `json:"required_approvals"`
	BlockOnRejectedReviews bool     `json:"block_on_rejected_reviews"`
	DismissStaleApprovals  bool     `json:"dismiss_stale_approvals"`
}

func (g Gitea) reviews(slug string, n int) ([]giteaReview, error) {
	var rs []giteaReview
	return rs, g.api(fmt.Sprintf("repos/%s/pulls/%d/reviews?limit=%d", slug, n, giteaPage), &rs)
}

func (g Gitea) status(slug, sha string) (giteaStatus, error) {
	var s giteaStatus
	if sha == "" {
		return s, nil
	}
	return s, g.api(fmt.Sprintf("repos/%s/commits/%s/status?limit=%d", slug, sha, giteaPage), &s)
}

// protection is the rule that protects base: one named for it, else the first
// whose glob matches it. nil when the branch is unprotected or the rules can't
// be read (a token without admin on the repo gets 403).
func (g Gitea) protection(slug, base string) *giteaProtection {
	var rules []giteaProtection
	if err := g.api(fmt.Sprintf("repos/%s/branch_protections", slug), &rules); err != nil {
		return nil
	}
	return protectionFor(rules, base)
}

func protectionFor(rules []giteaProtection, base string) *giteaProtection {
	for i, r := range rules {
		if r.name() == base {
			return &rules[i]
		}
	}
	for i, r := range rules {
		if globMatch(r.name(), base) {
			return &rules[i]
		}
	}
	return nil
}

func (r giteaProtection) name() string {
	if r.RuleName != "" {
		return r.RuleName
	}
	return r.BranchName
}

// globMatch is Gitea's glob for rule names and required contexts: * and ?
// match any characters, / included.
func globMatch(pattern, s string) bool {
	var re strings.Builder
	re.WriteString("^")
	for _, c := range pattern {
		switch c {
		case '*':
			re.WriteString(".*")
		case '?':
			re.WriteString(".")
		default:
			re.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	re.WriteString("$")
	ok, _ := regexp.MatchString(re.String(), s)
	return ok
}

// checkState is a Gitea commit status in the terms summarizeChecks reads.
// warning and skipped pass, as they do for Gitea's own required checks.
func checkState(s string) string {
	switch s {
	case "success":
		return "SUCCESS"
	case "pending":
		return "PENDING"
	case "failure":
		return "FAILURE"
	case "error":
		return "ERROR"
	case "skipped":
		return "SKIPPED"
	}
	return "NEUTRAL"
}

func passed(s string) bool { return s == "success" || s == "warning" || s == "skipped" }

// view is a PR in full: reviews, checks, review decision and merge state
// computed the way GitHub reports them.
func (g Gitea) view(slug string, n int, detail bool) (PR, error) {
	p, err := g.pull(slug, n)
	if err != nil {
		return PR{}, err
	}
	pr := p.pr()
	if !detail {
		return pr, nil
	}
	var reviews []giteaReview
	var st giteaStatus
	var prot *giteaProtection
	var rErr, sErr error
	var wg sync.WaitGroup
	wg.Go(func() { reviews, rErr = g.reviews(slug, n) })
	wg.Go(func() { st, sErr = g.status(slug, p.Head.SHA) })
	wg.Go(func() { prot = g.protection(slug, p.Base.Ref) })
	wg.Wait()
	if rErr != nil {
		return PR{}, rErr
	}
	if sErr != nil {
		return PR{}, sErr
	}
	fillDetail(&pr, p, reviews, st, prot)
	return pr, nil
}

// fillDetail sets what gh pr view's detail fields would: reviews, the check
// rollup, reviewDecision and mergeStateStatus.
func fillDetail(pr *PR, p giteaPull, reviews []giteaReview, st giteaStatus, prot *giteaProtection) {
	pr.Reviews = nil
	for _, r := range reviews {
		if r.Dismissed {
			continue
		}
		state := map[string]string{"APPROVED": "APPROVED", "REQUEST_CHANGES": "CHANGES_REQUESTED", "COMMENT": "COMMENTED"}[r.State]
		if state == "" {
			continue // PENDING (a draft review) or REQUEST_REVIEW
		}
		var rv Review
		rv.Author.Login = r.User.Login
		rv.State = state
		rv.SubmittedAt = r.SubmittedAt
		rv.Commit.OID = r.CommitID
		pr.Reviews = append(pr.Reviews, rv)
	}
	pr.Checks = nil
	failing := false
	for _, s := range st.Statuses {
		pr.Checks = append(pr.Checks, Check{Context: s.Context, State: checkState(s.Status)})
		if s.Status == "failure" || s.Status == "error" {
			failing = true
		}
	}
	required := 0
	if prot != nil {
		required = prot.RequiredApprovals
	}
	approvals, changes := reviewTally(reviews, required > 0, prot != nil && prot.DismissStaleApprovals)
	switch {
	case changes:
		pr.ReviewDecision = "CHANGES_REQUESTED"
	case required > 0 && approvals >= required:
		pr.ReviewDecision = "APPROVED"
	case required > 0:
		pr.ReviewDecision = "REVIEW_REQUIRED"
	case approvals > 0:
		pr.ReviewDecision = "APPROVED"
	default:
		pr.ReviewDecision = ""
	}
	blocked := false
	if prot != nil {
		if prot.EnableStatusCheck {
			for _, want := range prot.StatusCheckContexts {
				found, ok := false, false
				for _, s := range st.Statuses {
					if globMatch(want, s.Context) {
						found = true
						ok = passed(s.Status)
						if !ok {
							break
						}
					}
				}
				if !found {
					// GitHub lists a required check that hasn't reported as
					// EXPECTED, which counts as pending.
					pr.Checks = append(pr.Checks, Check{Context: want, State: "EXPECTED"})
				}
				if !ok {
					blocked = true
				}
			}
		}
		if required > 0 && approvals < required {
			blocked = true
		}
		if prot.BlockOnRejectedReviews && changes {
			blocked = true
		}
	}
	switch {
	case p.State != "open":
		pr.MergeState = ""
	case p.Draft:
		pr.MergeState = "DRAFT"
	case !p.Mergeable:
		pr.MergeState = "DIRTY"
	case blocked:
		pr.MergeState = "BLOCKED"
	case failing:
		pr.MergeState = "UNSTABLE"
	default:
		pr.MergeState = "CLEAN"
	}
}

// reviewTally counts each reviewer's latest standing review: approvals (only
// official ones when the branch requires approvals, and not stale ones when
// it dismisses them) and whether anyone requests changes.
func reviewTally(reviews []giteaReview, officialOnly, dropStale bool) (approvals int, changes bool) {
	latest := map[string]giteaReview{}
	var order []string
	for _, r := range reviews {
		if r.Dismissed || (r.State != "APPROVED" && r.State != "REQUEST_CHANGES") {
			continue
		}
		if _, ok := latest[r.User.Login]; !ok {
			order = append(order, r.User.Login)
		}
		latest[r.User.Login] = r
	}
	for _, login := range order {
		r := latest[login]
		switch {
		case r.State == "REQUEST_CHANGES":
			changes = true
		case officialOnly && !r.Official, dropStale && r.Stale:
		default:
			approvals++
		}
	}
	return approvals, changes
}

type giteaReviewComment struct {
	Path             string     `json:"path"`
	Position         int        `json:"position"`
	OriginalPosition int        `json:"original_position"`
	Resolver         *giteaUser `json:"resolver"`
}

// gate is a PR's merge gate: its open review threads at head.
func (g Gitea) gate(slug string, n int, now time.Time) (MergeGate, error) {
	p, err := g.pull(slug, n)
	if err != nil {
		return MergeGate{}, err
	}
	reviews, err := g.reviews(slug, n)
	if err != nil {
		return MergeGate{}, err
	}
	var comments []giteaReviewComment
	for _, r := range reviews {
		if r.Comments == 0 {
			continue
		}
		var cs []giteaReviewComment
		if err := g.api(fmt.Sprintf("repos/%s/pulls/%d/reviews/%d/comments", slug, n, r.ID), &cs); err != nil {
			return MergeGate{}, err
		}
		comments = append(comments, cs...)
	}
	return MergeGate{Head: p.Head.SHA, OpenThreads: openThreads(comments), At: now}, nil
}

// openThreads groups review comments into conversations by file and line and
// counts those not resolved and still placed in the diff (Gitea zeroes the
// position of a comment a later push made outdated).
func openThreads(comments []giteaReviewComment) int {
	type key struct {
		path string
		line int
	}
	resolved := map[key]bool{}
	outdated := map[key]bool{}
	var keys []key
	for _, c := range comments {
		k := key{c.Path, c.OriginalPosition}
		if k.line == 0 {
			k.line = c.Position
		}
		if _, seen := resolved[k]; !seen {
			keys = append(keys, k)
			resolved[k] = false
			outdated[k] = true
		}
		if c.Resolver != nil {
			resolved[k] = true
		}
		if c.Position != 0 {
			outdated[k] = false
		}
	}
	n := 0
	for _, k := range keys {
		if !resolved[k] && !outdated[k] {
			n++
		}
	}
	return n
}

// giteaPRRef is a Gitea PR URL split up; false for anything else.
func giteaPRRef(u string) (Gitea, string, int, bool) {
	host, slug, ok := giteaURL(u)
	if !ok {
		return Gitea{}, "", 0, false
	}
	m := prURL.FindStringSubmatch(u)
	if m == nil {
		return Gitea{}, "", 0, false
	}
	n, _ := strconv.Atoi(m[2])
	return Gitea{host}, slug, n, true
}

// splitGitea parts URLs into GitHub's and Gitea's, each in order.
func splitGitea(urls []string) (github, gitea []string) {
	for _, u := range urls {
		if isGiteaURL(u) {
			gitea = append(gitea, u)
		} else {
			github = append(github, u)
		}
	}
	return github, gitea
}

// giteaLookup fetches each Gitea PR's list fields, keyed by URL, a few at a
// time. A PR that can't be fetched is missing; the first error is returned.
func giteaLookup(urls []string) (map[string]PR, error) {
	prs := map[string]PR{}
	found := make([]*PR, len(urls))
	errs := make([]error, len(urls))
	eachLimit(len(urls), ghParallel, func(i int) {
		g, slug, n, ok := giteaPRRef(urls[i])
		if !ok {
			return
		}
		pr, err := g.view(slug, n, false)
		if err != nil {
			errs[i] = err
			return
		}
		found[i] = &pr
	})
	for i, u := range urls {
		if found[i] != nil {
			prs[u] = *found[i]
		}
	}
	return prs, errors.Join(slices.DeleteFunc(errs, func(e error) bool { return e == nil })...)
}

// giteaDigest is DigestPRs for Gitea PRs: state, review decision, conflicts
// and the head's combined status.
func giteaDigest(urls []string) (map[string]DigestPR, int, error) {
	prs := map[string]DigestPR{}
	found := make([]*DigestPR, len(urls))
	errs := make([]error, len(urls))
	eachLimit(len(urls), ghParallel, func(i int) {
		g, slug, n, ok := giteaPRRef(urls[i])
		if !ok {
			return
		}
		p, err := g.pull(slug, n)
		if err != nil {
			errs[i] = err
			return
		}
		d := DigestPR{Number: p.Number, URL: urls[i], State: p.state(), IsDraft: p.Draft}
		if p.MergedAt != nil {
			d.MergedAt = *p.MergedAt
		}
		if d.State == "OPEN" {
			d.Mergeable = "CONFLICTING"
			if p.Mergeable {
				d.Mergeable = "MERGEABLE"
			}
			full := PR{}
			reviews, rErr := g.reviews(slug, n)
			st, sErr := g.status(slug, p.Head.SHA)
			if err := errors.Join(rErr, sErr); err != nil {
				errs[i] = err
				return
			}
			fillDetail(&full, p, reviews, st, g.protection(slug, p.Base.Ref))
			d.ReviewDecision = full.ReviewDecision
			d.Checks = combinedState(st.State)
		}
		found[i] = &d
	})
	for i, u := range urls {
		if found[i] != nil {
			prs[u] = *found[i]
		}
	}
	return prs, len(urls), errors.Join(slices.DeleteFunc(errs, func(e error) bool { return e == nil })...)
}

func combinedState(s string) string {
	switch s {
	case "success", "warning":
		return "SUCCESS"
	case "pending":
		return "PENDING"
	case "failure":
		return "FAILURE"
	case "error":
		return "ERROR"
	}
	return ""
}

type giteaRun struct {
	ID           int64     `json:"id"`
	Status       string    `json:"status"`
	Conclusion   *string   `json:"conclusion"`
	Path         string    `json:"path"` // ci.yaml@refs/heads/main
	HTMLURL      string    `json:"html_url"`
	DisplayTitle string    `json:"display_title"`
	CreatedAt    time.Time `json:"created_at"`
}

// giteaRunRepo is how a Gitea run's repository is recorded: its URL, which
// gt -R takes and which says the host. GitHub runs keep owner/repo.
func giteaRunRepo(host, slug string) string { return "https://" + host + "/" + slug }

// runRepo is the repository to record for a run URL whose regexp submatch
// gave slug.
func runRepo(u, slug string) string {
	if host, _, ok := giteaURL(u); ok {
		return giteaRunRepo(host, slug)
	}
	return slug
}

// giteaRunView is RunView for a run recorded with giteaRunRepo.
func giteaRunView(repo, id string) (Run, bool, error) {
	host, slug, ok := giteaURL(repo + "/")
	if !ok {
		return Run{}, false, nil
	}
	var r giteaRun
	if err := (Gitea{host}).api(fmt.Sprintf("repos/%s/actions/runs/%s", slug, id), &r); err != nil {
		return Run{}, true, err
	}
	run := Run{
		ID:        r.ID,
		Status:    r.Status,
		Workflow:  strings.SplitN(r.Path, "@", 2)[0],
		URL:       r.HTMLURL,
		Title:     r.DisplayTitle,
		Repo:      repo,
		CreatedAt: r.CreatedAt,
	}
	if run.Status == "running" {
		run.Status = "in_progress"
	}
	if r.Conclusion != nil {
		run.Conclusion = *r.Conclusion
	}
	return run, true, nil
}

// giteaCommands rewrites the gh commands in a prompt for a Gitea repository:
// gt takes the same pr and run subcommands, and the two gh flags it lacks
// become the Gitea API calls behind them, from the worktree so {owner}/{repo}
// resolve.
func giteaCommands(s string) string {
	// The gh shim routes gh merges through the executor; gt has no shim, so
	// the line saying it does would be wrong for a Gitea worker.
	s = shimLine.ReplaceAllString(s, "")
	s = mergeAtHead.ReplaceAllStringFunc(s, func(m string) string {
		sm := mergeAtHead.FindStringSubmatch(m)
		return fmt.Sprintf("`%s api -X POST repos/{owner}/{repo}/pulls/%s/merge -f Do=squash -f head_commit_id=%s`", giteaCLI, prNumberArg(sm[1]), sm[2])
	})
	s = disableAuto.ReplaceAllStringFunc(s, func(m string) string {
		sm := disableAuto.FindStringSubmatch(m)
		return fmt.Sprintf("`%s api -X DELETE repos/{owner}/{repo}/pulls/%s/merge`", giteaCLI, prNumberArg(sm[1]))
	})
	s = strings.ReplaceAll(s, " --log-failed`", " --log`")
	s = strings.ReplaceAll(s, "`gh ", "`"+giteaCLI+" ")
	s = strings.ReplaceAll(s, "Auto-merge is on, so GitHub merges it", "Auto-merge is on, so Gitea merges it")
	s = strings.ReplaceAll(s, "; GitHub turns it off", "; Gitea drops it")
	return s
}

var (
	shimLine    = regexp.MustCompile("(?m)^- Your `gh` may be quartermaster's shim.*\n")
	mergeAtHead = regexp.MustCompile("`gh pr merge (\\S+) (?:--squash )?--match-head-commit (\\S+)`")
	disableAuto = regexp.MustCompile("`gh pr merge (\\S+) --disable-auto`")
)

// prNumberArg is the PR argument for an API path: Gitea's needs the number.
func prNumberArg(arg string) string {
	if n := prNumber(arg); n != 0 {
		return strconv.Itoa(n)
	}
	if arg == "<url>" {
		return "<number>"
	}
	return arg
}
