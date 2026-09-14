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
// source tenant and put it in the target with auto-registration enabled.
//
// Everything awkward about it is platform behaviour, measured rather than
// assumed (docs/platform-behaviour.md), and each piece is
// commented where it bites:
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

// TrustAnchorCutoverRequest moves one trusted certificate between tenants.
type TrustAnchorCutoverRequest struct {
	SourceTenant string `json:"sourceTenant"`
	TargetTenant string `json:"targetTenant"`

	// Fingerprint is the SHA-1 fingerprint, which is how the platform keys a
	// trusted certificate. Its own field rather than a name, because names are
	// not unique and this operation has an outage in it.
	Fingerprint string `json:"fingerprint"`
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
// Both refusals are things a cutover would otherwise discover after the source
// had already lost the certificate, which is the worst moment to find out.
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
	default:
		view.Movable = true
	}
	return view
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
		map[string]any{
			"name":            view.Name,
			"certInPemFormat": pem,
			"status":          "ENABLED",
			// The whole point: an arriving device is registered rather than
			// refused. No proof of possession is demanded for it, which is why
			// the public certificate is enough.
			"autoRegistrationEnabled": true,
		})
	if created.Err != nil {
		// The window is open and the fleet is on neither tenant. Putting it back
		// is the only recovery, and it is attempted here rather than left to the
		// caller because every second of this counts.
		restore := source.client.TrustedCertificates.Create(ctx,
			trustedcertificates.CreateOptions{TenantID: request.SourceTenant},
			map[string]any{
				"name":                    view.Name,
				"certInPemFormat":         pem,
				"status":                  gjson.GetBytes(raw, "status").String(),
				"autoRegistrationEnabled": view.AutoRegistrationEnabled,
			})
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
	// dropped autoRegistrationEnabled would leave every arriving device refused,
	// which is a silent failure of the only thing this operation is for.
	autoRegistration := gjson.GetBytes([]byte(created.Data.Raw()), "autoRegistrationEnabled").Bool()
	if !autoRegistration {
		confirmed := target.client.TrustedCertificates.Get(ctx, trustedcertificates.GetOptions{
			TenantID:    request.TargetTenant,
			Fingerprint: request.Fingerprint,
		})
		if confirmed.Err == nil {
			autoRegistration = gjson.GetBytes([]byte(confirmed.Data.Raw()),
				"autoRegistrationEnabled").Bool()
		}
	}

	// This used to tell the operator that the fleet still had to be pointed at the
	// target's hostname, on the belief that the platform resolves the tenant from
	// the hostname first. Measured false within one instance: a device dialling the
	// source host with a chain only the target trusted arrived in the target. Both
	// tenants here are on this instance by construction - a tenant elsewhere cannot
	// be subscribed to this service - so there is no device-side half to warn about.
	message := fmt.Sprintf(
		"%s now trusts this certificate and %s no longer does. The devices chained to it "+
			"arrive in %s on their next connection, with nothing to change on the device: "+
			"within one instance a connection is routed by the certificate it presents "+
			"rather than by the hostname it dials. Connected devices move when they "+
			"reconnect, and devices that are offline move when they return.",
		request.TargetTenant, request.SourceTenant, request.TargetTenant)
	if !autoRegistration {
		message += " WARNING: the target reports autoRegistrationEnabled as false, so " +
			"arriving devices will be refused until it is enabled."
	}

	// The container log is the only record this operation leaves. It creates no
	// job, because it is two calls rather than a run - but it takes a fleet
	// offline for a moment, and who did that is worth being able to find.
	slog.InfoContext(ctx, "trust anchor cutover",
		"by", caller.Username,
		"fingerprint", request.Fingerprint,
		"from", request.SourceTenant,
		"to", request.TargetTenant,
		"autoRegistration", autoRegistration)

	return &TrustAnchorCutoverResponse{
		Fingerprint:             request.Fingerprint,
		Name:                    view.Name,
		SourceTenant:            request.SourceTenant,
		TargetTenant:            request.TargetTenant,
		AutoRegistrationEnabled: autoRegistration,
		Warning:                 CoverageWarning,
		Message:                 message,
	}, nil
}
