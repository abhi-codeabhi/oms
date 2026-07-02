// Package grpc is the Connect handler for CatalogService. It maps proto requests to
// app use cases, app/domain types back to proto, and domain errors to Connect
// codes. No business logic lives here (CONVENTIONS.md: map only). The trusted
// tenant (restaurant_id) ALWAYS comes from the auth context, never the body.
package grpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	catalogv1 "github.com/restorna/platform/gen/go/restorna/catalog/v1"
	"github.com/restorna/platform/gen/go/restorna/catalog/v1/catalogv1connect"
	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	pkgerrors "github.com/restorna/platform/pkg/errors"
	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/catalog/internal/app"
	"github.com/restorna/platform/services/catalog/internal/domain"
)

// Handler adapts *app.App to the generated CatalogServiceHandler interface.
type Handler struct {
	catalogv1connect.UnimplementedCatalogServiceHandler
	uc *app.App
}

var _ catalogv1connect.CatalogServiceHandler = (*Handler)(nil)

// New builds a Connect handler around the use-case app.
func New(uc *app.App) *Handler { return &Handler{uc: uc} }

// --- Categories ---

func (h *Handler) UpsertCategory(ctx context.Context, req *connect.Request[catalogv1.UpsertCategoryRequest]) (*connect.Response[catalogv1.UpsertCategoryResponse], error) {
	c := req.Msg.GetCategory()
	out, err := h.uc.UpsertCategory(ctx, restaurantFromCtx(ctx), c.GetId(), c.GetName(), c.GetSort())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&catalogv1.UpsertCategoryResponse{Category: categoryToProto(out)}), nil
}

func (h *Handler) ListCategories(ctx context.Context, _ *connect.Request[catalogv1.ListCategoriesRequest]) (*connect.Response[catalogv1.ListCategoriesResponse], error) {
	cats, err := h.uc.ListCategories(ctx, restaurantFromCtx(ctx))
	if err != nil {
		return nil, toConnect(err)
	}
	out := make([]*catalogv1.Category, 0, len(cats))
	for _, c := range cats {
		out = append(out, categoryToProto(c))
	}
	return connect.NewResponse(&catalogv1.ListCategoriesResponse{Categories: out}), nil
}

// --- Items ---

func (h *Handler) UpsertItem(ctx context.Context, req *connect.Request[catalogv1.UpsertItemRequest]) (*connect.Response[catalogv1.UpsertItemResponse], error) {
	it := req.Msg.GetItem()
	out, err := h.uc.UpsertItem(ctx, restaurantFromCtx(ctx), app.UpsertItemInput{
		ID:          it.GetId(),
		CategoryID:  it.GetCategoryId(),
		Name:        it.GetName(),
		Description: it.GetDescription(),
		Price:       moneyFromProto(it.GetPrice()),
		Veg:         it.GetVeg(),
		Tags:        it.GetTags(),
		PrepMinutes: it.GetPrepMinutes(),
		Station:     it.GetStation(),
		Image:       assetFromProto(it.GetImage()),
	})
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&catalogv1.UpsertItemResponse{Item: itemToProto(out)}), nil
}

func (h *Handler) GetItem(ctx context.Context, req *connect.Request[catalogv1.GetItemRequest]) (*connect.Response[catalogv1.GetItemResponse], error) {
	out, err := h.uc.GetItem(ctx, restaurantFromCtx(ctx), req.Msg.GetItemId())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&catalogv1.GetItemResponse{Item: itemToProto(out)}), nil
}

func (h *Handler) GetMenu(ctx context.Context, req *connect.Request[catalogv1.GetMenuRequest]) (*connect.Response[catalogv1.GetMenuResponse], error) {
	// only_available defaults to true for a customer menu unless explicitly cleared.
	onlyAvailable := req.Msg.GetOnlyAvailable()
	evaluated, err := h.uc.GetMenu(ctx, restaurantFromCtx(ctx), req.Msg.GetPrefs(), onlyAvailable)
	if err != nil {
		return nil, toConnect(err)
	}
	out := make([]*catalogv1.Item, 0, len(evaluated))
	for _, e := range evaluated {
		out = append(out, itemToProto(e.Item))
	}
	return connect.NewResponse(&catalogv1.GetMenuResponse{Items: out}), nil
}

