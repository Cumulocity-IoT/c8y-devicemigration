// Package testfixture serves tests/fixtures/hierarchy.json over HTTP.
//
// The bats suite drives the bash implementation through a fake c8y binary that
// reads the same file. Serving it here means both implementations are exercised
// against the same synthetic tenant - including its cycle, its two-parent node,
// its unnamed object and its awkward names - which is what makes a differential
// comparison meaningful rather than approximate.
package testfixture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/session"
	"time"
)

// Hierarchy is the fixture shape: a map of id to device, plus a _comment array
// that documents why the fixture looks the way it does.
type Hierarchy map[string]Device

// Device is one entry in the fixture. Children are keyed by relationship type.
type Device struct {
	Name       string              `json:"name"`
	Type       string              `json:"type"`
	Children   map[string][]string `json:"children"`
	Identities []Identity          `json:"identities"`

	// Data is not served yet; it arrives with the data increment.
	Measurements []json.RawMessage `json:"measurements"`
	Events       []json.RawMessage `json:"events"`
	Alarms       []json.RawMessage `json:"alarms"`
}

// Identity is an external identity on a device.
type Identity struct {
	Type       string `json:"type"`
	ExternalID string `json:"externalId"`
}

// Server is an httptest server backed by a fixture.
type Server struct {
	*httptest.Server

	hierarchy Hierarchy

	// objectBodies overrides what GET /inventory/managedObjects/<id> returns.
	// The differential test fills this from tests/fake/c8y so both
	// implementations receive byte-identical responses; without that the two
	// artefacts could only ever be compared modulo formatting.
	objectBodies map[string][]byte

	mu       sync.Mutex
	requests []string

	// queries keeps the query string too. A filter that never reaches the
	// server is invisible when only paths are recorded, which is exactly how a
	// revert once planned to delete a whole tenant.
	queries []string

	// The target tenant this server also stands in for. Ids come from one
	// counter shared with identities, as a real platform's do, so a test cannot
	// accidentally depend on them being contiguous per kind.
	counter int

	// failCreateAfter refuses managed object creates once this many have
	// succeeded, and is only consulted when failCreateSet is true - so zero
	// means refuse every one, not "disabled". It models an import dying part way
	// through, which is the only way to observe that the mapping is written as
	// it goes rather than once at the end.
	failCreateSet   bool
	failCreateAfter int

	created    map[string][]byte
	identities []CreatedIdentity
	links      []Link
	deleted    []string

	// cascade models group propagation: deleting a key also removes its values.
	cascade map[string][]string

	// refuseDelete models an object that will not go, which is what a
	// permissions problem looks like from the outside.
	refuseDelete map[string]bool

	// totalOverride inflates what a count query reports, so a test can drive
	// the export's limit checks without generating a million records. The bash
	// harness does the same through FAKE_C8Y_TOTAL_*.
	totalOverride    map[string]int
	records          []CreatedRecord
	uploadedBinaries []string
	failDataSet      bool
	failDataAfter    int
}

// CreatedIdentity is one external identity the server was asked to create.
type CreatedIdentity struct {
	DeviceID   string
	Type       string
	ExternalID string
}

// Link is one parent/child relationship the server was asked to assign.
type Link struct {
	ParentID  string
	ChildType string
	ChildID   string
}

// Created returns the bodies of the managed objects this server created, keyed
// by the id it assigned.
func (s *Server) Created() map[string][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]byte, len(s.created))
	for id, body := range s.created {
		out[id] = body
	}
	return out
}

// CreatedIdentities returns the external identities the server was asked for, in
// the order they arrived.
func (s *Server) CreatedIdentities() []CreatedIdentity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]CreatedIdentity(nil), s.identities...)
}

// Links returns the relationships the server was asked to assign, in order.
func (s *Server) Links() []Link {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Link(nil), s.links...)
}

// FailCreateAfter makes the server refuse managed object creates once n have
// succeeded, so a test can interrupt an import at a known point. Zero refuses
// every create.
func (s *Server) FailCreateAfter(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failCreateSet = true
	s.failCreateAfter = n
}

// HideFromHierarchy removes an object from the source side, which is what an
// object deleted since the export looks like.
func (s *Server) HideFromHierarchy(ids ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		delete(s.hierarchy, id)
		delete(s.objectBodies, id)
	}
}

