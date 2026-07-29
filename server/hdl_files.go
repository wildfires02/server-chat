/******************************************************************************
 *
 *  描述 :
 *
 *    大文件上传/下载处理器。先验证请求，然后调用对应的处理器。
 *
 *****************************************************************************/

// Package main 实现即时通信服务端的协议、路由和业务逻辑。
package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"chat/pbx"
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
	"google.golang.org/grpc/peer"
)

// 允许的用户提供的 Content-type 字段的 MIME 类型。必须按字母顺序排序。
// 不在列表中的类型会转换为 "application/octet-stream"。
// 参见 https://www.iana.org/assignments/media-types/media-types.xhtml
var allowedMimeTypes = []string{"application/", "audio/", "font/", "image/", "text/", "video/"}

// largeFileServeHTTP 完成large文件ServeHTTP所需的内部处理。
func largeFileServeHTTP(wrt http.ResponseWriter, req *http.Request) {
	now := types.TimeNow()
	enc := json.NewEncoder(wrt)
	mh := store.Store.GetMediaHandler()
	statsInc("FileDownloadsTotal", 1)

	writeHttpResponse := func(msg *ServerComMessage, err error) {
		// Gorilla CompressHandler 要求设置 Content-Type。
		wrt.Header().Set("Content-Type", "application/json; charset=utf-8")
		wrt.WriteHeader(msg.Ctrl.Code)
		enc.Encode(msg)
		if err != nil {
			logs.Warn.Println("media serve:", req.URL.String(), err)
		}
	}

	// 预检请求：在任何安全检查之前处理。
	if req.Method == http.MethodOptions {
		headers, statusCode, err := mh.Headers(req.Method, req.URL, req.Header, true)
		if err != nil {
			writeHttpResponse(decodeStoreError(err, "", now, nil), err)
			return
		}
		for name, values := range headers {
			for _, value := range values {
				wrt.Header().Add(name, value)
			}
		}
		if statusCode <= 0 {
			statusCode = http.StatusNoContent
		}
		wrt.WriteHeader(statusCode)
		logs.Info.Println("media serve: preflight completed")
		return
	}

	// 检查是否为 GET/HEAD 请求。
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		writeHttpResponse(ErrOperationNotAllowed("", "", now), errors.New("method '"+req.Method+"' not allowed"))
		return
	}

	// 检查 API key 是否存在
	if isValid, _ := checkAPIKey(getAPIKey(req)); !isValid {
		writeHttpResponse(ErrAPIKeyRequired(now), errors.New("invalid or missing API key"))
		return
	}

	// 检查授权：必须存在认证信息或 SID
	authMethod, secret := getHttpAuth(req)
	uid, challenge, err := authFileRequest(authMethod, secret, req.FormValue("sid"), getRemoteAddr(req))
	if err != nil {
		writeHttpResponse(decodeStoreError(err, "", now, nil), err)
		return
	}

	if challenge != nil {
		writeHttpResponse(InfoChallenge("", now, challenge), nil)
		return
	}

	if uid.IsZero() {
		// 未认证
		writeHttpResponse(ErrAuthRequired("", "", now, now), errors.New("user not authenticated"))
		return
	}

	// 检查 media handler 是否需要重定向或添加头部。
	headers, statusCode, err := mh.Headers(req.Method, req.URL, req.Header, true)
	if err != nil {
		writeHttpResponse(decodeStoreError(err, "", now, nil), err)
		return
	}

	for name, values := range headers {
		for _, value := range values {
			wrt.Header().Add(name, value)
		}
	}

	if statusCode != 0 {
		// 处理器请求终止后续处理。
		wrt.WriteHeader(statusCode)
		if req.Method == http.MethodGet {
			enc.Encode(&ServerComMessage{
				Ctrl: &MsgServerCtrl{
					Code:      statusCode,
					Text:      http.StatusText(statusCode),
					Timestamp: now,
				},
			})
		}
		logs.Info.Println("media serve: completed with status", statusCode, "uid=", uid)
		return
	}

	if req.Method == http.MethodHead {
		wrt.WriteHeader(http.StatusOK)
		logs.Info.Println("media serve: completed", req.Method, "uid=", uid)
		return
	}

	fd, rsc, err := mh.Download(req.URL.String())
	if err != nil {
		writeHttpResponse(decodeStoreError(err, "", now, nil), err)
		return
	}

	defer rsc.Close()

	wrt.Header().Set("Content-Type", fd.MimeType)
	asAttachment, _ := strconv.ParseBool(req.URL.Query().Get("asatt"))
	// 作为安全措施，强制 html 文件下载。
	asAttachment = asAttachment ||
		strings.Contains(fd.MimeType, "html") ||
		strings.Contains(fd.MimeType, "xml") ||
		strings.HasPrefix(fd.MimeType, "application/") ||
		// 'message'、'model' 和 'multipart' 目前不会出现，但仍进行检查，
		// 以防 DetectContentType 更改其逻辑。
		strings.HasPrefix(fd.MimeType, "message/") ||
		strings.HasPrefix(fd.MimeType, "model/") ||
		strings.HasPrefix(fd.MimeType, "multipart/") ||
		strings.HasPrefix(fd.MimeType, "text/")
	if asAttachment {
		wrt.Header().Set("Content-Disposition", "attachment")
	}

	http.ServeContent(wrt, req, "", fd.UpdatedAt, rsc)

	logs.Info.Println("media serve: OK, uid=", uid)
}

