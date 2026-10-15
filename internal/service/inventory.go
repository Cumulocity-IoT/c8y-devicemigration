package service

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects/child"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects/childadditions"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects/childassets"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/inventory/managedobjects/childdevices"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/pagination"
	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/jsonmodels"
	"github.com/tidwall/gjson"

	"github.com/Cumulocity-IoT/c8y-devicemigration/internal/export"
)

// Choosing what to migrate means looking at a tenant the caller has no session
// in. A browser session belongs to one tenant; the hierarchy being migrated is
// in another, and only this service holds credentials for both. So these three
// reads exist for a user interface to build a picker on.
//
// They are reads and nothing else: no create, no update, no delete. What they do
// grant is real, though - an owner-tenant administrator holding the migration
// role can read any subscribed tenant's inventory through its service user.
// That is strictly less than the role already permits, which is migrating that
// tenant's hierarchy wholesale, but it is a capability and it is documented as
// one.

// DefaultPageSize is how many objects a listing returns when the caller does not
// say. Small enough to render, large enough that a picker rarely pages.
const DefaultPageSize = 20

// MaxPageSize caps what a caller can ask for, because this is a picker and not a
// bulk export.
const MaxPageSize = 200

// InventoryObject is one managed object, as much of it as choosing a root needs.
type InventoryObject struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type,omitempty"`
	Owner        string `json:"owner,omitempty"`
	CreationTime string `json:"creationTime,omitempty"`

	// IsDevice and IsGroup say which icon to draw, from the fragments the
	// platform marks objects with.
	IsDevice bool `json:"isDevice"`
	IsGroup  bool `json:"isGroup"`

	// Children counts what is underneath, per relationship kind. Present only
	// when the platform was asked for it, which the single-object read does.
	Children map[string]int `json:"children,omitempty"`

	// ChildType is the relationship this object was reached by, set only when it
	// was reached as somebody's child.
	ChildType string `json:"childType,omitempty"`
}

// InventoryResponse is a page of objects.
type InventoryResponse struct {
	Tenant  string            `json:"tenant"`
	Objects []InventoryObject `json:"objects"`

	// More says another page exists, so a picker can offer to fetch it without
	// having to guess from a page being full.
	More bool `json:"more"`
}

func (s *Service) handleListInventory(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Authorize(r.Context(), r); err != nil {
		writeError(w, r, err)
		return
	}

	tenant, err := s.tenant(r.PathValue("tenant"))
	if err != nil {
		writeError(w, r, err)
		return
	}

	pageSize, currentPage, err := paging(r)
	if err != nil {
		writeError(w, r, err)
		return
	}

	// The condition goes inside the query. The platform ignores type and
	// fragmentType whenever a query is present, and asking any other way
	// silently returns the whole inventory.
	//
	// Devices, assets and groups only. A tenant's inventory also holds
	// dashboards, smart rules, binaries, retention rules and this service's own
	// job records, none of which is a thing anybody would migrate, and all of
	// which would otherwise be offered as a choice.
	conditions := []string{migratable}
	if text := r.URL.Query().Get("text"); text != "" {
		conditions = append(conditions, fmt.Sprintf("(name eq '*%s*')", escapeQuery(text)))
	}

	options := managedobjects.ListOptions{
		PaginationOptions: pagination.PaginationOptions{
			PageSize:          pageSize,
			CurrentPage:       currentPage,
			WithTotalElements: true,
			WithTotalPages:    true,
		},
	}
	options.Query = "$filter=" + join(conditions, " and ")

	// A root is an object nothing else holds as a child. There is no fragment
	// saying so - the platform decides it by looking at an object's parents,
	// which is also how its own asset picker decides it - so this asks for the
	// parents and drops anything that has one.
	//
	// The platform's own onlyRoots parameter is sent as well, in case it is
	// honoured alongside a query. If it is, the pages come back already filtered
	// and the check below finds nothing to do; if it is ignored, as type and
	// fragmentType are, the check is what makes the answer right. Betting a
	// picker on which of those it is would be how this endpoint quietly listed
	// every child in the tenant.
	onlyRoots := r.URL.Query().Get("onlyRoots") == "true"
	if onlyRoots {
		options.OnlyRoots = true
		options.WithParents = true
	}

	result := tenant.client.ManagedObjects.List(r.Context(), options)
	if result.Err != nil {
		writeError(w, r, fmt.Errorf("could not read the inventory of %s: %w",
			tenant.session.Tenant, result.Err))
		return
	}

	response := InventoryResponse{Tenant: tenant.session.Tenant, Objects: []InventoryObject{}}
	for item, err := range result.Items() {
		if err != nil {
			writeError(w, r, fmt.Errorf("could not read the inventory of %s: %w",
				tenant.session.Tenant, err))
			return
		}
		if onlyRoots && hasParent(item.Bytes()) {
			continue
		}
		response.Objects = append(response.Objects, objectFrom(item.Bytes()))
	}
	response.More = morePages(result.Response, currentPage, pageSize, len(response.Objects))

	writeJSON(w, r, http.StatusOK, response)
}