// SetObjectBody makes the server return exactly these bytes for one managed
// object, instead of synthesising a response.
func (s *Server) SetObjectBody(id string, body []byte) {
	if s.objectBodies == nil {
		s.objectBodies = map[string][]byte{}
	}
	s.objectBodies[id] = body
}

// IDs returns every id in the fixture, so a caller can pre-generate responses.
func (s *Server) IDs() []string {
	ids := make([]string, 0, len(s.hierarchy))
	for id := range s.hierarchy {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// RepoRoot finds the repository root from this file's location, so tests do not
// depend on the working directory a test binary happens to run in.
func RepoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// FixturePath returns the path of a file under tests/fixtures.
func FixturePath(name string) string {
	return filepath.Join(RepoRoot(), "tests", "fixtures", name)
}

// Load reads a fixture.
//
// Decoded in two stages because the fixture documents itself with a _comment
// key holding an array of prose, which is not a Device and must be dropped
// before anything tries to decode it as one.
func Load(path string) (Hierarchy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	delete(entries, "_comment")

	hierarchy := make(Hierarchy, len(entries))
	for id, entry := range entries {
		var device Device
		if err := json.Unmarshal(entry, &device); err != nil {
			return nil, fmt.Errorf("device %s in %s: %w", id, path, err)
		}
		hierarchy[id] = device
	}
	return hierarchy, nil
}

// New starts a server for the named fixture and stops it when the test ends.
func New(t *testing.T, fixture string) *Server {
	t.Helper()

	hierarchy, err := Load(FixturePath(fixture))
	if err != nil {
		t.Fatalf("cannot load fixture %s: %v", fixture, err)
	}

	s := &Server{hierarchy: hierarchy}
	// TLS, not plain HTTP. The library warns on every request when credentials
	// travel over HTTP - "Using sensitive credentials in HTTP mode is not
	// secure" - which buried the actual test output in thousands of lines. It is
	// also what production looks like, so the client is exercised the same way.
	s.Server = httptest.NewTLSServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Server.Close)
	return s
}

// Requests returns the paths served, so a test can assert that a device with two
// parents was fetched once rather than twice.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// CountExact returns how many requests were for exactly this path, which is
// what distinguishes fetching an object from listing its children.
func (s *Server) CountExact(path string) int {
	n := 0
	for _, r := range s.Requests() {
		if r == path {
			n++
		}
	}
	return n
}

// Queries returns every request with its query string.
func (s *Server) Queries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queries...)
}

// CountRequests returns how many requests matched a path prefix.
func (s *Server) CountRequests(prefix string) int {
	n := 0
	for _, r := range s.Requests() {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.URL.Path)
	if r.URL.RawQuery != "" {
		s.queries = append(s.queries, r.URL.Path+"?"+r.URL.RawQuery)
	} else {
		s.queries = append(s.queries, r.URL.Path)
	}
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.URL.Path == "/tenant/currentTenant":
		s.writeJSON(w, map[string]any{"name": "t-fixture"})

	case r.Method == http.MethodPost && r.URL.Path == "/inventory/managedObjects":
		s.createManagedObject(w, r)

	// Device data. The fixture holds the records verbatim, so what the export
	// writes is what the fixture says - which is what makes it comparable with
	// the bash implementation reading the same file.
	case r.Method == http.MethodGet && r.URL.Path == "/measurement/measurements":
		s.listData(w, r, "measurements", "measurements")
	case r.Method == http.MethodGet && r.URL.Path == "/event/events":
		s.listData(w, r, "events", "events")
	case r.Method == http.MethodGet && r.URL.Path == "/alarm/alarms":
		s.listData(w, r, "alarms", "alarms")

	case r.Method == http.MethodPost && r.URL.Path == "/measurement/measurements":
		s.createMeasurements(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/event/events":
		s.createRecord(w, r, "events")
	case r.Method == http.MethodPost && r.URL.Path == "/alarm/alarms":
		s.createRecord(w, r, "alarms")

	case strings.HasPrefix(r.URL.Path, "/event/events/") && strings.HasSuffix(r.URL.Path, "/binaries"):
		s.eventBinary(w, r)

	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/identity/globalIds/"):
		id := strings.Split(strings.TrimPrefix(r.URL.Path, "/identity/globalIds/"), "/")[0]
		s.createIdentity(w, r, id)

	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/inventory/managedObjects/"):
		s.assignChild(w, r)

	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/inventory/managedObjects/"):
		s.updateManagedObject(w, r)

	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/inventory/managedObjects/"):
		s.deleteManagedObject(w, strings.TrimPrefix(r.URL.Path, "/inventory/managedObjects/"))

	// The marker query.
	case r.Method == http.MethodGet && r.URL.Path == "/inventory/managedObjects":
		s.findManagedObjects(w, r.URL.Query())

	case strings.HasPrefix(r.URL.Path, "/identity/globalIds/"):
		// /identity/globalIds/<id>/externalIds
		id := strings.Split(strings.TrimPrefix(r.URL.Path, "/identity/globalIds/"), "/")[0]
		s.writeExternalIDs(w, id)

	case strings.HasPrefix(r.URL.Path, "/inventory/managedObjects/"):
		rest := strings.TrimPrefix(r.URL.Path, "/inventory/managedObjects/")
		parts := strings.SplitN(rest, "/", 2)
		id := parts[0]

		if len(parts) == 1 {
			s.writeManagedObject(w, id)
			return
		}
		switch parts[1] {
		case "childAssets":
			s.writeChildren(w, id, "asset", "childAssets")
		case "childDevices":
			s.writeChildren(w, id, "device", "childDevices")
		case "childAdditions":
			s.writeChildren(w, id, "addition", "childAdditions")
		default:
			http.NotFound(w, r)
		}

	default:
		http.NotFound(w, r)
	}
}

