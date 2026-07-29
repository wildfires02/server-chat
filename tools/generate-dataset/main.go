// 生成 IM 批量合成测试数据集 data.json 的独立工具。
//
// 使用方法:
//
//	go run ./tools/generate-dataset -num_users=100 -out=data.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"time"
)

// UserSeed 保存用户Seed的数据和运行状态。
type UserSeed struct {
	// CreatedAt 保存CreatedAt时间。
	CreatedAt string `json:"createdAt"`
	// Email 保存Email。
	Email string `json:"email"`
	// Passhash 保存Passhash。
	Passhash string `json:"passhash"`
	// Private 按键索引Private。
	Private map[string]string `json:"private"`
	// Public 按键索引公开资料。
	Public map[string]string `json:"public"`
	// Tags 保存Tags列表。
	Tags []string `json:"tags"`
	// State 保存状态。
	State string `json:"state"`
	// Status 按键索引状态。
	Status map[string]string `json:"status"`
	// Username 指示是否启用或满足Username。
	Username string `json:"username"`
}

// GroupSeed 保存GroupSeed的数据和运行状态。
type GroupSeed struct {
	// CreatedAt 保存CreatedAt时间。
	CreatedAt string `json:"createdAt"`
	// Name 保存名称。
	Name string `json:"name"`
	// Owner 保存Owner。
	Owner string `json:"owner"`
	// Tags 保存Tags列表。
	Tags []string `json:"tags"`
	// Public 按键索引公开资料。
	Public map[string]string `json:"public"`
}

// P2PUserRef 保存P2P用户Ref的数据和运行状态。
type P2PUserRef struct {
	// Name 保存名称。
	Name string `json:"name"`
}

// P2PSeed 保存P2PSeed的数据和运行状态。
type P2PSeed struct {
	// CreatedAt 保存CreatedAt时间。
	CreatedAt string `json:"createdAt"`
	// Users 指示是否启用或满足Users。
	Users []P2PUserRef `json:"users"`
}

// GroupSubSeed 保存Group订阅Seed的数据和运行状态。
type GroupSubSeed struct {
	// CreatedAt 保存CreatedAt时间。
	CreatedAt string `json:"createdAt"`
	// Topic 保存Topic。
	Topic string `json:"topic"`
	// User 指示是否启用或满足用户。
	User string `json:"user"`
}

// Dataset 保存Dataset的数据和运行状态。
type Dataset struct {
	// Users 指示是否启用或满足Users。
	Users []UserSeed `json:"users"`
	// GroupTopics 保存GroupTopics列表。
	GroupTopics []GroupSeed `json:"grouptopics"`
	// P2PSubs 保存P2PSubs列表。
	P2PSubs []P2PSeed `json:"p2psubs"`
	// GroupSubs 保存GroupSubs列表。
	GroupSubs []GroupSubSeed `json:"groupsubs"`
}

// main 解析启动参数、初始化依赖并运行当前服务或命令。
func main() {
	numUsers := flag.Int("num_users", 50, "生成的测试用户账号数量")
	outFile := flag.String("out", "", "输出的 JSON 数据集文件路径 (默认为标准输出 stdout)")
	flag.Parse()

	if *numUsers < 2 {
		fmt.Fprintf(os.Stderr, "num_users 必须至少为 2\n")
		os.Exit(1)
	}

	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	dataset := Dataset{
		Users:       make([]UserSeed, 0, *numUsers),
		GroupTopics: make([]GroupSeed, 0),
		P2PSubs:     make([]P2PSeed, 0),
		GroupSubs:   make([]GroupSubSeed, 0),
	}

	userNames := make([]string, *numUsers)
	for i := 0; i < *numUsers; i++ {
		name := fmt.Sprintf("user%d", i)
		userNames[i] = name
		photo := fmt.Sprintf("https://picsum.photos/seed/%s/200", name)

		dataset.Users = append(dataset.Users, UserSeed{
			CreatedAt: fmt.Sprintf("-%dh", r.Intn(300)+1),
			Email:     fmt.Sprintf("%s@example.com", name),
			Passhash:  fmt.Sprintf("%s123", name),
			Private:   map[string]string{"comment": "some comment 123"},
			Public: map[string]string{
				"fn":    name,
				"photo": photo,
				"type":  "jpg",
			},
			Tags:     []string{name},
			State:    "ok",
			Status:   map[string]string{"text": fmt.Sprintf("my status %s", name)},
			Username: name,
		})
	}

	// 生成群组 Topic
	numGroups := r.Intn(*numUsers/2) + 1
	for i := 0; i < numGroups; i++ {
		owner := userNames[r.Intn(len(userNames))]
		photo := fmt.Sprintf("https://picsum.photos/seed/group%d/200", i)
		dataset.GroupTopics = append(dataset.GroupTopics, GroupSeed{
			CreatedAt: fmt.Sprintf("-%dh", r.Intn(300)+1),
			Name:      fmt.Sprintf("*ABCgroup%d", i),
			Owner:     owner,
			Tags:      []string{fmt.Sprintf("group%d", i)},
			Public: map[string]string{
				"fn":    fmt.Sprintf("My group %d", i),
				"photo": photo,
				"type":  "jpg",
			},
		})
	}

	// 生成 P2P 会话订阅关联
	for i := 0; i < *numUsers-1; i++ {
		rem := *numUsers - i - 1
		numContacts := r.Intn(min(15, rem)) + 1
		for c := 0; c < numContacts; c++ {
			targetIdx := i + 1 + r.Intn(rem)
			dataset.P2PSubs = append(dataset.P2PSubs, P2PSeed{
				CreatedAt: fmt.Sprintf("-%dh", r.Intn(300)+1),
				Users: []P2PUserRef{
					{Name: userNames[i]},
					{Name: userNames[targetIdx]},
				},
			})
		}
	}

	// 生成群组成员订阅关联
	for i := 0; i < numGroups; i++ {
		maxSize := min(10, *numUsers)
		groupSize := r.Intn(maxSize) + 1
		if maxSize >= 2 && groupSize < 2 {
			groupSize = 2
		}
		perm := r.Perm(*numUsers)
		for j := 0; j < groupSize; j++ {
			uid := userNames[perm[j]]
			dataset.GroupSubs = append(dataset.GroupSubs, GroupSubSeed{
				CreatedAt: fmt.Sprintf("-%dh", r.Intn(300)+1),
				Topic:     fmt.Sprintf("*ABCgroup%d", i),
				User:      uid,
			})
		}
	}

	// 序列化 JSON 输出
	jsonData, err := json.MarshalIndent(dataset, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "序列化 JSON 失败: %v\n", err)
		os.Exit(1)
	}

	if *outFile != "" {
		err = os.WriteFile(*outFile, jsonData, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "写入文件失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("成功将合成的包含 %d 个用户的测试数据集生成至: %s\n", *numUsers, *outFile)
	} else {
		os.Stdout.Write(jsonData)
	}
}
