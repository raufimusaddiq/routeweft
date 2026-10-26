package cloudcode

import (
	"encoding/json"
	"runtime"
	"testing"
)

// oauth.js:12-19 and appConstants.js:57-64 map os.platform()/arch() the same way.
func TestPlatformEnum(t *testing.T) {
	for _, tc := range []struct {
		goos, goarch string
		want         int
	}{
		{"darwin", "amd64", 1}, {"darwin", "arm64", 2},
		{"linux", "amd64", 3}, {"linux", "arm64", 4}, {"linux", "386", 3},
		{"windows", "amd64", 5}, {"windows", "arm64", 5},
		{"freebsd", "amd64", 0},
	} {
		if got := platform(tc.goos, tc.goarch); got != tc.want {
			t.Fatalf("platform(%s/%s)=%d want %d", tc.goos, tc.goarch, got, tc.want)
		}
	}
}

func TestClientMetadataShape(t *testing.T) {
	var got map[string]int
	if err := json.Unmarshal([]byte(ClientMetadata()), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"ideType": 9, "platform": platform(runtime.GOOS, runtime.GOARCH), "pluginType": 2}
	if len(got) != len(want) {
		t.Fatalf("metadata=%v want %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("metadata=%v want %v", got, want)
		}
	}
}
