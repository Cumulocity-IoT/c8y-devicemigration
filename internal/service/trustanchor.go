package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/reubenmiller/go-c8y/v2/pkg/c8y/api/trustedcertificates"
	"github.com/tidwall/gjson"
)

// Moving a certificate fleet, by moving what its tenant trusts.
//
// For a certificate fleet the credential is the chain, and it never changes: the
// device keeps its key and its certificate, and a migration only has to make the
// target tenant trust that chain. What this does is read a trusted CA out of the
// source tenant and put it in the target as it stood - its status and its
// auto-registration carried across rather than chosen here, because both decide
// which devices that CA may bring into a tenant.
//
// Everything awkward about it is platform behaviour, measured rather than
// assumed, and each piece is commented where it bites:
//
//   - A fingerprint can exist in only one tenant per instance, and **disabling
//     it in the source does not free it**. So this is delete-then-create, with a
//     window in which the fleet can reach neither tenant.
//   - No proof of possession is demanded, so the public certificate is all that
//     has to cross. Nobody needs the CA's private key.
//   - The platform keys a trusted certificate by its **SHA-1** fingerprint.
//   - Nothing records which devices a CA serves, so the blast radius cannot be
//     counted - only warned about.

// TrustedCertificateView is one of a tenant's trusted certificates, with this
// service's assessment of whether it can be moved.
type TrustedCertificateView struct {
	Fingerprint string `json:"fingerprint"`
	Name        string `json:"name,omitempty"`
	Status      string `json:"status,omitempty"`
	Subject     string `json:"subject,omitempty"`
	Issuer      string `json:"issuer,omitempty"`
	NotAfter    string `json:"notAfter,omitempty"`

	AutoRegistrationEnabled bool `json:"autoRegistrationEnabled"`

	// TenantCertificateAuthority marks the tenant's own CA, which cannot be
	// moved at all: a fleet enrolled against it needs re-enrollment, which is a
	// different mechanism.
	TenantCertificateAuthority bool `json:"tenantCertificateAuthority"`

	// Movable is whether a cutover would be attempted for this one.
	Movable bool `json:"movable"`

	// Reason says why not, when Movable is false.
	Reason string `json:"reason,omitempty"`
}

// TrustedCertificatesResponse is a tenant's truststore.
type TrustedCertificatesResponse struct {
	Tenant       string                   `json:"tenant"`
	Certificates []TrustedCertificateView `json:"certificates"`

	// Warning is the blast-radius warning, and it is on the listing rather than
	// only on the cutover because this is where somebody chooses.
	Warning string `json:"warning"`
}

// CoverageWarning is what this service can honestly say about a CA's reach.
//
// Not a number, and that is measured rather than lazy: a device object carries
// nothing naming the certificate, the issuer or the fingerprint that
// authenticates it, so there is no way to ask which devices a CA serves. An
// approximate count would be worse than an admission.
const CoverageWarning = "Every device whose certificate chains to this CA moves at once, " +
	"including devices no migration covered. The platform does not record which " +
	"devices a certificate authenticates, so this service cannot tell you how many " +
	"that is - check with whoever issued the certificates before cutting over."

// StatusDisabled is what the platform calls a trusted certificate that is not
// in use. A disabled certificate is never moved - see assess.
const StatusDisabled = "DISABLED"

// TrustAnchorCutoverRequest moves one trusted certificate between tenants.
type TrustAnchorCutoverRequest struct {
	SourceTenant string `json:"sourceTenant"`
	TargetTenant string `json:"targetTenant"`

	// Fingerprint is the SHA-1 fingerprint, which is how the platform keys a
	// trusted certificate. Its own field rather than a name, because names are
	// not unique and this operation has an outage in it.
	Fingerprint string `json:"fingerprint"`

	// AutoRegistrationEnabled decides whether the target registers a device
	// presenting this chain for the first time. Omitted means whatever the source
	// tenant had.
	//
	// A field, and a pointer, rather than forced to true: that would turn
	// auto-registration on for a tenant that trusted a certificate *without* it,
	// and nothing would say so. Which devices a CA may bring into a tenant is a
	// decision somebody takes, not a side effect of moving it - so the default
	// is the source's answer, and changing it has to be asked for.
	//
	// Turning it off is a real answer too: a fleet whose objects a migration has
	// already created can be bound to them by hand. The response says plainly
	// when the result is false, because arriving devices are then refused.
	AutoRegistrationEnabled *bool `json:"autoRegistrationEnabled,omitempty"`
}