// Cascade models Cumulocity propagating a deletion to an object's children, so
// a test can exercise the case where an object is already gone by the time its
// own request is sent.
func (s *Server) Cascade(parentID string, childIDs ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cascade == nil {
		s.cascade = map[string][]string{}
	}
	s.cascade[parentID] = append(s.cascade[parentID], childIDs...)
}

// Remove takes objects out behind the caller's back, so a test can model a
// tenant that changed since the mapping was written.
func (s *Server) Remove(ids ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		delete(s.created, id)
	}
}

// RefuseDelete makes an object survive a delete request.
func (s *Server) RefuseDelete(ids ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refuseDelete == nil {
		s.refuseDelete = map[string]bool{}
	}
	for _, id := range ids {
		s.refuseDelete[id] = true
	}
}

// Deleted returns the ids the server was asked to delete, in order, including
// requests for objects that were already gone.
func (s *Server) Deleted() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.deleted...)
}

func (s *Server) deleteManagedObject(w http.ResponseWriter, id string) {
	s.mu.Lock()
	s.deleted = append(s.deleted, id)

	if s.refuseDelete[id] {
		s.mu.Unlock()
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"inventory/Forbidden","message":"not allowed"}`))
		return
	}

	if _, exists := s.created[id]; !exists {
		s.mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"inventory/Not Found","message":"managed object not found"}`))
		return
	}
	delete(s.created, id)
	// Propagation takes the children with it, as a real group deletion does.
	for _, childID := range s.cascade[id] {
		delete(s.created, childID)
	}
	s.mu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

