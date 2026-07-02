// Package grpc is the Connect handler for ConnectorHubService. It maps proto
// requests to app use cases, app/domain types back to proto, and domain errors to
// Connect codes. No business logic lives here (CONVENTIONS.md: map only). Secrets
// are write-only: reads echo public config only.
package grpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	connectorv1 "github.com/restorna/platform/gen/go/restorna/connector/v1"
	"github.com/restorna/platform/gen/go/restorna/connector/v1/connectorv1connect"
	pkgerrors "github.com/restorna/platform/pkg/errors"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/connectorhub/internal/app"
	"github.com/restorna/platform/services/connectorhub/internal/domain"
	"github.com/restorna/platform/services/connectorhub/internal/ports"
)

// Handler adapts *app.App to the generated ConnectorHubServiceHandler interface.
type Handler struct {
	connectorv1connect.UnimplementedConnectorHubServiceHandler
	uc *app.App
}

var _ connectorv1connect.ConnectorHubServiceHandler = (*Handler)(nil)

// New builds a Connect handler around the use-case app.
func New(uc *app.App) *Handler { return &Handler{uc: uc} }

func (h *Handler) ListAvailable(ctx context.Context, _ *connect.Request[connectorv1.ListAvailableRequest]) (*connect.Response[connectorv1.ListAvailableResponse], error) {
	mans, err := h.uc.ListAvailable(ctx, ownerFromCtx(ctx))
	if err != nil {
		return nil, toConnect(err)
	}
	out := make([]*connectorv1.Manifest, 0, len(mans))
	for _, m := range mans {
		out = append(out, manifestToProto(m))
	}
	return connect.NewResponse(&connectorv1.ListAvailableResponse{Connectors: out}), nil
}

func (h *Handler) Install(ctx context.Context, req *connect.Request[connectorv1.InstallRequest]) (*connect.Response[connectorv1.InstallResponse], error) {
	scope, _ := tenancy.From(ctx)
	inst, err := h.uc.Install(ctx, app.InstallInput{
		OwnerID:      scope.OwnerID,
		RestaurantID: scope.RestaurantID,
		ConnectorID:  req.Msg.GetConnectorId(),
		TestMode:     req.Msg.GetTestMode(),
		Config:       req.Msg.GetConfig(),
	})
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&connectorv1.InstallResponse{Installation: installationToProto(inst)}), nil
}

func (h *Handler) UpdateInstallation(ctx context.Context, req *connect.Request[connectorv1.UpdateInstallationRequest]) (*connect.Response[connectorv1.UpdateInstallationResponse], error) {
	inst, err := h.uc.UpdateInstallation(ctx, app.UpdateInput{
		OwnerID:        ownerFromCtx(ctx),
		InstallationID: req.Msg.GetInstallationId(),
		Enabled:        req.Msg.GetEnabled(),
		Config:         req.Msg.GetConfig(),
	})
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&connectorv1.UpdateInstallationResponse{Installation: installationToProto(inst)}), nil
}

func (h *Handler) ListInstallations(ctx context.Context, req *connect.Request[connectorv1.ListInstallationsRequest]) (*connect.Response[connectorv1.ListInstallationsResponse], error) {
	list, err := h.uc.ListInstallations(ctx, ownerFromCtx(ctx), capabilityFromProto(req.Msg.GetCapability()))
	if err != nil {
		return nil, toConnect(err)
	}
	out := make([]*connectorv1.Installation, 0, len(list))
	for _, inst := range list {
		out = append(out, installationToProto(inst))
	}
	return connect.NewResponse(&connectorv1.ListInstallationsResponse{Installations: out}), nil
}

func (h *Handler) Resolve(ctx context.Context, req *connect.Request[connectorv1.ResolveRequest]) (*connect.Response[connectorv1.ResolveResponse], error) {
	r, err := h.uc.Resolve(ctx, ownerFromCtx(ctx), capabilityFromProto(req.Msg.GetCapability()), req.Msg.GetPreferConnectorId())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&connectorv1.ResolveResponse{
		ConnectorId:    r.ConnectorID,
		InstallationId: r.InstallationID,
		TestMode:       r.TestMode,
		Config:         r.Config, // decrypted; internal callers only
	}), nil
}

