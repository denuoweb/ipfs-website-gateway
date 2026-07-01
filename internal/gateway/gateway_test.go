package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	boxogateway "github.com/ipfs/boxo/gateway"
	ipfs "go.lumeweb.com/ipfs-sdk"
	"go.lumeweb.com/ipfs-website-gateway/internal/api"
	"go.lumeweb.com/ipfs-website-gateway/internal/cache"
	"go.lumeweb.com/ipfs-website-gateway/pkg/types"
	"go.uber.org/zap"
)

type mockAPIClient struct {
	getWebsiteFunc func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error)
}

func (m *mockAPIClient) GetWebsite(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
	if m.getWebsiteFunc != nil {
		return m.getWebsiteFunc(ctx, domain)
	}
	return nil, nil
}

func (m *mockAPIClient) Ping(_ context.Context) (*ipfs.PingResponse, error) {
	return nil, fmt.Errorf("ping not implemented in test mock")
}

func newTestGateway(apiClient api.APIClient, statusCache *cache.StatusCache) (*Gateway, error) {
	return &Gateway{
		logger:      zap.NewNop(),
		api:         apiClient,
		statusCache: statusCache,
	}, nil
}

func TestCheckAccess_ActiveWebsite(t *testing.T) {
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			return &types.GatewayWebsiteResponse{
				Domain:     "example.com",
				Status:     types.StatusActive,
				TargetHash: "QmTest",
				TargetType: "ipfs",
			}, nil
		},
	}

	gw, err := newTestGateway(apiClient, nil)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	website, err := gw.CheckAccess(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("CheckAccess: %v", err)
	}
	if website.Status != types.StatusActive {
		t.Errorf("expected status %s, got %s", types.StatusActive, website.Status)
	}
}

func TestCheckAccess_BrokenWebsite(t *testing.T) {
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			return &types.GatewayWebsiteResponse{
				Domain:     "broken.com",
				Status:     types.StatusBroken,
				TargetHash: "QmBroken",
				TargetType: "ipfs",
			}, nil
		},
	}

	gw, err := newTestGateway(apiClient, nil)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	website, err := gw.CheckAccess(context.Background(), "broken.com")
	if err != nil {
		t.Fatalf("CheckAccess: %v", err)
	}
	if website.Status != types.StatusBroken {
		t.Errorf("expected status %s, got %s", types.StatusBroken, website.Status)
	}
}

func TestCheckAccess_APIDenied(t *testing.T) {
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			return nil, errors.New("not found")
		},
	}

	gw, err := newTestGateway(apiClient, nil)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	website, err := gw.CheckAccess(context.Background(), "denied.com")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if website != nil {
		t.Fatal("expected nil website, got non-nil")
	}
}

func TestCheckAccess_IPAddressRejected(t *testing.T) {
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			t.Fatal("API should not be called for IP address")
			return nil, nil
		},
	}

	gw, err := newTestGateway(apiClient, nil)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	for _, ip := range []string{"1.2.3.4", "::1", "2001:db8::1"} {
		website, err := gw.CheckAccess(context.Background(), ip)
		if !errors.Is(err, ipfs.ErrNotFound) {
			t.Errorf("IP %q: expected ErrNotFound, got err=%v website=%v", ip, err, website)
		}
		if website != nil {
			t.Errorf("IP %q: expected nil website", ip)
		}
	}
}

func TestCheckAccess_NilAPI(t *testing.T) {
	gw, err := newTestGateway(nil, nil)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	website, err := gw.CheckAccess(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("CheckAccess: %v", err)
	}
	if website != nil {
		t.Fatal("expected nil website with nil API client")
	}
}

