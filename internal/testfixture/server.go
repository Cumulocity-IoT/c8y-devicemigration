// Package testfixture serves tests/fixtures/hierarchy.json over HTTP.
//
// The bats suite drives the bash implementation through a fake c8y binary that
// reads the same file. Serving it here means both implementations are exercised
// against the same synthetic tenant - including its cycle, its two-parent node,
// its unnamed object and its awkward names - which is what makes a differential
// comparison meaningful rather than approximate.
package testfixture

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
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
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
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
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.URL.Path == "/tenant/currentTenant":
		s.writeJSON(w, map[string]any{"name": "t-fixture"})

	case r.Method == http.MethodPost && r.URL.Path == "/inventory/managedObjects":
		s.createManagedObject(w, r)

	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/identity/globalIds/"):
		id := strings.Split(strings.TrimPrefix(r.URL.Path, "/identity/globalIds/"), "/")[0]
		s.createIdentity(w, r, id)

	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/inventory/managedObjects/"):
		s.assignChild(w, r)

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

// selfLink is the URL this server would report for one of its own objects.
//
// It points at the server itself, as a real tenant's does. That is what makes an
// export taken from here identifiable later as having come from here, which is
// what the same-tenant check relies on.
func (s *Server) selfLink(id string) string {
	return s.Server.URL + "/inventory/managedObjects/" + id
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
	encoded, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(encoded)
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

	s.writeJSON(w, object)
}

func (s *Server) writeChildren(w http.ResponseWriter, id, childType, property string) {
	device := s.hierarchy[id]

	references := []any{}
	for _, childID := range device.Children[childType] {
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

// WriteSessionFile writes a session file pointing at this server, so the tool
// under test reaches it the same way it reaches a real tenant.
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
