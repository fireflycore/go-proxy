package grpc

import (
	"fmt"

	"google.golang.org/protobuf/proto"
)

// ProtoCodec 是最小可用的 proto 编解码实现，仅用于 fallback。
type ProtoCodec struct{}

func (ProtoCodec) Name() string {
	// 名称保持为 "proto"，与 gRPC 默认 proto codec 语义一致。
	return "proto"
}

func (ProtoCodec) Marshal(v any) ([]byte, error) {
	// 标准 proto 编解码要求 v 为 proto.Message。
	m, ok := v.(proto.Message)
	if !ok {
		// 类型不匹配时返回明确错误，便于定位调用方问题。
		return nil, fmt.Errorf("expected proto.Message, got %T", v)
	}

	// 序列化为 protobuf bytes。
	return proto.Marshal(m)
}

func (ProtoCodec) Unmarshal(data []byte, v any) error {
	// 标准 proto 编解码要求 v 为 proto.Message。
	m, ok := v.(proto.Message)
	if !ok {
		// 类型不匹配时返回明确错误，便于定位调用方问题。
		return fmt.Errorf("expected proto.Message, got %T", v)
	}

	// 反序列化 protobuf bytes 到目标消息。
	return proto.Unmarshal(data, m)
}