// largeFileReceiveHTTP 通过 HTTP(S) 从客户端接收文件，并将其传递给配置的 media 处理器。
func largeFileReceiveHTTP(wrt http.ResponseWriter, req *http.Request) {
	now := types.TimeNow()
	enc := json.NewEncoder(wrt)
	mh := store.Store.GetMediaHandler()
	statsInc("FileUploadsTotal", 1)

	writeHttpResponse := func(msg *ServerComMessage, err error) {
		// Gorilla CompressHandler 要求设置 Content-Type。
		wrt.Header().Set("Content-Type", "application/json; charset=utf-8")
		wrt.WriteHeader(msg.Ctrl.Code)
		enc.Encode(msg)

		if err != nil {
			logs.Info.Println("media upload:", msg.Ctrl.Code, msg.Ctrl.Text, "/", err)
		}
	}

	// 预检请求：在任何安全检查之前处理。
	if req.Method == http.MethodOptions {
		headers, statusCode, err := mh.Headers(req.Method, req.URL, req.Header, true)
		if err != nil {
			writeHttpResponse(decodeStoreError(err, "", now, nil), err)
			return
		}
		for name, values := range headers {
			for _, value := range values {
				wrt.Header().Add(name, value)
			}
		}
		if statusCode <= 0 {
			statusCode = http.StatusNoContent
		}
		wrt.WriteHeader(statusCode)
		logs.Info.Println("media upload: preflight completed")
		return
	}

	// 检查是否为 POST/PUT/HEAD 请求。
	if req.Method != http.MethodPost && req.Method != http.MethodPut && req.Method != http.MethodHead {
		writeHttpResponse(ErrOperationNotAllowed("", "", now), errors.New("method '"+req.Method+"' not allowed"))
		return
	}

	if globals.maxFileUploadSize > 0 {
		// 强制执行最大上传大小。
		req.Body = http.MaxBytesReader(wrt, req.Body, globals.maxFileUploadSize)
	}

	// 检查 API key 是否存在
	if isValid, _ := checkAPIKey(getAPIKey(req)); !isValid {
		writeHttpResponse(ErrAPIKeyRequired(now), nil)
		return
	}

	msgID := req.FormValue("id")
	// 检查授权：必须存在认证信息或 SID
	authMethod, secret := getHttpAuth(req)
	uid, challenge, err := authFileRequest(authMethod, secret, req.FormValue("sid"), getRemoteAddr(req))
	if err != nil {
		writeHttpResponse(decodeStoreError(err, msgID, now, nil), err)
		return
	}
	if challenge != nil {
		writeHttpResponse(InfoChallenge(msgID, now, challenge), nil)
		return
	}
	if uid.IsZero() && req.FormValue("topic") != "newacc" {
		// 未认证且非注册请求。
		writeHttpResponse(ErrAuthRequired(msgID, "", now, now), nil)
		return
	}

	// 检查上传是否在其他地方处理。
	headers, statusCode, err := mh.Headers(req.Method, req.URL, req.Header, true)
	if err != nil {
		logs.Info.Println("media upload: headers check failed", err)
		writeHttpResponse(decodeStoreError(err, "", now, nil), err)
		return
	}

	for name, values := range headers {
		for _, value := range values {
			wrt.Header().Add(name, value)
		}
	}

	if statusCode != 0 {
		// 处理器请求终止后续处理。
		wrt.WriteHeader(statusCode)
		if req.Method == http.MethodPost || req.Method == http.MethodPut {
			enc.Encode(&ServerComMessage{
				Ctrl: &MsgServerCtrl{
					Code:      statusCode,
					Text:      http.StatusText(statusCode),
					Timestamp: now,
				},
			})
		}
		logs.Info.Println("media upload: completed with status", statusCode)
		return
	}

	if req.Method == http.MethodHead || req.Method == http.MethodOptions {
		wrt.WriteHeader(http.StatusOK)
		logs.Info.Println("media upload: completed", req.Method)
		return
	}

	file, header, err := req.FormFile("file")
	if err != nil {
		logs.Info.Println("media upload: invalid multipart form", err)
		if strings.Contains(err.Error(), "request body too large") {
			writeHttpResponse(ErrTooLarge(msgID, "", now), err)
		} else {
			writeHttpResponse(ErrMalformed(msgID, "", now), err)
		}
		return
	}

	buff := make([]byte, 512)
	if _, err = file.Read(buff); err != nil {
		writeHttpResponse(ErrUnknown(msgID, "", now), err)
		return
	}

	mimeType := http.DetectContentType(buff)
	// 如果 DetectContentType 失败，尝试使用客户端提供的内容类型。
	if mimeType == "application/octet-stream" {
		if userContentType, params, err := mime.ParseMediaType(header.Header.Get("Content-Type")); err == nil {
			// 确保 content-type 是合法的。
			for _, allowed := range allowedMimeTypes {
				if strings.HasPrefix(userContentType, allowed) {
					if userContentType = mime.FormatMediaType(userContentType, params); userContentType != "" {
						mimeType = userContentType
					}
					break
				}
			}
		}
	}

	fdef := &types.FileDef{
		ObjHeader: types.ObjHeader{
			Id: store.Store.GetUidString(),
		},
		User:     uid.String(),
		MimeType: mimeType,
	}
	fdef.InitTimes()

	if _, err = file.Seek(0, io.SeekStart); err != nil {
		writeHttpResponse(ErrUnknown(msgID, "", now), err)
		return
	}

	url, size, err := mh.Upload(fdef, file)
	if err != nil {
		logs.Info.Println("media upload: failed", file, "key", fdef.Location, err)
		store.Files.FinishUpload(fdef, false, 0)
		writeHttpResponse(decodeStoreError(err, msgID, now, nil), err)
		return
	}

	fdef, err = store.Files.FinishUpload(fdef, true, size)
	if err != nil {
		logs.Info.Println("media upload: failed to finalize", file, "key", fdef.Location, err)
		// 尽力清理。
		mh.Delete([]string{fdef.Location})
		writeHttpResponse(decodeStoreError(err, msgID, now, nil), err)
		return
	}

	params := map[string]string{"url": url}
	if globals.mediaGcPeriod > 0 {
		// 此文件在未附加到消息或 Topic 的情况下保证存在的时间。
		params["expires"] = now.Add(globals.mediaGcPeriod).Format(types.TimeFormatRFC3339)
	}

	writeHttpResponse(NoErrParams(msgID, "", now, params), nil)
	logs.Info.Println("media upload: ok", fdef.Id, fdef.Location)
}

