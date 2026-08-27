package service

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
)

// The platform's own asset selector is the right way to choose a device, and it
// cannot be pointed at a tenant the browser has no session in: it reads through
// InventoryService, which talks to whatever tenant the caller is signed in to.
//
// So this proxies the platform's inventory reads for one named tenant. A browser
// client configured with /tenants/{tenant}/platform as its base URL behaves
// exactly as if it were signed in to that tenant, and the platform's components
// work unchanged - searching, paging, expanding a group, all of it, without this
// repository reimplementing any of it.
//
// Three rules keep that from becoming a hole:
//
//   - Reads only, and only the inventory. There are three routes and no
//     wildcard, so the router itself is the allowlist: anything else is a 404
//     before a handler runs.
//   - The same authorization as everything else. Owner-tenant users holding the
//     migration role, no one else.
//   - The platform's answer, verbatim. The statistics, the ordering and the
//     shape are what make the platform's own paging and search work; reshaping
//     any of it would break the component this exists for.
//
// What it grants is what the picker endpoints already granted, generalised: an
// owner-tenant administrator with the role can read any subscribed tenant's
// inventory through its service user. That is still strictly less than the role
// already permits, which is migrating that tenant's hierarchy wholesale.

// applicationName is where this service is reached under /service/, which it
// needs in order to rewrite the links in a proxied response into links back to
// itself. The platform sets APPLICATION_NAME; the fallback is the name in the
// manifest, which is what the platform would have set.
func applicationName() string {
	if name := os.Getenv("APPLICATION_NAME"); name != "" {
		return name
	}
	return "devicemigration"
}

// childRelationships are the collections a managed object holds - what is under
// it, and what it is under - named exactly as the platform names them. Anything
// else is a 400 rather than a forwarded request, so a path parameter cannot
// become a way to reach the rest of the API.
var childRelationships = map[string]bool{
	"childAssets":     true,
	"childDevices":    true,
	"childAdditions":  true,
	"assetParents":    true,
	"deviceParents":   true,
	"additionParents": true,
}

// authorized answers the request itself when the caller may not be here, and
// says whether the handler should carry on.
//
// Every one of these checks before it validates anything. A 400 from an
// unauthenticated caller would tell them what this service considers a valid
// path, which is a thing they have no business learning.
func (s *Service) authorized(w http.ResponseWriter, r *http.Request) bool {
	if _, err := s.Authorize(r.Context(), r); err != nil {
		writeError(w, r, err)
		return false
	}
	return true
}

func (s *Service) handleProxyList(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	s.proxy(w, r, "/inventory/managedObjects")
}

func (s *Service) handleProxyObject(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}

	id := r.PathValue("id")
	if err := export.ValidateRootID(id); err != nil {
		writeError(w, r, &BadRequestError{Reason: err.Error()})
		return
	}
	s.proxy(w, r, "/inventory/managedObjects/"+id)
}

func (s *Service) handleProxyChildren(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}

	id := r.PathValue("id")
	if err := export.ValidateRootID(id); err != nil {
		writeError(w, r, &BadRequestError{Reason: err.Error()})
		return
	}

	relationship := r.PathValue("relationship")
	if !childRelationships[relationship] {
		writeError(w, r, &BadRequestError{Reason: fmt.Sprintf(
			"%q is not a managed object collection this proxies", relationship)})
		return
	}
	s.proxy(w, r, "/inventory/managedObjects/"+id+"/"+relationship)
}

// proxy performs one read against a named tenant and returns what the platform
// said, status and body alike. The caller has already been authorized.
//
// The status is passed through rather than translated: a client that understands
// Cumulocity's errors - which is every client this is for - understands these
// already, and a 422 from a malformed inventory query is far more useful than
// this service's paraphrase of it.
func (s *Service) proxy(w http.ResponseWriter, r *http.Request, path string) {
	name := r.PathValue("tenant")
	tenant, err := s.tenant(name)
	if err != nil {
		writeError(w, r, err)
		return
	}

	// The query string travels as it arrived. Every filter, every page and every
	// ordering the platform understands is one this passes on, because deciding
	// here which ones are allowed would be deciding which of the platform's own
	// components are allowed to work.
	response, err := tenant.client.HTTPClient.R().
		SetContext(r.Context()).
		SetHeader("Accept", "application/json").
		SetQueryString(r.URL.RawQuery).
		Get(path)
	if err != nil {
		writeError(w, r, fmt.Errorf("could not read %s in %s: %w",
			path, tenant.session.Tenant, err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(response.StatusCode())
	if _, err := w.Write(s.rewriteLinks(response.Bytes(), name)); err != nil {
		slog.WarnContext(r.Context(), "could not write the proxied response", "error", err)
	}
}

// rewriteLinks points the URLs in a proxied body back at this service.
//
// The platform fills self, next and previous with its own address, and inside a
// container that address is the platform's internal one - a host no browser can
// reach and none should be told about. Every one of them becomes the proxy path
// that produced the response, so a client that follows a link stays in the
// tenant it was reading.
func (s *Service) rewriteLinks(body []byte, tenant string) []byte {
	host := strings.TrimSuffix(s.baseURL, "/")
	if host == "" {
		return body
	}
	prefix := fmt.Sprintf("/service/%s/tenants/%s/platform", applicationName(), tenant)
	return bytes.ReplaceAll(body, []byte(host), []byte(prefix))
}
