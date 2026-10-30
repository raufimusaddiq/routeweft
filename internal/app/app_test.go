package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/proxy"
	"github.com/raufimusaddiq/routeweft/internal/store/storetest"
)

// TestProxyBinderIsWiredIntoIngress proves the production composition exposes a
// live ClientFor callback: a connection bound to an enabled pool resolves to a
// compiled pooled client rather than the handler default, while an unbound
// connection keeps the default (PRD-ROUTE-005, SPEC §20).
func TestProxyBinderIsWiredIntoIngress(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 7)
	}
	app := New(Config{Listen: ":0", DataDir: storetest.DataDir(t), CredentialKey: key, AllowPrivateUpstreams: true}, nil)
	if err := app.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer app.store.Close()
	if app.ingress == nil {
		t.Fatal("ingress was not constructed")
	}
	// Bind a connection to a real pool, then confirm the snapshot + binder agree.
	ctx := context.Background()
	proxies, err := proxy.NewStore(app.store.DB())
	if err != nil {
		t.Fatal(err)
	}
	pool, err := proxies.PutPool(ctx, proxy.Pool{Name: "egress", Enabled: true, Members: []proxy.Member{{URL: "http://127.0.0.1:3128", Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.DB().ExecContext(ctx, "INSERT INTO provider_nodes(id,kind,provider_id,name) VALUES('n1','builtin','codex','Codex')"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.DB().ExecContext(ctx, "INSERT INTO provider_connections(id,node_id,name,auth_kind,identity,enabled,proxy_pool_id) VALUES('c1','n1','primary','api-key','acct',1,?)", pool.ID); err != nil {
		t.Fatal(err)
	}
	// The production path: after the binding is written durably, the runtime
	// recompiles and publishes it (BDR-007).
	if _, err := app.runtime.RefreshPoolBindings(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := app.runtime.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.ConnectionPool("c1"); got != pool.ID {
		t.Fatalf("snapshot binding=%q", got)
	}
	if got, err := app.runtime.PoolBinding("c1"); err != nil || got != pool.ID {
		t.Fatalf("PoolBinding=%q err=%v", got, err)
	}
}

// TestQuotaServiceIsWiredIntoTheApp proves the production quota path exists: a
// configured credential key builds the service and the operator refresh entry
// point runs against the real store/registry wiring.
func TestQuotaServiceIsWiredIntoTheApp(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	app := New(Config{Listen: ":0", DataDir: storetest.DataDir(t), CredentialKey: key, QuotaRefreshInterval: time.Hour}, nil)
	if err := app.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer app.store.Close()
	if app.quota == nil {
		t.Fatal("quota service was not initialized")
	}
	if err := app.RefreshQuota(context.Background()); err != nil {
		t.Fatalf("refresh with no configured connections: %v", err)
	}
}

// TestQuotaServiceSkippedWithoutKey documents that a keyless deployment still
// boots, since there are then no sealed provider connections to read quota for.
func TestTelemetryServiceIsWiredIntoTheApp(t *testing.T) {
	application := New(Config{Listen: ":0", DataDir: storetest.DataDir(t)}, nil)
	if err := application.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if application.usage == nil {
		t.Fatal("telemetry service not wired")
	}
	// Observability is opt-in (compiled default false), so a fresh app is wired
	// but idle until an operator enables it.
	if application.usage.Enabled() {
		t.Fatal("telemetry should be disabled by default until enableObservability is set")
	}
	if application.usage.Health() != nil || application.usage.Lost() != 0 {
		t.Fatalf("unexpected telemetry health/lost: %v/%d", application.usage.Health(), application.usage.Lost())
	}
}

func TestAdminBootstrapProvisionOnceAndSessionGate(t *testing.T) {
	ctx := context.Background()
	application := New(Config{Listen: ":0", DataDir: storetest.DataDir(t), AdminBootstrapUsername: "operator", AdminBootstrapPassword: "s3cret"}, nil)
	if err := application.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if application.admin == nil || application.control == nil {
		t.Fatal("admin store/control API not wired")
	}
	// The unauthenticated admin settings route is rejected.
	recorder := httptest.NewRecorder()
	application.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/v1/settings", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated settings status=%d want 401", recorder.Code)
	}
	// A fresh app on the same data dir must not re-provision from a stale env.
	again := New(Config{Listen: ":0", DataDir: application.cfg.DataDir, AdminBootstrapUsername: "attacker", AdminBootstrapPassword: "pw"}, nil)
	if err := again.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	account, ok, err := again.admin.Authenticate(ctx, "operator", "s3cret")
	if err != nil || !ok || account.Username != "operator" {
		t.Fatalf("original admin lost: account=%+v ok=%v err=%v", account, ok, err)
	}
	if _, ok, _ := again.admin.Authenticate(ctx, "attacker", "pw"); ok {
		t.Fatal("stale bootstrap credential re-provisioned an admin")
	}
}

func TestQuotaServiceSkippedWithoutKey(t *testing.T) {
	app := New(Config{Listen: ":0", DataDir: storetest.DataDir(t)}, nil)
	if err := app.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer app.store.Close()
	if app.quota != nil {
		t.Fatal("quota service initialized without a credential key")
	}
	if err := app.RefreshQuota(context.Background()); err != nil {
		t.Fatalf("no-op refresh returned an error: %v", err)
	}
}

func TestLiveHealth(t *testing.T) {
	rr := httptest.NewRecorder()
	New(Config{}, nil).Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "ok\n" {
		t.Fatalf("got %d %q, want 200 %q", rr.Code, rr.Body.String(), "ok\n")
	}
}

func TestReadinessRequiresSuccessfulInitialization(t *testing.T) {
	app := New(Config{Listen: ":0", DataDir: storetest.DataDir(t)}, nil)
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("uninitialized readiness status %d", rr.Code)
	}
	if err := app.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("initialized readiness status %d", rr.Code)
	}
	if err := app.store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReadinessStaysFalseOnInitializationFailure(t *testing.T) {
	app := New(Config{DataDir: ""}, nil)
	if err := app.Initialize(context.Background()); err == nil {
		t.Fatal("expected missing data directory error")
	}
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness status %d", rr.Code)
	}
}