// LargeFileServe 是 largeFileServeHTTP 的 gRPC 版本。
func (*grpcNodeServer) LargeFileServe(req *pbx.FileDownReq, stream pbx.Node_LargeFileServeServer) error {
	now := types.TimeNow()

	writeResponse := func(msg *ServerComMessage, err error) {
		stream.Send(&pbx.FileDownResp{Id: msg.Ctrl.Id, Code: int32(msg.Ctrl.Code), Text: msg.Ctrl.Text})
		if err != nil {
			logs.Info.Println("media serve:", msg.Ctrl.Code, msg.Ctrl.Text, "/", err)
		}
	}

	msgID := req.GetId()

	// 检查授权：认证信息必须存在（gRPC 不使用 SID）。
	authMethod, secret := req.Auth.Scheme, req.Auth.Secret
	var remoteAddr string
	if p, ok := peer.FromContext(stream.Context()); ok {
		remoteAddr = p.Addr.String()
	}
	uid, challenge, err := authFileRequest(authMethod, secret, "", remoteAddr)
	if err != nil {
		writeResponse(decodeStoreError(err, msgID, now, nil), err)
		return nil
	}

	if challenge != nil {
		writeResponse(InfoChallenge(msgID, now, challenge), nil)
		return nil
	}

	if uid.IsZero() {
		// 未认证
		writeResponse(ErrAuthRequired(msgID, "", now, now), errors.New("user not authenticated"))
		return nil
	}

	// 检查 media handler 是否需要重定向或添加头部。
	mh := store.Store.GetMediaHandler()
	url, _ := url.Parse(req.Uri)
	headers, statusCode, err := mh.Headers(http.MethodGet, url, http.Header{}, true)
	if err != nil {
		writeResponse(decodeStoreError(err, "", now, nil), err)
		return nil
	}

	resp := pbx.FileDownResp{Meta: &pbx.FileMeta{}}
	if statusCode != 0 {
		// 处理器请求终止后续处理。
		resp.Code = int32(statusCode)
		resp.Text = http.StatusText(statusCode)
		resp.RedirUrl = headers.Get("Location")
		stream.Send(&resp)
		logs.Info.Println("media serve: completed with status", statusCode, "uid=", uid)
		return nil
	}

	fd, rsc, err := mh.Download(req.GetUri())
	if err != nil {
		writeResponse(decodeStoreError(err, msgID, now, nil), err)
		return nil
	}

	defer rsc.Close()

	resp.Code = http.StatusOK
	resp.Text = http.StatusText(http.StatusOK)
	resp.Meta.Name = fd.Location
	resp.Meta.MimeType = fd.MimeType
	resp.Meta.Size = fd.Size

	resp.Content = make([]byte, 1024*1024*2)
	var n int
	result := "OK"
	for {
		n, err = rsc.Read(resp.Content)
		if err == nil {
			resp.Content = resp.Content[:n]
			if err = stream.Send(&resp); err != nil {
				logs.Info.Println("media serve: failed, uid=", uid, err)
				break
			}
			continue
		}
		if err == io.EOF {
			err = nil
		} else {
			result = err.Error()
		}
		break
	}
	logs.Info.Println("media serve: ", result, ", uid=", uid)
	return err
}

