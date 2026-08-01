// Package main 实现 REST 认证示例服务。
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
)

// UserPublicPhoto 保存用户公开资料Photo的数据和运行状态。
type UserPublicPhoto struct {
	// Data 保存数据。
	Data string `json:"data,omitempty"`
	// Type 保存Type。
	Type string `json:"type,omitempty"`
}

// UserPublic 保存用户公开资料的数据和运行状态。
type UserPublic struct {
	// Fn 保存Fn。
	Fn string `json:"fn,omitempty"`
	// Photo 保存Photo。
	Photo *UserPublicPhoto `json:"photo,omitempty"`
}

// UserData 保存用户数据的数据和运行状态。
type UserData struct {
	// Anon 保存Anon。
	Anon string `json:"anon,omitempty"`
	// Auth 保存认证。
	Auth string `json:"auth,omitempty"`
	// AuthLvl 保存认证Lvl。
	AuthLvl string `json:"authlvl,omitempty"`
	// Features 保存Features。
	Features string `json:"features,omitempty"`
	// Password 保存密码。
	Password string `json:"password,omitempty"`
	// Private 保存Private。
	Private string `json:"private,omitempty"`
	// Public 保存公开资料。
	Public *UserPublic `json:"public,omitempty"`
	// Tags 保存Tags列表。
	Tags []string `json:"tags,omitempty"`
	// UID 保存用户标识。
	UID string `json:"uid,omitempty"`
}

// Rec 保存Rec的数据和运行状态。
type Rec struct {
	// UID 保存用户标识。
	UID string `json:"uid,omitempty"`
	// AuthLvl 保存认证Lvl。
	AuthLvl string `json:"authlvl,omitempty"`
	// Features 保存Features。
	Features string `json:"features,omitempty"`
	// Tags 保存Tags列表。
	Tags []string `json:"tags,omitempty"`
}

// NewAcc 保存NewAcc的数据和运行状态。
type NewAcc struct {
	// Auth 保存认证。
	Auth string `json:"auth,omitempty"`
	// Anon 保存Anon。
	Anon string `json:"anon,omitempty"`
	// Public 保存公开资料。
	Public *UserPublic `json:"public,omitempty"`
	// Private 保存Private。
	Private string `json:"private,omitempty"`
}

// AuthRequest 保存认证请求的数据和运行状态。
type AuthRequest struct {
	// Secret 保存密钥。
	Secret string `json:"secret"`
}

// LinkRequest 保存Link请求的数据和运行状态。
type LinkRequest struct {
	// Rec 保存Rec。
	Rec *Rec `json:"rec"`
	// Secret 保存密钥。
	Secret string `json:"secret"`
}

var (
	// dummyData 保存dummy数据的共享实例或运行状态。
	dummyData = make(map[string]*UserData)
	// dataMu 保存数据Mu的共享实例或运行状态。
	dataMu sync.RWMutex
	// dataFile 保存数据文件的共享实例或运行状态。
	dataFile = "dummy_data.json"
)

// parseSecret 将输入解析为密钥。
func parseSecret(encodedSecret string) (string, string) {
	data, err := base64.StdEncoding.DecodeString(encodedSecret)
	if err != nil {
		return "", ""
	}
	parts := strings.SplitN(string(data), ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return parts[0], ""
}

// loadDummyData 查询并返回Dummy数据。
func loadDummyData(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dataMu.Lock()
	defer dataMu.Unlock()
	return json.Unmarshal(data, &dummyData)
}

// saveDummyData 保存Dummy数据。
func saveDummyData(path string) error {
	dataMu.RLock()
	data, err := json.MarshalIndent(dummyData, "", "  ")
	dataMu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// jsonResponse 完成json响应所需的内部处理。
func jsonResponse(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// handleIndex 处理索引消息或事件。
func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		jsonResponse(w, http.StatusNotFound, map[string]string{"err": "not found"})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`Sample IM REST/JSON-RPC authentication service.`))
}

// handleUnsupported 处理Unsupported消息或事件。
func handleUnsupported(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]string{"err": "method not allowed"})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"err": "unsupported"})
}

// handleAuth 处理认证消息或事件。
func handleAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]string{"err": "method not allowed"})
		return
	}

	var req AuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Secret == "" {
		jsonResponse(w, http.StatusOK, map[string]string{"err": "malformed"})
		return
	}

	uname, password := parseSecret(req.Secret)

	dataMu.RLock()
	user, exists := dummyData[uname]
	dataMu.RUnlock()

	if !exists {
		jsonResponse(w, http.StatusOK, map[string]string{"err": "not found"})
		return
	}

	if user.Password != password {
		jsonResponse(w, http.StatusOK, map[string]string{"err": "failed"})
		return
	}

	resp := make(map[string]interface{})

	if user.UID != "" {
		//现有用户
		resp["rec"] = map[string]interface{}{
			"uid":      user.UID,
			"authlvl":  user.AuthLvl,
			"features": user.Features,
		}
	} else {
		//首次登录：告诉IM创建一个新帐户
		resp["rec"] = map[string]interface{}{
			"authlvl":  user.AuthLvl,
			"tags":     user.Tags,
			"features": user.Features,
		}
		resp["newacc"] = map[string]interface{}{
			"auth":    user.Auth,
			"anon":    user.Anon,
			"public":  user.Public,
			"private": user.Private,
		}
	}

	jsonResponse(w, http.StatusOK, resp)
}

// handleLink 处理Link消息或事件。
func handleLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]string{"err": "method not allowed"})
		return
	}

	var req LinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Rec == nil || req.Rec.UID == "" || req.Secret == "" {
		jsonResponse(w, http.StatusOK, map[string]string{"err": "malformed"})
		return
	}

	uname, _ := parseSecret(req.Secret)

	dataMu.Lock()
	user, exists := dummyData[uname]
	if !exists {
		dataMu.Unlock()
		jsonResponse(w, http.StatusOK, map[string]string{"err": "not found"})
		return
	}

	if user.UID != "" {
		dataMu.Unlock()
		jsonResponse(w, http.StatusOK, map[string]string{"err": "duplicate value"})
		return
	}

	user.UID = req.Rec.UID
	dataMu.Unlock()

	if err := saveDummyData(dataFile); err != nil {
		log.Printf("Failed to save dummy data: %v", err)
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{})
}

// handleRtags 处理Rtags消息或事件。
func handleRtags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]string{"err": "method not allowed"})
		return
	}
	regexBase64 := base64.StdEncoding.EncodeToString([]byte("^[a-z0-9_]{3,8}$"))
	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"strarr":  []string{"rest", "email"},
		"byteval": regexBase64,
	})
}

// main 解析启动参数、初始化依赖并运行当前服务或命令。
func main() {
	port := flag.Int("port", 8080, "Port to listen on")
	flag.StringVar(&dataFile, "data", "dummy_data.json", "Path to dummy data file")
	flag.Parse()

	if err := loadDummyData(dataFile); err != nil {
		log.Printf("Warning: failed to load dummy data from %s: %v", dataFile, err)
	} else {
		log.Printf("Loaded dummy data from %s (%d users)", dataFile, len(dummyData))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/auth", handleAuth)
	mux.HandleFunc("/link", handleLink)
	mux.HandleFunc("/rtagns", handleRtags)
	mux.HandleFunc("/add", handleUnsupported)
	mux.HandleFunc("/checkunique", handleUnsupported)
	mux.HandleFunc("/del", handleUnsupported)
	mux.HandleFunc("/gen", handleUnsupported)
	mux.HandleFunc("/upd", handleUnsupported)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("Starting IM REST Auth Service (Go) on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
