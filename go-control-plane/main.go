package main

import (
	"context"
	// "fmt"
	"log"
	"net"
	"os"
	"time"

	pb "sentinel-go/gen"

	"google.golang.org/grpc"
)

type server struct {
	pb.UnimplementedSentinelServiceServer
}

// ReportAnomaly implements the RPC call from Node(Go) to Brain(Python)
func (s *server) ReportAnomaly(ctx context.Context, in *pb.AnomalyReport) (*pb.AnomalyResponse, error) {
	log.Printf("Received Anomaly from %s; %s", in.SourceNode, in.ErrorType)

	return &pb.AnomalyResponse{
		AcknowledgementId: "ack-123",
		IsBeingProcessed:  true,
	}, nil
}

// ExecurteAction is what the Python Agent calls to tell Go Agent to execute a fix
func (s *server) ExecuteAction(ctx context.Context, in *pb.ActionRequest) (*pb.ActionResponse, error) {
	log.Printf("Executing AI Fix: %s", in.Command)
	// Here you would execute the command using os/exec in a real project

	if in.Command == "fix_nginx" {
		err := os.Remove("system_status.txt")
		if err != nil {
			return &pb.ActionResponse{Success: false, Output: err.Error()}, nil
		}
		return &pb.ActionResponse{Success: true, Output: "System status restored."}, nil
	}

	return &pb.ActionResponse{Success: false, Output: "Unknown command"}, nil
}

func monitorSystem(client pb.SentinelServiceClient) {
	for {
		if _, err := os.Stat("system_status.txt"); err == nil {
			log.Println("Anomaly detected: system_status.txt exists! Reporting to Brain...")

			ctx := context.Background()
			report := &pb.AnomalyReport{
				SourceNode:    "Node-01",
				ErrorType:     "SERVICE_DOWN",
				RawLogSnippet: "File 'system_status.txt' found. Nginx process missing.",
			}

			client.ReportAnomaly(ctx, report)
			// Wait a bit so we don't spam the Brain
			time.Sleep(10 * time.Second)
		}
		time.Sleep(2 * time.Second)
	}
}
func main() {
	// 1. Create a connection to OURSELVES (so the monitor can call the server's RPCs)
    // In a real system, the monitor might be a separate binary, but here it's a goroutine.
    conn, err := grpc.Dial("localhost:50051", grpc.WithInsecure())
    if err != nil {
        log.Fatalf("did not connect: %v", err)
    }
    defer conn.Close()
    client := pb.NewSentinelServiceClient(conn)

    // 2. START THE MONITOR IN THE BACKGROUND (The Goroutine)
    go monitorSystem(client) 

    // 3. Setup the gRPC Server
	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	// Create a new gRPC server
	s := grpc.NewServer()
	// Register our 'server' struct with the gRPC server
	pb.RegisterSentinelServiceServer(s, &server{})

	log.Printf("Server listening at %v", lis.Addr())
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