// LargeFileReceive 是 largeFileReceiveHTTP 的 gRPC 版本。
func (*grpcNodeServer) LargeFileReceive(stream pbx.Node_LargeFileReceiveServer) error {
	now := types.TimeNow()
	mh := store.Store.GetMediaHandler()

	writeResponse := func(msg *ServerComMessage, err error) {
		stream.SendAndClose(&pbx.FileUpResp{Id: msg.Ctrl.Id, Code: int32(msg.Ctrl.Code), Text: msg.Ctrl.Text})
		if err != nil {
			logs.Info.Println("media receive:", msg.Ctrl.Code, msg.Ctrl.Text, "/", err)
		}
	}

	req, err := stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			writeResponse(ErrDisconnected("", "", now), err)
		} else {
			writeResponse(decodeStoreError(err, "", now, nil), err)
		}
		return nil
	}

	msgID := req.GetId()
	// 检查授权：认证信息必须存在（gRPC 不使用 SID）。
	authMethod, secret := req.Auth.Scheme, req.Auth.Secret
	var remoteAddr string
	if p, ok := peer.FromContext(stream.Context()); ok {
		remoteAddr = p.Addr.String()
	}
	uid, challenge, err := authFileRequest(authMethod, secret, "", remoteAddr)
	if err != nil {
		writeResponse(decodeStoreError(err, msgID, now, nil), err)
		return nil
	}

	if challenge != nil {
		writeResponse(InfoChallenge(msgID, now, challenge), nil)
		return nil
	}

	if uid.IsZero() {
		// 未认证
		writeResponse(ErrAuthRequired(msgID, "", now, now), errors.New("user not authenticated"))
		return nil
	}

	// 检查上传是否在其他地方处理。
	headers, statusCode, err := mh.Headers(http.MethodPost, nil, http.Header{}, false)
	if err != nil {
		logs.Info.Println("media upload: headers check failed", err)
		writeResponse(decodeStoreError(err, "", now, nil), nil)
		return nil
	}

	if statusCode != 0 {
		// 处理器请求终止后续处理。
		err = stream.SendAndClose(&pbx.FileUpResp{
			Id:       msgID,
			Code:     int32(statusCode),
			Text:     http.StatusText(statusCode),
			RedirUrl: headers.Get("Location"),
		})
		logs.Info.Println("media upload: completed with status", statusCode, "uid=", uid, err)
		return err
	}

	mimeType := http.DetectContentType(req.Content)
	// 如果 DetectContentType 失败，使用客户端提供的内容类型。
	if mimeType == "application/octet-stream" {
		if contentType := req.Meta.GetMimeType(); contentType != "" {
			mimeType = contentType
		}
	}

	fdef := &types.FileDef{
		ObjHeader: types.ObjHeader{
			Id: store.Store.GetUidString(),
		},
		User:     uid.String(),
		MimeType: mimeType,
	}
	fdef.InitTimes()

	reader, writer := io.Pipe()
	// 创建一个非阻塞 Channel 来收集入站 IO 过程的错误。
	done := make(chan error, 1)
	go func() {
		defer writer.Close()
		for {
			if req, err := stream.Recv(); err == nil {
				chunk := req.GetContent()
				if _, err := writer.Write(chunk); err != nil {
					done <- err
					break
				}
			} else {
				if err == io.EOF {
					err = nil
				}
				done <- err
				break
			}
		}
	}()

	url, size, err := mh.Upload(fdef, reader)
	if err == nil {
		// 没有出站 IO 错误。也许有入站错误？
		err = <-done
	}
	if err != nil {
		logs.Info.Println("media upload: failed", req.Meta.Name, "key", fdef.Location, err)
		store.Files.FinishUpload(fdef, false, 0)
		writeResponse(decodeStoreError(err, msgID, now, nil), nil)
		return nil
	}

	err = stream.SendAndClose(&pbx.FileUpResp{
		Id:   msgID,
		Code: http.StatusOK,
		Text: http.StatusText(http.StatusOK),
		Meta: &pbx.FileMeta{
			Name:     url,
			MimeType: mimeType,
			Etag:     fdef.ETag,
			Size:     size,
		},
	})
	logs.Info.Println("media upload: ok", fdef.Id, fdef.Location, err)
	return err
}

