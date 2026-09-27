package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func scrape(t *testing.T, g *Gateway) string {
	t.Helper()
	rec := httptest.NewRecorder()
	g.metricsHandler(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Fatalf("content type %q", ct)
	}
	return rec.Body.String()
}

func TestMetricsCountRoomsAndRefusals(t *testing.T) {
	g, srv, _ := newMultiGateway(t, 1)
	mintSession(t, srv, "i-a")
	if code := tokenStatus(t, srv, "i-b"); code != http.StatusServiceUnavailable {
		t.Fatalf("second room = %d, want 503", code)
	}

	body := scrape(t, g)
	for _, want := range []string{
		"browcord_multi_room 1",
		"browcord_rooms_managed 1",
		`browcord_signins_total{result="ok"} 1`,
		`browcord_signins_total{result="full"} 1`,
		"browcord_rooms_started_total 1",
		"browcord_rooms_full_total 1",
		`browcord_watchdog_actions_total{action="restart"} 0`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("metrics missing %q", want)
		}
	}
	if strings.Contains(body, "i-a") {
		t.Error("metrics expose a Discord instance id")
	}
}

func TestMetricsAreNotOnThePublicHandler(t *testing.T) {
	_, srv := newTestGateway(t)
	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("/metrics is served on the public listener")
	}
}
