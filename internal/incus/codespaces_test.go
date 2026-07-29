package incus

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// suf is the inverse of suffixIndex: index → fixed-width 2-char base-26 `split` suffix.
func suf(i int) string { return string(rune('a'+i/26)) + string(rune('a'+i%26)) }

func TestSuffixIndex(t *testing.T) {
	for s, want := range map[string]int{"aa": 0, "ab": 1, "az": 25, "ba": 26, "bb": 27, "ca": 52} {
		if got := suffixIndex(s); got != want {
			t.Errorf("suffixIndex(%q) = %d, want %d", s, got, want)
		}
	}
}

func nr(suffix, data string) namedReader {
	return namedReader{name: "img.qcow2.tar.part_" + suffix, r: strings.NewReader(data)}
}

// assemble must order by the split suffix (NOT the input slice order) and concatenate.
func TestAssembleOrdersByNotSliceOrder(t *testing.T) {
	parts := []namedReader{nr("ac", "C"), nr("aa", "A"), nr("ab", "B")} // deliberately out of order
	var buf bytes.Buffer
	if err := assemble(parts, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "ABC" {
		t.Errorf("assemble = %q, want ABC", buf.String())
	}
}

// A gap in the sequence (aa, ab, ad) must be rejected, not silently concatenated into a corrupt image.
func TestAssembleRejectsGap(t *testing.T) {
	parts := []namedReader{nr("aa", "A"), nr("ab", "B"), nr("ad", "D")}
	if err := assemble(parts, &bytes.Buffer{}); err == nil {
		t.Error("expected a contiguity error for the aa,ab,ad gap")
	}
}

// The az→ba rollover past 26 parts must stay correctly ordered.
func TestAssembleRolloverPast26(t *testing.T) {
	n := 30 // aa..az, ba, bb, bc, bd  (crosses the boundary)
	parts := make([]namedReader, n)
	var want strings.Builder
	for i := 0; i < n; i++ {
		parts[n-1-i] = nr(suf(i), suf(i)) // insert in REVERSE order; content == suffix
		want.WriteString(suf(i))
	}
	var buf bytes.Buffer
	if err := assemble(parts, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != want.String() {
		t.Errorf("rollover order wrong:\n got %q\nwant %q", buf.String(), want.String())
	}
}

func TestAssembleEmpty(t *testing.T) {
	if err := assemble(nil, &bytes.Buffer{}); err == nil {
		t.Error("expected an error for zero parts")
	}
}

// ListCodespaceReleases: parse, exclude prereleases/drafts, mark HasImage + size, newest first.
func TestListCodespaceReleases(t *testing.T) {
	body := `[
      {"tag_name":"v2","published_at":"2026-07-29T00:00:00Z","assets":[
        {"name":"v2.qcow2.tar.part_aa","size":100,"browser_download_url":"http://x/aa"},
        {"name":"v2.qcow2.tar.part_ab","size":50,"browser_download_url":"http://x/ab"}]},
      {"tag_name":"v1","published_at":"2026-07-01T00:00:00Z","assets":[{"name":"notes.txt","size":1}]},
      {"tag_name":"vpre","prerelease":true,"published_at":"2026-08-01T00:00:00Z","assets":[]},
      {"tag_name":"vdraft","draft":true,"published_at":"2026-09-01T00:00:00Z","assets":[]}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/glueops/codespaces/releases" {
			_, _ = fmt.Fprint(w, body)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	defer swapBase(srv.URL)()

	rels, err := (&Client{}).ListCodespaceReleases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 2 {
		t.Fatalf("got %d releases, want 2 (prerelease+draft excluded): %+v", len(rels), rels)
	}
	if rels[0].Tag != "v2" {
		t.Errorf("newest-first: got %q", rels[0].Tag)
	}
	if !rels[0].HasImage || rels[0].SizeBytes != 150 {
		t.Errorf("v2: HasImage=%v Size=%d, want true/150", rels[0].HasImage, rels[0].SizeBytes)
	}
	if rels[1].HasImage {
		t.Errorf("v1 has only notes.txt, HasImage should be false")
	}
}

// resolveRelease: "" / "latest" hits /releases/latest; an explicit tag hits /releases/tags/<tag>.
func TestResolveRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/glueops/codespaces/releases/latest":
			_, _ = fmt.Fprint(w, `{"tag_name":"v9","published_at":"2026-07-29T00:00:00Z","assets":[{"name":"v9.qcow2.tar.part_aa","size":10,"browser_download_url":"http://x/aa"}]}`)
		case "/repos/glueops/codespaces/releases/tags/v5":
			_, _ = fmt.Fprint(w, `{"tag_name":"v5","published_at":"2026-06-01T00:00:00Z","assets":[{"name":"v5.qcow2.tar.part_aa","size":5,"browser_download_url":"http://x/aa"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	defer swapBase(srv.URL)()

	c := &Client{}
	if r, err := c.resolveRelease(context.Background(), ""); err != nil || r.Tag != "v9" {
		t.Fatalf("latest: tag=%q err=%v, want v9", r.Tag, err)
	}
	if r, err := c.resolveRelease(context.Background(), "v5"); err != nil || r.Tag != "v5" {
		t.Fatalf("explicit tag: tag=%q err=%v, want v5", r.Tag, err)
	}
	if _, err := c.resolveRelease(context.Background(), "nope"); err == nil {
		t.Error("expected an error for a missing tag")
	}
}

// isOurCodespaceAlias must treat per-tag aliases as ours but NOT the rolling `latest` — otherwise
// the retargetLatest clobber guard would hijack a user's hand-created glueops-codespace-latest.
func TestIsOurCodespaceAlias(t *testing.T) {
	cases := map[string]bool{
		"glueops-codespace-v0.153.0": true,
		"glueops-codespace-latest":   false, // the guarded name — must not count as ours
		"glueops-codespace-":         true,  // odd but still our prefix (not "latest")
		"some-other-image":           false,
		"ubuntu/24.04/cloud":         false,
		"":                           false,
	}
	for name, want := range cases {
		if got := isOurCodespaceAlias(name); got != want {
			t.Errorf("isOurCodespaceAlias(%q) = %v, want %v", name, got, want)
		}
	}
}

// buildMetadata must (a) quote the description so a YAML metacharacter in the tag can't produce a
// malformed metadata.yaml, and (b) stamp creation_date from published_at (fixed UTC) so the same
// tag always yields the same fingerprint (idempotent re-import).
func TestBuildMetadataQuotingAndDeterminism(t *testing.T) {
	rel := CodespaceRelease{Tag: "v1#weird@tag", PublishedAt: time.Unix(1700000000, 0).UTC()}
	meta, err := buildMetadata(rel)
	if err != nil {
		t.Fatal(err)
	}
	// Unpack the gzipped tar and read the single metadata.yaml member.
	gz, err := gzip.NewReader(bytes.NewReader(meta))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	h, err := tr.Next()
	if err != nil || h.Name != "metadata.yaml" {
		t.Fatalf("first tar member = %v (err %v), want metadata.yaml", h, err)
	}
	body, _ := io.ReadAll(tr)
	yaml := string(body)
	// The description value must be double-quoted (so the # / @ can't break the document).
	if !strings.Contains(yaml, `description: "GlueOps Codespace v1#weird@tag"`) {
		t.Errorf("description not safely quoted:\n%s", yaml)
	}
	// Deterministic creation_date (published_at as a fixed Unix timestamp).
	if !strings.Contains(yaml, "creation_date: 1700000000") {
		t.Errorf("creation_date not deterministic:\n%s", yaml)
	}
	if !strings.Contains(yaml, "architecture: x86_64") {
		t.Errorf("missing architecture:\n%s", yaml)
	}
}

// swapBase points the GitHub client at a test server and returns a restore func.
func swapBase(url string) func() {
	old := githubAPIBase
	githubAPIBase = url
	return func() { githubAPIBase = old }
}