func TestCheckAccess_CacheHit(t *testing.T) {
	called := false
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			called = true
			return &types.GatewayWebsiteResponse{
				Domain: "cached.com",
				Status: types.StatusActive,
			}, nil
		},
	}

	statusCache, err := cache.NewStatusCacheSimple(100, 5*time.Minute, 30*time.Second)
	if err != nil {
		t.Fatalf("NewStatusCache: %v", err)
	}

	gw, err := newTestGateway(apiClient, statusCache)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	website, err := gw.CheckAccess(context.Background(), "cached.com")
	if err != nil {
		t.Fatalf("CheckAccess: %v", err)
	}
	if website == nil || website.Status != types.StatusActive {
		t.Fatal("first call should return active website")
	}
	if !called {
		t.Fatal("first call should hit API")
	}

	called = false
	website, err = gw.CheckAccess(context.Background(), "cached.com")
	if err != nil {
		t.Fatalf("CheckAccess cache hit: %v", err)
	}
	if website == nil || website.Status != types.StatusActive {
		t.Fatal("cache hit should return active website")
	}
	if called {
		t.Fatal("cache hit should NOT call API")
	}
}

func TestCheckAccess_CacheInvalid(t *testing.T) {
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			return nil, errors.New("not found")
		},
	}

	statusCache, err := cache.NewStatusCacheSimple(100, 5*time.Minute, 30*time.Second)
	if err != nil {
		t.Fatalf("NewStatusCache: %v", err)
	}

	gw, err := newTestGateway(apiClient, statusCache)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	_, err = gw.CheckAccess(context.Background(), "invalid.com")
	if err == nil {
		t.Fatal("expected error for invalid domain")
	}

	result := statusCache.Get("invalid.com")
	if !result.Hit {
		t.Fatal("invalid domain should be cached")
	}
	if result.Entry.Err == nil {
		t.Fatal("cached entry for invalid domain should have error")
	}

	website, err := gw.CheckAccess(context.Background(), "invalid.com")
	if err == nil {
		t.Fatal("cached error should be replayed on second call")
	}
	if website != nil {
		t.Fatal("cached invalid domain should return nil website")
	}
}

func TestAccessControlMiddleware_ActiveDomain(t *testing.T) {
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			return &types.GatewayWebsiteResponse{
				Domain:     "example.com",
				Status:     types.StatusActive,
				TargetHash: "QmTest",
				TargetType: "ipfs",
			}, nil
		},
	}

	gw, err := newTestGateway(apiClient, nil)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ipfs/QmTest/" {
			t.Errorf("expected path /ipfs/QmTest/, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	})

	middleware, err := NewAccessControlMiddleware(gw, zap.NewNop())
	if err != nil {
		t.Fatalf("NewAccessControlMiddleware: %v", err)
	}
	handler := middleware.Wrap(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "example.com"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestAccessControlMiddleware_ActiveHNSHost(t *testing.T) {
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			if domain != "example" {
				t.Errorf("expected HNS host example, got %s", domain)
			}
			return &types.GatewayWebsiteResponse{
				Domain:     "example",
				Status:     types.StatusActive,
				TargetHash: "bafybeihns",
				TargetType: "ipfs",
			}, nil
		},
	}

	gw, err := newTestGateway(apiClient, nil)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	var receivedPath string
	var receivedCtx context.Context
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedCtx = r.Context()
		w.WriteHeader(http.StatusOK)
	})

	middleware, err := NewAccessControlMiddleware(gw, zap.NewNop())
	if err != nil {
		t.Fatalf("NewAccessControlMiddleware: %v", err)
	}
	handler := middleware.Wrap(inner)

	req := httptest.NewRequest(http.MethodGet, "/index.html", nil)
	req.Host = "example"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if receivedPath != "/ipfs/bafybeihns/index.html" {
		t.Errorf("expected /ipfs/bafybeihns/index.html, got %s", receivedPath)
	}
	dnslinkHost, ok := receivedCtx.Value(boxogateway.DNSLinkHostnameKey).(string)
	if !ok {
		t.Error("expected DNSLinkHostnameKey to be set in context")
	}
	if dnslinkHost != "example" {
		t.Errorf("expected DNSLinkHostnameKey to be 'example', got %s", dnslinkHost)
	}
}

func TestAccessControlMiddleware_BrokenDomain(t *testing.T) {
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			return &types.GatewayWebsiteResponse{
				Domain: "broken.com",
				Status: types.StatusBroken,
			}, nil
		},
	}

	gw, err := newTestGateway(apiClient, nil)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inner handler should not be called for broken domain")
	})

	middleware, err := NewAccessControlMiddleware(gw, zap.NewNop())
	if err != nil {
		t.Fatalf("NewAccessControlMiddleware: %v", err)
	}
	handler := middleware.Wrap(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "broken.com"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusGone {
		t.Errorf("expected 410, got %d", rec.Code)
	}
}