// handleGetInventoryObject reads one object, with what is underneath it.
//
// This is also how a caller finds out whether an id exists at all, rather than
// discovering a wrong root id when the migration runs, minutes later.
func (s *Service) handleGetInventoryObject(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Authorize(r.Context(), r); err != nil {
		writeError(w, r, err)
		return
	}

	tenant, err := s.tenant(r.PathValue("tenant"))
	if err != nil {
		writeError(w, r, err)
		return
	}

	id := r.PathValue("id")
	if err := export.ValidateRootID(id); err != nil {
		writeError(w, r, &BadRequestError{Reason: err.Error()})
		return
	}

	// WithChildrenCount, not the children themselves: a group with four hundred
	// devices should cost the same to look at as one with two.
	result := tenant.client.ManagedObjects.Get(r.Context(), id, managedobjects.GetOptions{
		WithChildrenCount: true,
	})
	if result.Err != nil {
		if result.IsNotFound() {
			writeError(w, r, &NotFoundError{
				What: fmt.Sprintf("object %s in tenant %s", id, tenant.session.Tenant)})
			return
		}
		writeError(w, r, fmt.Errorf("could not read %s in %s: %w",
			id, tenant.session.Tenant, result.Err))
		return
	}

	writeJSON(w, r, http.StatusOK, objectFrom(result.Data.Bytes()))
}

// handleListChildren is the drill-down: what is directly under one object.
func (s *Service) handleListChildren(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Authorize(r.Context(), r); err != nil {
		writeError(w, r, err)
		return
	}

	tenant, err := s.tenant(r.PathValue("tenant"))
	if err != nil {
		writeError(w, r, err)
		return
	}

	id := r.PathValue("id")
	if err := export.ValidateRootID(id); err != nil {
		writeError(w, r, &BadRequestError{Reason: err.Error()})
		return
	}

	// The same three kinds a migration can follow, named the same way, so a
	// picker and a migration request speak one vocabulary - including the
	// default, so drilling down without saying follows what a migration would.
	wanted := r.URL.Query().Get("childTypes")
	if wanted == "" {
		wanted = defaultChildTypes()
	}
	childTypes, err := export.ResolveChildTypes(wanted, false)
	if err != nil {
		writeError(w, r, &BadRequestError{Reason: err.Error()})
		return
	}

	response := InventoryResponse{Tenant: tenant.session.Tenant, Objects: []InventoryObject{}}
	for _, childType := range childTypes {
		objects, err := listChildren(r.Context(), tenant, id, childType)
		if err != nil {
			writeError(w, r, err)
			return
		}
		response.Objects = append(response.Objects, objects...)
	}

	writeJSON(w, r, http.StatusOK, response)
}

// listChildren reads one relationship kind, in the idiom internal/export uses.
func listChildren(ctx context.Context, tenant *tenantClient, id string,
	childType export.ChildType) ([]InventoryObject, error) {

	options := child.ListOptions{
		PaginationOptions: pagination.PaginationOptions{PageSize: MaxPageSize},
	}

	var iterator *pagination.Iterator[jsonmodels.ManagedObject]
	switch childType {
	case export.ChildAsset:
		iterator = tenant.client.ManagedObjects.ChildAssets.ListAll(ctx, id,
			childassets.ListOptions(options))
	case export.ChildDevice:
		iterator = tenant.client.ManagedObjects.ChildDevices.ListAll(ctx, id,
			childdevices.ListOptions(options))
	case export.ChildAddition:
		iterator = tenant.client.ManagedObjects.ChildAdditions.ListAll(ctx, id,
			childadditions.ListOptions(options))
	default:
		return nil, &BadRequestError{Reason: fmt.Sprintf("unsupported child type %q", childType)}
	}

	var objects []InventoryObject
	for item, err := range iterator.Items() {
		if err != nil {
			return nil, fmt.Errorf("could not read the %s children of %s in %s: %w",
				childType, id, tenant.session.Tenant, err)
		}
		object := objectFrom(item.Bytes())
		object.ChildType = string(childType)
		objects = append(objects, object)
	}
	if err := iterator.Err(); err != nil {
		return nil, fmt.Errorf("could not read the %s children of %s in %s: %w",
			childType, id, tenant.session.Tenant, err)
	}
	return objects, nil
}

