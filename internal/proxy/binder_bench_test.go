package proxy

import (
	"context"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// BenchmarkBinderClientFor is the per-attempt proxy selection cost on the
// inference path for a pool-bound and an unbound connection.
func BenchmarkBinderClientFor(b *testing.B) {
	pool := runtime.ProxyPool{ID: "pool-1", Enabled: true, Members: []runtime.ProxyMember{{URL: "http://pool.example:3128", Enabled: true}}}
	snapshot, err := (runtime.Compiler{}).Compile(runtime.Config{PoolBindings: map[string]string{"bound": "pool-1"}, ProxyPools: []runtime.ProxyPool{pool}}, 1)
	if err != nil {
		b.Fatal(err)
	}
	binder := NewBinder(func() *runtime.RuntimeSnapshot { return snapshot })
	for _, id := range []string{"bound", "unbound"} {
		b.Run(id, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				binder.ClientFor(context.Background(), id)
			}
		})
	}
}
