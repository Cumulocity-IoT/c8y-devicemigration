package migrate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/migrate"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/testfixture"
)

var allData = []export.DataType{export.DataMeasurements, export.DataEvents, export.DataAlarms}

func wholeHistory() map[export.DataType]export.Window {
	return map[export.DataType]export.Window{
		export.DataMeasurements: {}, export.DataEvents: {}, export.DataAlarms: {},
	}
}

// setup gives two tenants and somewhere to write the mapping.
type harness struct {
	source, target *testfixture.Server
	work           string
	mapping        string
	opts           migrate.Options
}

func setup(t *testing.T, opts migrate.Options) *harness {
	t.Helper()

	work := t.TempDir()
	h := &harness{
		source:  testfixture.New(t, "hierarchy.json"),
		target:  testfixture.New(t, "empty.json"),
		work:    work,
		mapping: filepath.Join(work, "id_mapping.json"),
	}

	if opts.RootID == "" {
		opts.RootID = "1"
	}
	if opts.ChildTypes == nil {
		opts.ChildTypes = export.DefaultChildTypes
	}
	if opts.Windows == nil {
		opts.Windows = wholeHistory()
	}
	opts.MappingFile = h.mapping
	opts.Force = true
	h.opts = opts
	return h
}

// run performs the migration and returns its stderr.
func (h *harness) run(t *testing.T, stdin string) (string, error) {
	t.Helper()

	sourceSession := h.source.ReadSession(t, h.work, "source.json")
	targetSession := h.target.ReadSession(t, h.work, "target.json")
	sourceClient, err := c8yclient.New(sourceSession, "")
	if err != nil {
		t.Fatal(err)
	}
	targetClient, err := c8yclient.New(targetSession, "")
	if err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	runErr := migrate.New(sourceClient, targetClient, sourceSession, targetSession,
		&stderr, strings.NewReader(stdin), h.opts).Run(context.Background())
	return stderr.String(), runErr
}

func (h *harness) journalLines(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(h.mapping + ".journal")
	if err != nil {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func (h *harness) journalHas(t *testing.T, line string) bool {
	t.Helper()
	for _, existing := range h.journalLines(t) {
		if existing == line {
			return true
		}
	}
	return false
}

func (h *harness) runID(t *testing.T) string {
	t.Helper()
	for _, line := range h.journalLines(t) {
		if rest, ok := strings.CutPrefix(line, "runId "); ok {
			return rest
		}
	}
	return ""
}

// --- chained ----------------------------------------------------------------

func TestChainedMigratesEverything(t *testing.T) {
	h := setup(t, migrate.Options{})
	stderr, err := h.run(t, "")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, stderr)
	}

	if got := len(h.target.Created()); got != 9 {
		t.Errorf("created %d objects, want 9", got)
	}
	if got := len(h.target.Links()); got != 10 {
		t.Errorf("assigned %d relationships, want 10", got)
	}
	if got := len(h.target.CreatedIdentities()); got != 4 {
		t.Errorf("created %d identities, want 4", got)
	}
	for _, want := range []string{
		"Exporting from the source tenant",
		"Importing into the target tenant",
		"Migration complete. Reverse it with:",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr is missing %q:\n%s", want, stderr)
		}
	}
}

func TestChainedWritesNoJournal(t *testing.T) {
	// It imports all the data in one step, so there is no point at which it
	// could honestly claim a device was finished.
	h := setup(t, migrate.Options{DataTypes: allData})
	if _, err := h.run(t, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.mapping + ".journal"); err == nil {
		t.Error("a chained migration wrote a journal")
	}
}

func TestTheStagingDirectoryIsRemovedOnSuccess(t *testing.T) {
	before := stagingDirectories(t)

	h := setup(t, migrate.Options{DataTypes: allData})
	if _, err := h.run(t, ""); err != nil {
		t.Fatal(err)
	}

	if after := stagingDirectories(t); after != before {
		t.Errorf("staging directories went from %d to %d", before, after)
	}
}

func TestKeepStagingKeepsItAndSaysWhere(t *testing.T) {
	h := setup(t, migrate.Options{KeepStaging: true})
	stderr, err := h.run(t, "")
	if err != nil {
		t.Fatal(err)
	}

	found := regexp.MustCompile(`Staging directory kept at (\S+)`).FindStringSubmatch(stderr)
	if found == nil {
		t.Fatalf("stderr does not say where staging was kept:\n%s", stderr)
	}
	if _, statErr := os.Stat(found[1]); statErr != nil {
		t.Errorf("the staging directory is not there: %v", statErr)
	}
	// and it holds the export, so a retry does not have to read the source again
	if _, statErr := os.Stat(filepath.Join(found[1], "export", "exported_device_1.json")); statErr != nil {
		t.Errorf("the kept staging directory holds no export: %v", statErr)
	}
	_ = os.RemoveAll(found[1])
}

// --- the confirmation -------------------------------------------------------

func TestTheConfirmationIsAskedWithoutForce(t *testing.T) {
	h := setup(t, migrate.Options{})
	h.opts.Force = false

	stderr, err := h.run(t, "yes\n")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, stderr)
	}
	// c8y's own dialog shape, so someone used to c8y reads the same thing here
	for _, want := range []string{
		"Confirm (job: 1)",
		"[y] Yes  [a] Yes to All  [n] No  [l] No to All",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the dialog is missing %q:\n%s", want, stderr)
		}
	}
	if len(h.target.Created()) != 9 {
		t.Error("a confirmed migration did not run")
	}
}

