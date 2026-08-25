package importer_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
)

// runGoExportInto exports the default hierarchy into dir, so the import tests do
// not each repeat the setup.
func runGoExportInto(t *testing.T, client *api.Client, dir string) error {
	t.Helper()
	var discard bytes.Buffer
	return export.New(client, &discard, export.Options{
		RootID: "1", OutputDirectory: dir, ChildTypes: export.DefaultChildTypes,
	}).Run(context.Background())
}