func TestAccessControlMiddleware_DeniedDomain(t *testing.T) {
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			return nil, errors.New("not found")
		},
	}

	gw, err := newTestGateway(apiClient, nil)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inner handler should not be called for denied domain")
	})

	middleware, err := NewAccessControlMiddleware(gw, zap.NewNop())
	if err != nil {
		t.Fatalf("NewAccessControlMiddleware: %v", err)
	}
	handler := middleware.Wrap(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "denied.com"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestAccessControlMiddleware_ErrNotFound(t *testing.T) {
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			return nil, fmt.Errorf("%w: website not found", ipfs.ErrNotFound)
		},
	}

	gw, err := newTestGateway(apiClient, nil)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inner handler should not be called for not found domain")
	})

	middleware, err := NewAccessControlMiddleware(gw, zap.NewNop())
	if err != nil {
		t.Fatalf("NewAccessControlMiddleware: %v", err)
	}
	handler := middleware.Wrap(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "notfound.com"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestAccessControlMiddleware_ErrGone(t *testing.T) {
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			return nil, fmt.Errorf("%w: website gone", ipfs.ErrGone)
		},
	}

	gw, err := newTestGateway(apiClient, nil)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inner handler should not be called for gone domain")
	})

	middleware, err := NewAccessControlMiddleware(gw, zap.NewNop())
	if err != nil {
		t.Fatalf("NewAccessControlMiddleware: %v", err)
	}
	handler := middleware.Wrap(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "gone.com"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusGone {
		t.Errorf("expected 410, got %d", rec.Code)
	}
}

func TestAccessControlMiddleware_ErrUnauthorized(t *testing.T) {
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			return nil, fmt.Errorf("%w: authentication required", ipfs.ErrUnauthorized)
		},
	}

	gw, err := newTestGateway(apiClient, nil)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inner handler should not be called for unauthorized domain")
	})

	middleware, err := NewAccessControlMiddleware(gw, zap.NewNop())
	if err != nil {
		t.Fatalf("NewAccessControlMiddleware: %v", err)
	}
	handler := middleware.Wrap(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "unauthorized.com"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestAccessControlMiddleware_IPAddressPassThrough(t *testing.T) {
	gw, err := newTestGateway(nil, nil)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	middleware, err := NewAccessControlMiddleware(gw, zap.NewNop())
	if err != nil {
		t.Fatalf("NewAccessControlMiddleware: %v", err)
	}
	handler := middleware.Wrap(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "127.0.0.1:8080"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if !called {
		t.Error("IP address should pass through to inner handler")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestAccessControlMiddleware_IpfSPrefixURLPassThrough(t *testing.T) {
	gw, err := newTestGateway(nil, nil)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	middleware, err := NewAccessControlMiddleware(gw, zap.NewNop())
	if err != nil {
		t.Fatalf("NewAccessControlMiddleware: %v", err)
	}
	handler := middleware.Wrap(inner)

	req := httptest.NewRequest(http.MethodGet, "/ipfs/QmTest", nil)
	req.Host = "gateway.example.com"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if !called {
		t.Error("/ipfs/ path should pass through to inner handler")
	}
}

func TestAccessControlMiddleware_XForwardedHost(t *testing.T) {
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			if domain != "behind-proxy.com" {
				t.Errorf("expected domain behind-proxy.com, got %s", domain)
			}
			return &types.GatewayWebsiteResponse{
				Domain:     "behind-proxy.com",
				Status:     types.StatusActive,
				TargetHash: "QmProxy",
				TargetType: "ipfs",
			}, nil
		},
	}

	gw, err := newTestGateway(apiClient, nil)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware, err := NewAccessControlMiddleware(gw, zap.NewNop())
	if err != nil {
		t.Fatalf("NewAccessControlMiddleware: %v", err)
	}
	handler := middleware.Wrap(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "proxy.example.com"
	req.Header.Set("X-Forwarded-Host", "behind-proxy.com")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestAccessControlMiddleware_PathRewrite(t *testing.T) {
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			return &types.GatewayWebsiteResponse{
				Domain:     "example.com",
				Status:     types.StatusActive,
				TargetHash: "QmTest",
				TargetType: "ipfs",
			}, nil
		},
	}

	gw, err := newTestGateway(apiClient, nil)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	var receivedPath string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	})

	middleware, err := NewAccessControlMiddleware(gw, zap.NewNop())
	if err != nil {
		t.Fatalf("NewAccessControlMiddleware: %v", err)
	}
	handler := middleware.Wrap(inner)

	req := httptest.NewRequest(http.MethodGet, "/assets/style.css", nil)
	req.Host = "example.com"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if receivedPath != "/ipfs/QmTest/assets/style.css" {
		t.Errorf("expected /ipfs/QmTest/assets/style.css, got %s", receivedPath)
	}
}

