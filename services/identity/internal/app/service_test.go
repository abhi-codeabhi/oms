package app

import (
	"context"
	"strings"
	"testing"
	"time"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	identityv1 "github.com/restorna/platform/gen/go/restorna/identity/v1"
	"github.com/restorna/platform/services/identity/internal/domain"
)

// --- in-memory fakes for the ports ---

type fakeUsers struct {
	byID   map[string]domain.User
	byAddr map[string]domain.User // key: realm|address
}

func newFakeUsers() *fakeUsers {
	return &fakeUsers{byID: map[string]domain.User{}, byAddr: map[string]domain.User{}}
}
func addrKey(r domain.Realm, a string) string { return r.String() + "|" + a }

func (f *fakeUsers) FindByAddress(_ context.Context, realm domain.Realm, address string) (domain.User, error) {
	if u, ok := f.byAddr[addrKey(realm, address)]; ok {
		return u, nil
	}
	return domain.User{}, domain.ErrUserNotFound
}
func (f *fakeUsers) FindByID(_ context.Context, id string) (domain.User, error) {
	if u, ok := f.byID[id]; ok {
		return u, nil
	}
	return domain.User{}, domain.ErrUserNotFound
}
func (f *fakeUsers) Create(_ context.Context, u domain.User) error {
	f.byID[u.ID] = u
	addr := u.Email
	if addr == "" {
		addr = u.Phone
	}
	f.byAddr[addrKey(u.Realm, addr)] = u
	return nil
}

type fakeChallenges struct{ m map[string]domain.OtpChallenge }

func newFakeChallenges() *fakeChallenges { return &fakeChallenges{m: map[string]domain.OtpChallenge{}} }
func (f *fakeChallenges) Save(_ context.Context, c domain.OtpChallenge) error {
	f.m[c.ID] = c
	return nil
}
func (f *fakeChallenges) Get(_ context.Context, id string) (domain.OtpChallenge, error) {
	if c, ok := f.m[id]; ok {
		return c, nil
	}
	return domain.OtpChallenge{}, domain.ErrChallengeNotFound
}
func (f *fakeChallenges) Update(_ context.Context, c domain.OtpChallenge) error {
	f.m[c.ID] = c
	return nil
}

type fakeRefresh struct{ byHash map[string]domain.RefreshToken }

func newFakeRefresh() *fakeRefresh { return &fakeRefresh{byHash: map[string]domain.RefreshToken{}} }
func (f *fakeRefresh) Save(_ context.Context, t domain.RefreshToken) error {
	f.byHash[t.TokenHash] = t
	return nil
}
func (f *fakeRefresh) GetByHash(_ context.Context, hash string) (domain.RefreshToken, error) {
	if t, ok := f.byHash[hash]; ok {
		return t, nil
	}
	return domain.RefreshToken{}, domain.ErrChallengeNotFound
}
func (f *fakeRefresh) Revoke(_ context.Context, id string) error {
	for k, t := range f.byHash {
		if t.ID == id {
			t.Revoked = true
			f.byHash[k] = t
		}
	}
	return nil
}

type fakeSender struct {
	lastCode string
	calls    int
}

func (f *fakeSender) Send(_ context.Context, _ domain.Channel, _, code string) error {
	f.lastCode = code
	f.calls++
	return nil
}

// fakeSigner encodes the grant into a readable token so tests can assert the
// claims without real crypto. Format: "userid|role|owner|brand|restaurant".
type fakeSigner struct{}

func (fakeSigner) Sign(g domain.TokenGrant, _ time.Duration) (string, error) {
	return strings.Join([]string{
		g.UserID, g.Role.String(),
		g.Scope.OwnerID, g.Scope.BrandID, g.Scope.RestaurantID,
	}, "|"), nil
}
func (fakeSigner) Verify(token string) (domain.TokenGrant, error) {
	p := strings.Split(token, "|")
	if len(p) != 5 {
		return domain.TokenGrant{}, domain.ErrInvalidAddress
	}
	return domain.TokenGrant{
		UserID: p[0],
		Role:   commonv1.Role(commonv1.Role_value[p[1]]),
		Scope: domain.TenantScope{
			OwnerID: p[2], BrandID: p[3], RestaurantID: p[4],
		},
	}, nil
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func newService(devMode bool) (*Service, *fakeUsers, *fakeChallenges, *fakeRefresh, *fakeSender) {
	users := newFakeUsers()
	chs := newFakeChallenges()
	rts := newFakeRefresh()
	snd := &fakeSender{}
	svc := New(Config{
		Users: users, Challenges: chs, Refresh: rts, Sender: snd,
		Signer: fakeSigner{},
		Clock:  fixedClock{t: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)},
		DevMode: devMode,
	})
	return svc, users, chs, rts, snd
}

