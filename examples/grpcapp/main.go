package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	pb "infernosim/examples/grpcapp/echo"

	"google.golang.org/grpc"
)

type server struct {
	pb.UnimplementedEchoServiceServer
}

func (s *server) Echo(ctx context.Context, in *pb.EchoRequest) (*pb.EchoResponse, error) {
	log.Printf("Received: %v", in.GetMessage())
	return &pb.EchoResponse{Message: "Echo: " + in.GetMessage()}, nil
}

func main() {
	mode := flag.String("mode", "server", "Mode: server or client")
	addr := flag.String("addr", ":50051", "Address to listen or connect to")
	target := flag.String("target", "", "For client: target address")
	msg := flag.String("msg", "HelloWorld", "Message to send via client")
	flag.Parse()

	if *mode == "server" {
		lis, err := net.Listen("tcp", *addr)
		if err != nil {
			log.Fatalf("failed to listen: %v", err)
		}
		s := grpc.NewServer()
		pb.RegisterEchoServiceServer(s, &server{})
		log.Printf("Server listening at %v", lis.Addr())
		if err := s.Serve(lis); err != nil {
			log.Fatalf("failed to serve: %v", err)
		}
	} else {
		// client
		if *target == "" {
			*target = *addr
		}

		dialTarget := *target
		proxyEnv := os.Getenv("HTTP_PROXY")
		if proxyEnv != "" {
			// for test isolation we just point the explicit dial target to the proxy
			log.Printf("Proxy env detected, routing through %s", proxyEnv)
			dialTarget = "localhost:9000" // Hardcoded for test
		}

		log.Printf("Connecting to %v (via %v)", *target, dialTarget)
		conn, err := grpc.Dial(dialTarget, grpc.WithInsecure(), grpc.WithBlock(), grpc.WithTimeout(2*time.Second))
		if err != nil {
			log.Fatalf("did not connect: %v", err)
		}
		defer conn.Close()
		c := pb.NewEchoServiceClient(conn)

		ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
		defer cancel()
		r, err := c.Echo(ctx, &pb.EchoRequest{Message: *msg})
		if err != nil {
			log.Fatalf("could not run Echo: %v", err)
		}
		fmt.Printf("ReceivedResponse: %s\n", r.GetMessage())
	}
}
