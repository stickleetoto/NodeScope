package servicecheck

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stickleetoto/NodeScope/internal/protocol"
)

func TestTCPCheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	st := Check(context.Background(), protocol.ServiceSpec{Name: "tcp", Type: "tcp", Target: ln.Addr().String()})
	if !st.Healthy {
		t.Fatalf("expected healthy TCP check: %+v", st)
	}
	_ = ln.Close()
	st = Check(context.Background(), protocol.ServiceSpec{Name: "tcp", Type: "tcp", Target: ln.Addr().String()})
	if st.Healthy {
		t.Fatalf("expected failed TCP check: %+v", st)
	}
}

func TestHTTPCheck(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()
	st := Check(context.Background(), protocol.ServiceSpec{Name: "web", Type: "http", Target: ts.URL})
	if !st.Healthy {
		t.Fatalf("expected healthy HTTP check: %+v", st)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	st = Check(context.Background(), protocol.ServiceSpec{Name: "web", Type: "http", Target: bad.URL})
	if st.Healthy {
		t.Fatalf("expected unhealthy HTTP check: %+v", st)
	}
}