func TestStartOtp(t *testing.T) {
	tests := []struct {
		name    string
		channel domain.Channel
		address string
		wantErr bool
	}{
		{"phone ok", identityv1.Channel_CHANNEL_PHONE, "+15551112222", false},
		{"email ok", identityv1.Channel_CHANNEL_EMAIL, "x@y.com", false},
		{"empty address", identityv1.Channel_CHANNEL_PHONE, "", true},
		{"bad channel", identityv1.Channel_CHANNEL_UNSPECIFIED, "x@y.com", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, chs, _, snd := newService(false)
			id, err := svc.StartOtp(context.Background(), tc.channel, tc.address, identityv1.Realm_REALM_TENANT)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if id == "" {
				t.Fatal("empty challenge id")
			}
			ch, _ := chs.Get(context.Background(), id)
			if ch.Code != snd.lastCode || len(snd.lastCode) != 6 {
				t.Errorf("sender code %q != stored %q (want 6 digits)", snd.lastCode, ch.Code)
			}
		})
	}
}

func TestVerifyOtp_IssuesTokensAndRegistersUser(t *testing.T) {
	svc, users, _, rts, snd := newService(false)
	ctx := context.Background()

	id, err := svc.StartOtp(ctx, identityv1.Channel_CHANNEL_PHONE, "+15550001111", identityv1.Realm_REALM_TENANT)
	if err != nil {
		t.Fatal(err)
	}

	// wrong code first
	if _, _, err := svc.VerifyOtp(ctx, id, "000000"); err != domain.ErrCodeMismatch {
		t.Fatalf("wrong code err = %v, want ErrCodeMismatch", err)
	}

	pair, user, err := svc.VerifyOtp(ctx, id, snd.lastCode)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatal("empty tokens")
	}
	if pair.ExpiresIn != int64(domain.AccessTTL.Seconds()) {
		t.Errorf("expires_in = %d", pair.ExpiresIn)
	}
	// auto-registered user with default tenant role (owner)
	if !strings.HasPrefix(user.ID, "usr_") {
		t.Errorf("user id = %q", user.ID)
	}
	if _, ok := users.byID[user.ID]; !ok {
		t.Error("user not persisted")
	}
	// access token carries the unscoped owner role
	g, _ := fakeSigner{}.Verify(pair.AccessToken)
	if g.Role != commonv1.Role_ROLE_OWNER {
		t.Errorf("role = %v, want ROLE_OWNER", g.Role)
	}
	// refresh token stored by hash
	if len(rts.byHash) != 1 {
		t.Errorf("refresh tokens stored = %d, want 1", len(rts.byHash))
	}

	// re-verifying a consumed challenge fails
	if _, _, err := svc.VerifyOtp(ctx, id, snd.lastCode); err != domain.ErrChallengeConsumed {
		t.Errorf("re-verify err = %v, want ErrChallengeConsumed", err)
	}
}

func TestVerifyOtp_DevCode(t *testing.T) {
	svc, _, _, _, _ := newService(true) // dev mode
	ctx := context.Background()
	id, _ := svc.StartOtp(ctx, identityv1.Channel_CHANNEL_EMAIL, "dev@r.com", identityv1.Realm_REALM_TENANT)
	if _, _, err := svc.VerifyOtp(ctx, id, domain.DevCode); err != nil {
		t.Fatalf("dev code should verify: %v", err)
	}
}

