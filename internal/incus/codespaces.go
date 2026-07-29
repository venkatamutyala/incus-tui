package incus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The GlueOps codespace VM image is published to GitHub Releases: the qcow2 is tar'd and
// `split -b 1024M` into assets named "<tag>.qcow2.tar.part_aa", "part_ab", … This file imports
// it into the local Incus image store. Kept vendor-specific in name but generic in mechanism:
// the repo is one const (env-overridable), and the asset-naming contract lives in exactly one
// place (codespacePartRe) so a publish-format change fails loud instead of silently.

const (
	codespacesRepoDefault = "glueops/codespaces"
	codespaceAliasPrefix  = "glueops-codespace-"
	codespaceLatestAlias  = codespaceAliasPrefix + "latest"
)

// codespacePartRe is the single source of truth for the split-image asset naming. If GlueOps
// changes the publish format (zstd, a single asset, different naming), releases will report
// HasImage=false and the importer emits a "publish format may have changed" error rather than
// silently doing the wrong thing.
var codespacePartRe = regexp.MustCompile(`\.qcow2\.tar\.part_[a-z]+$`)

// githubAPIBase is overridable in tests (point it at an httptest.Server). Asset download URLs
// come from the release JSON itself (browser_download_url), so tests drive both from one server.
var githubAPIBase = "https://api.github.com"

// codespacesRepo is the GitHub owner/repo the importer pulls from — the default, or the
// INCUS_TUI_CODESPACES_REPO override (the one knob that keeps the mechanism generic).
func codespacesRepo() string {
	if r := strings.TrimSpace(os.Getenv("INCUS_TUI_CODESPACES_REPO")); r != "" {
		return r
	}
	return codespacesRepoDefault
}

// CodespaceRelease is a flattened, UI-friendly view of a GitHub release that (maybe) carries a
// codespace image.
type CodespaceRelease struct {
	Tag         string
	PublishedAt time.Time
	Prerelease  bool
	HasImage    bool  // has *.qcow2.tar.part_* assets
	SizeBytes   int64 // sum of the part assets (the download size)
	parts       []ghAsset
}

// ImportProgress is reported to the caller during ImportCodespaceImage. Phase is one of
// resolve|download|assemble|import; Step is 1..4 for a "step N/4" display; Done/Total are bytes
// (download phase only — 0/0 for the opaque phases, where the UI shows an elapsed timer instead).
type ImportProgress struct {
	Phase string
	Step  int
	Done  int64
	Total int64
}

type ghAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
}

// httpClient returns a client suitable for a multi-minute image download: NO overall Timeout
// (that would guillotine a slow-but-progressing transfer) — cancellation is via ctx — but a
// bounded dial/response-header timeout so a *stuck* connection fails fast instead of hanging.
func httpClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: 30 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
			ExpectContinueTimeout: 5 * time.Second,
		},
	}
}

// ghGet performs a ctx-aware GET against the GitHub API, applying a best-effort token (a stale
// GITHUB_TOKEN must not break a public request — callers retry unauthenticated on 401). The
// caller owns closing the returned body.
func ghGet(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return httpClient().Do(req)
}

// rateLimited reports whether a 403 is a rate-limit (vs a permissions error), so the UI can tell
// the user to set GITHUB_TOKEN rather than sending them debugging the wrong thing.
func rateLimited(resp *http.Response) bool {
	return resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0"
}

// ghErr maps a non-2xx GitHub response to an actionable error.
func ghErr(resp *http.Response) error {
	switch {
	case rateLimited(resp):
		return fmt.Errorf("GitHub API rate limit reached — set GITHUB_TOKEN and retry")
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%s not found (private repo, or wrong INCUS_TUI_CODESPACES_REPO?)", codespacesRepo())
	default:
		return fmt.Errorf("GitHub API: %s", resp.Status)
	}
}

// toRelease flattens a raw GitHub release, marking whether it carries a codespace image.
func toRelease(r ghRelease) CodespaceRelease {
	cr := CodespaceRelease{Tag: r.TagName, PublishedAt: r.PublishedAt, Prerelease: r.Prerelease}
	for _, a := range r.Assets {
		if codespacePartRe.MatchString(a.Name) {
			cr.parts = append(cr.parts, a)
			cr.SizeBytes += a.Size
		}
	}
	cr.HasImage = len(cr.parts) > 0
	return cr
}

