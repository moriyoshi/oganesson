package app

import (
	"context"
	"net"
	"os"
	"testing"
	"testing/fstest"
)

// TestPrivateOrigin: start-server's appUrl for a listen address.
func TestPrivateOrigin(t *testing.T) {
	for _, c := range []struct {
		ip   string
		want string
	}{
		{"0.0.0.0", "http://localhost:3000"},
		{"::", "http://localhost:3000"},
		{"127.0.0.1", "http://127.0.0.1:3000"},
		{"::1", "http://[::1]:3000"},
	} {
		if got := privateOrigin(&net.TCPAddr{IP: net.ParseIP(c.ip), Port: 3000}); got != c.want {
			t.Errorf("%s: %s, want %s", c.ip, got, c.want)
		}
	}
}

// TestServeSetsPrivateOrigin: serve sets __NEXT_PRIVATE_ORIGIN from its
// listener before it makes the handler (which fails here: an empty app),
// and keeps a value the environment already has.
func TestServeSetsPrivateOrigin(t *testing.T) {
	t.Setenv(PrivateOriginEnv, "") // restored afterwards
	for _, preset := range []string{"", "http://example.test"} {
		if preset == "" {
			os.Unsetenv(PrivateOriginEnv)
		} else {
			os.Setenv(PrivateOriginEnv, preset)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		if err := serve(context.Background(), fstest.MapFS{}, ln, nil); err == nil {
			t.Fatal("serve of an empty app succeeded")
		}
		want := preset
		if want == "" {
			want = "http://" + ln.Addr().String()
		}
		if got := os.Getenv(PrivateOriginEnv); got != want {
			t.Errorf("preset %q: %s = %q, want %q", preset, PrivateOriginEnv, got, want)
		}
	}
}