func TestRefresh_RotatesAndPreservesGrant(t *testing.T) {
	svc, _, _, rts, snd := newService(false)
	ctx := context.Background()
	id, _ := svc.StartOtp(ctx, identityv1.Channel_CHANNEL_PHONE, "+15559998888", identityv1.Realm_REALM_PLATFORM)
	pair, _, err := svc.VerifyOtp(ctx, id, snd.lastCode)
	if err != nil {
		t.Fatal(err)
	}

	newPair, err := svc.Refresh(ctx, pair.RefreshToken)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if newPair.RefreshToken == pair.RefreshToken {
		t.Error("refresh token not rotated")
	}
	// platform login => platform_admin role preserved across refresh
	g, _ := fakeSigner{}.Verify(newPair.AccessToken)
	if g.Role != commonv1.Role_ROLE_PLATFORM_ADMIN {
		t.Errorf("role = %v, want PLATFORM_ADMIN", g.Role)
	}
	// old refresh token revoked -> reuse rejected
	if _, err := svc.Refresh(ctx, pair.RefreshToken); err == nil {
		t.Error("reused refresh token should fail")
	}
	_ = rts
}

func TestIssueScopedToken_Claims(t *testing.T) {
	svc, users, _, _, _ := newService(false)
	ctx := context.Background()
	u := domain.NewUser("usr_abc", "owner@r.com", identityv1.Channel_CHANNEL_EMAIL, identityv1.Realm_REALM_TENANT)
	_ = users.Create(ctx, u)

	scope := domain.TenantScope{OwnerID: "own_1", BrandID: "brnd_2", RestaurantID: "out_3"}
	pair, err := svc.IssueScopedToken(ctx, "usr_abc", scope, commonv1.Role_ROLE_MANAGER)
	if err != nil {
		t.Fatalf("issue scoped: %v", err)
	}
	g, _ := fakeSigner{}.Verify(pair.AccessToken)
	if g.UserID != "usr_abc" {
		t.Errorf("sub = %q", g.UserID)
	}
	if g.Role != commonv1.Role_ROLE_MANAGER {
		t.Errorf("role = %v, want MANAGER", g.Role)
	}
	if g.Scope != scope {
		t.Errorf("scope = %+v, want %+v", g.Scope, scope)
	}

	// unknown user rejected
	if _, err := svc.IssueScopedToken(ctx, "usr_missing", scope, commonv1.Role_ROLE_MANAGER); err != domain.ErrUserNotFound {
		t.Errorf("missing user err = %v", err)
	}
}

func TestCustomerSession_RoleAndScope(t *testing.T) {
	svc, _, _, _, _ := newService(false)
	ctx := context.Background()
	pair, err := svc.CustomerSession(ctx, "out_42", "T7")
	if err != nil {
		t.Fatalf("customer session: %v", err)
	}
	g, _ := fakeSigner{}.Verify(pair.AccessToken)
	if g.Role != commonv1.Role_ROLE_CUSTOMER {
		t.Errorf("role = %v, want ROLE_CUSTOMER", g.Role)
	}
	if g.Scope.RestaurantID != "out_42" {
		t.Errorf("restaurant = %q, want out_42", g.Scope.RestaurantID)
	}
	if !strings.HasPrefix(g.UserID, "cst_") {
		t.Errorf("customer subject = %q, want cst_ prefix", g.UserID)
	}
	// missing restaurant rejected
	if _, err := svc.CustomerSession(ctx, "", "T1"); err != domain.ErrInvalidAddress {
		t.Errorf("missing restaurant err = %v", err)
	}
}

func TestIntrospect(t *testing.T) {
	svc, _, _, _, snd := newService(false)
	ctx := context.Background()
	id, _ := svc.StartOtp(ctx, identityv1.Channel_CHANNEL_EMAIL, "i@r.com", identityv1.Realm_REALM_TENANT)
	pair, _, _ := svc.VerifyOtp(ctx, id, snd.lastCode)

	res, err := svc.Introspect(ctx, pair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Active {
		t.Error("token should be active")
	}
	if res.Role != commonv1.Role_ROLE_OWNER {
		t.Errorf("role = %v", res.Role)
	}

	// garbage token => inactive, no error
	bad, err := svc.Introspect(ctx, "not-a-token")
	if err != nil {
		t.Fatalf("introspect bad token err = %v", err)
	}
	if bad.Active {
		t.Error("garbage token should be inactive")
	}
}