func TestAccessControlMiddleware_PathRewriteIPNS(t *testing.T) {
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			return &types.GatewayWebsiteResponse{
				Domain:     "example.com",
				Status:     types.StatusActive,
				TargetHash: "k51qzi5uqu5djuc7yel4lzixq3e6ifsm0n1v0lrug8g9o18n4r0v2bgfjlkekm",
				TargetType: "ipns",
			}, nil
		},
	}

	gw, err := newTestGateway(apiClient, nil)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	var receivedPath string
	var receivedCtx context.Context
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedCtx = r.Context()
		w.WriteHeader(http.StatusOK)
	})

	middleware, err := NewAccessControlMiddleware(gw, zap.NewNop())
	if err != nil {
		t.Fatalf("NewAccessControlMiddleware: %v", err)
	}
	handler := middleware.Wrap(inner)

	req := httptest.NewRequest(http.MethodGet, "/assets/style.css", nil)
	req.Host = "example.com"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if receivedPath != "/ipns/k51qzi5uqu5djuc7yel4lzixq3e6ifsm0n1v0lrug8g9o18n4r0v2bgfjlkekm/assets/style.css" {
		t.Errorf("expected /ipns/k51qzi.../assets/style.css, got %s", receivedPath)
	}

	dnslinkHost, ok := receivedCtx.Value(boxogateway.DNSLinkHostnameKey).(string)
	if !ok {
		t.Error("expected DNSLinkHostnameKey to be set in context")
	}
	if dnslinkHost != "example.com" {
		t.Errorf("expected DNSLinkHostnameKey to be 'example.com', got %s", dnslinkHost)
	}
}

func TestAccessControlMiddleware_PendingValidationStatus(t *testing.T) {
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			return &types.GatewayWebsiteResponse{
				Domain: "pending.com",
				Status: types.StatusPendingValidation,
			}, nil
		},
	}

	gw, err := newTestGateway(apiClient, nil)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inner handler should not be called for pending domain")
	})

	middleware, err := NewAccessControlMiddleware(gw, zap.NewNop())
	if err != nil {
		t.Fatalf("NewAccessControlMiddleware: %v", err)
	}
	handler := middleware.Wrap(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "pending.com"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	// Pending validation now returns 200 with a custom HTML page explaining the status
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for pending_validation with custom page, got %d", rec.Code)
	}
}

func TestStripPort(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"example.com:8080", "example.com"},
		{"example.com", "example.com"},
		{"[::1]:8080", "::1"},
	}

	for _, tt := range tests {
		result := stripPort(tt.input)
		if result != tt.expected {
			t.Errorf("stripPort(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestIsSubResourceRequest(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		accept   string
		expected bool
	}{
		{"css file", "/assets/style.css", "", true},
		{"js file", "/assets/app.js", "", true},
		{"mjs file", "/assets/module.mjs", "", true},
		{"woff2 font", "/fonts/inter.woff2", "", true},
		{"svg image", "/img/logo.svg", "", true},
		{"png image", "/img/photo.png", "", true},
		{"wasm", "/app.wasm", "", true},
		{"html page", "/about", "text/html,application/xhtml+xml", false},
		{"html page with css ext but html accept", "/style.css", "text/html", false},
		{"root path", "/", "text/html", false},
		{"no ext no accept", "/api/data", "", false},
		{"json", "/data.json", "", true},
		{"xml", "/feed.xml", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}
			if isSubResourceRequest(req) != tt.expected {
				t.Errorf("isSubResourceRequest(%s, Accept=%s) = %v, want %v", tt.path, tt.accept, !tt.expected, tt.expected)
			}
		})
	}
}

