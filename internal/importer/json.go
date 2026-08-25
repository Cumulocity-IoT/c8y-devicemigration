package importer

import "encoding/json"

// jsonUnmarshal is only used for the artefacts this tool wrote itself - child
// index files and identity files, which hold nothing but strings. Managed object
// bodies never come through here: they are edited as raw text so their numbers
// and nulls survive. See internal/jsonraw.
func jsonUnmarshal(data []byte, target any) error {
	return json.Unmarshal(data, target)
}
