package bff_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	identityv1 "github.com/restorna/platform/gen/go/restorna/identity/v1"
	staffv1 "github.com/restorna/platform/gen/go/restorna/staff/v1"
	"github.com/restorna/platform/pkg/auth"
	"github.com/restorna/platform/services/gateway/internal/bff"
	"github.com/restorna/platform/services/gateway/internal/clients"
	"github.com/restorna/platform/services/gateway/internal/middleware"
)

// newServer builds a gateway under test: a fake client Set behind the real router +
// auth middleware, served via httptest. Returns the server and the signing key so a
// test can mint tokens with a chosen role.
func newServer(t *testing.T, set *clients.Set) (*httptest.Server, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	authmw := middleware.NewAuth(string(pub))
	mux := http.NewServeMux()
	bff.NewRouter(bff.New(set), authmw).Mount(mux)
	return httptest.NewServer(mux), priv
}

func bearer(t *testing.T, priv ed25519.PrivateKey, role commonv1.Role) string {
	t.Helper()
	tok, err := auth.Sign(priv, auth.Claims{UserID: "usr_1", Role: role, Owner: "own_1", Restaurant: "out_1"}, time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return tok
}

// --- /api/auth/start-otp : public, maps JSON -> identity, projects response ---

func TestStartOTP(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		fake        *fakeIdentity
		wantStatus  int
		wantChannel identityv1.Channel
		wantBodyHas string
	}{
		{
			name:        "happy path returns challenge id",
			body:        `{"channel":"email","address":"a@b.com","realm":"tenant"}`,
			fake:        &fakeIdentity{startOtpResp: &identityv1.StartOtpResponse{ChallengeId: "chl_1"}},
			wantStatus:  http.StatusOK,
			wantChannel: identityv1.Channel_CHANNEL_EMAIL,
			wantBodyHas: `"challenge_id":"chl_1"`,
		},
		{
			name:        "downstream error maps to status",
			body:        `{"channel":"phone","address":"+91","realm":"tenant"}`,
			fake:        &fakeIdentity{startOtpErr: connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("bad address"))},
			wantStatus:  http.StatusBadRequest,
			wantChannel: identityv1.Channel_CHANNEL_PHONE,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newServer(t, &clients.Set{Identity: tc.fake})
			defer srv.Close()

			resp, err := http.Post(srv.URL+"/api/auth/start-otp", "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status: got %d want %d", resp.StatusCode, tc.wantStatus)
			}
			if tc.fake.gotStartOtp.GetChannel() != tc.wantChannel {
				t.Fatalf("channel mapped wrong: got %v want %v", tc.fake.gotStartOtp.GetChannel(), tc.wantChannel)
			}
			if tc.wantBodyHas != "" {
				var sb strings.Builder
				_, _ = sbCopy(&sb, resp)
				if !strings.Contains(sb.String(), tc.wantBodyHas) {
					t.Fatalf("body %q missing %q", sb.String(), tc.wantBodyHas)
				}
			}
		})
	}
}

// --- /api/manager/staff : role-gated, maps + projects list ---

func TestManagerListStaff(t *testing.T) {
	tests := []struct {
		name       string
		role       commonv1.Role
		query      string
		fake       *fakeStaff
		wantStatus int
		wantCount  int
	}{
		{
			name: "manager lists staff",
			role: commonv1.Role_ROLE_MANAGER,
			query: "?restaurant_id=out_1",
			fake: &fakeStaff{listResp: &staffv1.ListStaffResponse{Members: []*staffv1.StaffMember{
				staffMember("stf_1", "Asha", commonv1.Role_ROLE_WAITER, true),
				staffMember("stf_2", "Ravi", commonv1.Role_ROLE_KITCHEN, true),
			}}},
			wantStatus: http.StatusOK,
			wantCount:  2,
		},
		{
			name:       "missing restaurant_id is 400 before any client call",
			role:       commonv1.Role_ROLE_MANAGER,
			query:      "",
			fake:       &fakeStaff{},
			wantStatus: http.StatusBadRequest,
			wantCount:  0,
		},
		{
			name:       "waiter is forbidden by the role gate",
			role:       commonv1.Role_ROLE_WAITER,
			query:      "?restaurant_id=out_1",
			fake:       &fakeStaff{listResp: &staffv1.ListStaffResponse{}},
			wantStatus: http.StatusForbidden,
			wantCount:  0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, priv := newServer(t, &clients.Set{Staff: tc.fake})
			defer srv.Close()

			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/manager/staff"+tc.query, nil)
			req.Header.Set("Authorization", "Bearer "+bearer(t, priv, tc.role))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status: got %d want %d", resp.StatusCode, tc.wantStatus)
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			var out struct {
				Members []map[string]any `json:"members"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(out.Members) != tc.wantCount {
				t.Fatalf("members: got %d want %d", len(out.Members), tc.wantCount)
			}
			if tc.wantCount > 0 && out.Members[0]["role"] != "ROLE_WAITER" {
				t.Fatalf("role projection wrong: %v", out.Members[0]["role"])
			}
		})
	}
}

// AddStaff: proves the JSON body maps to the proto request (role + fields).
func TestManagerAddStaffMapping(t *testing.T) {
	fake := &fakeStaff{addResp: &staffv1.AddStaffResponse{Member: staffMember("stf_9", "Neha", commonv1.Role_ROLE_WAITER, true)}}
	srv, priv := newServer(t, &clients.Set{Staff: fake})
	defer srv.Close()

	body := `{"restaurant_id":"out_1","name":"Neha","email":"n@x.com","phone":"+91","role":"waiter"}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/manager/staff", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+bearer(t, priv, commonv1.Role_ROLE_OWNER)) // owner allowed too
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	if fake.gotAdd.GetRestaurantId() != "out_1" || fake.gotAdd.GetName() != "Neha" ||
		fake.gotAdd.GetRole() != commonv1.Role_ROLE_WAITER {
		t.Fatalf("request not mapped: %+v", fake.gotAdd)
	}
}

// sbCopy copies an http response body into b (small helper to avoid io import churn).
func sbCopy(b *strings.Builder, resp *http.Response) (int, error) {
	buf := make([]byte, 4096)
	total := 0
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
			total += n
		}
		if err != nil {
			return total, nil
		}
	}
}
