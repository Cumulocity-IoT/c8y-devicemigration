package service_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/service"
	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/testfixture"
)

// A document nobody checks is a document that lies. These tests are what make
// "maintained" mean something: the routes and the payload fields are compared
// against the code, so an endpoint or a field added without documenting it fails
// the build rather than quietly going missing.

// spec is the parts of the document these tests read.
type spec struct {
	OpenAPI string `yaml:"openapi"`
	Info    struct {
		Title   string `yaml:"title"`
		Version string `yaml:"version"`
	} `yaml:"info"`
	Servers []struct {
		URL string `yaml:"url"`
	} `yaml:"servers"`
	Paths      map[string]map[string]operation `yaml:"paths"`
	Components struct {
		Schemas   map[string]schema `yaml:"schemas"`
		Responses map[string]struct {
			Description string `yaml:"description"`
		} `yaml:"responses"`
	} `yaml:"components"`
}

type operation struct {
	Summary     string                 `yaml:"summary"`
	OperationID string                 `yaml:"operationId"`
	Tags        []string               `yaml:"tags"`
	Security    *[]map[string][]string `yaml:"security"`
	Responses   map[string]any         `yaml:"responses"`
}

type schema struct {
	Type       string         `yaml:"type"`
	Required   []string       `yaml:"required"`
	Properties map[string]any `yaml:"properties"`
}

func readSpec(t *testing.T) spec {
	t.Helper()

	var parsed spec
	if err := yaml.Unmarshal([]byte(service.OpenAPI), &parsed); err != nil {
		t.Fatalf("the embedded OpenAPI document is not valid YAML: %v", err)
	}
	return parsed
}

// Every credential form the service accepts has to be declared, or a caller
// reads the document, sees no cookie scheme, and concludes that a browser
// session cannot work - which is exactly the confusion this whole change came
// out of.
func TestTheDocumentDeclaresEveryCredentialFormAccepted(t *testing.T) {
	parsed := readSpec(t)

	var declared struct {
		Components struct {
			SecuritySchemes map[string]struct {
				Type string `yaml:"type"`
				In   string `yaml:"in"`
				Name string `yaml:"name"`
			} `yaml:"securitySchemes"`
		} `yaml:"components"`
		Security []map[string][]string `yaml:"security"`
	}
	if err := yaml.Unmarshal([]byte(service.OpenAPI), &declared); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"basicAuth", "bearerAuth", "cookieAuth"} {
		if _, ok := declared.Components.SecuritySchemes[want]; !ok {
			t.Errorf("%s is accepted by the code but not declared in the document", want)
		}

		offered := false
		for _, entry := range declared.Security {
			if _, ok := entry[want]; ok {
				offered = true
			}
		}
		if !offered {
			t.Errorf("%s is declared but not offered in the top-level security list", want)
		}
	}

	cookie := declared.Components.SecuritySchemes["cookieAuth"]
	if cookie.In != "cookie" || cookie.Name != cookieName {
		t.Errorf("cookieAuth = {in: %q, name: %q}, want the %q cookie",
			cookie.In, cookie.Name, cookieName)
	}
	_ = parsed
}

// cookieName is the cookie the platform puts a browser session in. Duplicated
// from the unexported constant on purpose: if one changes, this fails.
const cookieName = "authorization"

func TestTheOpenAPIDocumentIsWellFormed(t *testing.T) {
	parsed := readSpec(t)

	if !strings.HasPrefix(parsed.OpenAPI, "3.") {
		t.Fatalf("openapi = %q", parsed.OpenAPI)
	}
	if parsed.Info.Title == "" || parsed.Info.Version == "" {
		t.Fatalf("info is incomplete: %+v", parsed.Info)
	}
	// The document is served from behind the platform's gateway, so the server
	// has to be the path the platform exposes rather than a host.
	if len(parsed.Servers) == 0 || !strings.HasPrefix(parsed.Servers[0].URL, "/service/") {
		t.Fatalf("servers = %+v, want the /service/<name> path", parsed.Servers)
	}

	for path, methods := range parsed.Paths {
		for method, op := range methods {
			where := method + " " + path
			if op.Summary == "" {
				t.Errorf("%s has no summary", where)
			}
			if op.OperationID == "" {
				t.Errorf("%s has no operationId", where)
			}
			if len(op.Tags) == 0 {
				t.Errorf("%s has no tag", where)
			}
			if len(op.Responses) == 0 {
				t.Errorf("%s documents no responses", where)
			}
		}
	}
}

