package main

import (
	"context"
	// "fmt"
	"log"
	"net"

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
		IsBeingProcessed: true,
	}, nil
}

// ExecurteAction is what the Python Agent calls to tell Go Agent to execute a fix
func (s * server) ExecuteAction(ctx context.Context, in *pb.ActionRequest) (*pb.ActionResponse, error) {
	log.Printf("AI Agebt requested command: %s", in.Command)
	// Here you would execute the command using os/exec in a real project

	return &pb.ActionResponse{
		Success: true,
		Output: "Simulated success for: " + in.Command,
		ExitCode: 0,
	}, nil
}

func main() {
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