func (h *Handler) IngestWebhook(ctx context.Context, req *connect.Request[connectorv1.IngestWebhookRequest]) (*connect.Response[connectorv1.IngestWebhookResponse], error) {
	res, err := h.uc.IngestWebhook(ctx, app.WebhookInput{
		OwnerID:     ownerFromCtx(ctx),
		ConnectorID: req.Msg.GetConnectorId(),
		Body:        req.Msg.GetBody(),
		Headers:     req.Msg.GetHeaders(),
	})
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&connectorv1.IngestWebhookResponse{
		EventType: res.EventType,
		Accepted:  res.Accepted,
	}), nil
}

// --- mapping helpers ---

func manifestToProto(m ports.ConnectorManifest) *connectorv1.Manifest {
	caps := make([]connectorv1.Capability, 0, len(m.Capabilities))
	for _, c := range m.Capabilities {
		caps = append(caps, capabilityToProto(c))
	}
	return &connectorv1.Manifest{
		Id:           m.ID,
		Name:         m.Name,
		Capabilities: caps,
		ConfigSchema: string(m.ConfigSchema),
		LogoUrl:      m.LogoURL,
	}
}

// installationToProto maps an Installation to proto. It echoes ONLY the non-secret
// (public) config — secrets are write-only and never returned (CONVENTIONS.md /
// proto contract).
func installationToProto(i domain.Installation) *connectorv1.Installation {
	pub := make(map[string]string, len(i.PublicConfig))
	for k, v := range i.PublicConfig {
		pub[k] = v
	}
	return &connectorv1.Installation{
		Id:          i.ID,
		ConnectorId: i.ConnectorID,
		Enabled:     i.Enabled,
		TestMode:    i.TestMode,
		Config:      pub,
		InstalledAt: i.InstalledAt.UTC().Format(rfc3339),
	}
}

const rfc3339 = "2006-01-02T15:04:05Z07:00"

func capabilityToProto(c domain.Capability) connectorv1.Capability {
	switch c {
	case domain.CapabilityPayment:
		return connectorv1.Capability_CAPABILITY_PAYMENT
	case domain.CapabilityAggregator:
		return connectorv1.Capability_CAPABILITY_AGGREGATOR
	case domain.CapabilityCRM:
		return connectorv1.Capability_CAPABILITY_CRM
	case domain.CapabilityERP:
		return connectorv1.Capability_CAPABILITY_ERP
	case domain.CapabilityNotification:
		return connectorv1.Capability_CAPABILITY_NOTIFICATION
	default:
		return connectorv1.Capability_CAPABILITY_UNSPECIFIED
	}
}

func capabilityFromProto(c connectorv1.Capability) domain.Capability {
	switch c {
	case connectorv1.Capability_CAPABILITY_PAYMENT:
		return domain.CapabilityPayment
	case connectorv1.Capability_CAPABILITY_AGGREGATOR:
		return domain.CapabilityAggregator
	case connectorv1.Capability_CAPABILITY_CRM:
		return domain.CapabilityCRM
	case connectorv1.Capability_CAPABILITY_ERP:
		return domain.CapabilityERP
	case connectorv1.Capability_CAPABILITY_NOTIFICATION:
		return domain.CapabilityNotification
	default:
		return "" // unspecified => no capability filter
	}
}

// toConnect maps domain/app errors to Connect codes (the ONLY place this happens).
func toConnect(err error) error {
	var qe *app.QuotaError
	if errors.As(err, &qe) {
		return connect.NewError(connect.CodeResourceExhausted, err)
	}
	switch {
	case errors.Is(err, tenancy.ErrPermissionDenied):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, domain.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, domain.ErrWebhookUnverified):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, domain.ErrQuotaExceeded):
		return connect.NewError(connect.CodeResourceExhausted, err)
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, domain.ErrAlreadyExists), isAlreadyExists(err):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

func isAlreadyExists(err error) bool {
	return err != nil && errors.Is(err, pkgerrors.ErrAlreadyExists)
}

// ownerFromCtx reads the JWT-derived tenancy scope set by the auth interceptor.
// The owner id ALWAYS comes from the auth context, never the request body
// (CONVENTIONS.md multi-tenancy rule).
func ownerFromCtx(ctx context.Context) string {
	if s, ok := tenancy.From(ctx); ok {
		return s.OwnerID
	}
	return ""
}
