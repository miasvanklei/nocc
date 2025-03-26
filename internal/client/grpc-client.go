package client

import (
	"context"
	"fmt"
	"net"
	"time"

	"nocc/pb"

	"golang.org/x/net/proxy"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type GRPCClient struct {
	remoteHostPort string
	connection     *grpc.ClientConn
	callContext    context.Context
	cancelFunc     context.CancelFunc
	pb             pb.CompilationServiceClient
}

func MakeGRPCClient(remoteHostPort string, socksProxyAddr string) (*GRPCClient, error) {
	// this connection is non-blocking: it's created immediately
	// if the remote is not available, it will fail on request

	dialOpts := createDialOpts(socksProxyAddr)

	var remoteAddress string

	if socksProxyAddr != "" {
		remoteAddress = fmt.Sprintf("passthrough:%s", remoteHostPort)
	} else {
		remoteAddress = fmt.Sprintf("dns:///%s", remoteHostPort)
	}

	connection, err := grpc.NewClient(
		remoteAddress,
		dialOpts...,
	)

	if err != nil {
		return nil, err
	}

	ctx, cancelFunc := context.WithCancel(context.Background())
	return &GRPCClient{
		remoteHostPort: remoteHostPort,
		connection:     connection,
		callContext:    ctx,
		cancelFunc:     cancelFunc,
		pb:             pb.NewCompilationServiceClient(connection),
	}, nil
}

func createDialOpts(socksProxyAddr string) []grpc.DialOption {
	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(),
	}

	if socksProxyAddr != "" {
		dialOpt, err := runInSocks5(socksProxyAddr)
		if err == nil {
			dialOpts = append(dialOpts, dialOpt)
		}
	}
	return dialOpts
}

func runInSocks5(proxyAddr string) (grpc.DialOption, error) {
	dialer, err := proxy.SOCKS5("unix", proxyAddr, nil, proxy.Direct)
	if err != nil {
		return nil, err
	}

	customResolver := &net.Resolver{
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialer.Dial(network, address)
		},
	}

	customDialer := func(ctx context.Context, addr string) (net.Conn, error) {
		newctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		ips, err := customResolver.LookupIP(newctx, "ip4", addr)
		if err != nil {
			return nil, err
		}

		return dialer.Dial("tcp", ips[0].String())
	}

	return grpc.WithContextDialer(customDialer), nil
}

func (grpcClient *GRPCClient) Clear() {
	if grpcClient.connection != nil {
		grpcClient.cancelFunc()
		_ = grpcClient.connection.Close()

		grpcClient.connection = nil
		grpcClient.callContext = nil
		grpcClient.cancelFunc = nil
		grpcClient.pb = nil
	}
}
