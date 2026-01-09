## grpc proxy

一个 gRPC 透明代理实现：
- 代理作为 gRPC server 接收入站请求
- 代理作为 gRPC client 连接到目标 gRPC server 并转发请求

## 安装

```bash
go get github.com/fireflycore/go-proxy
```

## 快速开始

### 1) 透明代理（UnknownServiceHandler）

通过注入 Director 决定每个 RPC 的目标连接（出站 client 侧），并透传 metadata：

```go
package main

import (
	"context"
	"net"

	grpcProxy "github.com/fireflycore/go-proxy/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func main() {
	director := func(ctx context.Context, fullMethodName string) (context.Context, *grpc.ClientConn, error) {
		endPoint := map[string][]string{
			"/acme.demo.v1.DemoService/GetDemo": {"127.0.0.1:9000"},
		}

		nodes, ok := endPoint[fullMethodName]
		if !ok || len(nodes) == 0 {
			return ctx, nil, status.Errorf(codes.Unimplemented, "unknown method: %s", fullMethodName)
		}

		md, _ := metadata.FromIncomingContext(ctx)
		outgoingCtx := metadata.NewOutgoingContext(ctx, md.Copy())

		cc, err := grpc.DialContext(
			outgoingCtx,
			nodes[0],
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			return outgoingCtx, nil, err
		}

		return outgoingCtx, cc, nil
	}

	lis, _ := net.Listen("tcp", ":8080")
	server := grpc.NewServer(
		grpcProxy.DefaultProxyServerOpt(),
		grpc.UnknownServiceHandler(grpcProxy.TransparentHandler(director)),
	)
	_ = server.Serve(lis)
}
```

### 2) 使用固定目标连接（同一个 ClientConn）

如果所有请求都转发到同一个目标 gRPC server，可直接使用 NewProxy（内部已默认启用代理 codec 与 metadata 透传）：

```go
grpcBackendConn, _ := grpc.Dial("127.0.0.1:9000", grpc.WithTransportCredentials(insecure.NewCredentials()))
proxyServer := grpcProxy.NewProxy(grpcBackendConn)
_ = proxyServer
```

## API 说明

- `StreamDirector`：按 `fullMethodName` 选择目标连接（出站 client 侧），并可返回新的 outgoing context。
- `TransparentHandler`：生成可用于 `grpc.UnknownServiceHandler` 的透明代理 handler。
- `RegisterService`：按服务名/方法名注册白名单代理（仅暴露指定方法）。
- `DefaultProxyServerOpt`：代理 server 必需的 codec 配置（确保请求按原始 protobuf bytes 转发）。
