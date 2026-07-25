# IM Protocol Buffer & gRPC 包 (`pbx`)

本目录（`chat/pbx`）为 IM 服务端、客户端以及扩展插件的原生 **Go gRPC Protocol Buffer SDK 包**。

## 包含文件

- `model.proto`：IM Protocol Buffer 协议定义文件。
- `model.pb.go`：编译生成的 Go 报文与数据类型。
- `model_grpc.pb.go`：编译生成的 Go gRPC 服务客户端与服务端 Stub 接口。
- `go-generate.sh`：自动编译生成 Go 代码的脚本。

## 代码生成

若修改了 `model.proto` 协议定义，可通过执行 `go-generate.sh` 重新编译出最新的 Go gRPC 代码：

```bash
./go-generate.sh
```

或者使用 Go 标准构建命令：

```bash
go generate ./...
```
