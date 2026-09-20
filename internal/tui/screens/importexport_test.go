package screens

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

type fakeImportSource struct {
	gotFileName string
	gotData     []byte
	profiles    []profile.Profile
	err         error
}

func (f *fakeImportSource) Import(fileName string, data []byte) ([]profile.Profile, error) {
	f.gotFileName = fileName
	f.gotData = data
	if f.err != nil {
		return nil, f.err
	}
	return f.profiles, nil
}

type fakeExportSource struct {
	gotProfileIDs []string
	data          []byte
	err           error
}

func (f *fakeExportSource) Export(profileIDs []string) ([]byte, error) {
	f.gotProfileIDs = profileIDs
	if f.err != nil {
		return nil, f.err
	}
	return f.data, nil
}

func TestImportFromPathReadsFileAndCallsImport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	content := []byte(`{"profiles":[]}`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fake := &fakeImportSource{profiles: []profile.Profile{{ID: "p1", Name: "one"}}}
	got, err := ImportFromPath(fake, path)
	if err != nil {
		t.Fatalf("ImportFromPath returned error: %v", err)
	}
	if fake.gotFileName != "profiles.json" {
		t.Fatalf("gotFileName = %q, want %q", fake.gotFileName, "profiles.json")
	}
	if string(fake.gotData) != string(content) {
		t.Fatalf("gotData = %q, want %q", fake.gotData, content)
	}
	if len(got) != 1 || got[0].ID != "p1" {
		t.Fatalf("unexpected imported profiles: %#v", got)
	}
}

func TestImportFromPathMissingFile(t *testing.T) {
	fake := &fakeImportSource{}
	_, err := ImportFromPath(fake, filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Fatalf("expected an error for a missing file")
	}
}

func TestImportFromPathPropagatesCodecError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	wantErr := errors.New("invalid import JSON")
	fake := &fakeImportSource{err: wantErr}
	_, err := ImportFromPath(fake, path)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped %v, got %v", wantErr, err)
	}
}

func TestExportToPathWritesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "export.json")

	fake := &fakeExportSource{data: []byte(`{"profiles":[]}`)}
	err := ExportToPath(fake, path, []string{"p1", "p2"})
	if err != nil {
		t.Fatalf("ExportToPath returned error: %v", err)
	}
	if len(fake.gotProfileIDs) != 2 {
		t.Fatalf("gotProfileIDs = %#v, want 2 entries", fake.gotProfileIDs)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(written) != `{"profiles":[]}` {
		t.Fatalf("written content = %q", written)
	}
}

func TestExportToPathWritesWithOwnerOnlyPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "export.json")

	fake := &fakeExportSource{data: []byte(`{"profiles":[]}`)}
	if err := ExportToPath(fake, path, nil); err != nil {
		t.Fatalf("ExportToPath returned error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file permissions = %o, want %o", got, 0o600)
	}
}

func TestExportToPathPropagatesCodecError(t *testing.T) {
	wantErr := errors.New("profile was not found")
	fake := &fakeExportSource{err: wantErr}
	err := ExportToPath(fake, filepath.Join(t.TempDir(), "export.json"), nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped %v, got %v", wantErr, err)
	}
}

func TestPathPromptSubmit(t *testing.T) {
	p := NewPathPrompt("Export to file:", "/tmp/default.json")
	if got := p.Submit(); got != "/tmp/default.json" {
		t.Fatalf("Submit() = %q, want %q", got, "/tmp/default.json")
	}
}