// findManagedObjects answers an inventory search over the created objects.
//
// ASSUMPTION, and the reason this is fussier than it looks: Cumulocity IGNORES
// fragmentType, type, owner and the other simple filters when a `query`
// parameter is present. The two are mutually exclusive, and a caller that sets
// both silently gets the query alone.
//
// Modelling that is what makes this fake worth having. An earlier version
// honoured fragmentType regardless, so a revert that sent both - which is what
// the library's paging does, since it adds its own query for the id keyset -
// passed every test here and then planned to delete an entire tenant.
func (s *Server) findManagedObjects(w http.ResponseWriter, params url.Values) {
	query := params.Get("query")
	fragmentType := params.Get("fragmentType")

	var required []string
	var equals [][2]string
	var afterID string
	if query != "" {
		// query wins: whatever fragmentType said is discarded, exactly as the
		// platform discards it.
		fragmentType = ""
		required = hasTerms(query)
		equals = equalTerms(query)
		afterID = idCursor(query)
	}
	if fragmentType != "" {
		required = append(required, fragmentType)
	}

	s.mu.Lock()
	type found struct {
		id   string
		body []byte
	}
	var matches []found

	// The tenant's own devices, not only what this server was asked to create.
	// A real inventory holds both, and a listing that saw only creations made
	// searching a source tenant impossible to model.
	for id, device := range s.hierarchy {
		body, err := json.Marshal(s.sourceObject(id, device))
		if err != nil {
			continue
		}
		if !matchesAll(body, required) || !equalsAll(body, equals) {
			continue
		}
		if afterID != "" && id <= afterID {
			continue
		}
		matches = append(matches, found{id: id, body: body})
	}

	for id, body := range s.created {
		if !matchesAll(body, required) {
			continue
		}
		if !equalsAll(body, equals) {
			continue
		}
		if afterID != "" && id <= afterID {
			continue
		}
		matches = append(matches, found{id: id, body: body})
	}
	s.mu.Unlock()

	// Sorted, so a test can compare without caring about map iteration order.
	sort.Slice(matches, func(i, j int) bool { return matches[i].id < matches[j].id })

	objects := []any{}
	for _, match := range matches {
		var object map[string]any
		_ = json.Unmarshal(match.body, &object)
		object["id"] = match.id
		object["self"] = s.selfLink(match.id)
		objects = append(objects, object)
	}

	s.writeJSON(w, map[string]any{
		"self":           s.Server.URL + "/inventory/managedObjects",
		"managedObjects": objects,
		"statistics": map[string]any{
			"pageSize": 2000, "currentPage": 1,
			"totalElements": len(objects), "totalPages": 1,
		},
	})
}

// equalTerms pulls the "<path> eq '<value>'" comparisons out of a query.
//
// Modelled because the microservice looks a job record up by the id inside its
// fragment rather than by reading every record: without this the fixture would
// answer such a query with everything, and a query that could never work on a
// real platform would pass its tests.
func equalTerms(query string) [][2]string {
	var terms [][2]string
	rest := query
	for {
		at := strings.Index(rest, " eq '")
		if at < 0 {
			return terms
		}

		// The path is the word before " eq ", which may be wrapped in the
		// parentheses the query language uses to group conditions.
		path := strings.TrimLeft(rest[:at], "( ")
		if space := strings.LastIndexAny(path, "( "); space >= 0 {
			path = path[space+1:]
		}

		rest = rest[at+len(" eq '"):]
		end := strings.Index(rest, "'")
		if end < 0 {
			return terms
		}
		if path != "" {
			terms = append(terms, [2]string{path, rest[:end]})
		}
		rest = rest[end+1:]
	}
}

// equalsAll is whether an object satisfies every comparison.
func equalsAll(body []byte, terms [][2]string) bool {
	for _, term := range terms {
		// The dotted path addresses a property inside a fragment, which is what
		// gjson reads natively.
		if gjson.GetBytes(body, term[0]).String() != term[1] {
			return false
		}
	}
	return true
}

// hasTerms pulls the fragment names out of every has(...) in a query.
func hasTerms(query string) []string {
	var terms []string
	rest := query
	for {
		start := strings.Index(rest, "has(")
		if start < 0 {
			return terms
		}
		rest = rest[start+len("has("):]
		end := strings.Index(rest, ")")
		if end < 0 {
			return terms
		}
		if name := strings.TrimSpace(rest[:end]); name != "" {
			terms = append(terms, name)
		}
		rest = rest[end+1:]
	}
}

// idCursor pulls the keyset bound out of "_id gt '<id>'", which is how the
// library pages the inventory.
func idCursor(query string) string {
	const marker = "_id gt '"
	start := strings.Index(query, marker)
	if start < 0 {
		return ""
	}
	rest := query[start+len(marker):]
	end := strings.Index(rest, "'")
	if end < 0 {
		return ""
	}
	if value := rest[:end]; value != "0" {
		return value
	}
	return ""
}

// matchesAll reports whether every named top-level fragment is present.
func matchesAll(body []byte, fragments []string) bool {
	if len(fragments) == 0 {
		return true
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		return false
	}
	for _, fragment := range fragments {
		if _, present := object[fragment]; !present {
			return false
		}
	}
	return true
}

