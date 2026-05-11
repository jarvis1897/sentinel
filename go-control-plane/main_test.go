package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pb "sentinel-go/gen"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// ---- helpers ----------------------------------------------------------------

const bufSize = 1024 * 1024

// startTestServer wires an in-memory gRPC server backed by a given anomaly
// channel and returns a connected client plus a teardown function.
func startTestServer(t *testing.T, ch chan string) (pb.SentinelServiceClient, func()) {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	s := grpc.NewServer()
	pb.RegisterSentinelServiceServer(s, &server{anomalyCh: ch})
	go s.Serve(lis)

	conn, err := grpc.DialContext(
		context.Background(), "bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	return pb.NewSentinelServiceClient(conn), func() { conn.Close(); s.Stop() }
}

// swapServices replaces the global services slice for the duration of a test.
func swapServices(t *testing.T, svc []serviceCheck) {
	t.Helper()
	orig := services
	services = svc
	t.Cleanup(func() { services = orig })
}

// fastMonitor sets monitorInterval to 100 ms for the duration of a test.
func fastMonitor(t *testing.T) {
	t.Helper()
	orig := monitorInterval
	monitorInterval = 100 * time.Millisecond
	t.Cleanup(func() { monitorInterval = orig })
}

// ---- policy engine ----------------------------------------------------------

func TestIsCommandAllowed(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"docker restart sentinel-nginx", true},
		{"docker restart sentinel-redis", true},
		{"docker start sentinel-nginx", true},
		{"docker start sentinel-redis", true},
		{"docker restart sentinel-my-app-01", true},
		{" docker restart sentinel-nginx ", true}, // leading/trailing whitespace trimmed
		{"docker rm sentinel-nginx", false},
		{"docker restart nginx", false},                           // missing sentinel- prefix
		{"docker restart sentinel-", false},                      // empty name after prefix
		{"rm -rf /", false},
		{"systemctl restart nginx", false},
		{"docker restart sentinel-nginx; rm -rf /", false},       // shell injection
		{"docker restart sentinel-nginx && whoami", false},       // shell injection
		{"docker restart sentinel-nginx | cat /etc/passwd", false}, // pipe injection
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.cmd, func(t *testing.T) {
			if got := isCommandAllowed(c.cmd); got != c.want {
				t.Errorf("isCommandAllowed(%q) = %v, want %v", c.cmd, got, c.want)
			}
		})
	}
}

// ---- health check factories -------------------------------------------------

func TestMakeHTTPCheck_Healthy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	if !makeHTTPCheck(ts.URL)() {
		t.Error("expected healthy")
	}
}

func TestMakeHTTPCheck_Non200(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	if makeHTTPCheck(ts.URL)() {
		t.Error("expected unhealthy on non-200")
	}
}

func TestMakeHTTPCheck_Unreachable(t *testing.T) {
	if makeHTTPCheck("http://127.0.0.1:19999")() {
		t.Error("expected unhealthy for unreachable host")
	}
}

func TestMakeTCPCheck_Healthy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	if !makeTCPCheck(ln.Addr().String())() {
		t.Error("expected healthy")
	}
}

func TestMakeTCPCheck_Unreachable(t *testing.T) {
	if makeTCPCheck("127.0.0.1:19998")() {
		t.Error("expected unhealthy for unreachable addr")
	}
}

// ---- gRPC handlers ----------------------------------------------------------

func TestReportAnomaly_Returns_Ack(t *testing.T) {
	client, cleanup := startTestServer(t, make(chan string, 1))
	defer cleanup()

	resp, err := client.ReportAnomaly(context.Background(), &pb.AnomalyReport{
		SourceNode:    "test-node",
		ErrorType:     "TEST_DOWN",
		RawLogSnippet: "unit test failure",
		Timestamp:     123,
	})
	if err != nil {
		t.Fatalf("rpc: %v", err)
	}
	if !resp.IsBeingProcessed {
		t.Error("expected IsBeingProcessed=true")
	}
	if resp.AcknowledgementId == "" {
		t.Error("expected non-empty ack id")
	}
}

func TestExecuteAction_BlockedByPolicy(t *testing.T) {
	client, cleanup := startTestServer(t, make(chan string, 1))
	defer cleanup()

	for _, cmd := range []string{"rm -rf /", "systemctl restart nginx", "docker rm sentinel-nginx"} {
		resp, err := client.ExecuteAction(context.Background(), &pb.ActionRequest{
			ActionId: "test",
			Command:  cmd,
		})
		if err != nil {
			t.Fatalf("rpc: %v", err)
		}
		if resp.Success {
			t.Errorf("command %q should be blocked", cmd)
		}
		if !strings.Contains(resp.Output, "blocked by policy engine") {
			t.Errorf("expected policy block message for %q, got: %q", cmd, resp.Output)
		}
		if resp.ExitCode != -1 {
			t.Errorf("expected exit code -1 for blocked command, got %d", resp.ExitCode)
		}
	}
}