func TestSubResourceErrorHandler_SwallowsErrorBody(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Length", "45")
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = w.Write([]byte("timeout occurred after finding 1 provider(s)"))
	})

	req := httptest.NewRequest(http.MethodGet, "/_astro/index.abc123.css", nil)
	rec := httptest.NewRecorder()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w = newSubResourceErrorHandler(w, r)
		inner.ServeHTTP(w, r)
	})

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusGatewayTimeout {
		t.Errorf("expected status 504, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("expected empty body for sub-resource error, got %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "" {
		t.Errorf("expected Content-Type to be stripped for sub-resource error, got %q", ct)
	}
	if cl := rec.Header().Get("Content-Length"); cl != "" && cl != "0" {
		t.Errorf("expected Content-Length to be stripped for sub-resource error, got %q", cl)
	}
}

func TestSubResourceErrorHandler_PassesHTMLThrough(t *testing.T) {
	body := "<html>error page</html>"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = w.Write([]byte(body))
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w = newSubResourceErrorHandler(w, r)
		inner.ServeHTTP(w, r)
	})

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusGatewayTimeout {
		t.Errorf("expected status 504, got %d", rec.Code)
	}
	if rec.Body.String() != body {
		t.Errorf("expected body %q for HTML request, got %q", body, rec.Body.String())
	}
}

func TestSubResourceErrorHandler_PassesSuccessThrough(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("body { color: red; }"))
	})

	req := httptest.NewRequest(http.MethodGet, "/style.css", nil)
	rec := httptest.NewRecorder()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w = newSubResourceErrorHandler(w, r)
		inner.ServeHTTP(w, r)
	})

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
	if rec.Body.String() != "body { color: red; }" {
		t.Errorf("expected CSS body, got %q", rec.Body.String())
	}
}

func TestCheckAccess_StaleActiveOnTransientError(t *testing.T) {
	callCount := 0
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			callCount++
			if callCount == 1 {
				return &types.GatewayWebsiteResponse{
					Domain:     "example.com",
					Status:     types.StatusActive,
					TargetHash: "QmTest",
					TargetType: "ipfs",
				}, nil
			}
			return nil, fmt.Errorf("connection refused")
		},
	}

	statusCache, err := cache.NewStatusCacheSimple(100, 10*time.Millisecond, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("NewStatusCache: %v", err)
	}

	gw, err := newTestGateway(apiClient, statusCache)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	website, err := gw.CheckAccess(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("first CheckAccess: %v", err)
	}
	if website == nil || website.Status != types.StatusActive {
		t.Fatal("first call should return active")
	}

	time.Sleep(15 * time.Millisecond)

	website, err = gw.CheckAccess(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("second CheckAccess (stale): %v", err)
	}
	if website == nil || website.Status != types.StatusActive {
		t.Fatal("stale-while-revalidate should return cached active data despite transient error")
	}
}