func (h *Handler) ListAllItems(ctx context.Context, _ *connect.Request[catalogv1.ListAllItemsRequest]) (*connect.Response[catalogv1.ListAllItemsResponse], error) {
	items, err := h.uc.ListAllItems(ctx, restaurantFromCtx(ctx))
	if err != nil {
		return nil, toConnect(err)
	}
	out := make([]*catalogv1.Item, 0, len(items))
	for _, it := range items {
		out = append(out, itemToProto(it))
	}
	return connect.NewResponse(&catalogv1.ListAllItemsResponse{Items: out}), nil
}

func (h *Handler) SetAvailability(ctx context.Context, req *connect.Request[catalogv1.SetAvailabilityRequest]) (*connect.Response[catalogv1.SetAvailabilityResponse], error) {
	out, err := h.uc.SetAvailability(ctx, restaurantFromCtx(ctx), req.Msg.GetItemId(), req.Msg.GetAvailable())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&catalogv1.SetAvailabilityResponse{Item: itemToProto(out)}), nil
}

func (h *Handler) SetOutletOverride(ctx context.Context, req *connect.Request[catalogv1.SetOutletOverrideRequest]) (*connect.Response[catalogv1.SetOutletOverrideResponse], error) {
	var price *money.Money
	if p := req.Msg.GetPrice(); p != nil {
		m := moneyFromProto(p)
		price = &m
	}
	out, err := h.uc.SetOutletOverride(ctx, restaurantFromCtx(ctx), req.Msg.GetItemId(), price, req.Msg.GetAvailable(), req.Msg.GetClear())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&catalogv1.SetOutletOverrideResponse{Item: itemToProto(out)}), nil
}

// --- mapping helpers ---

func categoryToProto(c domain.Category) *catalogv1.Category {
	return &catalogv1.Category{Id: c.ID, Name: c.Name, Sort: c.Sort}
}

func itemToProto(it domain.Item) *catalogv1.Item {
	return &catalogv1.Item{
		Id:          it.ID,
		CategoryId:  it.CategoryID,
		Name:        it.Name,
		Description: it.Description,
		Price:       moneyToProto(it.Price),
		Veg:         it.Veg,
		Tags:        it.Tags,
		PrepMinutes: it.PrepMinutes,
		Available:   it.Available,
		Image:       assetToProto(it.Image),
		Station:     it.Station,
	}
}

func moneyToProto(m money.Money) *commonv1.Money {
	return &commonv1.Money{Minor: m.Minor, Currency: m.Currency}
}

func moneyFromProto(m *commonv1.Money) money.Money {
	if m == nil {
		return money.Money{}
	}
	return money.New(m.GetMinor(), m.GetCurrency())
}

func assetToProto(a *domain.Asset) *commonv1.Asset {
	if a == nil {
		return nil
	}
	return &commonv1.Asset{Id: a.ID, Url: a.URL, ContentType: a.ContentType}
}

func assetFromProto(a *commonv1.Asset) *domain.Asset {
	if a == nil {
		return nil
	}
	return &domain.Asset{ID: a.GetId(), URL: a.GetUrl(), ContentType: a.GetContentType()}
}

// toConnect maps domain/app errors to Connect codes (the ONLY place this happens).
func toConnect(err error) error {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, pkgerrors.ErrAlreadyExists):
		return connect.NewError(connect.CodeAlreadyExists, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

// restaurantFromCtx reads the JWT-derived tenancy scope set by the auth
// interceptor. The outlet (restaurant) id ALWAYS comes from the auth context,
// never the request body (CONVENTIONS.md multi-tenancy rule).
func restaurantFromCtx(ctx context.Context) string {
	if s, ok := tenancy.From(ctx); ok {
		return s.RestaurantID
	}
	return ""
}