// TrustAnchorCutoverResponse is what happened.
type TrustAnchorCutoverResponse struct {
	Fingerprint  string `json:"fingerprint"`
	Name         string `json:"name,omitempty"`
	SourceTenant string `json:"sourceTenant"`
	TargetTenant string `json:"targetTenant"`

	// AutoRegistrationEnabled is what the target says after the move, read back
	// rather than assumed - a platform that dropped the flag would otherwise
	// leave a fleet that arrives and is refused.
	AutoRegistrationEnabled bool `json:"autoRegistrationEnabled"`

	Warning string `json:"warning"`
	Message string `json:"message"`
}

func (s *Service) handleListTrustedCertificates(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Authorize(r.Context(), r); err != nil {
		writeError(w, r, err)
		return
	}

	tenant := r.PathValue("tenant")
	certificates, err := s.trustedCertificates(r.Context(), tenant)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, TrustedCertificatesResponse{
		Tenant:       tenant,
		Certificates: certificates,
		Warning:      CoverageWarning,
	})
}

// trustedCertificates reads a tenant's truststore and assesses each entry.
//
// Reading needs ROLE_TENANT_MANAGEMENT_READ, which the manifest already holds.
// Writing one needs ROLE_TENANT_MANAGEMENT_ADMIN, which it gained for this.
func (s *Service) trustedCertificates(
	ctx context.Context, tenant string) ([]TrustedCertificateView, error) {

	client, err := s.tenant(tenant)
	if err != nil {
		return nil, err
	}

	iterator := client.client.TrustedCertificates.ListAll(ctx,
		trustedcertificates.ListOptions{TenantID: tenant})

	views := make([]TrustedCertificateView, 0)
	for certificate, err := range iterator.Items() {
		if err != nil {
			return nil, fmt.Errorf("could not read the trusted certificates of %s: %w", tenant, err)
		}
		views = append(views, assess([]byte(certificate.Raw()), time.Now()))
	}
	if err := iterator.Err(); err != nil {
		return nil, fmt.Errorf("could not read the trusted certificates of %s: %w", tenant, err)
	}
	return views, nil
}

// assess turns a trusted certificate into a view, and decides whether a cutover
// would attempt it.
//
// Every refusal here is something a cutover would otherwise discover after the
// source had already lost the certificate, which is the worst moment to find
// out - or, for a disabled one, never discover at all.
func assess(raw []byte, now time.Time) TrustedCertificateView {
	view := TrustedCertificateView{
		Fingerprint:                gjson.GetBytes(raw, "fingerprint").String(),
		Name:                       gjson.GetBytes(raw, "name").String(),
		Status:                     gjson.GetBytes(raw, "status").String(),
		Subject:                    gjson.GetBytes(raw, "subject").String(),
		Issuer:                     gjson.GetBytes(raw, "issuer").String(),
		NotAfter:                   gjson.GetBytes(raw, "notAfter").String(),
		AutoRegistrationEnabled:    gjson.GetBytes(raw, "autoRegistrationEnabled").Bool(),
		TenantCertificateAuthority: gjson.GetBytes(raw, "tenantCertificateAuthority").Bool(),
	}

	switch {
	case view.Fingerprint == "":
		view.Reason = "the platform reported no fingerprint, which is how a trusted " +
			"certificate is addressed"
	case view.TenantCertificateAuthority:
		view.Reason = "this is the tenant's own certificate authority and cannot be moved. " +
			"A fleet enrolled against it needs re-enrolling in the target tenant, which is " +
			"a different mechanism"
	case expired(view.NotAfter, now):
		view.Reason = fmt.Sprintf("this certificate expired on %s. Moving it would produce "+
			"a cutover that cannot work, after an outage that cannot be avoided", view.NotAfter)
	case view.Status == StatusDisabled:
		// A disabled certificate authenticates nobody, so there is no fleet here
		// to move and an outage would buy nothing. More to the point, it is
		// disabled because somebody disabled it - decommissioned, superseded,
		// suspended after an incident - and a migration is not the place to
		// decide that is over with. Re-enable it in the source first if it should
		// be moved.
		view.Reason = "this certificate is disabled in this tenant, so it authenticates no " +
			"device and there is no fleet to move. Enable it here first if it should be moved"
	default:
		view.Movable = true
	}
	return view
}

