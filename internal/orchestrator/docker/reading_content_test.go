package docker

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeRunCmd serves canned responses keyed by substring of the command. If a
// command matches a key, the corresponding response (output, err) is returned.
// A "" key matches any unmatched command.
type fakeRunCmd struct {
	calls     []string
	responses map[string]struct {
		out string
		err error
	}
}

func (f *fakeRunCmd) run(_ context.Context, cmd string) (string, error) {
	f.calls = append(f.calls, cmd)
	for key, resp := range f.responses {
		if key != "" && strings.Contains(cmd, key) {
			return resp.out, resp.err
		}
	}
	if r, ok := f.responses[""]; ok {
		return r.out, r.err
	}
	return "", nil
}

func TestGetReadingContent_AllThreeFiles(t *testing.T) {
	fake := &fakeRunCmd{
		responses: map[string]struct {
			out string
			err error
		}{
			"raw.html":     {out: "<html>hello</html>"},
			"extracted.md": {out: "# hello"},
			"content.json": {out: `{"url":"https://x"}`},
		},
	}
	m := newManagerWithRunner(fake.run)

	raw, extracted, sidecar, err := m.GetReadingContent(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("GetReadingContent: %v", err)
	}
	if string(raw) != "<html>hello</html>" {
		t.Errorf("raw = %q", raw)
	}
	if string(extracted) != "# hello" {
		t.Errorf("extracted = %q", extracted)
	}
	if string(sidecar) != `{"url":"https://x"}` {
		t.Errorf("sidecar = %q", sidecar)
	}
	if len(fake.calls) != 3 {
		t.Errorf("expected 3 docker cp calls, got %d", len(fake.calls))
	}
	for _, want := range []string{"raw.html", "extracted.md", "content.json"} {
		var found bool
		for _, c := range fake.calls {
			if strings.Contains(c, "/home/agent/workspace/"+want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no call referenced /home/agent/workspace/%s", want)
		}
	}
}

func TestGetReadingContent_MissingExtractedTolerated(t *testing.T) {
	// Non-HTML payloads have no extracted.md; the docker cp call returns
	// non-nil error. The method must surface zero bytes rather than fail.
	fake := &fakeRunCmd{
		responses: map[string]struct {
			out string
			err error
		}{
			"raw.pdf":      {out: "%PDF-1.4\nminimal"},
			"extracted.md": {err: errors.New("no such file")},
			"content.json": {out: `{"url":"https://x","content_type":"application/pdf","content_status":"captured"}`},
		},
	}
	m := newManagerWithRunner(fake.run)

	raw, extracted, sidecar, err := m.GetReadingContent(context.Background(), "abc")
	if err != nil {
		t.Fatalf("GetReadingContent: %v", err)
	}
	if string(raw) != "%PDF-1.4\nminimal" {
		t.Errorf("raw = %q", raw)
	}
	if extracted != nil {
		t.Errorf("extracted should be nil, got %q", extracted)
	}
	if string(sidecar) != `{"url":"https://x","content_type":"application/pdf","content_status":"captured"}` {
		t.Errorf("sidecar = %q", sidecar)
	}
	for _, c := range fake.calls {
		if strings.Contains(c, "/home/agent/workspace/raw.html") {
			t.Errorf("non-HTML capture should not copy raw.html, got call: %s", c)
		}
	}
}

func TestGetReadingContent_FailureStatusSidecarOnly(t *testing.T) {
	// On fetch_failed / over_size_cap the in-container fetcher writes only a
	// sidecar (no raw, no extracted). The extractor must surface the sidecar
	// bytes so the orchestrator can record the failure status on the row.
	fake := &fakeRunCmd{
		responses: map[string]struct {
			out string
			err error
		}{
			"raw":          {err: errors.New("no such file")},
			"extracted.md": {err: errors.New("no such file")},
			"content.json": {out: `{"url":"https://x","content_status":"fetch_failed"}`},
		},
	}
	m := newManagerWithRunner(fake.run)

	raw, extracted, sidecar, err := m.GetReadingContent(context.Background(), "abc")
	if err != nil {
		t.Fatalf("GetReadingContent: %v", err)
	}
	if raw != nil {
		t.Errorf("raw should be nil on capture failure, got %q", raw)
	}
	if extracted != nil {
		t.Errorf("extracted should be nil on capture failure, got %q", extracted)
	}
	if string(sidecar) != `{"url":"https://x","content_status":"fetch_failed"}` {
		t.Errorf("sidecar = %q, want failure-status sidecar", sidecar)
	}
}

func TestGetReadingContent_LegacyContainerNoFiles(t *testing.T) {
	// All three files missing — pre-feature container. Should return all-nil
	// bytes and no error so the caller can mark content_status=''.
	fake := &fakeRunCmd{
		responses: map[string]struct {
			out string
			err error
		}{
			"": {err: errors.New("no such file")},
		},
	}
	m := newManagerWithRunner(fake.run)

	raw, extracted, sidecar, err := m.GetReadingContent(context.Background(), "abc")
	if err != nil {
		t.Fatalf("GetReadingContent: %v", err)
	}
	if raw != nil || extracted != nil || sidecar != nil {
		t.Errorf("expected all-nil bytes, got raw=%v extracted=%v sidecar=%v", raw, extracted, sidecar)
	}
}
