package grpc

import "google.golang.org/grpc"

// RegisterService 注册代理服务
func RegisterService(server *grpc.Server, director StreamDirector, serviceName string, methodNames ...string) {
	// streamer 作为 service implementation 挂载在 server 上。
	streamer := &Handler{director: director}

	// fakeDesc 用于“伪造”一个服务描述，从而只暴露指定的方法列表。
	fakeDesc := &grpc.ServiceDesc{
		// ServiceName 必须与客户端调用的 fully-qualified service 一致。
		ServiceName: serviceName,
		// HandlerType 仅用于满足 grpc.RegisterService 的形状要求。
		HandlerType: (*interface{})(nil),
	}

	// 为每个方法名注册一个同名 stream handler。
	for _, m := range methodNames {
		// 统一将每个方法当作双向流处理，handler 内部按 bytes 透传。
		streamDesc := grpc.StreamDesc{
			// StreamName 对应方法名（不含 service 前缀）。
			StreamName: m,
			// Handler 指向透明代理的核心处理函数。
			Handler: streamer.Handler,
			// 允许服务端流。
			ServerStreams: true,
			// 允许客户端流。
			ClientStreams: true,
		}

		// 累积所有被允许的方法。
		fakeDesc.Streams = append(fakeDesc.Streams, streamDesc)
	}

	// 将 fakeDesc 注册到 grpc server。
	server.RegisterService(fakeDesc, streamer)
}
