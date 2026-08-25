// Package jsonraw edits JSON documents without re-serialising them.
//
// This is not a style preference. Go's encoding/json renders float64(600) as
// "600" and cannot tell an absent field from an explicit null, and
// tests/fixtures/export-9object - captured from a real tenant - contains both
// 600.0 and explicit nulls. Round-tripping a managed object through
// map[string]any would silently rewrite a customer's data on its way into the
// target tenant.
//
// sjson edits the raw text instead, so every byte it does not touch survives.
package jsonraw

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ServerManagedProperties are the fields the platform owns. They are stripped
// before a managed object is recreated, because the target tenant assigns its
// own and sending them back is either rejected or silently ignored.
//
// Mirrors STRIP_PROPERTIES in commands/import:126.
var ServerManagedProperties = []string{
	"id", "self", "lastUpdated", "creationTime", "owner",
	"childDevices", "childAssets", "childAdditions",
	"deviceParents", "assetParents", "additionParents",
	"c8y_Availability", "c8y_ActiveAlarmsStatus", "c8y_Connection",
}

// StripServerManaged removes the platform's own fields from a managed object.
func StripServerManaged(body []byte) ([]byte, error) {
	out := body
	for _, key := range ServerManagedProperties {
		var err error
		out, err = sjson.DeleteBytes(out, escapePath(key))
		if err != nil {
			return nil, fmt.Errorf("cannot remove %s: %w", key, err)
		}
	}
	return out, nil
}

// AddFragment adds an empty fragment under the given name.
//
// The result is valid JSON but not pretty: sjson appends rather than reflowing.
// That is fine, because this only ever becomes a request body - the platform
// does not care about whitespace, and nothing on disk is compared against it.
// Reformatting would mean running the document through a printer, which is
// exactly the kind of thing that turns 600.0 into 600.
func AddFragment(body []byte, name string) ([]byte, error) {
	out, err := sjson.SetRawBytes(body, escapePath(name), []byte("{}"))
	if err != nil {
		return nil, fmt.Errorf("cannot add fragment %s: %w", name, err)
	}
	return out, nil
}

// Name reads a managed object's name, or "" when it has none.
func Name(body []byte) string {
	return gjson.GetBytes(body, "name").String()
}

// String reads any top-level string field.
func String(body []byte, key string) string {
	return gjson.GetBytes(body, escapePath(key)).String()
}

// escapePath protects the gjson/sjson path syntax from field names that use its
// metacharacters. Cumulocity fragment names are conventionally
// [A-Za-z0-9_]-ish, but customers own that namespace and a name like
// "my.fragment" would otherwise be read as a nested path.
func escapePath(key string) string {
	var b strings.Builder
	for _, r := range key {
		switch r {
		case '.', '*', '?', '|', '#', '@', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
