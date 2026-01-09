package grpc

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// bufConnSize 是 bufconn 的缓冲区大小。
const bufConnSize = 1024 * 1024

func dialBufConn(ctx context.Context, lis *bufconn.Listener, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	// dialer 把 grpc Dial 转换为对 bufconn 的 Dial。
	dialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}

	// baseOpts 提供 bufconn dialer + 明文传输（测试用）。
	baseOpts := []grpc.DialOption{
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}
	// 合并调用方追加的 dial options（例如默认 call options）。
	baseOpts = append(baseOpts, opts...)

	// 使用固定 target 名称即可（bufconn 不依赖真实地址）。
	return grpc.DialContext(ctx, "bufnet", baseOpts...)
}

type testTargetServer struct {
	// receivedMetadata 保存目标 server 在 incoming context 上看到的 metadata。
	receivedMetadata atomic.Value
}

func (b *testTargetServer) handler(srv any, stream grpc.ServerStream) error {
	// 读取目标 server 收到的 incoming metadata。
	md, _ := metadata.FromIncomingContext(stream.Context())
	// 保存一份供断言使用。
	b.receivedMetadata.Store(md)

	// 先发送一组 header，供代理侧在第一条消息后透传。
	if err := stream.SendHeader(metadata.Pairs("x-target-header", "1")); err != nil {
		return err
	}

	// 接收代理转发来的第一条消息。
	req := &frame{}
	if err := stream.RecvMsg(req); err != nil {
		return err
	}

	// 构造响应 payload：原样回显并追加 "::ok" 标记。
	respPayload := append([]byte(nil), req.payload...)
	respPayload = append(respPayload, []byte("::ok")...)
	// 发送响应消息回代理。
	if err := stream.SendMsg(&frame{payload: respPayload}); err != nil {
		return err
	}

	// 设置 trailer，供代理侧透传回客户端。
	stream.SetTrailer(metadata.Pairs("x-target-trailer", "1"))
	return nil
}

func startTargetServer(t *testing.T) (*grpc.Server, *bufconn.Listener, *testTargetServer) {
	// 标记为 helper，失败时把行号归因到调用方。
	t.Helper()

	// 创建内存 listener，避免占用真实端口。
	lis := bufconn.Listen(bufConnSize)
	// 目标 server 强制使用 rawProtoCodec，确保能以 frame 方式收发原始 bytes。
	srv := grpc.NewServer(grpc.ForceServerCodec(RawProtoCodec{}))

	// 构造目标服务实现，用于收集 metadata 并回显 payload。
	targetServer := &testTargetServer{}
	// 注册一个服务与一个方法，供代理转发调用。
	srv.RegisterService(&grpc.ServiceDesc{
		ServiceName: "acme.demo.v1.DemoService",
		HandlerType: (*interface{})(nil),
		Streams: []grpc.StreamDesc{
			{
				StreamName:    "Echo",
				Handler:       targetServer.handler,
				ServerStreams: true,
				ClientStreams: true,
			},
		},
	}, targetServer)

	// 异步启动 server。
	go func() {
		_ = srv.Serve(lis)
	}()

	// 测试结束时停止 server 并关闭 listener。
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})

	return srv, lis, targetServer
}

func startProxy(t *testing.T, targetConn *grpc.ClientConn) (*grpc.Server, *bufconn.Listener) {
	// 标记为 helper，失败时把行号归因到调用方。
	t.Helper()

	// 创建内存 listener，避免占用真实端口。
	lis := bufconn.Listen(bufConnSize)
	// 使用库提供的 NewProxy，默认启用 codec 与 UnknownServiceHandler。
	srv := NewProxy(targetConn)

	// 异步启动 proxy server。
	go func() {
		_ = srv.Serve(lis)
	}()

	// 测试结束时停止 server 并关闭 listener。
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})

	return srv, lis
}

