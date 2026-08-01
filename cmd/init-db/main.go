// Package main 实现数据库初始化命令。
package main

import (
	crand "crypto/rand"
	"encoding/json"
	"flag"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"chat/internal/configutil"
	"chat/server/auth"
	_ "chat/server/db/mongodb"
	_ "chat/server/db/mysql"
	_ "chat/server/db/postgres"
	_ "chat/server/db/rethinkdb"
	"chat/server/store"
	"chat/server/store/types"
)

// configType 保存配置Type的数据和运行状态。
type configType struct {
	// P2PDeleteEnabled 保存P2P删除Enabled。
	P2PDeleteEnabled bool `json:"p2p_delete_enabled"`
	// StoreConfig 保存存储配置。
	StoreConfig json.RawMessage `json:"store_config"`
}

// theCard 保存theCard的数据和运行状态。
type theCard struct {
	// Fn 保存Fn。
	Fn string `json:"fn"`
	// Photo 保存Photo。
	Photo string `json:"photo"`
	// Type 保存Type。
	Type string `json:"type"`
}

// tPrivate 保存tPrivate的数据和运行状态。
type tPrivate struct {
	// Comment 保存Comment。
	Comment string `json:"comment"`
}

// tTrusted 保存t可信资料的数据和运行状态。
type tTrusted struct {
	// Verified 保存Verified。
	Verified bool `json:"verified,omitempty"`
	// Staff 保存Staff。
	Staff bool `json:"staff,omitempty"`
}

// IsZero 判断对象是否满足 Is Zero 条件。
func (t tTrusted) IsZero() bool {
	return !t.Verified && !t.Staff
}

//DefAccess是默认访问模式。
type DefAccess struct {
	// Auth 保存认证。
	Auth string `json:"auth"`
	// Anon 保存Anon。
	Anon string `json:"anon"`
}

/*
User object in data.json

	   "createdAt": "-140h",
	   "email": "alice@example.com",
	   "tel": "17025550001",
	   "passhash": "alice123",
	   "private": {"comment": "some comment 123"},
	   "public": {"fn": "Alice Johnson", "photo": "alice-64.jpg", "type": "jpg"},
	   "state": "ok",
	   "authLevel": "auth",
	   "status": {
	     "text": "DND"
	   },
	   "username": "alice",
		"tags": ["tag1"],
		"addressBook": ["email:bob@example.com", "email:carol@example.com", "email:dave@example.com",
			"email:eve@example.com","email:frank@example.com","email:george@example.com","email:tob@example.com",
			"tel:17025550001", "tel:17025550002", "tel:17025550003", "tel:17025550004", "tel:17025550005",
			"tel:17025550006", "tel:17025550007", "tel:17025550008", "tel:17025550009"]
	  }
*/
type User struct {
	// CreatedAt 保存CreatedAt时间。
	CreatedAt string `json:"createdAt"`
	// Email 保存Email。
	Email string `json:"email"`
	// Tel 保存Tel。
	Tel string `json:"tel"`
	// AuthLevel 保存认证Level。
	AuthLevel string `json:"authLevel"`
	// Username 指示是否启用或满足Username。
	Username string `json:"username"`
	// Password 保存密码。
	Password string `json:"passhash"`
	// Private 保存Private。
	Private tPrivate `json:"private"`
	// Public 保存公开资料。
	Public theCard `json:"public"`
	// Trusted 保存可信资料。
	Trusted tTrusted `json:"trusted"`
	// State 保存状态。
	State string `json:"state"`
	// Status 保存状态。
	Status any `json:"status"`
	// AddressBook 保存AddressBook列表。
	AddressBook []string `json:"addressBook"`
	// Tags 保存Tags列表。
	Tags []string `json:"tags"`
}

/*
GroupTopic object in data.json

	"createdAt": "-128h",
	"name": "*ABC",
	"owner": "carol",
	"channel": true,
	"public": {"fn": "Let's talk about flowers", "photo": "abc-64.jpg", "type": "jpg"}
*/
type GroupTopic struct {
	// CreatedAt 保存CreatedAt时间。
	CreatedAt string `json:"createdAt"`
	// Name 保存名称。
	Name string `json:"name"`
	// Owner 保存Owner。
	Owner string `json:"owner"`
	// Channel 保存通道。
	Channel bool `json:"channel"`
	// Public 保存公开资料。
	Public theCard `json:"public"`
	// Trusted 保存可信资料。
	Trusted tTrusted `json:"trusted"`
	// Access 保存Access。
	Access DefAccess `json:"access"`
	// Tags 保存Tags列表。
	Tags []string `json:"tags"`
	// OwnerPrivate 保存OwnerPrivate。
	OwnerPrivate tPrivate `json:"ownerPrivate"`
}

