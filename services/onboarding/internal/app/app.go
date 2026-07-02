// Package app holds the onboarding saga use cases. It depends only on ports +
// domain. Each use case is a saga step that:
//   1. loads the OnboardingState ledger,
//   2. SKIPS work already recorded as done (reusing stored ids) → idempotent,
//   3. calls the downstream service ports for the not-yet-done work,
//   4. records the step in the ledger and persists it → resumable.
//
// A retried call therefore re-drives only the remaining work. Failures leave the
// ledger at the last successfully-completed step so the client can simply call
// the same RPC again.
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	onboardingv1 "github.com/restorna/platform/gen/go/restorna/onboarding/v1"
	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/onboarding/internal/domain"
	"github.com/restorna/platform/services/onboarding/internal/ports"
)

// EventCompleted is the CloudEvents type emitted when the saga reaches GOLIVE. It
// carries owner/brand/restaurant ids so downstream (catalog/floor) can seed the
// menu and generate table QR codes — those are data-plane concerns handled later.
const EventCompleted = "restorna.onboarding.completed.v1"

// defaultPlanID is assigned when the StartOnboarding request omits a plan.
const defaultPlanID = "free"

// App is the onboarding saga use-case set, wired against its ports.
type App struct {
	repo     ports.Repo
	identity ports.Identity
	tenant   ports.Tenant
	ents     ports.Entitlements
	staff    ports.Staff
	settings ports.Settings
}

// New wires the saga against its ports. The five service clients are injected so
// they can be real Connect clients in production and fakes in tests.
func New(repo ports.Repo, identity ports.Identity, tenant ports.Tenant, ents ports.Entitlements, staff ports.Staff, settings ports.Settings) *App {
	return &App{repo: repo, identity: identity, tenant: tenant, ents: ents, staff: staff, settings: settings}
}

// StartInput carries the StartOnboarding parameters.
type StartInput struct {
	OwnerName    string
	ContactEmail string
	ContactPhone string
	Country      string
	PlanID       string
}

// StartOnboarding is the first saga step. It creates the owner login (identity),
// the owner record (tenant), assigns the plan (entitlements), and persists the
// ledger with ACCOUNT + PLAN completed. It is idempotent end-to-end: a retry that
// already has an owner id reuses it and re-asserts the plan rather than creating
// a second owner.
//
// Only platform admins (Restorna staff) may start an onboarding — owners do not
// yet exist when this runs, so the caller is the platform console.
func (a *App) StartOnboarding(ctx context.Context, in StartInput) (domain.State, error) {
	if err := requireRole(ctx, commonv1.Role_ROLE_PLATFORM_ADMIN); err != nil {
		return domain.State{}, err
	}

	name := strings.TrimSpace(in.OwnerName)
	if name == "" {
		return domain.State{}, fmt.Errorf("%w: owner_name is required", domain.ErrInvalidInput)
	}
	if strings.TrimSpace(in.ContactEmail) == "" && strings.TrimSpace(in.ContactPhone) == "" {
		return domain.State{}, fmt.Errorf("%w: contact email or phone is required", domain.ErrInvalidInput)
	}

	plan := strings.TrimSpace(in.PlanID)
	if plan == "" {
		plan = defaultPlanID
	}

	st := domain.New(ids.New("onb"))
	st.PlanID = plan

	// ACCOUNT: owner login + owner record. Idempotent on the identity/tenant side
	// by contact; we store the produced ids in the ledger.
	userID, err := a.identity.EnsureOwnerUser(ctx, in.ContactEmail, in.ContactPhone, name)
	if err != nil {
		return domain.State{}, fmt.Errorf("identity.EnsureOwnerUser: %w", err)
	}
	st.UserID = userID

	ownerID, err := a.tenant.CreateOwner(ctx, name, name, in.Country)
	if err != nil {
		return domain.State{}, fmt.Errorf("tenant.CreateOwner: %w", err)
	}
	st.OwnerID = ownerID
	st.Complete(onboardingv1.Step_STEP_ACCOUNT)

	// PLAN: assign the entitlement so quota checks (staff limits, outlets) resolve
	// for every later step.
	if err := a.ents.AssignPlan(ctx, ownerID, plan); err != nil {
		// ACCOUNT is done and persisted-on-success below; here we have not yet
		// persisted, so return and let the caller retry Start (idempotent).
		return domain.State{}, fmt.Errorf("entitlements.AssignPlan: %w", err)
	}
	st.Complete(onboardingv1.Step_STEP_PLAN)

	if err := a.repo.Create(ctx, st, nil); err != nil {
		return domain.State{}, fmt.Errorf("persist onboarding: %w", err)
	}
	return st, nil
}