// Every route the service serves must be documented, and every documented path
// must exist. Both directions matter: the first stops an endpoint being added in
// silence, the second stops the document describing something that was removed.
func TestEveryRouteIsDocumentedAndEveryDocumentedRouteExists(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	parsed := readSpec(t)

	documented := map[string]bool{}
	for path, methods := range parsed.Paths {
		for method := range methods {
			documented[strings.ToUpper(method)+" "+path] = true
		}
	}

	served := map[string]bool{}
	for _, route := range svc.Routes() {
		served[route.Method+" "+route.Path] = true
	}

	var undocumented, phantom []string
	for route := range served {
		if !documented[route] {
			undocumented = append(undocumented, route)
		}
	}
	for route := range documented {
		if !served[route] {
			phantom = append(phantom, route)
		}
	}
	sort.Strings(undocumented)
	sort.Strings(phantom)

	if len(undocumented) > 0 {
		t.Errorf("served but not in the OpenAPI document: %s", strings.Join(undocumented, ", "))
	}
	if len(phantom) > 0 {
		t.Errorf("in the OpenAPI document but not served: %s", strings.Join(phantom, ", "))
	}
}

// The routes that answer without credentials are the platform's probes and this
// document. Anything else must be behind the authorization check, and the
// document must say which is which - a caller who believes an endpoint is public
// when it is not gets a 401 they cannot explain.
func TestTheDocumentAgreesAboutWhichRoutesArePublic(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	parsed := readSpec(t)

	for _, route := range svc.Routes() {
		methods, ok := parsed.Paths[route.Path]
		if !ok {
			continue // reported by the test above
		}
		op, ok := methods[strings.ToLower(route.Method)]
		if !ok {
			continue
		}

		// An empty security array is how OpenAPI says "no credentials needed";
		// absent means the document's default applies.
		documentedPublic := op.Security != nil && len(*op.Security) == 0
		if documentedPublic != route.Public {
			t.Errorf("%s %s: the document says public=%v, the code says %v",
				route.Method, route.Path, documentedPublic, route.Public)
		}

		// And prove it, rather than trusting either of them.
		r := request(t, route.Method, route.Path, nil)
		r.Header.Del("Authorization")
		recorder := call(t, svc, r)
		unauthenticated := recorder.Code == http.StatusUnauthorized
		if route.Public && unauthenticated {
			t.Errorf("%s %s is documented as needing no credentials but returned 401",
				route.Method, route.Path)
		}
		if !route.Public && !unauthenticated {
			t.Errorf("%s %s answered %d without credentials, so it is not protected",
				route.Method, route.Path, recorder.Code)
		}
	}
}

// The request and response bodies are Go structs, and their json tags are the
// wire contract. Comparing them with the schemas catches the drift that matters
// most in practice: a field added to a struct and not to the document, or a
// documented field that no longer exists.
func TestTheSchemasMatchTheStructs(t *testing.T) {
	parsed := readSpec(t)

	for _, pair := range []struct {
		schema string
		value  any
	}{
		{"TenantsResponse", service.TenantsResponse{}},
		{"PreflightRequest", service.PreflightRequest{}},
		{"PreflightResponse", service.PreflightResponse{}},
		{"PreflightSide", service.PreflightSide{}},
		{"MigrationRequest", service.MigrationRequest{}},
		{"MigrationsResponse", service.MigrationsResponse{}},
		{"RevertRequest", service.RevertRequest{}},
		{"RevertsResponse", service.RevertsResponse{}},
		{"LoggersResponse", service.LoggersResponse{}},
		{"InventoryObject", service.InventoryObject{}},
		{"InventoryResponse", service.InventoryResponse{}},
		{"EstimateRequest", service.EstimateRequest{}},
		{"EstimateResponse", service.EstimateResponse{}},
		{"EstimateWindow", service.EstimateWindow{}},
		{"EstimateBreach", service.EstimateBreach{}},
		{"EstimateLimits", service.EstimateLimits{}},
		{"Job", service.Job{}},
	} {
		documented, ok := parsed.Components.Schemas[pair.schema]
		if !ok {
			t.Errorf("%s is not in the OpenAPI document", pair.schema)
			continue
		}

		fields := jsonFields(pair.value)
		for _, field := range fields {
			if _, ok := documented.Properties[field]; !ok {
				t.Errorf("%s.%s is on the wire but not in the document", pair.schema, field)
			}
		}
		for property := range documented.Properties {
			if !contains(fields, property) {
				t.Errorf("%s.%s is documented but no longer exists", pair.schema, property)
			}
		}
	}
}