/*
GroupSub object in data.json

	"createdAt": "-112h",
	"private": "My super cool group topic",
	"topic": "*ABC",
	"user": "alice",
	"asChan: false,
	"want": "JRWPSA",
	"have": "JRWP"
*/
type GroupSub struct {
	// CreatedAt 保存CreatedAt时间。
	CreatedAt string `json:"createdAt"`
	// Private 保存Private。
	Private tPrivate `json:"private"`
	// Topic 保存Topic。
	Topic string `json:"topic"`
	// User 指示是否启用或满足用户。
	User string `json:"user"`
	// AsChan 保存AsChan。
	AsChan bool `json:"asChan"`
	// Want 保存Want。
	Want string `json:"want"`
	// Have 保存Have。
	Have string `json:"have"`
}

/*
P2PUser topic in data.json

"createdAt": "-117h",
"users": [

	{"name": "eve", "private": {"comment":"ho ho"}, "want": "JRWP", "have": "N"},
	{"name": "alice", "private": {"comment": "ha ha"}}

]
*/
type P2PUser struct {
	// Name 保存名称。
	Name string `json:"name"`
	// Private 保存Private。
	Private tPrivate `json:"private"`
	// Want 保存Want。
	Want string `json:"want"`
	// Have 保存Have。
	Have string `json:"have"`
}

// P2PSub is a p2p 订阅 in data.json
type P2PSub struct {
	// CreatedAt 保存CreatedAt时间。
	CreatedAt string `json:"createdAt"`
	// Users 指示是否启用或满足Users。
	Users []P2PUser `json:"users"`
	//缓存值“user1:user2”作为代理主题名称
	pair string
}

// Data is a 消息 in data.json.
type Data struct {
	// Users 指示是否启用或满足Users。
	Users []User `json:"users"`
	// Grouptopics 保存Grouptopics列表。
	Grouptopics []GroupTopic `json:"grouptopics"`
	// Groupsubs 保存Groupsubs列表。
	Groupsubs []GroupSub `json:"groupsubs"`
	// P2psubs 保存P2psubs列表。
	P2psubs []P2PSub `json:"p2psubs"`
	// Messages 保存Messages列表。
	Messages []string `json:"messages"`
	// Forms 保存Forms列表。
	Forms []map[string]any `json:"forms"`
	// datapath 保存datapath。
	datapath string
}

// 生成随机字符串作为组主题的名称
func genTopicName() string {
	return "grp" + store.Store.GetUidString()
}

// 生成长度为n的密码
func getPassword(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-/.+?=&"

	rbuf := make([]byte, n)
	if _, err := crand.Read(rbuf); err != nil {
		log.Fatalln("Unable to generate password", err)
	}

	passwd := make([]byte, n)
	for i, r := range rbuf {
		passwd[i] = letters[int(r)%len(letters)]
	}

	return string(passwd)
}