func TestCheckAccess_StaleServedWithinStaleWindow(t *testing.T) {
	callCount := 0
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			callCount++
			if callCount == 1 {
				return &types.GatewayWebsiteResponse{
					Domain:     "example.com",
					Status:     types.StatusActive,
					TargetHash: "QmTest",
					TargetType: "ipfs",
				}, nil
			}
			return nil, fmt.Errorf("%w: website not found", ipfs.ErrNotFound)
		},
	}

	staleTTL := 50 * time.Millisecond
	statusCache, err := cache.NewStatusCache(100, 10*time.Millisecond, 10*time.Millisecond, staleTTL, nil)
	if err != nil {
		t.Fatalf("NewStatusCache: %v", err)
	}

	gw, err := newTestGateway(apiClient, statusCache)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	website, err := gw.CheckAccess(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("first CheckAccess: %v", err)
	}
	if website == nil || website.Status != types.StatusActive {
		t.Fatal("first call should return active")
	}

	time.Sleep(15 * time.Millisecond)

	// Within staleTTL: stale active data is served immediately (SWR)
	website, err = gw.CheckAccess(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("SWR should serve stale active data, got error: %v", err)
	}
	if website == nil || website.Status != types.StatusActive {
		t.Fatal("SWR should return stale active website")
	}

	// After staleTTL expires: cache truly expires, API is called
	time.Sleep(60 * time.Millisecond)

	_, err = gw.CheckAccess(context.Background(), "example.com")
	if err == nil {
		t.Fatal("past staleTTL, ErrNotFound should be returned from API")
	}
	if !errors.Is(err, ipfs.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCheckAccess_ErrGonePastStaleWindow(t *testing.T) {
	callCount := 0
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			callCount++
			if callCount == 1 {
				return &types.GatewayWebsiteResponse{
					Domain:     "example.com",
					Status:     types.StatusActive,
					TargetHash: "QmTest",
					TargetType: "ipfs",
				}, nil
			}
			return nil, fmt.Errorf("%w: website is broken or deleted", ipfs.ErrGone)
		},
	}

	staleTTL := 50 * time.Millisecond
	statusCache, err := cache.NewStatusCache(100, 10*time.Millisecond, 10*time.Millisecond, staleTTL, nil)
	if err != nil {
		t.Fatalf("NewStatusCache: %v", err)
	}

	gw, err := newTestGateway(apiClient, statusCache)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	website, err := gw.CheckAccess(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("first CheckAccess: %v", err)
	}
	if website == nil || website.Status != types.StatusActive {
		t.Fatal("first call should return active")
	}

	time.Sleep(15 * time.Millisecond)

	// Within staleTTL: stale active data is served
	website, err = gw.CheckAccess(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("SWR should serve stale active data, got error: %v", err)
	}
	if website == nil || website.Status != types.StatusActive {
		t.Fatal("SWR should return stale active website")
	}

	// After staleTTL expires: API is called, ErrGone returned
	time.Sleep(60 * time.Millisecond)

	_, err = gw.CheckAccess(context.Background(), "example.com")
	if err == nil {
		t.Fatal("past staleTTL, ErrGone should be returned from API")
	}
	if !errors.Is(err, ipfs.ErrGone) {
		t.Fatalf("expected ErrGone, got %v", err)
	}
}

func TestCheckAccess_ErrUnauthorizedPastStaleWindow(t *testing.T) {
	callCount := 0
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			callCount++
			if callCount == 1 {
				return &types.GatewayWebsiteResponse{
					Domain:     "example.com",
					Status:     types.StatusActive,
					TargetHash: "QmTest",
					TargetType: "ipfs",
				}, nil
			}
			return nil, fmt.Errorf("%w: authentication required", ipfs.ErrUnauthorized)
		},
	}

	staleTTL := 50 * time.Millisecond
	statusCache, err := cache.NewStatusCache(100, 10*time.Millisecond, 10*time.Millisecond, staleTTL, nil)
	if err != nil {
		t.Fatalf("NewStatusCache: %v", err)
	}

	gw, err := newTestGateway(apiClient, statusCache)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	website, err := gw.CheckAccess(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("first CheckAccess: %v", err)
	}
	if website == nil || website.Status != types.StatusActive {
		t.Fatal("first call should return active")
	}

	time.Sleep(15 * time.Millisecond)

	// Within staleTTL: stale active data is served
	website, err = gw.CheckAccess(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("SWR should serve stale active data, got error: %v", err)
	}
	if website == nil || website.Status != types.StatusActive {
		t.Fatal("SWR should return stale active website")
	}

	// After staleTTL expires: API is called, ErrUnauthorized returned
	time.Sleep(60 * time.Millisecond)

	_, err = gw.CheckAccess(context.Background(), "example.com")
	if err == nil {
		t.Fatal("past staleTTL, ErrUnauthorized should be returned from API")
	}
	if !errors.Is(err, ipfs.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestCheckAccess_NoStaleDataOnFreshMissWithError(t *testing.T) {
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}

	statusCache, err := cache.NewStatusCacheSimple(100, 5*time.Minute, 30*time.Second)
	if err != nil {
		t.Fatalf("NewStatusCache: %v", err)
	}

	gw, err := newTestGateway(apiClient, statusCache)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	website, err := gw.CheckAccess(context.Background(), "example.com")
	if err == nil {
		t.Fatal("expected error when no stale data and API fails")
	}
	if website != nil {
		t.Fatal("expected nil website on API failure with no stale data")
	}
}

