package service_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/importer"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/service"
)

// The fragment names are written in two languages, and neither end can change
// one alone.
//
// The service writes these onto managed objects; the UI plugin queries for them -
// to count what a migration created, and to count how many of those devices have
// collected a credential. A rename in Go would leave the plugin querying for a
// fragment nothing carries, which does not fail: it reports zero, forever, and
// reads as "no device has collected yet".
//
// Pinned the way this repository already pins the Go engine against the bash
// implementation: by reading the other side's source rather than by trusting a
// comment to be noticed.
func TestTheUIAgreesAboutFragmentNames(t *testing.T) {
	source := readUISource(t, "fragments.ts")

	for _, constant := range []struct {
		name string
		want string
	}{
		{"MARKER_PREFIX", importer.MarkerPrefix},
		{"CREDENTIALS_ISSUED_FRAGMENT", service.IssuedFragment},
	} {
		got := stringConstant(t, source, constant.name)
		if got != constant.want {
			t.Errorf("ui/src/api/fragments.ts has %s = %q, but this service writes %q",
				constant.name, got, constant.want)
		}
	}
}

// The marker prefix used to be written out by hand in a template as well. If it
// comes back, the constant above stops being the single place it is spelled.
func TestNoUITemplateSpellsTheMarkerOutByHand(t *testing.T) {
	root := filepath.Join("..", "..", "ui", "src")

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		switch filepath.Ext(path) {
		case ".html", ".ts":
		default:
			return nil
		}
		// The one file allowed to say it, and the generated API types, which
		// carry whatever the OpenAPI document's examples contain.
		if filepath.Base(path) == "fragments.ts" || filepath.Base(path) == "types.ts" {
			return nil
		}

		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if regexp.MustCompile(regexp.QuoteMeta(importer.MarkerPrefix)).Match(body) {
			t.Errorf("%s spells %q out by hand; use markerFragment() from api/fragments",
				path, importer.MarkerPrefix)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("could not walk the UI source: %v", err)
	}
}

// readUISource reads a file from the UI plugin, skipping if it is not there -
// the Go build does not depend on the plugin, and a source tree without it
// should not fail this.
func readUISource(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join("..", "..", "ui", "src", "api", name)
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skipf("no UI plugin source at %s", path)
		}
		t.Fatalf("could not read %s: %v", path, err)
	}
	return string(body)
}

// stringConstant reads `export const NAME = '...'` out of TypeScript source.
//
// A regexp rather than a parser on purpose: the alternative is a build
// dependency on node for one assertion, and the declaration this reads is one
// line in a file that exists to hold it.
func stringConstant(t *testing.T, source, name string) string {
	t.Helper()

	pattern := regexp.MustCompile(`export const ` + regexp.QuoteMeta(name) + ` = '([^']*)'`)
	match := pattern.FindStringSubmatch(source)
	if match == nil {
		t.Fatalf("ui/src/api/fragments.ts declares no %s", name)
	}
	return match[1]
}