// withStatus adds the source's status to a certificate body, or leaves it out.
//
// Carried across rather than set, so moving a certificate the source had
// disabled does not enable it. Omitted when the source reported none: a
// platform that answers without a status has a default for it, and inventing
// one here - in either direction - would be this service deciding whether a CA
// is in use.
func withStatus(status string, body map[string]any) map[string]any {
	if status != "" {
		body["status"] = status
	}
	return body
}

func expired(notAfter string, now time.Time) bool {
	if notAfter == "" {
		return false
	}
	deadline, err := time.Parse(time.RFC3339, notAfter)
	if err != nil {
		// An unparseable date is not evidence of expiry, and refusing on it
		// would strand a fleet over a format this service does not know.
		return false
	}
	return deadline.Before(now)
}

func (s *Service) handleTrustAnchorCutover(w http.ResponseWriter, r *http.Request) {
	caller, err := s.Authorize(r.Context(), r)
	if err != nil {
		writeError(w, r, err)
		return
	}

	var request TrustAnchorCutoverRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, r, err)
		return
	}

	response, err := s.cutOver(r.Context(), request, caller)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, response)
}

// cutOver moves a trusted certificate from one tenant to the other.
//
// Synchronous, not a job, and deliberately: the whole operation is two platform
// calls with an **outage between them**, and a failure has to reach the person
// who asked while they are still there to act on it. A queued job that failed
// halfway would report it into a record nobody is watching, with the fleet down.
//
// Everything that can be checked is checked before the source loses anything.
// That is the one design rule this operation has, because there is no flag to
// flip back: recovery from a failed create is re-creating in the source, not
// reversing a status.
func (s *Service) cutOver(ctx context.Context, request TrustAnchorCutoverRequest,
	caller *Caller) (*TrustAnchorCutoverResponse, error) {

	if request.SourceTenant == "" || request.TargetTenant == "" || request.Fingerprint == "" {
		return nil, &BadRequestError{
			Reason: "sourceTenant, targetTenant and fingerprint are all required"}
	}
	if request.SourceTenant == request.TargetTenant {
		return nil, &BadRequestError{
			Reason: "the source and the target are the same tenant"}
	}

	source, err := s.tenant(request.SourceTenant)
	if err != nil {
		return nil, err
	}
	target, err := s.tenant(request.TargetTenant)
	if err != nil {
		return nil, err
	}

	// 1. The certificate has to be there, and movable.
	existing := source.client.TrustedCertificates.Get(ctx, trustedcertificates.GetOptions{
		TenantID:    request.SourceTenant,
		Fingerprint: request.Fingerprint,
	})
	if existing.Err != nil {
		if existing.HTTPStatus == http.StatusNotFound {
			return nil, &NotFoundError{What: fmt.Sprintf(
				"trusted certificate %s in %s", request.Fingerprint, request.SourceTenant)}
		}
		return nil, fmt.Errorf("could not read trusted certificate %s in %s: %w",
			request.Fingerprint, request.SourceTenant, existing.Err)
	}

	raw := []byte(existing.Data.Raw())
	view := assess(raw, time.Now())
	if !view.Movable {
		return nil, &BadRequestError{Reason: view.Reason}
	}

	// Resolved before anything is deleted, so what the target will be asked for
	// is settled while the source still has the certificate.
	autoRegistration := view.AutoRegistrationEnabled
	if request.AutoRegistrationEnabled != nil {
		autoRegistration = *request.AutoRegistrationEnabled
	}

	// 2. The PEM has to be readable, or there is nothing to create in the target.
	//    Checked here rather than after the delete for the obvious reason.
	pem := gjson.GetBytes(raw, "certInPemFormat").String()
	if pem == "" {
		return nil, &BadRequestError{Reason: fmt.Sprintf(
			"the source tenant returned no certInPemFormat for %s, so there is nothing "+
				"to recreate in %s", request.Fingerprint, request.TargetTenant)}
	}

	// 3. The target must not already have it. It would refuse anyway - a
	//    fingerprint is unique per instance - but finding out after the source
	//    has lost it would be an outage for nothing.
	inTarget := target.client.TrustedCertificates.Get(ctx, trustedcertificates.GetOptions{
		TenantID:    request.TargetTenant,
		Fingerprint: request.Fingerprint,
	})
	if inTarget.Err == nil {
		return nil, &ConflictError{Reason: fmt.Sprintf(
			"%s already has trusted certificate %s, so there is nothing to move",
			request.TargetTenant, request.Fingerprint)}
	}

	// From here the fleet is affected. Both calls, no interruption, and a
	// restore attempt if the second one fails.
	deleted := source.client.TrustedCertificates.Delete(ctx, trustedcertificates.DeleteOptions{
		TenantID:    request.SourceTenant,
		Fingerprint: request.Fingerprint,
	})
	if deleted.Err != nil {
		return nil, fmt.Errorf("could not remove trusted certificate %s from %s, so nothing "+
			"was changed: %w", request.Fingerprint, request.SourceTenant, deleted.Err)
	}

	created := target.client.TrustedCertificates.Create(ctx,
		trustedcertificates.CreateOptions{TenantID: request.TargetTenant},
		withStatus(view.Status, map[string]any{
			"name":            view.Name,
			"certInPemFormat": pem,
			// What the source had, or what the caller asked for instead. Forcing
			// this to true would let a cutover quietly grant a CA the run of a
			// tenant it had only been trusted in; see the request field.
			"autoRegistrationEnabled": autoRegistration,
		}))
	if created.Err != nil {
		// The window is open and the fleet is on neither tenant. Putting it back
		// is the only recovery, and it is attempted here rather than left to the
		// caller because every second of this counts.
		//
		// The source's own values, never the request's: this is putting back what
		// was there, not making the move somewhere else.
		restore := source.client.TrustedCertificates.Create(ctx,
			trustedcertificates.CreateOptions{TenantID: request.SourceTenant},
			withStatus(view.Status, map[string]any{
				"name":                    view.Name,
				"certInPemFormat":         pem,
				"autoRegistrationEnabled": view.AutoRegistrationEnabled,
			}))
		if restore.Err != nil {
			return nil, fmt.Errorf(
				"%s refused the certificate (%w), and putting it back into %s ALSO failed "+
					"(%v). The fleet chained to %s can reach neither tenant. Recreate the "+
					"certificate in one of them by hand, from the PEM the certificate's "+
					"owner holds",
				request.TargetTenant, created.Err, request.SourceTenant, restore.Err,
				request.Fingerprint)
		}
		return nil, fmt.Errorf(
			"%s refused the certificate, so it was put back into %s and nothing has "+
				"moved: %w", request.TargetTenant, request.SourceTenant, created.Err)
	}

	// Read back rather than trusted. A target that accepted the certificate but
	// did not take autoRegistrationEnabled with it would decide, silently, which
	// devices this CA may bring into that tenant.
	//
	// The second read is for a disagreement rather than for a false: the create
	// response does not always carry the field, and since false can be the
	// answer somebody asked for, reading an absent field as "wrong" would send a
	// deliberate choice back for confirmation every time.
	inTargetNow := gjson.GetBytes([]byte(created.Data.Raw()), "autoRegistrationEnabled").Bool()
	if inTargetNow != autoRegistration {
		confirmed := target.client.TrustedCertificates.Get(ctx, trustedcertificates.GetOptions{
			TenantID:    request.TargetTenant,
			Fingerprint: request.Fingerprint,
		})
		if confirmed.Err == nil {
			inTargetNow = gjson.GetBytes([]byte(confirmed.Data.Raw()),
				"autoRegistrationEnabled").Bool()
		}
	}

	// The fleet does not have to be pointed at the target's hostname. Within one
	// instance the platform routes a connection by the certificate it presents,
	// not by the hostname it dials - measured: a device dialling the source host
	// with a chain only the target trusts arrives in the target. Both tenants
	// here are on this instance by construction - a tenant elsewhere cannot be
	// subscribed to this service - so there is no device-side half to warn about.
	message := fmt.Sprintf(
		"%s now trusts this certificate and %s no longer does. The devices chained to it "+
			"arrive in %s on their next connection, with nothing to change on the device: "+
			"within one instance a connection is routed by the certificate it presents "+
			"rather than by the hostname it dials. Connected devices move when they "+
			"reconnect, and devices that are offline move when they return.",
		request.TargetTenant, request.SourceTenant, request.TargetTenant)

	switch {
	case inTargetNow != autoRegistration:
		// What the target did is not what was asked for, in either direction, and
		// that decides which devices this CA may bring into the tenant. Said
		// first and as a warning, because it is the half nobody would notice.
		message += fmt.Sprintf(
			" WARNING: auto-registration was requested as %t and the target reports %t. "+
				"Check it in Administration before the fleet reconnects.",
			autoRegistration, inTargetNow)
	case !inTargetNow:
		message += " Auto-registration is off, as it was in the source tenant, so a device " +
			"arriving with no device user of its own in " + request.TargetTenant +
			" is refused. Devices a migration already created one for are unaffected."
	case !view.AutoRegistrationEnabled:
		// On in the target and off in the source, which is a change somebody
		// asked for - repeated back, because it widens what this CA may do.
		message += " Auto-registration is now on in " + request.TargetTenant +
			", where it was off in " + request.SourceTenant +
			": any device presenting a chain to this certificate is registered there."
	}

	// What this line adds to the record, rather than being the record.
	//
	// The platform audits both halves in their own tenants - "Trusted certificate
	// deleted" in the source, "Trusted certificate uploaded" in the target - so
	// what happened, where and when survives this container without any help from
	// here. This operation creates no job, being two calls rather than a run, and
	// it does not need to.
	//
	// What those entries name as the actor is the *service user*, because that is
	// who makes the calls: the operator is an owner-tenant user with no rights in
	// either subtenant, so there is no identity to act under but this service's
	// own. That makes this line the only place the caller appears, in a log that
	// does not outlive the container.
	//
	// Accepted rather than closed, and the reasoning is in
	// docs/microservice.md: writing an audit record naming the caller would cost
	// this service ROLE_AUDIT_ADMIN in every subscribed tenant, which is a wide
	// permission to hold for one line of provenance.
	slog.InfoContext(ctx, "trust anchor cutover",
		"by", caller.Username,
		"fingerprint", request.Fingerprint,
		"from", request.SourceTenant,
		"to", request.TargetTenant,
		"status", view.Status,
		// All three, because the interesting line in this log is the one where
		// they disagree: what the source had, what was asked for, what the target
		// now says.
		"sourceAutoRegistration", view.AutoRegistrationEnabled,
		"requestedAutoRegistration", autoRegistration,
		"autoRegistration", inTargetNow)

	return &TrustAnchorCutoverResponse{
		Fingerprint:             request.Fingerprint,
		Name:                    view.Name,
		SourceTenant:            request.SourceTenant,
		TargetTenant:            request.TargetTenant,
		AutoRegistrationEnabled: inTargetNow,
		Warning:                 CoverageWarning,
		Message:                 message,
	}, nil
}