// objectFrom reads the fields a picker needs out of a managed object.
//
// Straight from the bytes rather than through a struct, because a managed object
// carries whatever fragments a customer put on it and none of that has to be
// understood here.
func objectFrom(body []byte) InventoryObject {
	object := InventoryObject{
		ID:           gjson.GetBytes(body, "id").String(),
		Name:         gjson.GetBytes(body, "name").String(),
		Type:         gjson.GetBytes(body, "type").String(),
		Owner:        gjson.GetBytes(body, "owner").String(),
		CreationTime: gjson.GetBytes(body, "creationTime").String(),
		IsDevice:     gjson.GetBytes(body, "c8y_IsDevice").Exists(),
		IsGroup:      gjson.GetBytes(body, "c8y_IsDeviceGroup").Exists(),
	}

	// childAdditions and friends carry a count when the platform was asked for
	// one. Absent is different from zero, so an absent count is left out.
	children := map[string]int{}
	for path, name := range map[string]string{
		"childAssets.references.#":    "assets",
		"childDevices.references.#":   "devices",
		"childAdditions.references.#": "additions",
	} {
		if count := gjson.GetBytes(body, path); count.Exists() {
			children[name] = int(count.Int())
		}
	}
	if len(children) > 0 {
		object.Children = children
	}
	return object
}

// paging reads the two paging parameters, refusing values that are not numbers
// rather than quietly using a default - a picker asking for page "next" has a
// bug worth surfacing.
func paging(r *http.Request) (pageSize, currentPage int, err error) {
	pageSize, currentPage = DefaultPageSize, 1

	if raw := r.URL.Query().Get("pageSize"); raw != "" {
		pageSize, err = strconv.Atoi(raw)
		if err != nil || pageSize < 1 {
			return 0, 0, &BadRequestError{Reason: fmt.Sprintf("pageSize %q is not a positive number", raw)}
		}
		if pageSize > MaxPageSize {
			return 0, 0, &BadRequestError{Reason: fmt.Sprintf(
				"pageSize %d is more than the %d this endpoint returns", pageSize, MaxPageSize)}
		}
	}
	if raw := r.URL.Query().Get("currentPage"); raw != "" {
		currentPage, err = strconv.Atoi(raw)
		if err != nil || currentPage < 1 {
			return 0, 0, &BadRequestError{Reason: fmt.Sprintf("currentPage %q is not a positive number", raw)}
		}
	}
	return pageSize, currentPage, nil
}

// hasParent says whether something else holds this object as a child, in any of
// the three relationships. Only meaningful when the platform was asked for
// parents; without that the collections are absent and everything looks like a
// root, which is why the caller sets WithParents rather than hoping.
func hasParent(body []byte) bool {
	for _, path := range []string{
		"assetParents.references.#",
		"deviceParents.references.#",
		"additionParents.references.#",
	} {
		if gjson.GetBytes(body, path).Int() > 0 {
			return true
		}
	}
	return false
}

// morePages says whether another page exists.
//
// From the platform's own statistics where they are there, because a page whose
// children were filtered out is shorter than the page size and would otherwise
// look like the end of the inventory. Counting is the fallback, for a response
// that carries no statistics at all.
func morePages(envelope []byte, currentPage, pageSize, returned int) bool {
	if total := gjson.GetBytes(envelope, "statistics.totalPages"); total.Exists() {
		return int64(currentPage) < total.Int()
	}
	return returned == pageSize
}

// migratable is what counts as something worth migrating: the fragments the
// platform marks devices, groups and assets with.
//
// The same three the platform's own inventory search uses for its default
// query, which is the closest thing to a definition of "something a person
// would pick".
const migratable = "((has(c8y_IsDevice)) or (has(c8y_IsDeviceGroup)) or (has(c8y_IsAsset)))"

// escapeQuery keeps a search term from ending the string it sits in.
func escapeQuery(text string) string {
	escaped := make([]rune, 0, len(text))
	for _, r := range text {
		if r == '\'' {
			continue
		}
		escaped = append(escaped, r)
	}
	return string(escaped)
}

func join(parts []string, sep string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += sep
		}
		out += part
	}
	return out
}
