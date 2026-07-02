// Package grpc is the Connect handler for TenantService. It maps proto requests to
// app use cases, app/domain types back to proto, and domain errors to Connect
// codes. No business logic lives here (CONVENTIONS.md: map only).
package grpc

import (
	"context"
	"errors"
	"strconv"

	"connectrpc.com/connect"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	tenantv1 "github.com/restorna/platform/gen/go/restorna/tenant/v1"
	"github.com/restorna/platform/gen/go/restorna/tenant/v1/tenantv1connect"
	pkgerrors "github.com/restorna/platform/pkg/errors"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/tenant/internal/app"
	"github.com/restorna/platform/services/tenant/internal/domain"
)

// Handler adapts *app.App to the generated TenantServiceHandler interface.
type Handler struct {
	tenantv1connect.UnimplementedTenantServiceHandler
	uc *app.App
}

var _ tenantv1connect.TenantServiceHandler = (*Handler)(nil)

// New builds a Connect handler around the use-case app.
func New(uc *app.App) *Handler { return &Handler{uc: uc} }

// --- Owner ---

func (h *Handler) CreateOwner(ctx context.Context, req *connect.Request[tenantv1.CreateOwnerRequest]) (*connect.Response[tenantv1.CreateOwnerResponse], error) {
	o, err := h.uc.CreateOwner(ctx, app.CreateOwnerInput{
		Name:      req.Msg.GetName(),
		LegalName: req.Msg.GetLegalName(),
		Country:   req.Msg.GetCountry(),
	})
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&tenantv1.CreateOwnerResponse{Owner: ownerToProto(o)}), nil
}

func (h *Handler) GetOwner(ctx context.Context, req *connect.Request[tenantv1.GetOwnerRequest]) (*connect.Response[tenantv1.GetOwnerResponse], error) {
	o, err := h.uc.GetOwner(ctx, req.Msg.GetOwnerId())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&tenantv1.GetOwnerResponse{Owner: ownerToProto(o)}), nil
}

// ListOwners is the platform-admin cross-tenant owner index. The app layer gates
// it on ROLE_PLATFORM_ADMIN (tenancy.Require) and returns PermissionDenied
// otherwise — mapped to the Connect code here.
func (h *Handler) ListOwners(ctx context.Context, req *connect.Request[tenantv1.ListOwnersRequest]) (*connect.Response[tenantv1.ListOwnersResponse], error) {
	limit, offset := pageParams(req.Msg.GetPage())
	owners, total, err := h.uc.ListOwners(ctx, req.Msg.GetQuery(), limit, offset)
	if err != nil {
		return nil, toConnect(err)
	}
	out := make([]*tenantv1.Owner, 0, len(owners))
	for _, o := range owners {
		out = append(out, ownerToProto(o))
	}
	return connect.NewResponse(&tenantv1.ListOwnersResponse{
		Owners: out,
		Page:   pageResponse(total, offset+len(owners)),
	}), nil
}

// --- Brand ---

func (h *Handler) CreateBrand(ctx context.Context, req *connect.Request[tenantv1.CreateBrandRequest]) (*connect.Response[tenantv1.CreateBrandResponse], error) {
	b, err := h.uc.CreateBrand(ctx, app.CreateBrandInput{
		OwnerID:      req.Msg.GetOwnerId(),
		Name:         req.Msg.GetName(),
		PrimaryColor: req.Msg.GetPrimaryColor(),
	})
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&tenantv1.CreateBrandResponse{Brand: brandToProto(b)}), nil
}

func (h *Handler) SetBrandLogo(ctx context.Context, req *connect.Request[tenantv1.SetBrandLogoRequest]) (*connect.Response[tenantv1.SetBrandLogoResponse], error) {
	// The logo is passed as an already-uploaded Asset reference in this RPC; raw
	// byte upload is handled by the BFF/gateway which calls the BlobStore-backed
	// app path. Here we attach the supplied asset ref.
	var pre *domain.Asset
	if l := req.Msg.GetLogo(); l != nil {
		pre = &domain.Asset{ID: l.GetId(), URL: l.GetUrl(), ContentType: l.GetContentType()}
	}
	b, err := h.uc.SetBrandLogo(ctx, ownerFromCtx(ctx), req.Msg.GetBrandId(), nil, "", pre)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&tenantv1.SetBrandLogoResponse{Brand: brandToProto(b)}), nil
}

