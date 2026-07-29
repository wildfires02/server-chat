# IM Protobuf 与 gRPC 协议包

本目录（`chat/api/pbx`）为服务端、客户端和扩展插件共享的 Go gRPC SDK。

## 包含文件

- `model.proto`：IM Protocol Buffer 协议定义文件。
- `model.pb.go`：编译生成的 Go 报文与数据类型。
- `model_grpc.pb.go`：编译生成的 Go gRPC 服务客户端与服务端 Stub 接口。
- `go-generate.sh`：自动编译生成 Go 代码的脚本。

## 代码生成

若修改了 `model.proto` 协议定义，可在仓库根目录执行生成脚本，重新生成 Go gRPC 代码：

```bash
./api/pbx/go-generate.sh
```

或者使用 Go 标准构建命令：

```bash
go generate ./internal/server
```