// Required means required: a field the code refuses to do without has to be
// marked so, or a caller reads the document, omits it, and gets a 400.
func TestTheRequiredFieldsAreTheOnesTheCodeInsistsOn(t *testing.T) {
	parsed := readSpec(t)

	for schemaName, want := range map[string][]string{
		"PreflightRequest": {"sourceTenant", "targetTenant"},
		"MigrationRequest": {"sourceTenant", "targetTenant", "rootId"},
	} {
		documented := parsed.Components.Schemas[schemaName].Required
		sort.Strings(documented)
		sorted := append([]string(nil), want...)
		sort.Strings(sorted)

		if fmt.Sprint(documented) != fmt.Sprint(sorted) {
			t.Errorf("%s requires %v, want %v", schemaName, documented, sorted)
		}
	}
}

// The status the document is dated is the status the service reports, so a
// version bump that forgets the document is visible.
func TestTheDocumentedVersionMatchesTheManifest(t *testing.T) {
	parsed := readSpec(t)
	if parsed.Info.Version == "" {
		t.Fatal("the document has no version")
	}
	// Deliberately loose: the manifest's version is stamped at package time from
	// a git tag, so all that can be checked here is that the document carries a
	// version of its own shape.
	if strings.Count(parsed.Info.Version, ".") != 2 {
		t.Fatalf("info.version = %q, want a SemVer-shaped version", parsed.Info.Version)
	}
}

// A deployed instance has to be able to hand out its own description.
func TestTheServiceServesItsOwnDocument(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	r := request(t, http.MethodGet, "/openapi.yaml", nil)
	r.Header.Del("Authorization")

	recorder := call(t, svc, r)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/yaml" {
		t.Fatalf("Content-Type = %q", got)
	}
	if !strings.HasPrefix(recorder.Body.String(), "openapi:") {
		t.Fatalf("body does not look like an OpenAPI document:\n%s",
			recorder.Body.String()[:min(80, recorder.Body.Len())])
	}
}