// selfLink is the URL this server would report for one of its own objects.
//
// It points at the server itself, as a real tenant's does. That is what makes an
// export taken from here identifiable later as having come from here, which is
// what the same-tenant check relies on.
func (s *Server) selfLink(id string) string {
	return s.Server.URL + "/inventory/managedObjects/" + id
}

// Records the server was asked to create, in arrival order.
type CreatedRecord struct {
	Kind           string
	DeviceID       string
	Body           []byte
	BatchSize      int
	ProcessingMode string
}

// CreatedRecords returns the data records the server accepted.
func (s *Server) CreatedRecords() []CreatedRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]CreatedRecord(nil), s.records...)
}

// UploadedBinaries returns the event ids an attachment was uploaded for.
func (s *Server) UploadedBinaries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.uploadedBinaries...)
}

// SetReportedTotal makes a count query claim this many records of one kind,
// whatever the fixture actually holds.
func (s *Server) SetReportedTotal(kind string, total int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.totalOverride == nil {
		s.totalOverride = map[string]int{}
	}
	s.totalOverride[kind] = total
}

// FailDataAfter refuses data creates once this many have been accepted, so a
// test can interrupt a data import at a known point.
func (s *Server) FailDataAfter(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failDataSet = true
	s.failDataAfter = n
}

// listData answers a data collection query out of the fixture.
//
// dateFrom and dateTo are applied, because the export's whole windowing story
// depends on them and a server that ignored them would make those tests
// vacuous.
func (s *Server) listData(w http.ResponseWriter, r *http.Request, field, property string) {
	params := r.URL.Query()
	deviceID := params.Get("source")

	s.mu.Lock()
	device := s.hierarchy[deviceID]
	s.mu.Unlock()

	var all []json.RawMessage
	switch field {
	case "measurements":
		all = device.Measurements
	case "events":
		all = device.Events
	case "alarms":
		all = device.Alarms
	}

	from, hasFrom := parseTimeParam(params.Get("dateFrom"))
	to, hasTo := parseTimeParam(params.Get("dateTo"))

	var kept []json.RawMessage
	for _, record := range all {
		if hasFrom || hasTo {
			stamp, ok := recordTime(record)
			if ok {
				if hasFrom && stamp.Before(from) {
					continue
				}
				if hasTo && stamp.After(to) {
					continue
				}
			}
		}
		kept = append(kept, record)
	}

	// A count query asks for one element and the statistics.
	pageSize := 5
	if raw := params.Get("pageSize"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			pageSize = parsed
		}
	}
	page := kept
	if len(page) > pageSize {
		page = page[:pageSize]
	}

	// Passed through as raw JSON, never decoded into map[string]any. Decoding
	// would render 22.0 as 22 and drop explicit nulls, so this server would be
	// incapable of delivering the fixture's own numbers - and the tests that
	// check the export and import preserve them would be checking nothing.
	items := []json.RawMessage{}
	items = append(items, page...)

	total := len(kept)
	s.mu.Lock()
	if override, ok := s.totalOverride[field]; ok {
		total = override
	}
	s.mu.Unlock()

	totalPages := 1
	if pageSize > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	s.writeJSON(w, map[string]any{
		"self":   s.Server.URL + r.URL.Path,
		property: items,
		"statistics": map[string]any{
			"pageSize": pageSize, "currentPage": 1,
			"totalElements": total, "totalPages": totalPages,
		},
	})
}

func parseTimeParam(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z0700"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func recordTime(record json.RawMessage) (time.Time, bool) {
	var probe struct {
		Time string `json:"time"`
	}
	if err := json.Unmarshal(record, &probe); err != nil {
		return time.Time{}, false
	}
	return parseTimeParam(probe.Time)
}

// createMeasurements accepts the measurement collection, which is how a bulk
// create arrives: one request holding many records.
func (s *Server) createMeasurements(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var collection struct {
		Measurements []json.RawMessage `json:"measurements"`
	}
	if err := json.Unmarshal(body, &collection); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}

	if s.refuseData(w, len(collection.Measurements)) {
		return
	}

	mode := r.Header.Get("X-Cumulocity-Processing-Mode")
	s.mu.Lock()
	for _, record := range collection.Measurements {
		s.records = append(s.records, CreatedRecord{
			Kind:           "measurements",
			DeviceID:       sourceIDOf(record),
			Body:           record,
			BatchSize:      len(collection.Measurements),
			ProcessingMode: mode,
		})
	}
	s.mu.Unlock()

	w.WriteHeader(http.StatusCreated)
	s.writeJSON(w, map[string]any{"measurements": []any{}})
}

