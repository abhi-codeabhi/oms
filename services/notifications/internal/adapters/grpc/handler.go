// Package grpc is the Connect handler for NotificationsService. It maps proto
// requests to app use cases, app/domain types back to proto, and domain errors to
// Connect codes. No business logic lives here (CONVENTIONS.md: map only).
package grpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	notificationsv1 "github.com/restorna/platform/gen/go/restorna/notifications/v1"
	"github.com/restorna/platform/gen/go/restorna/notifications/v1/notificationsv1connect"
	pkgerrors "github.com/restorna/platform/pkg/errors"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/notifications/internal/app"
	"github.com/restorna/platform/services/notifications/internal/domain"
)

// Handler adapts *app.App to the generated NotificationsServiceHandler interface.
type Handler struct {
	notificationsv1connect.UnimplementedNotificationsServiceHandler
	uc *app.App
}

var _ notificationsv1connect.NotificationsServiceHandler = (*Handler)(nil)

// New builds a Connect handler around the use-case app.
func New(uc *app.App) *Handler { return &Handler{uc: uc} }

// Send renders + dispatches a message (idempotent by idempotency_key).
func (h *Handler) Send(ctx context.Context, req *connect.Request[notificationsv1.SendRequest]) (*connect.Response[notificationsv1.SendResponse], error) {
	msg, err := h.uc.Send(ctx, app.SendInput{
		OwnerID:        ownerFromCtx(ctx),
		RestaurantID:   restaurantFromCtx(ctx),
		Channel:        channelFromProto(req.Msg.GetChannel()),
		To:             req.Msg.GetTo(),
		TemplateID:     req.Msg.GetTemplate(),
		Vars:           req.Msg.GetVars(),
		IdempotencyKey: req.Msg.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&notificationsv1.SendResponse{Message: messageToProto(msg)}), nil
}

// GetStatus returns a message's current delivery status.
func (h *Handler) GetStatus(ctx context.Context, req *connect.Request[notificationsv1.GetStatusRequest]) (*connect.Response[notificationsv1.GetStatusResponse], error) {
	msg, err := h.uc.GetStatus(ctx, ownerFromCtx(ctx), req.Msg.GetMessageId())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&notificationsv1.GetStatusResponse{Message: messageToProto(msg)}), nil
}

// UpsertTemplate creates/replaces owner-configurable copy for a channel.
func (h *Handler) UpsertTemplate(ctx context.Context, req *connect.Request[notificationsv1.UpsertTemplateRequest]) (*connect.Response[notificationsv1.UpsertTemplateResponse], error) {
	t, err := h.uc.UpsertTemplate(ctx, app.UpsertTemplateInput{
		OwnerID: ownerFromCtx(ctx),
		ID:      req.Msg.GetId(),
		Channel: channelFromProto(req.Msg.GetChannel()),
		Subject: req.Msg.GetSubject(),
		Body:    req.Msg.GetBody(),
	})
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&notificationsv1.UpsertTemplateResponse{Id: t.ID}), nil
}

// ListTemplates returns the owner's templates.
func (h *Handler) ListTemplates(ctx context.Context, _ *connect.Request[notificationsv1.ListTemplatesRequest]) (*connect.Response[notificationsv1.ListTemplatesResponse], error) {
	list, err := h.uc.ListTemplates(ctx, ownerFromCtx(ctx))
	if err != nil {
		return nil, toConnect(err)
	}
	out := make([]*notificationsv1.UpsertTemplateRequest, 0, len(list))
	for _, t := range list {
		out = append(out, &notificationsv1.UpsertTemplateRequest{
			Id:      t.ID,
			Channel: channelToProto(t.Channel),
			Subject: t.Subject,
			Body:    t.Body,
		})
	}
	return connect.NewResponse(&notificationsv1.ListTemplatesResponse{Templates: out}), nil
}

// --- mapping helpers ---

func messageToProto(m domain.Message) *notificationsv1.Message {
	return &notificationsv1.Message{
		Id:        m.ID,
		Channel:   channelToProto(m.Channel),
		To:        m.To,
		Template:  m.TemplateID,
		Vars:      m.Vars,
		Status:    statusToProto(m.Status),
		CreatedAt: m.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

func channelFromProto(c notificationsv1.Channel) domain.Channel {
	switch c {
	case notificationsv1.Channel_SMS:
		return domain.ChannelSMS
	case notificationsv1.Channel_WHATSAPP:
		return domain.ChannelWhatsApp
	case notificationsv1.Channel_EMAIL:
		return domain.ChannelEmail
	case notificationsv1.Channel_PUSH:
		return domain.ChannelPush
	default:
		return domain.ChannelUnspecified
	}
}

func channelToProto(c domain.Channel) notificationsv1.Channel {
	switch c {
	case domain.ChannelSMS:
		return notificationsv1.Channel_SMS
	case domain.ChannelWhatsApp:
		return notificationsv1.Channel_WHATSAPP
	case domain.ChannelEmail:
		return notificationsv1.Channel_EMAIL
	case domain.ChannelPush:
		return notificationsv1.Channel_PUSH
	default:
		return notificationsv1.Channel_CHANNEL_UNSPECIFIED
	}
}

func statusToProto(s domain.DeliveryStatus) notificationsv1.DeliveryStatus {
	switch s {
	case domain.StatusQueued:
		return notificationsv1.DeliveryStatus_QUEUED
	case domain.StatusSent:
		return notificationsv1.DeliveryStatus_SENT
	case domain.StatusDelivered:
		return notificationsv1.DeliveryStatus_DELIVERED
	case domain.StatusFailed:
		return notificationsv1.DeliveryStatus_FAILED
	default:
		return notificationsv1.DeliveryStatus_DELIVERY_UNSPECIFIED
	}
}

// toConnect maps domain/app errors to Connect codes (the ONLY place this happens).
func toConnect(err error) error {
	switch {
	case errors.Is(err, tenancy.ErrPermissionDenied):
		return connect.NewError(connect.CodePermissionDenied, err)
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

// ownerFromCtx reads the JWT-derived owner id set by the auth interceptor. The owner
// id ALWAYS comes from the auth context, never the request body.
func ownerFromCtx(ctx context.Context) string {
	if s, ok := tenancy.From(ctx); ok {
		return s.OwnerID
	}
	return ""
}

func restaurantFromCtx(ctx context.Context) string {
	if s, ok := tenancy.From(ctx); ok {
		return s.RestaurantID
	}
	return ""
}