// largeFileRunGarbageCollection 每隔 'period' 运行一次，最多删除 'blockSize' 个未使用的文件。
// 返回可用于停止进程的 Channel。
func largeFileRunGarbageCollection(period time.Duration, blockSize int) chan<- bool {
	// 无缓冲的停止 Channel。停止 gc 的人必须等待进程完成。
	stop := make(chan bool)
	go func() {
		// 为 tick 周期添加一些随机性以去同步集群节点的运行：
		// 0.75 * period + rand(0, 0.5) * period。
		period = (period >> 1) + (period >> 2) + time.Duration(rand.Intn(int(period>>1)))
		gcTicker := time.Tick(period)
		for {
			select {
			case <-gcTicker:
				if err := store.Files.DeleteUnused(time.Now().Add(-time.Hour), blockSize); err != nil {
					logs.Warn.Println("media gc:", err)
				}
			case <-stop:
				return
			}
		}
	}()

	return stop
}

// 认证非 WebSocket HTTP 请求
func authFileRequest(authMethod, secret, sid, remoteAddr string) (types.Uid, []byte, error) {
	var uid types.Uid
	if authMethod != "" {
		decodedSecret := make([]byte, base64.StdEncoding.DecodedLen(len(secret)))
		n, err := base64.StdEncoding.Decode(decodedSecret, []byte(secret))
		if err != nil {
			logs.Info.Println("media: invalid auth secret", authMethod, "'"+secret+"'")
			return uid, nil, types.ErrMalformed
		}

		if authhdl := store.Store.GetLogicalAuthHandler(authMethod); authhdl != nil {
			rec, challenge, err := authhdl.Authenticate(decodedSecret[:n], remoteAddr)
			if err != nil {
				return uid, nil, err
			}
			if challenge != nil {
				return uid, challenge, nil
			}
			uid = rec.Uid
		} else {
			logs.Info.Println("media: unknown auth method", authMethod)
			return uid, nil, types.ErrMalformed
		}
	} else {
		// 查找 Session，确保它已经过适当认证。
		sess := globals.sessionStore.Get(sid)
		if sess != nil {
			uid = sess.uid
		}
	}
	return uid, nil, nil
}