func (s *Server) createRecord(w http.ResponseWriter, r *http.Request, kind string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.refuseData(w, 1) {
		return
	}

	id := s.nextID()
	mode := r.Header.Get("X-Cumulocity-Processing-Mode")

	s.mu.Lock()
	s.records = append(s.records, CreatedRecord{
		Kind: kind, DeviceID: sourceIDOf(body), Body: body, ProcessingMode: mode,
	})
	s.mu.Unlock()

	var object map[string]any
	_ = json.Unmarshal(body, &object)
	object["id"] = id

	w.WriteHeader(http.StatusCreated)
	s.writeJSON(w, object)
}

// refuseData reports whether this create should be refused, and answers it.
func (s *Server) refuseData(w http.ResponseWriter, n int) bool {
	s.mu.Lock()
	accepted := 0
	for _, record := range s.records {
		_ = record
		accepted++
	}
	refuse := s.failDataSet && accepted >= s.failDataAfter
	s.mu.Unlock()

	if !refuse {
		return false
	}
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte(`{"error":"test/Refused","message":"refused by the test fixture"}`))
	return true
}

func (s *Server) eventBinary(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/event/events/"), "/binaries")

	switch r.Method {
	case http.MethodGet:
		// The attachment's bytes are synthesised from the event id, so the
		// import can be checked to have uploaded the right file for the right
		// event.
		_, _ = w.Write([]byte("attachment of event " + trimmed + "\n"))
	case http.MethodPost:
		s.mu.Lock()
		s.uploadedBinaries = append(s.uploadedBinaries, trimmed)
		s.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		s.writeJSON(w, map[string]any{"self": s.Server.URL + r.URL.Path})
	default:
		http.NotFound(w, r)
	}
}

// sourceIDOf reads the device a record is attached to.
func sourceIDOf(body []byte) string {
	var probe struct {
		Source struct {
			ID any `json:"id"`
		} `json:"source"`
	}
	_ = json.Unmarshal(body, &probe)
	return stringify(probe.Source.ID)
}

// nextID allocates from one counter shared by every kind of created thing, the
// way a real tenant does.
func (s *Server) nextID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counter == 0 {
		s.counter = 1000
	}
	s.counter++
	return strconv.Itoa(s.counter)
}

