package grpc

import "google.golang.org/grpc"

// NewProxy 创建一个透明代理 Server，并默认启用：
// - 原始 protobuf bytes 转发 codec（server 侧）
// - UnknownServiceHandler 透明转发（代理作为 client 连接到目标 server）
func NewProxy(dst *grpc.ClientConn, opts ...grpc.ServerOption) *grpc.Server {
	// defaultOpts 放在前面，允许调用方在 opts 中覆盖/追加行为。
	defaultOpts := []grpc.ServerOption{DefaultProxyServerOpt(), DefaultProxyOpt(dst)}
	// 将默认配置与调用方配置合并后创建 server。
	return grpc.NewServer(append(defaultOpts, opts...)...)
}

// DefaultProxyOpt 返回 UnknownServiceHandler 配置，使 server 能转发“未注册的服务/方法”。
func DefaultProxyOpt(cc *grpc.ClientConn) grpc.ServerOption {
	// TransparentHandler 会把未知方法转发给 director 选择的目标连接（代理作为 client）。
	return grpc.UnknownServiceHandler(TransparentHandler(DefaultDirector(cc)))
}

// DefaultProxyServerOpt 为代理 server 注入 codec。
// 该 codec 会把消息当作原始 bytes 透传，从而无需目标服务的具体消息类型。
func DefaultProxyServerOpt() grpc.ServerOption {
	// ForceServerCodec 保证服务端侧使用指定 codec 解/编码。
	return grpc.ForceServerCodec(RawProtoCodec{})
}

// DefaultClientCallOpts 返回代理 client stream 的默认调用选项。
// 通过强制 codec，确保 client stream 使用与 server 侧一致的“原始 bytes”协议。
func DefaultClientCallOpts() []grpc.CallOption {
	// ForceCodec 让 client stream 在 SendMsg/RecvMsg 时使用指定 codec。
	return []grpc.CallOption{grpc.ForceCodec(RawProtoCodec{})}
}
