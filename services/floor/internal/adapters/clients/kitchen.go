// Package clients holds the generated Connect clients to OTHER services, each
// adapted to a floor app port. This file: KitchenService -> ports.KitchenBoard.
// The floor reads the cook board (cooking tickets) + serve queue (ready, unserved)
// to DERIVE per-table status. This is the only place that knows about the
// generated kitchen client; the app stays infra-free.
package clients

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	kitchenv1 "github.com/restorna/platform/gen/go/restorna/kitchen/v1"
	"github.com/restorna/platform/gen/go/restorna/kitchen/v1/kitchenv1connect"
	"github.com/restorna/platform/services/floor/internal/ports"
)

// KitchenClient implements ports.KitchenBoard over a Connect KitchenService client.
type KitchenClient struct {
	svc kitchenv1connect.KitchenServiceClient
}

var _ ports.KitchenBoard = (*KitchenClient)(nil)

// NewKitchen builds a KitchenClient talking to the kitchen service at baseURL.
func NewKitchen(httpClient *http.Client, baseURL string, opts ...connect.ClientOption) *KitchenClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &KitchenClient{svc: kitchenv1connect.NewKitchenServiceClient(httpClient, baseURL, opts...)}
}

// NewKitchenFromClient wraps an already-built generated client (tests/wiring).
func NewKitchenFromClient(svc kitchenv1connect.KitchenServiceClient) *KitchenClient {
	return &KitchenClient{svc: svc}
}

// Board returns the still-cooking tickets. KitchenService scopes the read to the
// caller's restaurant from the auth/internal-token context; the floor never trusts
// a body id for tenancy. The restaurantID argument is carried for symmetry.
func (c *KitchenClient) Board(ctx context.Context, _ string) ([]ports.KitchenTicket, error) {
	resp, err := c.svc.GetBoard(ctx, connect.NewRequest(&kitchenv1.GetBoardRequest{}))
	if err != nil {
		return nil, err
	}
	return ticketsToPorts(resp.Msg.GetTickets()), nil
}

// ServeQueue returns the ready-but-unserved tickets.
func (c *KitchenClient) ServeQueue(ctx context.Context, _ string) ([]ports.KitchenTicket, error) {
	resp, err := c.svc.ServeQueue(ctx, connect.NewRequest(&kitchenv1.ServeQueueRequest{}))
	if err != nil {
		return nil, err
	}
	return ticketsToPorts(resp.Msg.GetTickets()), nil
}

func ticketsToPorts(in []*kitchenv1.Ticket) []ports.KitchenTicket {
	out := make([]ports.KitchenTicket, 0, len(in))
	for _, t := range in {
		out = append(out, ports.KitchenTicket{Table: t.GetTable()})
	}
	return out
}