// updateManagedObject merges a PUT body into an object this server created.
//
// The migration engine never updates a managed object - it only ever creates,
// links and deletes - so this route did not exist until the microservice needed
// it for its job records, which it keeps in the tenant because a container has
// nowhere else to keep anything.
func (s *Server) updateManagedObject(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/inventory/managedObjects/")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var patch map[string]any
	if err := json.Unmarshal(body, &patch); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}

	s.mu.Lock()
	existing, ok := s.created[id]
	if !ok {
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}

	var object map[string]any
	if err := json.Unmarshal(existing, &object); err != nil {
		s.mu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// A fragment at a time, as the platform does: a PUT naming one fragment
	// leaves the others alone.
	for key, value := range patch {
		object[key] = value
	}

	merged, err := json.Marshal(object)
	if err != nil {
		s.mu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.created[id] = merged
	s.mu.Unlock()

	object["id"] = id
	object["self"] = s.selfLink(id)
	s.writeJSON(w, object)
}

func (s *Server) createManagedObject(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	if s.failCreateSet && len(s.created) >= s.failCreateAfter {
		s.mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		s.writeJSON(w, map[string]any{
			"error":   "inventory/Internal Server Error",
			"message": "refused by the test fixture",
		})
		return
	}
	s.mu.Unlock()

	id := s.nextID()

	s.mu.Lock()
	if s.created == nil {
		s.created = map[string][]byte{}
	}
	s.created[id] = body
	s.mu.Unlock()

	// Echo the request back with the id the platform assigned, which is what a
	// real create returns.
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	object["id"] = id
	object["self"] = s.selfLink(id)

	w.WriteHeader(http.StatusCreated)
	s.writeJSON(w, object)
}

func (s *Server) createIdentity(w http.ResponseWriter, r *http.Request, deviceID string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var request struct {
		Type       string `json:"type"`
		ExternalID string `json:"externalId"`
	}
	_ = json.Unmarshal(body, &request)

	// An external id the tenant already knows is a 409, which is the failure
	// import has to survive rather than abort on.
	s.mu.Lock()
	for _, existing := range s.identities {
		if existing.Type == request.Type && existing.ExternalID == request.ExternalID {
			s.mu.Unlock()
			w.WriteHeader(http.StatusConflict)
			s.writeJSON(w, map[string]any{
				"error":   "identity/Duplicate External ID",
				"message": "External ID already exists",
			})
			return
		}
	}
	s.identities = append(s.identities, CreatedIdentity{
		DeviceID: deviceID, Type: request.Type, ExternalID: request.ExternalID,
	})
	s.mu.Unlock()

	w.WriteHeader(http.StatusCreated)
	s.writeJSON(w, map[string]any{
		"externalId":    request.ExternalID,
		"type":          request.Type,
		"managedObject": map[string]any{"id": deviceID},
	})
}

func (s *Server) assignChild(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/inventory/managedObjects/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}

	var childType string
	switch parts[1] {
	case "childAssets":
		childType = "asset"
	case "childDevices":
		childType = "device"
	case "childAdditions":
		childType = "addition"
	default:
		http.NotFound(w, r)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// A managedObjectReference: {"managedObject":{"id":"..."}}
	childID := gjsonID(body)

	s.mu.Lock()
	s.links = append(s.links, Link{ParentID: parts[0], ChildType: childType, ChildID: childID})
	s.mu.Unlock()

	w.WriteHeader(http.StatusCreated)
	s.writeJSON(w, map[string]any{"managedObject": map[string]any{"id": childID}})
}

// gjsonID pulls the child id out of a managedObjectReference without caring how
// the client chose to nest it.
func gjsonID(body []byte) string {
	var reference struct {
		ManagedObject struct {
			ID any `json:"id"`
		} `json:"managedObject"`
		ID any `json:"id"`
	}
	_ = json.Unmarshal(body, &reference)
	if id := stringify(reference.ManagedObject.ID); id != "" {
		return id
	}
	return stringify(reference.ID)
}

func stringify(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case float64:
		return strconv.FormatInt(int64(value), 10)
	default:
		return ""
	}
}

func (s *Server) writeJSON(w http.ResponseWriter, v any) {
	// HTML escaping off. json.Marshal turns < and > into \u003c and \u003e by
	// default, which is valid JSON and decodes identically - but it is not what
	// the platform sends, and the fixture's alarm text contains "<!DOCTYPE
	// html>" precisely so that a re-encoding somewhere is visible.
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Encode appends a newline, which no API does.
	_, _ = w.Write(bytes.TrimRight(buffer.Bytes(), "\n"))
}

// writeManagedObject emits a managed object shaped like a real one: the
// fixture's own fields plus the server managed properties that an import is
// expected to strip. The shape mirrors what tests/fake/c8y emits, so the two
// implementations see the same bytes.
func (s *Server) writeManagedObject(w http.ResponseWriter, id string) {
	// An object this run created lives in the target tenant, which is a
	// different id space from the source fixture.
	s.mu.Lock()
	createdBody, wasCreated := s.created[id]
	s.mu.Unlock()
	if wasCreated {
		var object map[string]any
		_ = json.Unmarshal(createdBody, &object)
		object["id"] = id
		object["self"] = s.selfLink(id)
		s.writeJSON(w, object)
		return
	}

	if body, ok := s.objectBodies[id]; ok {
		_, _ = w.Write(body)
		return
	}

	device, ok := s.hierarchy[id]
	if !ok {
		http.Error(w, fmt.Sprintf(`{"error":"inventory/Not Found","message":"managed object %s not found"}`, id),
			http.StatusNotFound)
		return
	}

	s.writeJSON(w, s.sourceObject(id, device))
}