// ListCodespaceReleases returns the newest ~30 non-draft releases (newest first), each marked
// with whether it carries a codespace image. Prereleases are excluded (users consume stable
// tags). Used to populate the import picker.
func (c *Client) ListCodespaceReleases(ctx context.Context) ([]CodespaceRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=30", githubAPIBase, codespacesRepo())
	resp, err := ghGet(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("listing releases: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, ghErr(resp)
	}
	var raw []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decoding releases: %w", err)
	}
	out := make([]CodespaceRelease, 0, len(raw))
	for _, r := range raw {
		if r.Draft || r.Prerelease {
			continue
		}
		out = append(out, toRelease(r))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].PublishedAt.After(out[j].PublishedAt) })
	return out, nil
}

// resolveRelease fetches a single release: "" / "latest" via the /releases/latest endpoint
// (GitHub's own "latest" excludes prereleases and drafts), an explicit tag via /releases/tags/…
// (pagination-independent — valid old tags stay importable as the repo grows).
func (c *Client) resolveRelease(ctx context.Context, tag string) (CodespaceRelease, error) {
	repo := codespacesRepo()
	var url string
	if tag == "" || tag == "latest" {
		url = fmt.Sprintf("%s/repos/%s/releases/latest", githubAPIBase, repo)
	} else {
		url = fmt.Sprintf("%s/repos/%s/releases/tags/%s", githubAPIBase, repo, tag)
	}
	resp, err := ghGet(ctx, url)
	if err != nil {
		return CodespaceRelease{}, fmt.Errorf("resolving release %q: %w", tag, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return CodespaceRelease{}, ghErr(resp)
	}
	var r ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return CodespaceRelease{}, fmt.Errorf("decoding release: %w", err)
	}
	return toRelease(r), nil
}

// partSuffix extracts the fixed-width `split` suffix (e.g. "aa","ab",…,"az","ba") used to order
// parts. It returns "" for a non-matching name so callers can reject.
func partSuffix(name string) string {
	i := strings.LastIndex(name, ".part_")
	if i < 0 {
		return ""
	}
	return name[i+len(".part_"):]
}

// namedReader pairs a part's name with its byte stream, for ordered reassembly.
type namedReader struct {
	name string
	r    io.Reader
}

// assemble concatenates the split parts in `split`-suffix order into w. It is pure and the
// highest-value unit test: it sorts by the fixed-width base-26 suffix (NOT the caller's slice
// order, which for GitHub is the API's arbitrary asset order) and verifies contiguity — a gap
// (e.g. aa, ab, ad) is an error, not a silently-corrupt concatenation.
func assemble(parts []namedReader, w io.Writer) error {
	if len(parts) == 0 {
		return fmt.Errorf("no image parts to assemble")
	}
	ordered := make([]namedReader, len(parts))
	copy(ordered, parts)
	sort.SliceStable(ordered, func(i, j int) bool { return partSuffix(ordered[i].name) < partSuffix(ordered[j].name) })

	width := len(partSuffix(ordered[0].name))
	for i, p := range ordered {
		suf := partSuffix(p.name)
		if suf == "" || len(suf) != width {
			return fmt.Errorf("part %q has an unexpected split suffix (expected %d-char)", p.name, width)
		}
		if got := suffixIndex(suf); got != i {
			return fmt.Errorf("image parts are not contiguous: expected part #%d, got %q", i, p.name)
		}
	}
	buf := make([]byte, 1<<20) // bounded — never read a multi-GB part into memory
	for _, p := range ordered {
		if _, err := io.CopyBuffer(w, p.r, buf); err != nil {
			return fmt.Errorf("assembling %s: %w", p.name, err)
		}
	}
	return nil
}

// suffixIndex maps a base-26 lowercase `split` suffix ("aa"=0, "ab"=1, …, "ba"=26) to its ordinal.
func suffixIndex(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		n = n*26 + int(s[i]-'a')
	}
	return n
}

// ctxReader makes an otherwise un-cancelable read loop (tar extract, part concatenation) respond
// to ctx cancellation — those phases are non-HTTP and would otherwise ignore esc for minutes.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (cr *ctxReader) Read(p []byte) (int, error) {
	if err := cr.ctx.Err(); err != nil {
		return 0, err
	}
	return cr.r.Read(p)
}

// progressReader reports cumulative bytes read through onRead (used for the download phase).
type progressReader struct {
	r      io.Reader
	done   *int64
	onRead func(done int64)
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.r.Read(p)
	if n > 0 {
		*pr.done += int64(n)
		if pr.onRead != nil {
			pr.onRead(*pr.done)
		}
	}
	return n, err
}