func (h *Handler) ListBrands(ctx context.Context, req *connect.Request[tenantv1.ListBrandsRequest]) (*connect.Response[tenantv1.ListBrandsResponse], error) {
	limit, offset := pageParams(req.Msg.GetPage())
	brands, total, err := h.uc.ListBrands(ctx, req.Msg.GetOwnerId(), limit, offset)
	if err != nil {
		return nil, toConnect(err)
	}
	out := make([]*tenantv1.Brand, 0, len(brands))
	for _, b := range brands {
		out = append(out, brandToProto(b))
	}
	return connect.NewResponse(&tenantv1.ListBrandsResponse{
		Brands: out,
		Page:   pageResponse(total, offset+len(brands)),
	}), nil
}

// --- Restaurant ---

func (h *Handler) CreateRestaurant(ctx context.Context, req *connect.Request[tenantv1.CreateRestaurantRequest]) (*connect.Response[tenantv1.CreateRestaurantResponse], error) {
	r, err := h.uc.CreateRestaurant(ctx, app.CreateRestaurantInput{
		OwnerID:  ownerFromCtx(ctx),
		BrandID:  req.Msg.GetBrandId(),
		Name:     req.Msg.GetName(),
		Address:  req.Msg.GetAddress(),
		Timezone: req.Msg.GetTimezone(),
		GSTIN:    req.Msg.GetGstin(),
	})
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&tenantv1.CreateRestaurantResponse{Restaurant: restaurantToProto(r)}), nil
}

func (h *Handler) ListRestaurants(ctx context.Context, req *connect.Request[tenantv1.ListRestaurantsRequest]) (*connect.Response[tenantv1.ListRestaurantsResponse], error) {
	limit, offset := pageParams(req.Msg.GetPage())
	list, total, err := h.uc.ListRestaurants(ctx, ownerFromCtx(ctx), req.Msg.GetBrandId(), limit, offset)
	if err != nil {
		return nil, toConnect(err)
	}
	out := make([]*tenantv1.Restaurant, 0, len(list))
	for _, r := range list {
		out = append(out, restaurantToProto(r))
	}
	return connect.NewResponse(&tenantv1.ListRestaurantsResponse{
		Restaurants: out,
		Page:        pageResponse(total, offset+len(list)),
	}), nil
}

func (h *Handler) SetRestaurantActive(ctx context.Context, req *connect.Request[tenantv1.SetRestaurantActiveRequest]) (*connect.Response[tenantv1.SetRestaurantActiveResponse], error) {
	r, err := h.uc.SetRestaurantActive(ctx, ownerFromCtx(ctx), req.Msg.GetRestaurantId(), req.Msg.GetActive())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&tenantv1.SetRestaurantActiveResponse{Restaurant: restaurantToProto(r)}), nil
}

// --- mapping helpers ---

func ownerToProto(o domain.Owner) *tenantv1.Owner {
	return &tenantv1.Owner{
		Id:        o.ID,
		Name:      o.Name,
		LegalName: o.LegalName,
		Country:   o.Country,
		CreatedAt: o.CreatedAt.UTC().Format(rfc3339),
	}
}

func brandToProto(b domain.Brand) *tenantv1.Brand {
	return &tenantv1.Brand{
		Id:           b.ID,
		OwnerId:      b.OwnerID,
		Name:         b.Name,
		Logo:         assetToProto(b.Logo),
		PrimaryColor: b.PrimaryColor,
		CreatedAt:    b.CreatedAt.UTC().Format(rfc3339),
	}
}

func restaurantToProto(r domain.Restaurant) *tenantv1.Restaurant {
	return &tenantv1.Restaurant{
		Id:        r.ID,
		BrandId:   r.BrandID,
		OwnerId:   r.OwnerID,
		Name:      r.Name,
		Address:   r.Address,
		Timezone:  r.Timezone,
		Gstin:     r.GSTIN,
		Logo:      assetToProto(r.Logo),
		Active:    r.Active,
		CreatedAt: r.CreatedAt.UTC().Format(rfc3339),
	}
}

func assetToProto(a *domain.Asset) *commonv1.Asset {
	if a == nil {
		return nil
	}
	return &commonv1.Asset{Id: a.ID, Url: a.URL, ContentType: a.ContentType}
}

const rfc3339 = "2006-01-02T15:04:05Z07:00"

func pageParams(p *commonv1.PageRequest) (limit, offset int) {
	if p == nil {
		return 0, 0
	}
	limit = int(p.GetPageSize())
	if tok := p.GetPageToken(); tok != "" {
		if n, err := strconv.Atoi(tok); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}

func pageResponse(total, nextOffset int) *commonv1.PageResponse {
	next := ""
	if nextOffset < total {
		next = strconv.Itoa(nextOffset)
	}
	return &commonv1.PageResponse{NextPageToken: next, Total: int32(total)}
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
	case errors.Is(err, domain.ErrQuotaExceeded):
		return connect.NewError(connect.CodeResourceExhausted, err)
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case isAlreadyExists(err):
		return connect.NewError(connect.CodeAlreadyExists, err)
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