// sourceObject is a managed object of the tenant's own, as the platform would
// render it. Shared by the single read and the listing, so a search cannot see a
// different object from the one that is then fetched.
func (s *Server) sourceObject(id string, device Device) map[string]any {
	object := map[string]any{
		"id":           id,
		"self":         s.selfLink(id),
		"lastUpdated":  "2026-01-01T00:00:00.000Z",
		"creationTime": "2025-01-01T00:00:00.000Z",
		"owner":        "service_test",
		"type":         device.Type,

		"childDevices":   map[string]any{"self": "x", "references": []any{}},
		"childAssets":    map[string]any{"self": "x", "references": []any{}},
		"childAdditions": map[string]any{"self": "x", "references": []any{}},
		"deviceParents":  map[string]any{"references": []any{}},
		"assetParents":   map[string]any{"references": []any{}},
		"additionParents": map[string]any{
			"references": []any{},
		},

		"c8y_Availability":       map[string]any{"status": "AVAILABLE"},
		"c8y_ActiveAlarmsStatus": map[string]any{"critical": 1},
		"c8y_IsDevice":           map[string]any{},
		"my_Custom-Fragment":     map[string]any{"nested": map[string]any{"keep": true}},
	}
	// The fixture's unnamed object must stay unnamed, or the <unnamed> path is
	// never exercised.
	if device.Name != "" {
		object["name"] = device.Name
	}
	return object
}

func (s *Server) writeChildren(w http.ResponseWriter, id, childType, property string) {
	// Two id spaces, as for a single managed object: an id this run created is
	// in the target tenant, so its children are the ones actually assigned
	// there. Reading the source fixture for a target id would return nothing,
	// which is worse than it sounds - it makes every existing relationship look
	// missing, so a resume assigns all of them a second time.
	s.mu.Lock()
	_, wasCreated := s.created[id]
	var assigned []string
	if wasCreated {
		for _, link := range s.links {
			if link.ParentID == id && link.ChildType == childType {
				assigned = append(assigned, link.ChildID)
			}
		}
	}
	device := s.hierarchy[id]
	s.mu.Unlock()

	childIDs := device.Children[childType]
	if wasCreated {
		childIDs = assigned
	}

	references := []any{}
	for _, childID := range childIDs {
		child := s.hierarchy[childID]
		managedObject := map[string]any{"id": childID, "type": child.Type}
		if child.Name != "" {
			managedObject["name"] = child.Name
		}
		references = append(references, map[string]any{"managedObject": managedObject})
	}

	// A managedObjectReferenceCollection, as the platform really returns it:
	// the children live under "references", each wrapped in a managedObject.
	// The library reads references.#.managedObject, so the property name is not
	// where the array goes.
	s.writeJSON(w, map[string]any{
		"self":       s.selfLink(id) + "/" + property,
		"references": references,
		"statistics": map[string]any{
			"pageSize":      2000,
			"currentPage":   1,
			"totalElements": len(references),
			"totalPages":    1,
		},
	})
}

func (s *Server) writeExternalIDs(w http.ResponseWriter, id string) {
	device := s.hierarchy[id]

	externalIDs := []any{}
	for _, identity := range device.Identities {
		externalIDs = append(externalIDs, map[string]any{
			"externalId":    identity.ExternalID,
			"type":          identity.Type,
			"self":          s.Server.URL + "/identity/externalIds/" + identity.Type + "/" + identity.ExternalID,
			"managedObject": map[string]any{"id": id},
		})
	}

	s.writeJSON(w, map[string]any{
		"self":        s.Server.URL + "/identity/globalIds/" + id + "/externalIds",
		"externalIds": externalIDs,
	})
}

// WriteSessionFile writes a go-c8y-cli session file pointing at this server
// and returns its path. Most callers want ReadSession instead - a session read
// from this file cannot reach the server, because the certificate is
// self-signed.
func (s *Server) WriteSessionFile(t *testing.T, dir, name string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	body := map[string]string{
		"host":     s.URL,
		"tenant":   "t-fixture",
		"username": "fixture",
		"password": "fixture",
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// ReadSession writes a session file for this server, reads it back through the
// real session loader, and marks the result as trusting this server's
// self-signed certificate. That last part is why it exists: the flag is a Go
// field with no session-file equivalent, so a fixture has to set it in code.
func (s *Server) ReadSession(t *testing.T, dir, name string) *session.Session {
	t.Helper()

	sess, err := session.Read(s.WriteSessionFile(t, dir, name))
	if err != nil {
		t.Fatal(err)
	}
	sess.InsecureSkipVerify = true
	return sess
}