func TestTransparentProxy_ForwardsPayloadAndMetadata(t *testing.T) {
	// 启动目标服务端，并拿到实例用于断言 metadata。
	_, targetLis, targetServer := startTargetServer(t)

	// 为整条测试链路设置超时，避免 goroutine 泄漏导致卡死。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	// 连接目标服务端（bufconn），并为调用默认设置 rawProtoCodec。
	targetConn, err := dialBufConn(ctx, targetLis, grpc.WithDefaultCallOptions(DefaultClientCallOpts()...))
	if err != nil {
		t.Fatalf("dial target: %v", err)
	}
	// 测试结束关闭连接。
	t.Cleanup(func() { _ = targetConn.Close() })

	// 启动 proxy server，并拿到其 listener。
	_, proxyLis := startProxy(t, targetConn)

	// 连接 proxy（bufconn），并为调用默认设置 rawProtoCodec。
	proxyConn, err := dialBufConn(ctx, proxyLis, grpc.WithDefaultCallOptions(DefaultClientCallOpts()...))
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	// 测试结束关闭连接。
	t.Cleanup(func() { _ = proxyConn.Close() })

	// 构造 outgoing metadata，模拟网关传入的链路信息。
	callCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-test", "1"))
	// 通过 proxy 创建 client stream，调用 fully-qualified 方法名。
	stream, err := grpc.NewClientStream(callCtx, clientStreamDescForProxying, proxyConn, "/acme.demo.v1.DemoService/Echo", DefaultClientCallOpts()...)
	if err != nil {
		t.Fatalf("new client stream: %v", err)
	}

	// 发送一条消息到 proxy。
	if err := stream.SendMsg(&frame{payload: []byte("ping")}); err != nil {
		t.Fatalf("send: %v", err)
	}
	// 关闭发送方向，触发目标服务端 handler 返回。
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}

	// 接收目标服务端经由 proxy 转发回来的响应。
	resp := &frame{}
	if err := stream.RecvMsg(resp); err != nil {
		t.Fatalf("recv: %v", err)
	}
	// 断言 payload 被正确透传并由目标服务端追加 "::ok"。
	if got, want := string(resp.payload), "ping::ok"; got != want {
		t.Fatalf("payload mismatch: got %q want %q", got, want)
	}

	// 读取目标服务端保存的 incoming metadata，断言 x-test 透传成功。
	received, _ := targetServer.receivedMetadata.Load().(metadata.MD)
	vals := received.Get("x-test")
	if len(vals) != 1 || vals[0] != "1" {
		t.Fatalf("metadata not forwarded: %v", received)
	}
}

func TestRegisterService_AllowsOnlySpecifiedMethods(t *testing.T) {
	// 启动目标服务端。
	_, targetLis, _ := startTargetServer(t)

	// 为整条测试链路设置超时，避免 goroutine 泄漏导致卡死。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	// 连接目标服务端（bufconn），并为调用默认设置 rawProtoCodec。
	targetConn, err := dialBufConn(ctx, targetLis, grpc.WithDefaultCallOptions(DefaultClientCallOpts()...))
	if err != nil {
		t.Fatalf("dial target: %v", err)
	}
	// 测试结束关闭连接。
	t.Cleanup(func() { _ = targetConn.Close() })

	// 启动一个仅注册白名单方法的 proxy server。
	proxyLis := bufconn.Listen(bufConnSize)
	// proxySrv 强制启用 server codec，确保能够以 frame 方式收发原始 bytes。
	proxySrv := grpc.NewServer(DefaultProxyServerOpt())
	// 只注册 Echo 方法，不注册 Nope。
	RegisterService(proxySrv, DefaultDirector(targetConn), "acme.demo.v1.DemoService", "Echo")
	// 异步启动 server。
	go func() {
		_ = proxySrv.Serve(proxyLis)
	}()
	// 测试结束时停止 server 并关闭 listener。
	t.Cleanup(func() {
		proxySrv.Stop()
		_ = proxyLis.Close()
	})

	// 连接白名单 proxy（bufconn），并为调用默认设置 rawProtoCodec。
	proxyConn, err := dialBufConn(ctx, proxyLis, grpc.WithDefaultCallOptions(DefaultClientCallOpts()...))
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	// 测试结束关闭连接。
	t.Cleanup(func() { _ = proxyConn.Close() })

	// 调用被允许的方法 Echo，应正常返回。
	allowedStream, err := grpc.NewClientStream(ctx, clientStreamDescForProxying, proxyConn, "/acme.demo.v1.DemoService/Echo", DefaultClientCallOpts()...)
	if err != nil {
		t.Fatalf("new allowed stream: %v", err)
	}
	if err := allowedStream.SendMsg(&frame{payload: []byte("ping")}); err != nil {
		t.Fatalf("allowed send: %v", err)
	}
	if err := allowedStream.CloseSend(); err != nil {
		t.Fatalf("allowed close send: %v", err)
	}
	if err := allowedStream.RecvMsg(&frame{}); err != nil {
		t.Fatalf("allowed recv: %v", err)
	}

	// 调用未注册的方法 Nope，应返回 Unimplemented。
	deniedStream, err := grpc.NewClientStream(ctx, clientStreamDescForProxying, proxyConn, "/acme.demo.v1.DemoService/Nope", DefaultClientCallOpts()...)
	if err != nil {
		t.Fatalf("new denied stream: %v", err)
	}
	// 关闭发送方向，触发服务端返回状态。
	_ = deniedStream.CloseSend()
	// 读取响应，此时应得到带 status 的 error。
	err = deniedStream.RecvMsg(&frame{})
	// 断言 status code 为 Unimplemented（未注册方法）。
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("unexpected status code: %v (%v)", err, status.Code(err))
	}
}
