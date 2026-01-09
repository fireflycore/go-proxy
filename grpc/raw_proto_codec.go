package grpc

import (
	"google.golang.org/grpc/encoding"
)

// RawProtoFrame 是代理层的“消息容器”：
// - payload 保存原始 protobuf 消息 bytes
// - RawProtoFrame 会把 RecvMsg/SendMsg 的 v 识别为 *RawProtoFrame，并直接读写 payload
type RawProtoFrame struct {
	// payload 为一条 protobuf message 的原始序列化结果。
	Payload []byte
}

// RawProtoCodec 是代理 codec：
// - 若 v 是 *RawProtoFrame：直接透传 payload bytes
// - 否则：回退到 baseProtoCodec（标准 proto 编解码）
type RawProtoCodec struct{}

// baseProtoCodec 提供标准 proto 编解码能力。
// 这里使用内部实现作为 fallback，避免 grpc-go 的 codec registry 未注册 "proto" 时返回 nil。
var BaseProtoCodec encoding.Codec = ProtoCodec{}

func (RawProtoCodec) Name() string {
	return BaseProtoCodec.Name()
}

func (RawProtoCodec) Marshal(v any) ([]byte, error) {
	// 代理路径：识别 *frame 时直接返回原始 payload。
	f, ok := v.(*RawProtoFrame)
	if !ok {
		// 非代理路径：回退到标准 proto 编解码。
		return BaseProtoCodec.Marshal(v)
	}

	// 直接透传 Payload（不做拷贝，由上游保证不可变或自行管理）。
	return f.Payload, nil
}

func (RawProtoCodec) Unmarshal(data []byte, v any) error {
	// 代理路径：识别 *frame 时把原始 bytes 写入 payload。
	f, ok := v.(*RawProtoFrame)
	if !ok {
		// 非代理路径：回退到标准 proto 编解码。
		return BaseProtoCodec.Unmarshal(data, v)
	}

	// 空消息时显式设置为 nil，避免复用 frame 导致残留数据。
	if len(data) == 0 {
		f.Payload = nil
		return nil
	}

	// 复用底层数组以减少分配，并拷贝 data 避免引用 grpc 内部缓冲区。
	f.Payload = append(f.Payload[:0], data...)

	return nil
}
