package grpc

import (
	"context"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	// clientStreamDescForProxying 描述一个双向流，用于在代理侧统一承载任意 RPC（无须知道具体方法签名）。
	clientStreamDescForProxying = &grpc.StreamDesc{
		// ServerStreams 表示服务端可以发送多条消息。
		ServerStreams: true,
		// ClientStreams 表示客户端可以发送多条消息。
		ClientStreams: true,
	}
)

// TransparentHandler 提供一个透明代理的方式：
// - 代理作为 gRPC server 接收入站请求
// - 代理作为 gRPC client 连接到目标 server 并转发请求
// 返回值可以作为 grpc.UnknownServiceHandler 使用
func TransparentHandler(director StreamDirector) grpc.StreamHandler {
	// 每个 server 使用一个 handler 实例即可，director 决定如何路由到目标连接。
	streamer := &Handler{director: director}

	// 返回 grpc.StreamHandler 供 UnknownServiceHandler 挂载。
	return streamer.Handler
}

type Handler struct {
	// director 根据 fullMethodName 选择目标连接，并可返回新的 outgoing context。
	director StreamDirector
}

/*
** handle 是代理功能的核心所在，负责将请求转发到其他服务。
** 就像调用任何 gRPC 服务端流一样。
** 使用 RawProtoFrame 类型作为载体，在输入流和输出流之间转发调用。
 */
func (h *Handler) Handler(srv interface{}, serverStream grpc.ServerStream) error {
	// 从 serverStream 提取完整方法名（形如 /package.Service/Method）。
	fullMethodName, ok := grpc.MethodFromServerStream(serverStream)
	if !ok {
		// lowLevelServerStream 在上下文中不存在
		return status.Errorf(codes.Internal, "lowLevelServerStream does not exist in context")
	}

	// director 返回的上下文继承自 serverStream.Context()，并用于出站 client 侧。
	outgoingCtx, targetConn, err := h.director(serverStream.Context(), fullMethodName)
	if err != nil {
		// director 决策失败（例如找不到目标连接、鉴权失败）直接向上返回。
		return err
	}
	if targetConn == nil {
		// 避免在 NewClientStream 处触发空指针或难定位错误。
		return status.Errorf(codes.Unavailable, "target connection is nil")
	}

	// 使用可取消的 clientCtx，保证任一方向转发失败时能中止另一侧。
	clientCtx, clientCancel := context.WithCancel(outgoingCtx)
	defer clientCancel()

	// TODO(mwitkow): Add a `forwarded` header to metadata, https://en.wikipedia.org/wiki/X-Forwarded-For.
	// 建立到目标 server 的出站 client stream，并强制使用代理 codec 以便按原始 bytes 转发。
	clientStream, err := grpc.NewClientStream(clientCtx, clientStreamDescForProxying, targetConn, fullMethodName, DefaultClientCallOpts()...)
	if err != nil {
		// 创建出站流失败（例如连接不可用）直接向上返回。
		return err
	}

	// inboundToOutboundErrChan 负责把入站请求转发到出站 client stream。
	inboundToOutboundErrChan := h.ForwardInboundToOutbound(serverStream, clientStream)
	// outboundToInboundErrChan 负责把出站响应转发回入站连接。
	outboundToInboundErrChan := h.ForwardOutboundToInbound(clientStream, serverStream)

	// 使用 select 语句进行非阻塞式等待, 避免程序陷入等待特定通道可读的死循环中。
	for i := 0; i < 2; i++ {
		select {
		case inboundToOutboundErr := <-inboundToOutboundErrChan:
			if inboundToOutboundErr == io.EOF {
				// 入站已结束发送：向出站关闭发送方向，让目标 server 能结束读取。
				if cCloseErr := clientStream.CloseSend(); cCloseErr != nil {
					return cCloseErr
				}

			} else {
				// 入站转发失败：取消 clientCtx，尽快终止出站侧。
				clientCancel()

				return status.Errorf(codes.Internal, "failed proxying inbound to outbound: %v", inboundToOutboundErr)
			}
		case outboundToInboundErr := <-outboundToInboundErrChan:
			// 将出站 trailer 透传到入站连接。
			serverStream.SetTrailer(clientStream.Trailer())

			if outboundToInboundErr != io.EOF {
				// 出站返回了非 EOF 错误，直接透传（保持 gRPC status 语义）。
				return outboundToInboundErr
			}

			// 出站正常结束。
			return nil
		}
	}

	// 理论上不会走到这里：两个方向的转发 goroutine 其一会先返回并触发 return。
	return status.Errorf(codes.Internal, "gRPC proxying should never reach this stage.")
}

func (h *Handler) ForwardOutboundToInbound(src grpc.ClientStream, dst grpc.ServerStream) chan error {
	// ret 用于把 goroutine 内的最终结果送回主协程。
	ret := make(chan error, 1)

	go func() {
		// f 作为复用容器，承载原始 protobuf bytes。
		f := &RawProtoFrame{}

		// i 用于在第一条消息到来时透传出站 header。
		for i := 0; ; i++ {
			// 从出站接收一条消息。
			if err := src.RecvMsg(f); err != nil {
				ret <- err
				break
			}

			if i == 0 {
				// 这是一个有点取巧的做法，但客户端到服务器的头部信息只能在收到第一个客户端消息后才能读取，
				// 但必须在刷新第一个消息之前写入服务器流。
				// 这是唯一一个合适的地方来处理它。
				md, err := src.Header()
				if err != nil {
					ret <- err
					break
				}

				if err := dst.SendHeader(md); err != nil {
					ret <- err
					break
				}
			}

			// 把出站消息转发回入站连接。
			if err := dst.SendMsg(f); err != nil {
				ret <- err
				break
			}
		}
	}()

	return ret
}

func (h *Handler) ForwardInboundToOutbound(src grpc.ServerStream, dst grpc.ClientStream) chan error {
	// ret 用于把 goroutine 内的最终结果送回主协程。
	ret := make(chan error, 1)

	go func() {
		// f 作为复用容器，承载原始 protobuf bytes。
		f := &RawProtoFrame{}

		for {
			// 从入站连接接收一条消息。
			if err := src.RecvMsg(f); err != nil {
				ret <- err
				break
			}

			// 把入站消息转发到出站。
			if err := dst.SendMsg(f); err != nil {
				ret <- err
				break
			}
		}
	}()

	return ret
}
