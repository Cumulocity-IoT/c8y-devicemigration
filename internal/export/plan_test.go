package export_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/c8yclient"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/testfixture"
)

// planFor walks a fixture and returns what an export would move.
func planFor(t *testing.T, fixture string, opts export.PlanOptions) *export.Plan {
	t.Helper()

	server := testfixture.New(t, fixture)
	session := server.ReadSession(t, t.TempDir(), "fixture.json")
	client, err := c8yclient.New(session, "")
	if err != nil {
		t.Fatal(err)
	}

	plan, err := export.MakePlan(context.Background(), client, opts)
	if err != nil {
		t.Fatalf("could not plan: %v", err)
	}
	return plan
}

// windows resolves a data window the way the command line does. The fixture's
// records are dated January, so a test that wants to count them has to say so -
// the default window for measurements is the last fourteen days.
func windows(t *testing.T, dateFrom string) map[export.DataType]export.Window {
	t.Helper()
	resolved, err := export.ResolveWindows(dateFrom, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// fixtureWindow covers everything in the committed fixtures.
const fixtureWindow = "2026-01-01"

func TestAPlanCountsTheWholeHierarchy(t *testing.T) {
	plan := planFor(t, "hierarchy.json", export.PlanOptions{
		RootID:     "1",
		ChildTypes: export.DefaultChildTypes,
	})

	// The same nine objects the export writes, and the same four identities it
	// reports - which is what makes this worth showing before a migration.
	if len(plan.ObjectIDs) != 9 {
		t.Fatalf("planned %d objects, want 9: %v", len(plan.ObjectIDs), plan.ObjectIDs)
	}
	if plan.ObjectIDs[0] != "1" {
		t.Fatalf("the root is not first: %v", plan.ObjectIDs)
	}
	if plan.Identities != 4 {
		t.Fatalf("counted %d identities, want 4", plan.Identities)
	}
	if len(plan.Breaches) != 0 {
		t.Fatalf("a hierarchy within every limit reported breaches: %+v", plan.Breaches)
	}
}

// A plan of the same hierarchy has to be the same plan: the traversal collects
// into a map, whose order is deliberately random.
func TestAPlanIsOrderedTheSameWayTwice(t *testing.T) {
	opts := export.PlanOptions{RootID: "1", ChildTypes: export.DefaultChildTypes}

	first := strings.Join(planFor(t, "hierarchy.json", opts).ObjectIDs, ",")
	for range 4 {
		if got := strings.Join(planFor(t, "hierarchy.json", opts).ObjectIDs, ","); got != first {
			t.Fatalf("plans differ between runs:\n%s\n%s", first, got)
		}
	}
}

func TestAPlanCountsRecordsOnlyForTheKindsAsked(t *testing.T) {
	plan := planFor(t, "hierarchy.json", export.PlanOptions{
		RootID:     "1",
		ChildTypes: export.DefaultChildTypes,
		DataTypes:  []export.DataType{export.DataMeasurements},
		Windows:    windows(t, fixtureWindow),
	})

	if plan.Totals[export.DataMeasurements] == 0 {
		t.Fatal("no measurements were counted")
	}
	if _, counted := plan.Totals[export.DataEvents]; counted {
		t.Fatalf("events were counted without being asked for: %v", plan.Totals)
	}

	// Per device as well as in total, because the limits are per device.
	perDevice := 0
	for _, device := range plan.Devices {
		perDevice += device.Records[export.DataMeasurements]
	}
	if perDevice != plan.Totals[export.DataMeasurements] {
		t.Fatalf("per-device counts sum to %d, total says %d",
			perDevice, plan.Totals[export.DataMeasurements])
	}
}

// Nothing is counted for a hierarchy that moves no data, and that is not the
// same as counting zero.
func TestAPlanWithNoDataTypesCountsNoRecords(t *testing.T) {
	plan := planFor(t, "hierarchy.json", export.PlanOptions{
		RootID:     "1",
		ChildTypes: export.DefaultChildTypes,
	})

	if len(plan.Totals) != 0 {
		t.Fatalf("totals = %v, want none", plan.Totals)
	}
	for _, device := range plan.Devices {
		if len(device.Records) != 0 {
			t.Fatalf("%s has record counts: %v", device.ObjectID, device.Records)
		}
	}
}

// The breach a wizard has to show before somebody presses Start. The fixture
// reports a total rather than generating a million records.
func TestAPlanReportsABreachedRecordLimit(t *testing.T) {
	server := testfixture.New(t, "hierarchy.json")
	server.SetReportedTotal("measurements", export.MaxMeasurementsPerDevice+42)

	session := server.ReadSession(t, t.TempDir(), "fixture.json")
	client, err := c8yclient.New(session, "")
	if err != nil {
		t.Fatal(err)
	}

	plan, err := export.MakePlan(context.Background(), client, export.PlanOptions{
		RootID:     "1",
		ChildTypes: export.DefaultChildTypes,
		DataTypes:  []export.DataType{export.DataMeasurements},
		Windows:    windows(t, fixtureWindow),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(plan.Breaches) != 1 {
		t.Fatalf("breaches = %+v, want one", plan.Breaches)
	}
	breach := plan.Breaches[0]
	if breach.Kind != export.DataMeasurements {
		t.Fatalf("breach kind = %q", breach.Kind)
	}
	if breach.Count != export.MaxMeasurementsPerDevice+42 {
		t.Fatalf("breach count = %d", breach.Count)
	}
	if breach.Allowed != export.MaxMeasurementsPerDevice {
		t.Fatalf("breach allowance = %d, want the engine's own limit", breach.Allowed)
	}
	if breach.ObjectID == "" {
		t.Fatal("the breach names no device, so nobody knows which one to narrow")
	}
	if breach.Devices != 9 {
		t.Fatalf("breach devices = %d, want all 9 over the limit", breach.Devices)
	}
}

// The object limit is counted the way the export counts it: the root is not a
// child of anything.
func TestTheObjectLimitExcludesTheRoot(t *testing.T) {
	plan := planFor(t, "hierarchy-wide.json", export.PlanOptions{
		RootID:     "1",
		ChildTypes: export.DefaultChildTypes,
	})

	children := len(plan.ObjectIDs) - 1
	if children <= export.MaxChildObjects {
		t.Skipf("hierarchy-wide.json holds %d children, not enough to breach the limit", children)
	}

	if len(plan.Breaches) == 0 {
		t.Fatal("a hierarchy over the object limit reported no breach")
	}
	breach := plan.Breaches[0]
	if breach.Kind != "" {
		t.Fatalf("the first breach is %q, want the object count", breach.Kind)
	}
	if breach.Count != children {
		t.Fatalf("breach count = %d, want %d children excluding the root",
			breach.Count, children)
	}
	if breach.Allowed != export.MaxChildObjects {
		t.Fatalf("breach allowance = %d", breach.Allowed)
	}
}

// The limits travel with the plan so a caller does not hardcode them and drift.
func TestAPlanCarriesTheEnforcedLimits(t *testing.T) {
	plan := planFor(t, "hierarchy.json", export.PlanOptions{
		RootID: "1", ChildTypes: export.DefaultChildTypes,
	})

	if plan.Limits.MaxChildObjects != export.MaxChildObjects {
		t.Errorf("MaxChildObjects = %d", plan.Limits.MaxChildObjects)
	}
	for kind, want := range map[export.DataType]int{
		export.DataMeasurements: export.MaxMeasurementsPerDevice,
		export.DataEvents:       export.MaxEventsPerDevice,
		export.DataAlarms:       export.MaxAlarmsPerDevice,
	} {
		if got := plan.Limits.PerDevice[kind]; got != want {
			t.Errorf("limit for %s = %d, want %d", kind, got, want)
		}
	}
}

// A root id that would end up in a file name is refused here too, before a
// single request.
func TestAPlanRefusesARootIdThatIsAPath(t *testing.T) {
	server := testfixture.New(t, "hierarchy.json")
	session := server.ReadSession(t, t.TempDir(), "fixture.json")
	client, err := c8yclient.New(session, "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := export.MakePlan(context.Background(), client,
		export.PlanOptions{RootID: "./1", ChildTypes: export.DefaultChildTypes}); err == nil {
		t.Fatal("a root id containing a path separator was planned")
	}

	if requests := server.CountRequests("/inventory"); requests != 0 {
		t.Fatalf("%d inventory requests were made before the id was checked", requests)
	}
}

// Planning writes nothing. It is the one guarantee that lets a wizard call this
// while somebody is still deciding.
func TestPlanningCreatesNothing(t *testing.T) {
	server := testfixture.New(t, "hierarchy.json")
	session := server.ReadSession(t, t.TempDir(), "fixture.json")
	client, err := c8yclient.New(session, "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := export.MakePlan(context.Background(), client, export.PlanOptions{
		RootID: "1", ChildTypes: export.DefaultChildTypes,
		DataTypes: export.AllDataTypes(), Windows: windows(t, fixtureWindow),
	}); err != nil {
		t.Fatal(err)
	}

	if created := len(server.Created()); created != 0 {
		t.Fatalf("planning created %d objects", created)
	}
	if deleted := len(server.Deleted()); deleted != 0 {
		t.Fatalf("planning deleted %d objects", deleted)
	}
}

// An estimate has to describe the migration that will actually run, and the
// window is the difference between "1.2 million measurements" and "none". The
// fixture's records are dated January, so the default fourteen-day window
// excludes them - exactly as a real run would.
func TestAPlanCountsOnlyWhatIsInsideTheWindow(t *testing.T) {
	inside := planFor(t, "hierarchy.json", export.PlanOptions{
		RootID:     "1",
		ChildTypes: export.DefaultChildTypes,
		DataTypes:  []export.DataType{export.DataMeasurements},
		Windows:    windows(t, fixtureWindow),
	})
	if inside.Totals[export.DataMeasurements] == 0 {
		t.Fatal("a window covering the fixture counted no measurements")
	}

	outside := planFor(t, "hierarchy.json", export.PlanOptions{
		RootID:     "1",
		ChildTypes: export.DefaultChildTypes,
		DataTypes:  []export.DataType{export.DataMeasurements},
		Windows:    windows(t, ""), // the default: the last fourteen days
	})
	if got := outside.Totals[export.DataMeasurements]; got != 0 {
		t.Fatalf("the default window counted %d measurements from January, so the "+
			"estimate would promise data a run would not move", got)
	}
}