func TestTheDialogAcceptsWhatC8yAccepts(t *testing.T) {
	// y, yes, a and yestoall all mean yes there, so they mean yes here.
	for _, answer := range []string{"y\n", "Y\n", "yes\n", "YES\n", "a\n", "yestoall\n"} {
		h := setup(t, migrate.Options{})
		h.opts.Force = false

		if _, err := h.run(t, answer); err != nil {
			t.Errorf("answer %q should proceed: %v", answer, err)
			continue
		}
		if len(h.target.Created()) != 9 {
			t.Errorf("answer %q did not migrate", answer)
		}
	}
}

func TestAnythingElseMigratesNothing(t *testing.T) {
	// Including an empty line: the safe answer is the default, and a typo must
	// never be read as consent.
	for _, answer := range []string{"no\n", "n\n", "l\n", "maybe\n", "\n", ""} {
		h := setup(t, migrate.Options{})
		h.opts.Force = false

		_, err := h.run(t, answer)
		if !errors.Is(err, migrate.ErrAborted) {
			t.Errorf("answer %q should abort, got %v", answer, err)
		}
		if len(h.target.Created()) != 0 {
			t.Errorf("answer %q created objects anyway", answer)
		}
	}
}

// --- incremental ------------------------------------------------------------

func TestIncrementalProducesTheSameResultAsChained(t *testing.T) {
	// The main correctness argument for the incremental path.
	chained := setup(t, migrate.Options{DataTypes: allData})
	if _, err := chained.run(t, ""); err != nil {
		t.Fatal(err)
	}

	incremental := setup(t, migrate.Options{DataTypes: allData, Incremental: true})
	if _, err := incremental.run(t, ""); err != nil {
		t.Fatal(err)
	}

	if got, want := describe(t, incremental), describe(t, chained); got != want {
		t.Errorf("the two paths differ\nchained:     %s\nincremental: %s", want, got)
	}
}

func TestIncrementalJournalsEveryDevice(t *testing.T) {
	h := setup(t, migrate.Options{DataTypes: allData, Incremental: true})
	stderr, err := h.run(t, "")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, stderr)
	}

	var data int
	for _, line := range h.journalLines(t) {
		if strings.HasPrefix(line, "data ") {
			data++
		}
	}
	if data != 9 {
		t.Errorf("journalled %d devices, want 9\n%s", data, strings.Join(h.journalLines(t), "\n"))
	}
	if h.runID(t) == "" {
		t.Error("the journal does not record which run it belongs to")
	}
	if !strings.Contains(stderr, "Migrating data for 9 devices, one at a time") {
		t.Errorf("stderr:\n%s", stderr)
	}
}

func TestIncrementalHoldsOneDevicesDataAtATime(t *testing.T) {
	// What actually bounds the disk. If the export of device N+1 ran before the
	// import of device N, the staging directory would grow with the hierarchy
	// however promptly it was cleaned afterwards.
	h := setup(t, migrate.Options{DataTypes: allData, Incremental: true})
	stderr, err := h.run(t, "")
	if err != nil {
		t.Fatal(err)
	}

	// The per-device lines interleave export and import rather than forming one
	// block of each, which is all a chained run could produce.
	var order []string
	for _, line := range strings.Split(stderr, "\n") {
		switch {
		case strings.HasPrefix(line, "Exported ") && strings.Contains(line, "measurement"):
			order = append(order, "read")
		case strings.HasPrefix(line, "Imported ") && strings.Contains(line, "measurement"):
			order = append(order, "write")
		}
	}
	writes := 0
	for i, step := range order {
		if step == "write" && (i == 0 || order[i-1] != "write") {
			writes++
		}
	}
	if writes < 2 {
		t.Errorf("reads and writes did not interleave, so data was not moved per device:\n%s", stderr)
	}
}

func TestIncrementalWithNoDataStopsAfterTheHierarchy(t *testing.T) {
	h := setup(t, migrate.Options{Incremental: true})
	stderr, err := h.run(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.target.Created()) != 9 {
		t.Errorf("created %d objects, want 9", len(h.target.Created()))
	}
	if strings.Contains(stderr, "Migrating data for") {
		t.Errorf("a data phase ran with no data types:\n%s", stderr)
	}
}

// --- helpers ----------------------------------------------------------------

// describe summarises a target tenant, named by source id so two runs are
// comparable despite allocating different target ids.
func describe(t *testing.T, h *harness) string {
	t.Helper()

	raw, err := os.ReadFile(h.mapping)
	if err != nil {
		t.Fatal(err)
	}
	var mapping map[string]string
	if err := json.Unmarshal(raw, &mapping); err != nil {
		t.Fatal(err)
	}
	reverse := map[string]string{}
	for oldID, newID := range mapping {
		reverse[newID] = oldID
	}

	var links []string
	for _, link := range h.target.Links() {
		links = append(links, reverse[link.ParentID]+" "+link.ChildType+" "+reverse[link.ChildID])
	}
	sort.Strings(links)

	counts := map[string]int{}
	for _, record := range h.target.CreatedRecords() {
		counts[record.Kind]++
	}

	var identities []string
	for _, identity := range h.target.CreatedIdentities() {
		identities = append(identities, reverse[identity.DeviceID]+" "+identity.Type+" "+identity.ExternalID)
	}
	sort.Strings(identities)

	return strings.Join([]string{
		"objects=" + itoa(len(h.target.Created())),
		"links=" + strings.Join(links, ","),
		"identities=" + strings.Join(identities, ","),
		"measurements=" + itoa(counts["measurements"]),
		"events=" + itoa(counts["events"]),
		"alarms=" + itoa(counts["alarms"]),
		"attachments=" + itoa(len(h.target.UploadedBinaries())),
	}, " ")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// stagingDirectories counts the staging directories in the temp dir, so a leak
// is visible.
func stagingDirectories(t *testing.T) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "c8y-devicemigration-staging.*"))
	if err != nil {
		t.Fatal(err)
	}
	return len(matches)
}
