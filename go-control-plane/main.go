package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"

	pb "sentinel-go/gen"

	"google.golang.org/grpc"
)

// --- Command Policy Engine ---

var allowedCommands = []*regexp.Regexp{
	regexp.MustCompile(`^docker (restart|start) sentinel-[a-z0-9-]+$`),
}

func isCommandAllowed(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	for _, pattern := range allowedCommands {
		if pattern.MatchString(cmd) {
			return true
		}
	}
	return false
}

// --- Service Health Checks ---

type serviceCheck struct {
	containerName string
	errorType     string
	logSnippet    string
	check         func() bool
}

var httpClient = &http.Client{Timeout: 3 * time.Second}

// monitorInterval controls how often the monitor polls. A var so tests can speed it up.
var monitorInterval = 5 * time.Second

func makeHTTPCheck(url string) func() bool {
	return func() bool {
		resp, err := httpClient.Get(url)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}
}

func makeTCPCheck(addr string) func() bool {
	return func() bool {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}
}

var services = []serviceCheck{
	{
		containerName: "sentinel-nginx",
		errorType:     "NGINX_DOWN",
		logSnippet:    "nginx health check failed: GET http://localhost:8080/health returned non-200 or connection refused",
		check:         makeHTTPCheck("http://localhost:8080/health"),
	},
	{
		containerName: "sentinel-redis",
		errorType:     "REDIS_DOWN",
		logSnippet:    "redis health check failed: TCP connect to localhost:6379 refused",
		check:         makeTCPCheck("localhost:6379"),
	},
}

// --- gRPC Server ---

type server struct {
	pb.UnimplementedSentinelServiceServer
	anomalyCh chan string
}

func (s *server) ReportAnomaly(ctx context.Context, in *pb.AnomalyReport) (*pb.AnomalyResponse, error) {
	log.Printf("[ANOMALY] source=%s type=%s", in.SourceNode, in.ErrorType)
	return &pb.AnomalyResponse{
		AcknowledgementId: fmt.Sprintf("ack-%d", time.Now().UnixMilli()),
		IsBeingProcessed:  true,
	}, nil
}

func (s *server) ExecuteAction(ctx context.Context, in *pb.ActionRequest) (*pb.ActionResponse, error) {
	cmd := strings.TrimSpace(in.Command)
	log.Printf("[ACTION] requested: %q", cmd)

	if !isCommandAllowed(cmd) {
		log.Printf("[POLICY] BLOCKED: %q", cmd)
		return &pb.ActionResponse{
			Success:  false,
			Output:   "blocked by policy engine: " + cmd,
			ExitCode: -1,
		}, nil
	}

	parts := strings.Fields(cmd)
	out, err := exec.CommandContext(ctx, parts[0], parts[1:]...).CombinedOutput()
	output := strings.TrimSpace(string(out))

	if err != nil {
		exitCode := int32(1)
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = int32(exitErr.ExitCode())
		}
		log.Printf("[ACTION] FAILED exit=%d output=%q", exitCode, output)
		return &pb.ActionResponse{Success: false, Output: output, ExitCode: exitCode}, nil
	}

	log.Printf("[ACTION] OK output=%q", output)
	return &pb.ActionResponse{Success: true, Output: output, ExitCode: 0}, nil
}

// StreamLogs is repurposed as the anomaly push stream for the Python brain.
// The brain subscribes once and receives an event for every failure detected.
func (s *server) StreamLogs(in *pb.LogRequest, stream pb.SentinelService_StreamLogsServer) error {
	log.Printf("[STREAM] Python Brain subscribed (node=%s)", in.NodeId)
	for {
		select {
		case msg := <-s.anomalyCh:
			if err := stream.Send(&pb.LogLine{Content: msg}); err != nil {
				log.Printf("[STREAM] Brain disconnected: %v", err)
				return err
			}
		case <-stream.Context().Done():
			log.Printf("[STREAM] Brain disconnected cleanly")
			return nil
		}
	}
}

// --- Monitor ---

// monitorSystem pushes pipe-separated anomaly strings into anomalyCh.
// Format: "container_name|ERROR_TYPE|log_snippet"
func monitorSystem(anomalyCh chan<- string) {
	inFlight := map[string]bool{}

	for {
		for _, svc := range services {
			healthy := svc.check()

			if !healthy && !inFlight[svc.containerName] {
				log.Printf("[MONITOR] down: %s — pushing to Brain", svc.containerName)
				inFlight[svc.containerName] = true
				msg := fmt.Sprintf("%s|%s|%s", svc.containerName, svc.errorType, svc.logSnippet)
				select {
				case anomalyCh <- msg:
				default:
					log.Printf("[MONITOR] anomaly channel full, dropping event for %s", svc.containerName)
				}
			}

			if healthy && inFlight[svc.containerName] {
				log.Printf("[MONITOR] recovered: %s", svc.containerName)
				delete(inFlight, svc.containerName)
			}
		}
		time.Sleep(monitorInterval)
	}
}

// --- Main ---

func main() {
	anomalyCh := make(chan string, 10)
	go monitorSystem(anomalyCh)

	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterSentinelServiceServer(s, &server{anomalyCh: anomalyCh})

	log.Printf("[SENTINEL] gRPC server listening on %v", lis.Addr())
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
