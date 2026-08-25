// Package importer recreates an exported hierarchy in a target tenant.
package importer

import (
	"encoding/json"
	"os"
	"strings"
)

// Mapping records which source object became which target object.
//
// It keeps insertion order, because the file is compared byte for byte against
// the one commands/import writes and jq preserves the order entries were added
// in. Go's map marshalling sorts keys instead, which happens to agree for
// numeric ids but would diverge the moment an entry arrived out of order - on a
// resume, for instance.
type Mapping struct {
	path string

	order []string
	newID map[string]string
	names map[string]string
}

func NewMapping(path string) *Mapping {
	return &Mapping{
		path:  path,
		newID: map[string]string{},
		names: map[string]string{},
	}
}

// Add records a created object and returns whether it was new.
func (m *Mapping) Add(oldID, newID, name string) {
	if _, seen := m.newID[oldID]; !seen {
		m.order = append(m.order, oldID)
	}
	m.newID[oldID] = newID
	m.names[oldID] = name
}

// NewID returns the target id for a source id, or "" when it was not imported.
func (m *Mapping) NewID(oldID string) string { return m.newID[oldID] }

// Name returns the name recorded for a source id.
func (m *Mapping) Name(oldID string) string { return m.names[oldID] }

// Len is how many objects have been created.
func (m *Mapping) Len() int { return len(m.order) }

// Write saves the mapping.
//
// Called after every created object, not once at the end: an import that dies
// half way must still be revertable, and a mapping written only on success
// would strand everything it had already created. That was a real bug once.
func (m *Mapping) Write() error {
	var b strings.Builder
	b.WriteString("{")
	for i, oldID := range m.order {
		if i > 0 {
			b.WriteString(",")
		}
		key, _ := json.Marshal(oldID)
		value, _ := json.Marshal(m.newID[oldID])
		b.WriteString("\n  ")
		b.Write(key)
		b.WriteString(": ")
		b.Write(value)
	}
	if len(m.order) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("}\n")

	return os.WriteFile(m.path, []byte(b.String()), 0o644)
}
