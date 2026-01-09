## go-proxy

基于 gRPC 的透明代理库，提供：
- UnknownServiceHandler 透明转发（按 fullMethodName 路由）
- metadata 透传
- 白名单代理注册（指定 service/method）
- 内置原始 protobuf bytes 转发 codec（无需目标消息类型）

## 安装

```bash
go get github.com/fireflycore/go-proxy
```

## 目录

- [grpc](grpc/README.md)：gRPC 透明代理实现
