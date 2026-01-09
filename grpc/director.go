package grpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type StreamDirector func(ctx context.Context, fullMethodName string) (context.Context, *grpc.ClientConn, error)

// DefaultDirector 返回一个固定目标连接的 director，并且会把 incoming metadata 复制到 outgoing context。
func DefaultDirector(cc *grpc.ClientConn) StreamDirector {
	// 返回 StreamDirector 的闭包，按 fullMethodName 选择目标连接。
	return func(ctx context.Context, fullMethodName string) (context.Context, *grpc.ClientConn, error) {
		// 从 incoming context 读取 metadata（若不存在则 md 为空）。
		md, _ := metadata.FromIncomingContext(ctx)
		// 复制 metadata 到 outgoing context，避免复用导致并发写问题。
		outgoingCtx := metadata.NewOutgoingContext(ctx, md.Copy())
		// 返回 outgoing context + 固定目标连接。
		return outgoingCtx, cc, nil
	}
}