// main 解析启动参数、初始化依赖并运行当前服务或命令。
func main() {
	reset := flag.Bool("reset", false, "force database reset")
	upgrade := flag.Bool("upgrade", false, "perform database version upgrade")
	noInit := flag.Bool("no_init", false, "check that database exists but don't create if missing")
	addRoot := flag.String("add_root", "", "create ROOT user, auth scheme 'basic'")
	makeRoot := flag.String("make_root", "", "promote ordinary user to ROOT, auth scheme 'basic'")
	datafile := flag.String("data", "", "name of file with sample data to load")
	conffile := flag.String("config", "configs/init-db.yaml", "数据库初始化 YAML 配置文件路径")

	flag.Parse()

	var data Data
	if *datafile != "" && *datafile != "-" {
		raw, err := os.ReadFile(*datafile)
		if err != nil {
			log.Fatalln("Failed to read sample data file:", err)
		}
		err = json.Unmarshal(raw, &data)
		if err != nil {
			log.Fatalln("Failed to parse sample data:", err)
		}
	}

	rand.Seed(time.Now().UnixNano())
	data.datapath, _ = filepath.Split(*datafile)

	var config configType
	if err := configutil.DecodeFile(*conffile, &config); err != nil {
		log.Fatal(err)
	}

	err := store.Store.Open(1, config.StoreConfig)
	defer store.Store.Close()

	adapterVersion := store.Store.GetAdapterVersion()
	databaseVersion := 0
	if store.Store.IsOpen() {
		databaseVersion = store.Store.GetDbVersion()
	}
	log.Printf("Database adapter: '%s'; version: %d", store.Store.GetAdapterName(), adapterVersion)

	var created bool

	if err != nil {
		if strings.Contains(err.Error(), "Database not initialized") {
			if *noInit {
				log.Fatalln("Database not found.")
			}
			if *reset {
				log.Println("Database is missing or uninitialized. Reset requested; recreating.")
			} else {
				log.Println("Database not found. Creating.")
			}
			err = store.Store.InitDb(config.StoreConfig, *reset)
			if err == nil {
				log.Println("Database successfully created.")
				created = true
			}
		} else if strings.Contains(err.Error(), "Invalid database version") {
			msg := "Wrong DB version: expected " + strconv.Itoa(adapterVersion) + ", got " +
				strconv.Itoa(databaseVersion) + "."

			if *reset {
				log.Println(msg, "Reset Requested. Dropping and recreating the database.")
				err = store.Store.InitDb(config.StoreConfig, true)
				if err == nil {
					log.Println("Database successfully reset.")
				}
			} else if *upgrade {
				if databaseVersion > adapterVersion {
					log.Fatalln(msg, "Unable to upgrade: database has greater version than the adapter.")
				}
				log.Println(msg, "Upgrading the database.")
				err = store.Store.UpgradeDb(config.StoreConfig)
				if err == nil {
					log.Println("Database successfully upgraded.")
				}
			} else {
				log.Fatalln(msg, "Use --reset to reset, --upgrade to upgrade.")
			}
		} else {
			log.Fatalln("Failed to init DB adapter:", err)
		}
	} else if *reset {
		log.Println("Reset requested. Dropping and recreating the database.")
		err = store.Store.InitDb(config.StoreConfig, true)
		if err == nil {
			log.Println("Database successfully reset.")
		}
	} else {
		log.Println("Database exists, version is correct.")
	}

	if err != nil {
		log.Fatalln("Failure:", err)
	}

	if *reset || created {
		genDb(&data, config.P2PDeleteEnabled)
	} else if len(data.Users) > 0 {
		log.Println("Sample data ignored.")
	}

	// Promote existing 用户 account to root
	if *makeRoot != "" {
		adapter := store.Store.GetAdapter()
		userId := types.ParseUserId(*makeRoot)
		if userId.IsZero() {
			log.Fatalf("Must specify a valid user ID '%s' to promote to ROOT", *makeRoot)
		}
		if err := adapter.AuthUpdRecord(userId, "basic", "", auth.LevelRoot, nil, time.Time{}); err != nil {
			log.Fatalln("Failed to promote user to ROOT", err)
		}
		log.Printf("User '%s' promoted to ROOT", *makeRoot)
	}

	// Create root 用户 account.
	if *addRoot != "" {
		var password string
		parts := strings.Split(*addRoot, ":")
		uname := parts[0]
		if len(uname) < 3 {
			log.Fatalf("Failed to create a ROOT user: username '%s' is too short", uname)
		}

		if len(parts) == 1 || parts[1] == "" {
			password = getPassword(10)
		} else {
			password = parts[1]
		}

		var user types.User
		user.Public = &card{
			Fn: "ROOT " + uname,
		}
		store.Users.Create(&user, nil)

		if _, err := store.Users.Create(&user, nil); err != nil {
			log.Fatalln("Failed to create ROOT user:", err)
		}

		authHandler := store.Store.GetAuthHandler("basic")
		if _, err := authHandler.AddRecord(&auth.Rec{Uid: user.Uid(), AuthLevel: auth.LevelRoot},
			[]byte(uname+":"+password), ""); err != nil {
			store.Users.Delete(user.Uid(), true)
			log.Fatalln("Failed to add ROOT auth record:", err)
		}
		log.Printf("ROOT user created: '%s:%s'", uname, password)
	}

	log.Println("All done.")

	os.Exit(0)
}
