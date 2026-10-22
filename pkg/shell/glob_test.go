package shell

import (
	"reflect"
	"testing"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

func TestGlobExpandWildcardsInAnyPart(t *testing.T) {
	fs := completionFS{dirs: map[string][]vfs.FileInfo{
		"/":              {{FileName: "specs", Dir: true}},
		"/specs":         {{FileName: "auth", Dir: true}, {FileName: "billing", Dir: true}, {FileName: "notes.md"}, {FileName: ".lore", Dir: true}},
		"/specs/auth":    {{FileName: "spec.md"}},
		"/specs/billing": {{FileName: "spec.md"}, {FileName: "design.md"}},
		"/specs/.lore":   {{FileName: "spec.md"}},
	}}
	s := NewShell(fs)
	s.cwd = "/specs"

	cases := map[string][]string{
		"/specs/*/spec.md": {"/specs/auth/spec.md", "/specs/billing/spec.md"},
		"*/spec.md":        {"/specs/auth/spec.md", "/specs/billing/spec.md"},
		"billing/*.md":     {"/specs/billing/design.md", "/specs/billing/spec.md"},
		"*/nothing.md":     nil,
		"/sp*/a*/spec.md":  {"/specs/auth/spec.md"},
	}
	for pattern, want := range cases {
		if got := s.globExpand(pattern); !reflect.DeepEqual(got, want) {
			t.Errorf("globExpand(%q) = %v, want %v", pattern, got, want)
		}
	}
}
