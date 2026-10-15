package export

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Identity is one external identity, in the field order the artefact uses.
//
// The order matters: the artefact format puts type first, and the JSON encoder
// emits the keys in field order.
type Identity struct {
	Type       string `json:"type"`
	ExternalID string `json:"externalId"`
}

// artefacts records every file written, so a refused export can remove exactly
// what it created and nothing else.
type artefacts struct {
	paths []string

	// createdOutputDirectory is set only when this run created the output
	// directory, so a pre-existing one is left alone on cleanup.
	createdOutputDirectory string
}

func (a *artefacts) record(path string) { a.paths = append(a.paths, path) }

// revert removes the artefacts in reverse order, then tidies the directories
// they lived in.
func (a *artefacts) revert() {
	for i := len(a.paths) - 1; i >= 0; i-- {
		_ = os.Remove(a.paths[i])
	}

	if a.createdOutputDirectory != "" {
		_ = os.RemoveAll(a.createdOutputDirectory)
		return
	}

	// Walk each artefact's directory upwards, removing empty ones and stopping
	// at the first that still holds something.
	for i := len(a.paths) - 1; i >= 0; i-- {
		dir := filepath.Dir(a.paths[i])
		for dir != "." && dir != string(filepath.Separator) {
			if err := os.Remove(dir); err != nil {
				break
			}
			dir = filepath.Dir(dir)
		}
	}
}

// writeJSONIndented writes a value pretty-printed: two-space indent and a
// trailing newline.
func writeJSONIndented(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return os.WriteFile(path, encoded, 0o644)
}

// writeRaw writes bytes verbatim. Managed objects go through here rather than
// through any encoder: re-serialising would rewrite 600.0 as 600 and drop the
// distinction between an absent field and an explicit null, which is a silent
// change to a customer's data.
func writeRaw(path string, body []byte) error {
	return os.WriteFile(path, body, 0o644)
}

func objectPath(dir, id string) string {
	return filepath.Join(dir, fmt.Sprintf("exported_device_%s.json", id))
}

func identitiesPath(dir, id string) string {
	return filepath.Join(dir, fmt.Sprintf("%s_identities.json", id))
}

func childIndexPath(dir, id string, childType ChildType) string {
	return filepath.Join(dir, fmt.Sprintf("%s_child_%s.json", id, childType))
}

// displayName makes a name fit on one progress line: tabs and newlines become
// spaces, and a name that is empty or all spaces is reported as <unnamed>.
func displayName(name string) string {
	name = strings.ReplaceAll(name, "\t", " ")
	name = strings.ReplaceAll(name, "\n", " ")
	if strings.TrimSpace(name) == "" {
		return "<unnamed>"
	}
	return name
}