func TestCheckAccess_ContextCanceledNotCached(t *testing.T) {
	callCount := 0
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			callCount++
			if callCount == 1 {
				return nil, context.Canceled
			}
			return &types.GatewayWebsiteResponse{
				Domain:     "example.com",
				Status:     types.StatusActive,
				TargetHash: "QmTest",
				TargetType: "ipfs",
			}, nil
		},
	}

	statusCache, err := cache.NewStatusCacheSimple(100, 5*time.Minute, 30*time.Second)
	if err != nil {
		t.Fatalf("NewStatusCache: %v", err)
	}

	gw, err := newTestGateway(apiClient, statusCache)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	// First call: context.Canceled, should NOT be cached
	_, err = gw.CheckAccess(context.Background(), "example.com")
	if err == nil {
		t.Fatal("expected context.Canceled error on first call")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	// Second call: should hit API again (not return cached error)
	website, err := gw.CheckAccess(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("second CheckAccess should succeed, got: %v", err)
	}
	if website == nil || website.Status != types.StatusActive {
		t.Fatal("second call should return active website")
	}
	if callCount != 2 {
		t.Errorf("Expected API called twice, got %d", callCount)
	}
}

func TestCheckAccess_DeadlineExceededNotCached(t *testing.T) {
	callCount := 0
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			callCount++
			if callCount == 1 {
				return nil, context.DeadlineExceeded
			}
			return &types.GatewayWebsiteResponse{
				Domain:     "example.com",
				Status:     types.StatusActive,
				TargetHash: "QmTest",
				TargetType: "ipfs",
			}, nil
		},
	}

	statusCache, err := cache.NewStatusCacheSimple(100, 5*time.Minute, 30*time.Second)
	if err != nil {
		t.Fatalf("NewStatusCache: %v", err)
	}

	gw, err := newTestGateway(apiClient, statusCache)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	// First call: DeadlineExceeded, should NOT be cached
	_, err = gw.CheckAccess(context.Background(), "example.com")
	if err == nil {
		t.Fatal("expected context.DeadlineExceeded error on first call")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}

	// Second call: should hit API again
	website, err := gw.CheckAccess(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("second CheckAccess should succeed, got: %v", err)
	}
	if website == nil || website.Status != types.StatusActive {
		t.Fatal("second call should return active website")
	}
	if callCount != 2 {
		t.Errorf("Expected API called twice, got %d", callCount)
	}
}

func TestCheckAccess_WrappedContextCanceledNotCached(t *testing.T) {
	callCount := 0
	apiClient := &mockAPIClient{
		getWebsiteFunc: func(ctx context.Context, domain string) (*types.GatewayWebsiteResponse, error) {
			callCount++
			if callCount == 1 {
				return nil, fmt.Errorf("GetWebsite: %w", context.Canceled)
			}
			return &types.GatewayWebsiteResponse{
				Domain:     "example.com",
				Status:     types.StatusActive,
				TargetHash: "QmTest",
				TargetType: "ipfs",
			}, nil
		},
	}

	statusCache, err := cache.NewStatusCacheSimple(100, 5*time.Minute, 30*time.Second)
	if err != nil {
		t.Fatalf("NewStatusCache: %v", err)
	}

	gw, err := newTestGateway(apiClient, statusCache)
	if err != nil {
		t.Fatalf("newTestGateway: %v", err)
	}

	// First call: wrapped context.Canceled, should NOT be cached
	_, err = gw.CheckAccess(context.Background(), "example.com")
	if err == nil {
		t.Fatal("expected error on first call")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected wrapped context.Canceled, got %v", err)
	}

	// Second call: should hit API again
	website, err := gw.CheckAccess(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("second CheckAccess should succeed, got: %v", err)
	}
	if website == nil || website.Status != types.StatusActive {
		t.Fatal("second call should return active website")
	}
	if callCount != 2 {
		t.Errorf("Expected API called twice, got %d", callCount)
	}
}