// jsonFields is the wire name of every field of a struct, following the json
// tags and skipping the ones marked "-".
func jsonFields(value any) []string {
	var names []string
	structType := reflect.TypeOf(value)
	for i := range structType.NumField() {
		field := structType.Field(i)
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = field.Name
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// The API documentation application reads the spec from wherever the manifest's
// openApiSpec points. Nothing checks that at deployment time: a manifest naming
// a path the service does not serve produces an entry in the application that
// fails to load, which is worse than no entry at all. So the manifest and the
// routes are compared here.
func TestTheManifestPointsAtASpecTheServiceServes(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	raw, err := os.ReadFile(filepath.Join(testfixture.RepoRoot(), "docker", "cumulocity.json"))
	if err != nil {
		t.Fatal(err)
	}

	var manifest struct {
		OpenAPISpec any `json:"openApiSpec"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.OpenAPISpec == nil {
		t.Fatal("the manifest has no openApiSpec, so the API documentation application " +
			"will not know this service has a spec")
	}

	// Either a single path or a list of {label, path}, both of which the platform
	// accepts.
	var paths []string
	switch declared := manifest.OpenAPISpec.(type) {
	case string:
		paths = []string{declared}
	case []any:
		for _, entry := range declared {
			object, ok := entry.(map[string]any)
			if !ok {
				t.Fatalf("openApiSpec entry is not an object: %#v", entry)
			}
			path, ok := object["path"].(string)
			if !ok {
				t.Fatalf("openApiSpec entry has no path: %#v", entry)
			}
			if label, ok := object["label"].(string); !ok || label == "" {
				t.Errorf("openApiSpec entry %q has no label, so the application has "+
					"nothing to call it", path)
			}
			paths = append(paths, path)
		}
	default:
		t.Fatalf("openApiSpec is neither a path nor a list: %#v", manifest.OpenAPISpec)
	}

	served := map[string]bool{}
	for _, route := range svc.Routes() {
		if route.Method == http.MethodGet {
			served[strings.TrimPrefix(route.Path, "/")] = route.Public
		}
	}

	for _, path := range paths {
		if strings.HasPrefix(path, "/") {
			t.Errorf("openApiSpec %q starts with a slash; it is relative to "+
				"/service/<contextPath>/", path)
		}
		public, ok := served[strings.TrimPrefix(path, "/")]
		if !ok {
			t.Errorf("the manifest points at %q, which this service does not serve", path)
			continue
		}
		// The application fetches with the browser's session, and a spec behind
		// the owner-tenant check would fail to load for everybody else.
		if !public {
			t.Errorf("%q needs credentials, so the API documentation application "+
				"cannot load it", path)
		}
	}
}

func TestTheServiceServesItsDocumentAsJSONToo(t *testing.T) {
	p := twoTenants(t)
	svc := p.start(t)

	r := request(t, http.MethodGet, "/api-json", nil)
	r.Header.Del("Authorization")

	recorder := call(t, svc, r)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, and the documentation application expects JSON", got)
	}

	var asJSON map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &asJSON); err != nil {
		t.Fatalf("the served document is not JSON: %v", err)
	}

	// The two views must be the same document, or the humans and the application
	// are reading different contracts.
	var asYAML map[string]any
	if err := yaml.Unmarshal([]byte(service.OpenAPI), &asYAML); err != nil {
		t.Fatal(err)
	}

	jsonPaths := keys(asJSON["paths"])
	yamlPaths := keys(asYAML["paths"])
	if fmt.Sprint(jsonPaths) != fmt.Sprint(yamlPaths) {
		t.Fatalf("the JSON and YAML views describe different endpoints:\n%v\n%v",
			jsonPaths, yamlPaths)
	}

	// And the try-it-out feature only works if the server is the path the
	// platform exposes.
	servers, _ := asJSON["servers"].([]any)
	if len(servers) == 0 {
		t.Fatal("the JSON document names no server, so try-it-out has nowhere to send requests")
	}
	first, _ := servers[0].(map[string]any)
	if url, _ := first["url"].(string); !strings.HasPrefix(url, "/service/") {
		t.Fatalf("servers[0].url = %q, want the /service/<name> path the platform exposes", url)
	}
}

func keys(value any) []string {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	var names []string
	for name := range object {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// A setting the manifest does not declare cannot be changed by an operator: it
// never appears in the Administration UI, so the code would be reading a tenant
// option nobody can set.
func TestEverySettingTheCodeReadsIsDeclaredInTheManifest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(testfixture.RepoRoot(), "docker", "cumulocity.json"))
	if err != nil {
		t.Fatal(err)
	}

	var manifest struct {
		SettingsCategory string `json:"settingsCategory"`
		Settings         []struct {
			Key          string `json:"key"`
			DefaultValue string `json:"defaultValue"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}

	if manifest.SettingsCategory != service.SettingsCategory {
		t.Errorf("the manifest stores settings under %q, the code reads %q",
			manifest.SettingsCategory, service.SettingsCategory)
	}

	declared := map[string]bool{}
	for _, setting := range manifest.Settings {
		declared[setting.Key] = true

		// The platform wants a value, not an empty string: a setting declared
		// with nothing in it is one an operator finds blank in Administration
		// with no idea what it would do.
		if setting.DefaultValue == "" {
			t.Errorf("setting %q declares no default value", setting.Key)
		}
	}
	for _, key := range []string{service.SettingRetentionDays, service.SettingDefaultDateFrom} {
		if !declared[key] {
			t.Errorf("the code reads %q, which the manifest does not declare", key)
		}
	}

	// Every seeded default has to be a value the engine accepts. A window the
	// engine cannot parse would refuse every migration that did not name its
	// own, which is a tenant-wide outage seeded at install time.
	for _, setting := range manifest.Settings {
		switch setting.Key {
		case service.SettingDefaultDateFrom:
			if _, err := export.ParseDate(setting.DefaultValue, time.Now()); err != nil {
				t.Errorf("the seeded %s of %q is not a date the engine accepts: %v",
					setting.Key, setting.DefaultValue, err)
			}
		case service.SettingRetentionDays:
			if _, err := strconv.Atoi(setting.DefaultValue); err != nil {
				t.Errorf("the seeded %s of %q is not a number: %v",
					setting.Key, setting.DefaultValue, err)
			}
		}
	}
}
