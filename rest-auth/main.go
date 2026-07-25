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

type UserPublicPhoto struct {
	Data string `json:"data,omitempty"`
	Type string `json:"type,omitempty"`
}

type UserPublic struct {
	Fn    string           `json:"fn,omitempty"`
	Photo *UserPublicPhoto `json:"photo,omitempty"`
}

type UserData struct {
	Anon     string      `json:"anon,omitempty"`
	Auth     string      `json:"auth,omitempty"`
	AuthLvl  string      `json:"authlvl,omitempty"`
	Features string      `json:"features,omitempty"`
	Password string      `json:"password,omitempty"`
	Private  string      `json:"private,omitempty"`
	Public   *UserPublic `json:"public,omitempty"`
	Tags     []string    `json:"tags,omitempty"`
	UID      string      `json:"uid,omitempty"`
}

type Rec struct {
	UID      string   `json:"uid,omitempty"`
	AuthLvl  string   `json:"authlvl,omitempty"`
	Features string   `json:"features,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

type NewAcc struct {
	Auth    string      `json:"auth,omitempty"`
	Anon    string      `json:"anon,omitempty"`
	Public  *UserPublic `json:"public,omitempty"`
	Private string      `json:"private,omitempty"`
}

type AuthRequest struct {
	Secret string `json:"secret"`
}

type LinkRequest struct {
	Rec    *Rec   `json:"rec"`
	Secret string `json:"secret"`
}

var (
	dummyData = make(map[string]*UserData)
	dataMu    sync.RWMutex
	dataFile  = "dummy_data.json"
)

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

func loadDummyData(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dataMu.Lock()
	defer dataMu.Unlock()
	return json.Unmarshal(data, &dummyData)
}

func saveDummyData(path string) error {
	dataMu.RLock()
	data, err := json.MarshalIndent(dummyData, "", "  ")
	dataMu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func jsonResponse(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		jsonResponse(w, http.StatusNotFound, map[string]string{"err": "not found"})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`Sample IM REST/JSON-RPC authentication service.`))
}

func handleUnsupported(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]string{"err": "method not allowed"})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"err": "unsupported"})
}

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
		// Existing user
		resp["rec"] = map[string]interface{}{
			"uid":      user.UID,
			"authlvl":  user.AuthLvl,
			"features": user.Features,
		}
	} else {
		// First login: tell IM to create a new account
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