// SubmitBrandInput carries the SubmitBrand parameters.
type SubmitBrandInput struct {
	OnboardingID    string
	BrandName       string
	PrimaryColor    string
	Logo            []byte
	LogoContentType string
}

// SubmitBrand advances the BRAND step: create the brand and upload its logo. If
// the BRAND step is already complete (a retry), it reuses the stored brand id and
// returns without calling tenant again — fully idempotent.
func (a *App) SubmitBrand(ctx context.Context, in SubmitBrandInput) (domain.State, string, error) {
	st, err := a.load(ctx, in.OnboardingID)
	if err != nil {
		return domain.State{}, "", err
	}
	if err := st.Require(onboardingv1.Step_STEP_ACCOUNT, onboardingv1.Step_STEP_PLAN); err != nil {
		return domain.State{}, "", err
	}
	if st.IsDone(onboardingv1.Step_STEP_BRAND) {
		return st, st.BrandID, nil // idempotent: already created
	}

	if strings.TrimSpace(in.BrandName) == "" {
		return domain.State{}, "", fmt.Errorf("%w: brand_name is required", domain.ErrInvalidInput)
	}

	// Create the brand only if not already created in a prior partial run.
	if st.BrandID == "" {
		brandID, err := a.tenant.CreateBrand(ctx, st.OwnerID, in.BrandName, in.PrimaryColor)
		if err != nil {
			return domain.State{}, "", fmt.Errorf("tenant.CreateBrand: %w", err)
		}
		st.BrandID = brandID
		// Persist the brand id immediately so a crash before the logo upload does
		// not orphan/duplicate the brand on retry.
		if err := a.repo.Save(ctx, st, nil); err != nil {
			return domain.State{}, "", fmt.Errorf("persist brand id: %w", err)
		}
	}

	// Upload the logo bytes (optional — an owner may skip a logo).
	if len(in.Logo) > 0 {
		url, err := a.tenant.SetBrandLogo(ctx, st.BrandID, in.Logo, in.LogoContentType)
		if err != nil {
			return domain.State{}, "", fmt.Errorf("tenant.SetBrandLogo: %w", err)
		}
		st.LogoURL = url
	}

	st.Complete(onboardingv1.Step_STEP_BRAND)
	if err := a.repo.Save(ctx, st, nil); err != nil {
		return domain.State{}, "", fmt.Errorf("persist onboarding: %w", err)
	}
	return st, st.BrandID, nil
}

// SubmitOutletInput carries the SubmitOutlet parameters.
type SubmitOutletInput struct {
	OnboardingID string
	Name         string
	Address      string
	Timezone     string
	GSTIN        string
}