func TestExecuteAction_InjectionAttempt(t *testing.T) {
	client, cleanup := startTestServer(t, make(chan string, 1))
	defer cleanup()

	resp, err := client.ExecuteAction(context.Background(), &pb.ActionRequest{
		Command: "docker restart sentinel-nginx; rm -rf /",
	})
	if err != nil {
		t.Fatalf("rpc: %v", err)
	}
	if resp.Success {
		t.Error("injection attempt should not succeed")
	}
	if resp.ExitCode != -1 {
		t.Error("injection attempt should return exit code -1")
	}
}

// ---- monitor ----------------------------------------------------------------

func TestMonitorSystem_DetectsFailure(t *testing.T) {
	ch := make(chan string, 2)
	fastMonitor(t)
	swapServices(t, []serviceCheck{
		{
			containerName: "test-svc",
			errorType:     "TEST_DOWN",
			logSnippet:    "test failure",
			check:         func() bool { return false }, // always down
		},
	})

	go monitorSystem(ch)

	select {
	case msg := <-ch:
		if !strings.Contains(msg, "TEST_DOWN") {
			t.Errorf("unexpected anomaly msg: %q", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for anomaly")
	}
}

func TestMonitorSystem_NoFalsePositive(t *testing.T) {
	ch := make(chan string, 2)
	fastMonitor(t)
	swapServices(t, []serviceCheck{
		{
			containerName: "test-svc",
			errorType:     "TEST_DOWN",
			logSnippet:    "test",
			check:         func() bool { return true }, // always healthy
		},
	})

	go monitorSystem(ch)
	time.Sleep(500 * time.Millisecond) // five poll cycles at 100 ms

	select {
	case msg := <-ch:
		t.Errorf("unexpected anomaly for healthy service: %q", msg)
	default:
		// correct: nothing fired
	}
}

func TestMonitorSystem_DeduplicatesInFlight(t *testing.T) {
	ch := make(chan string, 10)
	fastMonitor(t)
	swapServices(t, []serviceCheck{
		{
			containerName: "test-svc",
			errorType:     "TEST_DOWN",
			logSnippet:    "test",
			check:         func() bool { return false }, // stays down
		},
	})

	go monitorSystem(ch)
	time.Sleep(500 * time.Millisecond) // several poll cycles

	if len(ch) > 1 {
		t.Errorf("monitor should report each failure only once, got %d messages", len(ch))
	}
}

// ---- StreamLogs delivers anomaly --------------------------------------------

func TestStreamLogs_DeliversAnomaly(t *testing.T) {
	ch := make(chan string, 2)
	client, cleanup := startTestServer(t, ch)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.StreamLogs(ctx, &pb.LogRequest{NodeId: "test-brain"})
	if err != nil {
		t.Fatalf("StreamLogs: %v", err)
	}

	ch <- "sentinel-nginx|NGINX_DOWN|nginx not responding"

	line, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if line.Content != "sentinel-nginx|NGINX_DOWN|nginx not responding" {
		t.Errorf("unexpected content: %q", line.Content)
	}
}

// ---- E2E simulation ---------------------------------------------------------
// Full path: fake service goes down → monitor detects → StreamLogs pushes to
// "Brain" client → client calls ExecuteAction (simulating the Python brain).

func TestE2E_MonitorStreamAction(t *testing.T) {
	ch := make(chan string, 10)
	client, cleanup := startTestServer(t, ch)
	defer cleanup()
	fastMonitor(t)

	// Fake nginx starts down so there is no race between writing healthy and the goroutine reading it.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	swapServices(t, []serviceCheck{
		{
			containerName: "sentinel-nginx",
			errorType:     "NGINX_DOWN",
			logSnippet:    "nginx health check failed",
			check:         makeHTTPCheck(ts.URL),
		},
	})

	go monitorSystem(ch)

	// Open the stream (simulates Python brain subscribing).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := client.StreamLogs(ctx, &pb.LogRequest{NodeId: "e2e-test"})
	if err != nil {
		t.Fatalf("StreamLogs: %v", err)
	}

	// Receive the anomaly pushed by the monitor.
	line, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv anomaly: %v", err)
	}
	if !strings.Contains(line.Content, "NGINX_DOWN") {
		t.Errorf("expected NGINX_DOWN in content, got: %q", line.Content)
	}
	t.Logf("Brain received anomaly: %s", line.Content)

	// Simulate the Python brain calling ExecuteAction with the fix.
	resp, err := client.ExecuteAction(ctx, &pb.ActionRequest{
		ActionId: "e2e-fix",
		Command:  "docker restart sentinel-nginx",
	})
	if err != nil {
		t.Fatalf("ExecuteAction rpc: %v", err)
	}
	// The command must pass the policy engine regardless of Docker availability.
	if strings.Contains(resp.Output, "blocked by policy engine") {
		t.Errorf("docker restart sentinel-nginx must not be blocked by policy, got: %q", resp.Output)
	}
	t.Logf("ExecuteAction result: success=%v exit=%d output=%q", resp.Success, resp.ExitCode, resp.Output)
}