// SubmitOutlet advances OUTLET + SETTINGS: create the first restaurant and seed
// default settings (currency/gst). Idempotent: a completed OUTLET reuses the
// stored restaurant id; SETTINGS overrides are themselves idempotent upserts.
func (a *App) SubmitOutlet(ctx context.Context, in SubmitOutletInput) (domain.State, string, error) {
	st, err := a.load(ctx, in.OnboardingID)
	if err != nil {
		return domain.State{}, "", err
	}
	if err := st.Require(onboardingv1.Step_STEP_BRAND); err != nil {
		return domain.State{}, "", err
	}
	if st.IsDone(onboardingv1.Step_STEP_OUTLET) && st.IsDone(onboardingv1.Step_STEP_SETTINGS) {
		return st, st.OutletID, nil // idempotent: already provisioned + seeded
	}

	if strings.TrimSpace(in.Name) == "" {
		return domain.State{}, "", fmt.Errorf("%w: outlet name is required", domain.ErrInvalidInput)
	}
	tz := strings.TrimSpace(in.Timezone)
	if tz == "" {
		tz = "Asia/Kolkata"
	}

	// OUTLET: create the restaurant only if not already created.
	if st.OutletID == "" {
		outletID, err := a.tenant.CreateRestaurant(ctx, st.BrandID, in.Name, in.Address, tz, in.GSTIN)
		if err != nil {
			return domain.State{}, "", fmt.Errorf("tenant.CreateRestaurant: %w", err)
		}
		st.OutletID = outletID
		st.Complete(onboardingv1.Step_STEP_OUTLET)
		if err := a.repo.Save(ctx, st, nil); err != nil {
			return domain.State{}, "", fmt.Errorf("persist outlet id: %w", err)
		}
	}

	// SETTINGS: seed sane outlet defaults. Each SetOverride is an idempotent
	// upsert, so re-running the step is safe.
	if !st.IsDone(onboardingv1.Step_STEP_SETTINGS) {
		for _, d := range defaultOutletSettings(in.GSTIN) {
			if err := a.settings.SetOverride(ctx, st.OwnerID, st.BrandID, st.OutletID, d.key, d.valueType, d.raw); err != nil {
				return domain.State{}, "", fmt.Errorf("settings.SetOverride(%s): %w", d.key, err)
			}
		}
		st.Complete(onboardingv1.Step_STEP_SETTINGS)
		if err := a.repo.Save(ctx, st, nil); err != nil {
			return domain.State{}, "", fmt.Errorf("persist settings step: %w", err)
		}
	}

	return st, st.OutletID, nil
}

// setting is a default override to seed at the outlet.
type setting struct {
	key       string
	valueType string // INT | BOOL | STRING | DECIMAL
	raw       string
}

// defaultOutletSettings returns the billing/locale defaults seeded on the first
// outlet. GST is enabled by default when a GSTIN is supplied.
func defaultOutletSettings(gstin string) []setting {
	gst := "0"
	if strings.TrimSpace(gstin) != "" {
		gst = "5"
	}
	return []setting{
		{key: "billing.currency", valueType: "STRING", raw: "INR"},
		{key: "billing.gst_pct", valueType: "DECIMAL", raw: gst},
		{key: "billing.service_charge_pct", valueType: "DECIMAL", raw: "0"},
		{key: "billing.rounding", valueType: "STRING", raw: "nearest_1"},
		{key: "ordering.modes", valueType: "JSON", raw: `["dine_in"]`},
	}
}

// InviteInput is one team invite for the TEAM step.
type InviteInput struct {
	Name  string
	Email string
	Phone string
	Role  string
}

// InviteResult reports the outcome of a single invite so the caller learns which
// ones failed (e.g. quota exhausted) without the whole step aborting.
type InviteResult struct {
	Name    string
	Email   string
	Role    string
	StaffID string
	Invited bool
	Error   string // non-empty when this invite failed
}

// InviteTeam advances TEAM: for each invite, AddStaff then InviteStaff. A quota
// rejection (ResourceExhausted from staff) is captured per-invite and reported;
// the step still completes for the invites that succeeded. The step is marked
// done so the saga can proceed to go-live even if some invites need a plan
// upgrade — the owner can retry the failed ones after upgrading.
func (a *App) InviteTeam(ctx context.Context, onboardingID string, invites []InviteInput) (domain.State, []InviteResult, error) {
	st, err := a.load(ctx, onboardingID)
	if err != nil {
		return domain.State{}, nil, err
	}
	if err := st.Require(onboardingv1.Step_STEP_OUTLET); err != nil {
		return domain.State{}, nil, err
	}

	results := make([]InviteResult, 0, len(invites))
	for _, inv := range invites {
		res := InviteResult{Name: inv.Name, Email: inv.Email, Role: inv.Role}

		staffID, err := a.staff.AddStaff(ctx, st.OutletID, inv.Name, inv.Email, inv.Phone, inv.Role)
		if err != nil {
			if errors.Is(err, ports.ErrQuotaExhausted) {
				res.Error = "quota exhausted: plan upgrade required for this role"
			} else {
				res.Error = err.Error()
			}
			results = append(results, res)
			continue
		}
		res.StaffID = staffID

		if _, err := a.staff.InviteStaff(ctx, staffID); err != nil {
			res.Error = fmt.Sprintf("added but invite failed: %v", err)
			results = append(results, res)
			continue
		}
		res.Invited = true
		results = append(results, res)
	}

	// TEAM is complete once attempted; failures are reported, not fatal. Retrying
	// InviteTeam with only the failed invites is the recovery path.
	st.Complete(onboardingv1.Step_STEP_TEAM)
	if err := a.repo.Save(ctx, st, nil); err != nil {
		return domain.State{}, nil, fmt.Errorf("persist team step: %w", err)
	}
	return st, results, nil
}

// completedEventData is the payload of restorna.onboarding.completed.v1 carrying
// the ids downstream needs to seed the menu and generate table QR codes.
type completedEventData struct {
	OnboardingID string `json:"onboarding_id"`
	OwnerID      string `json:"owner_id"`
	UserID       string `json:"user_id,omitempty"`
	BrandID      string `json:"brand_id"`
	RestaurantID string `json:"restaurant_id"`
	PlanID       string `json:"plan_id"`
}

// Complete advances GOLIVE: it marks the saga done and emits
// restorna.onboarding.completed.v1 (transactionally via the outbox) so the
// data-plane can seed menu + QR codes. Idempotent: a saga already done returns
// without re-emitting (the ledger is the guard).
func (a *App) Complete(ctx context.Context, onboardingID string) (domain.State, error) {
	st, err := a.load(ctx, onboardingID)
	if err != nil {
		return domain.State{}, err
	}
	if st.Done {
		return st, nil // idempotent: already live, do not re-emit
	}
	if err := st.CanComplete(); err != nil {
		return domain.State{}, err
	}

	st.Complete(onboardingv1.Step_STEP_GOLIVE)
	evt := &ports.OutboxEvent{Type: EventCompleted, Data: completedEventData{
		OnboardingID: st.ID,
		OwnerID:      st.OwnerID,
		UserID:       st.UserID,
		BrandID:      st.BrandID,
		RestaurantID: st.OutletID,
		PlanID:       st.PlanID,
	}}
	if err := a.repo.Save(ctx, st, evt); err != nil {
		return domain.State{}, fmt.Errorf("persist golive: %w", err)
	}
	return st, nil
}

// GetState returns the current saga ledger.
func (a *App) GetState(ctx context.Context, onboardingID string) (domain.State, error) {
	return a.load(ctx, onboardingID)
}

// load fetches the saga and asserts the caller is allowed to drive it. Platform
// admins onboard new clients; the owner being onboarded may also read/advance
// their own saga.
func (a *App) load(ctx context.Context, onboardingID string) (domain.State, error) {
	if strings.TrimSpace(onboardingID) == "" {
		return domain.State{}, fmt.Errorf("%w: onboarding_id is required", domain.ErrInvalidInput)
	}
	scope, ok := tenancy.From(ctx)
	if !ok {
		return domain.State{}, fmt.Errorf("%w: missing tenancy scope", domain.ErrNotInScope)
	}
	st, err := a.repo.Get(ctx, onboardingID)
	if err != nil {
		return domain.State{}, err
	}
	// Platform admins may drive any onboarding; otherwise the scope's owner must
	// match the saga's owner.
	if scope.Role != commonv1.Role_ROLE_PLATFORM_ADMIN && scope.OwnerID != st.OwnerID {
		return domain.State{}, fmt.Errorf("%w: onboarding belongs to another owner", domain.ErrNotInScope)
	}
	return st, nil
}

// requireRole asserts the caller holds one of the allowed roles.
func requireRole(ctx context.Context, allowed ...commonv1.Role) error {
	scope, ok := tenancy.From(ctx)
	if !ok {
		return fmt.Errorf("%w: missing tenancy scope", domain.ErrNotInScope)
	}
	return scope.Require(allowed...)
}
