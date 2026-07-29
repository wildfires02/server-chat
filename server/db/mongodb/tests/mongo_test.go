//go:build mongodb
// +build mongodb

// 用于测试其它数据库后端：
// 1) Create GetAdapter function inside your db backend adapter package (like one inside mongodb adapter)
// 2) Uncomment your db backend package ('backend' named package)
// 3) Write own initConnectionToDb and 'db' variable
// 4) Replace mongodb specific db queries inside test to your own queries.
// 5) Run.

// Package tests 提供数据库持久化、迁移或测试支持。
package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"reflect"
	"testing"
	"time"

	"chat/internal/configutil"
	adapter "chat/server/db"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	b "go.mongodb.org/mongo-driver/v2/bson"
	mdb "go.mongodb.org/mongo-driver/v2/mongo"
	mdbopts "go.mongodb.org/mongo-driver/v2/mongo/options"

	"chat/server/db/common"
	"chat/server/db/common/test_data"
	backend "chat/server/db/mongodb"
	"chat/server/logs"
	"chat/server/store/types"
)

// configType 保存配置Type的数据和运行状态。
type configType struct {
	// If Reset=true test will recreate 数据库 every time it runs
	Reset bool `json:"reset_db_data"`
	// Configurations for individual adapters.
	Adapters map[string]json.RawMessage `json:"adapters"`
}

// config 保存配置的共享实例或运行状态。
var config configType

// adp 保存adp的共享实例或运行状态。
var adp adapter.Adapter

// db 保存数据库的共享实例或运行状态。
var db *mdb.Database

// ctx 保存ctx的共享实例或运行状态。
var ctx context.Context

// testData 保存test数据的共享实例或运行状态。
var testData *test_data.TestData

// TestCreateDb 验证 Create Db 相关行为。
func TestCreateDb(t *testing.T) {
	if err := adp.CreateDb(config.Reset); err != nil {
		t.Fatal(err)
	}
}

// ================== Create tests ================================
func TestUserCreate(t *testing.T) {
	for _, user := range testData.Users {
		if err := adp.UserCreate(user); err != nil {
			t.Error(err)
		}
	}
	count, err := db.Collection("users").CountDocuments(ctx, b.M{})
	if err != nil {
		t.Error(err)
	}
	if count == 0 {
		t.Error("No users created!")
	}
}

// TestCredUpsert 验证 Cred Upsert 相关行为。
func TestCredUpsert(t *testing.T) {
	// Test just inserts:
	for i := 0; i < 2; i++ {
		inserted, err := adp.CredUpsert(testData.Creds[i])
		if err != nil {
			t.Fatal(err)
		}
		if !inserted {
			t.Error("Should be inserted, but updated")
		}
	}

	// Test duplicate:
	_, err := adp.CredUpsert(testData.Creds[1])
	if err != types.ErrDuplicate {
		t.Error("Should return duplicate error but got", err)
	}
	_, err = adp.CredUpsert(testData.Creds[2])
	if err != types.ErrDuplicate {
		t.Error("Should return duplicate error but got", err)
	}

	// Test add new unvalidated credentials
	inserted, err := adp.CredUpsert(testData.Creds[3])
	if err != nil {
		t.Fatal(err)
	}
	if !inserted {
		t.Error("Should be inserted, but updated")
	}
	inserted, err = adp.CredUpsert(testData.Creds[3])
	if err != nil {
		t.Fatal(err)
	}
	if inserted {
		t.Error("Should be updated, but inserted")
	}

	// Just insert other creds (used in other tests)
	for _, cred := range testData.Creds[4:] {
		_, err = adp.CredUpsert(cred)
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestAuthAddRecord 验证 Auth Add Record 相关行为。
func TestAuthAddRecord(t *testing.T) {
	for _, rec := range testData.Recs {
		err := adp.AuthAddRecord(types.ParseUserId("usr"+rec.UserId), rec.Scheme, rec.Unique,
			rec.AuthLvl, rec.Secret, rec.Expires)
		if err != nil {
			t.Fatal(err)
		}
	}
	//Test duplicate
	err := adp.AuthAddRecord(types.ParseUserId("usr"+testData.Users[0].Id), testData.Recs[0].Scheme,
		testData.Recs[0].Unique, testData.Recs[0].AuthLvl, testData.Recs[0].Secret, testData.Recs[0].Expires)
	if err != types.ErrDuplicate {
		t.Fatal("Should be duplicate error but got", err)
	}
}

// TestTopicCreate 验证 Topic Create 相关行为。
func TestTopicCreate(t *testing.T) {
	err := adp.TopicCreate(testData.Topics[0])
	if err != nil {
		t.Error(err)
	}
	for _, tpc := range testData.Topics[3:] {
		err = adp.TopicCreate(tpc)
		if err != nil {
			t.Error(err)
		}
	}
}

// TestTopicCreateP2P 验证 Topic Create P 2 P 相关行为。
func TestTopicCreateP2P(t *testing.T) {
	err := adp.TopicCreateP2P(testData.Subs[2], testData.Subs[3])
	if err != nil {
		t.Fatal(err)
	}

	oldModeGiven := testData.Subs[2].ModeGiven
	testData.Subs[2].ModeGiven = 255
	err = adp.TopicCreateP2P(testData.Subs[4], testData.Subs[2])
	if err != nil {
		t.Fatal(err)
	}
	var got types.Subscription
	err = db.Collection("subscriptions").FindOne(ctx, b.M{"_id": testData.Subs[2].Id}).Decode(&got)
	if err != nil {
		t.Fatal(err)
	}
	if got.ModeGiven == oldModeGiven {
		t.Error("ModeGiven update failed")
	}
}

// TestTopicShare 验证 Topic Share 相关行为。
func TestTopicShare(t *testing.T) {
	if err := adp.TopicShare(testData.Subs[0].Topic, testData.Subs); err != nil {
		t.Fatal(err)
	}
}

// TestMessageSave 验证 Message Save 相关行为。
func TestMessageSave(t *testing.T) {
	for _, msg := range testData.Msgs {
		err := adp.MessageSave(msg)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Some 消息 are soft deleted, but it's ignored by adp.MessageSave
	for _, msg := range testData.Msgs {
		if len(msg.DeletedFor) > 0 {
			for _, del := range msg.DeletedFor {
				toDel := types.DelMessage{
					Topic:       msg.Topic,
					DeletedFor:  del.User,
					DelId:       del.DelId,
					SeqIdRanges: []types.Range{{Low: msg.SeqId}},
				}
				adp.MessageDeleteList(msg.Topic, &toDel)
			}
		}
	}
}

// TestFileStartUpload 验证 File Start Upload 相关行为。
func TestFileStartUpload(t *testing.T) {
	for _, f := range testData.Files {
		err := adp.FileStartUpload(f)
		if err != nil {
			t.Fatal(err)
		}
	}
}

// ================== Read tests ==================================
func TestUserGet(t *testing.T) {
	// Test not found
	got, err := adp.UserGet(types.ParseUserId("dummyuserid"))
	if err == nil && got != nil {
		t.Error("user should be nil.")
	}

	got, err = adp.UserGet(types.ParseUserId("usr" + testData.Users[0].Id))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, testData.Users[0]) {
		t.Error(mismatchErrorString("User", got, testData.Users[0]))
	}
}

// TestUserGetAll 验证 User Get All 相关行为。
func TestUserGetAll(t *testing.T) {
	// Test not found
	got, err := adp.UserGetAll(types.ParseUserId("dummyuserid"), types.ParseUserId("otherdummyid"))
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("result users should be nil.")
	}

	got, err = adp.UserGetAll(types.ParseUserId("usr"+testData.Users[0].Id), types.ParseUserId("usr"+testData.Users[1].Id))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatal(mismatchErrorString("resultUsers length", len(got), 2))
	}
	for i, usr := range got {
		if !reflect.DeepEqual(&usr, testData.Users[i]) {
			t.Error(mismatchErrorString("User", &usr, testData.Users[i]))
		}
	}
}

// TestUserGetByCred 验证 User Get By Cred 相关行为。
func TestUserGetByCred(t *testing.T) {
	// Test not found
	got, err := adp.UserGetByCred("foo", "bar")
	if err != nil {
		t.Fatal(err)
	}
	if got != types.ZeroUid {
		t.Error("result uid should be ZeroUid")
	}

	got, _ = adp.UserGetByCred(testData.Creds[0].Method, testData.Creds[0].Value)
	if got != types.ParseUserId("usr"+testData.Creds[0].User) {
		t.Error(mismatchErrorString("Uid", got, types.ParseUserId("usr"+testData.Creds[0].User)))
	}
}

// TestCredGetActive 验证 Cred Get Active 相关行为。
func TestCredGetActive(t *testing.T) {
	got, err := adp.CredGetActive(types.ParseUserId("usr"+testData.Users[2].Id), "tel")
	if err != nil {
		t.Error(err)
	}
	if !reflect.DeepEqual(got, testData.Creds[3]) {
		t.Error(mismatchErrorString("Credential", got, testData.Creds[3]))
	}

	// Test not found
	got, err = adp.CredGetActive(types.ParseUserId("dummyusrid"), "")
	if err != nil {
		t.Error(err)
	}
	if got != nil {
		t.Error("result should be nil, got", got)
	}
}

// TestCredGetAll 验证 Cred Get All 相关行为。
func TestCredGetAll(t *testing.T) {
	got, err := adp.CredGetAll(types.ParseUserId("usr"+testData.Users[2].Id), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Error(mismatchErrorString("Credentials length", len(got), 3))
	}

	got, _ = adp.CredGetAll(types.ParseUserId("usr"+testData.Users[2].Id), "tel", false)
	if len(got) != 2 {
		t.Error(mismatchErrorString("Credentials length", len(got), 2))
	}

	got, _ = adp.CredGetAll(types.ParseUserId("usr"+testData.Users[2].Id), "", true)
	if len(got) != 1 {
		t.Error(mismatchErrorString("Credentials length", len(got), 1))
	}

	got, _ = adp.CredGetAll(types.ParseUserId("usr"+testData.Users[2].Id), "tel", true)
	if len(got) != 1 {
		t.Error(mismatchErrorString("Credentials length", len(got), 1))
	}
}

// TestAuthGetUniqueRecord 验证 Auth Get Unique Record 相关行为。
func TestAuthGetUniqueRecord(t *testing.T) {
	uid, authLvl, secret, expires, err := adp.AuthGetUniqueRecord("basic:alice")
	if err != nil {
		t.Fatal(err)
	}
	if uid != types.ParseUserId("usr"+testData.Recs[0].UserId) ||
		authLvl != testData.Recs[0].AuthLvl ||
		!bytes.Equal(secret, testData.Recs[0].Secret) ||
		expires != testData.Recs[0].Expires {

		got := fmt.Sprintf("%v %v %v %v", uid, authLvl, secret, expires)
		want := fmt.Sprintf("%v %v %v %v", testData.Recs[0].UserId, testData.Recs[0].AuthLvl,
			testData.Recs[0].Secret, testData.Recs[0].Expires)
		t.Error(mismatchErrorString("Auth record", got, want))
	}

	// Test not found
	uid, _, _, _, err = adp.AuthGetUniqueRecord("qwert:asdfg")
	if err == nil && !uid.IsZero() {
		t.Error("Auth record found but shouldn't. Uid:", uid.String())
	}
}

// TestAuthGetRecord 验证 Auth Get Record 相关行为。
func TestAuthGetRecord(t *testing.T) {
	recId, authLvl, secret, expires, err := adp.AuthGetRecord(types.ParseUserId("usr"+testData.Recs[0].UserId), "basic")
	if err != nil {
		t.Fatal(err)
	}
	if recId != testData.Recs[0].Unique ||
		authLvl != testData.Recs[0].AuthLvl ||
		!bytes.Equal(secret, testData.Recs[0].Secret) ||
		expires != testData.Recs[0].Expires {

		got := fmt.Sprintf("%v %v %v %v", recId, authLvl, secret, expires)
		want := fmt.Sprintf("%v %v %v %v", testData.Recs[0].Unique, testData.Recs[0].AuthLvl,
			testData.Recs[0].Secret, testData.Recs[0].Expires)
		t.Error(mismatchErrorString("Auth record", got, want))
	}

	// Test not found
	recId, _, _, _, err = adp.AuthGetRecord(types.ParseUserId("dummyuserid"), "scheme")
	if err != types.ErrNotFound {
		t.Error("Auth record found but shouldn't. recId:", recId)
	}
}

// TestTopicGet 验证 Topic Get 相关行为。
func TestTopicGet(t *testing.T) {
	got, err := adp.TopicGet(testData.Topics[0].Id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, testData.Topics[0]) {
		t.Error(mismatchErrorString("Topic", got, testData.Topics[0]))
	}
	// Test not found
	got, err = adp.TopicGet("asdfasdfasdf")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("Topic should be nil but got:", got)
	}
}

// TestTopicsForUser 验证 Topics For User 相关行为。
func TestTopicsForUser(t *testing.T) {
	qOpts := types.QueryOpt{
		Topic: testData.Topics[1].Id,
		Limit: 999,
	}
	gotSubs, err := adp.TopicsForUser(types.ParseUserId("usr"+testData.Users[0].Id), false, &qOpts)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotSubs) != 1 {
		t.Error(mismatchErrorString("Subs length", len(gotSubs), 1))
	}

	gotSubs, err = adp.TopicsForUser(types.ParseUserId("usr"+testData.Users[1].Id), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotSubs) != 2 {
		t.Error(mismatchErrorString("Subs length (2)", len(gotSubs), 2))
	}

	qOpts.Topic = ""
	ims := testData.Now.Add(15 * time.Minute)
	qOpts.IfModifiedSince = &ims
	gotSubs, err = adp.TopicsForUser(types.ParseUserId("usr"+testData.Users[0].Id), false, &qOpts)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotSubs) != 1 {
		t.Error(mismatchErrorString("Subs length (IMS)", len(gotSubs), 1))
	}

	// time.Now() is correct (as opposite to testData.Now)
	// Topic is modified using time.Now().
	ims = time.Now().Add(15 * time.Minute)
	gotSubs, err = adp.TopicsForUser(types.ParseUserId("usr"+testData.Users[0].Id), false, &qOpts)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotSubs) != 0 {
		t.Error(mismatchErrorString("Subs length (IMS 2)", len(gotSubs), 0))
	}
}

// TestUsersForTopic 验证 Users For Topic 相关行为。
func TestUsersForTopic(t *testing.T) {
	qOpts := types.QueryOpt{
		User:  types.ParseUserId("usr" + testData.Users[0].Id),
		Limit: 999,
	}
	gotSubs, err := adp.UsersForTopic(testData.Topics[0].Id, false, &qOpts)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotSubs) != 1 {
		t.Error(mismatchErrorString("Subs length", len(gotSubs), 1))
	}

	gotSubs, err = adp.UsersForTopic(testData.Topics[0].Id, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotSubs) != 2 {
		t.Error(mismatchErrorString("Subs length", len(gotSubs), 2))
	}

	gotSubs, err = adp.UsersForTopic(testData.Topics[1].Id, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotSubs) != 2 {
		t.Error(mismatchErrorString("Subs length", len(gotSubs), 2))
	}
}

// TestOwnTopics 验证 Own Topics 相关行为。
func TestOwnTopics(t *testing.T) {
	gotSubs, err := adp.OwnTopics(types.ParseUserId("usr" + testData.Users[0].Id))
	if err != nil {
		t.Fatal(err)
	}
	if len(gotSubs) != 1 {
		t.Fatalf("Got topic length %v instead of %v", len(gotSubs), 1)
	}
	if gotSubs[0] != testData.Topics[0].Id {
		t.Errorf("Got topic %v instead of %v", gotSubs[0], testData.Topics[0].Id)
	}
}

// TestSubscriptionGet 验证 Subscription Get 相关行为。
func TestSubscriptionGet(t *testing.T) {
	got, err := adp.SubscriptionGet(testData.Topics[0].Id, types.ParseUserId("usr"+testData.Users[0].Id), false)
	if err != nil {
		t.Error(err)
	}
	opts := cmpopts.IgnoreUnexported(types.Subscription{}, types.ObjHeader{})
	if !cmp.Equal(got, testData.Subs[0], opts) {
		t.Error(mismatchErrorString("Subs", got, testData.Subs[0]))
	}
	// Test not found
	got, err = adp.SubscriptionGet("dummytopic", types.ParseUserId("dummyuserid"), false)
	if err != nil {
		t.Error(err)
	}
	if got != nil {
		t.Error("result sub should be nil.")
	}
}

// TestSubsForUser 验证 Subs For User 相关行为。
func TestSubsForUser(t *testing.T) {
	gotSubs, err := adp.SubsForUser(types.ParseUserId("usr" + testData.Users[0].Id))
	if err != nil {
		t.Error(err)
	}
	if len(gotSubs) != 2 {
		t.Error(mismatchErrorString("Subs length", len(gotSubs), 1))
	}

	// Test not found
	gotSubs, err = adp.SubsForUser(types.ParseUserId("usr12345678"))
	if err != nil {
		t.Error(err)
	}
	if len(gotSubs) != 0 {
		t.Error(mismatchErrorString("Subs length", len(gotSubs), 0))
	}
}

// TestSubsForTopic 验证 Subs For Topic 相关行为。
func TestSubsForTopic(t *testing.T) {
	qOpts := types.QueryOpt{
		User:  types.ParseUserId("usr" + testData.Users[0].Id),
		Limit: 999,
	}
	gotSubs, err := adp.SubsForTopic(testData.Topics[0].Id, false, &qOpts)
	if err != nil {
		t.Error(err)
	}
	if len(gotSubs) != 1 {
		t.Error(mismatchErrorString("Subs length", len(gotSubs), 1))
	}
	// Test not found
	gotSubs, err = adp.SubsForTopic("dummytopicid", false, nil)
	if err != nil {
		t.Error(err)
	}
	if len(gotSubs) != 0 {
		t.Error(mismatchErrorString("Subs length", len(gotSubs), 0))
	}
}

// TestFind 验证 Find 相关行为。
func TestFind(t *testing.T) {
	reqTags := [][]string{{"alice", "bob", "carol", "travel", "qwer", "asdf", "zxcv"}}
	gotSubs, err := adp.Find("usr"+testData.Users[2].Id, "", reqTags, nil, true)
	if err != nil {
		t.Error(err)
	}
	if len(gotSubs) != 3 {
		t.Error(mismatchErrorString("result length", len(gotSubs), 3))
	}
}

// TestMessageGetAll 验证 Message Get All 相关行为。
func TestMessageGetAll(t *testing.T) {
	opts := types.QueryOpt{
		Since:  1,
		Before: 2,
		Limit:  999,
	}
	gotMsgs, err := adp.MessageGetAll(testData.Topics[0].Id,
		types.ParseUserId("usr"+testData.Users[0].Id), &opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotMsgs) != 1 {
		t.Error(mismatchErrorString("Messages length opts", len(gotMsgs), 1))
	}
	gotMsgs, _ = adp.MessageGetAll(testData.Topics[0].Id, types.ParseUserId("usr"+testData.Users[0].Id), nil)
	if len(gotMsgs) != 2 {
		t.Error(mismatchErrorString("Messages length no opts", len(gotMsgs), 2))
	}
	gotMsgs, _ = adp.MessageGetAll(testData.Topics[0].Id, types.ZeroUid, nil)
	if len(gotMsgs) != 3 {
		t.Error(mismatchErrorString("Messages length zero uid", len(gotMsgs), 3))
	}
}

// TestFileGet 验证 File Get 相关行为。
func TestFileGet(t *testing.T) {
	// General test done during TestFileFinishUpload().

	// Test not found
	got, err := adp.FileGet("dummyfileid")
	if err != nil {
		if got != nil {
			t.Error("File found but shouldn't:", got)
		}
	}
}

// ================== Update tests ================================
func TestUserUpdate(t *testing.T) {
	update := map[string]any{
		"UserAgent": "Test Agent v0.11",
		"UpdatedAt": testData.Now.Add(30 * time.Minute),
	}
	err := adp.UserUpdate(types.ParseUserId("usr"+testData.Users[0].Id), update)
	if err != nil {
		t.Fatal(err)
	}

	var got types.User
	err = db.Collection("users").FindOne(ctx, b.M{"_id": testData.Users[0].Id}).Decode(&got)
	if err != nil {
		t.Fatal(err)
	}
	if got.UserAgent != "Test Agent v0.11" {
		t.Error(mismatchErrorString("UserAgent", got.UserAgent, "Test Agent v0.11"))
	}
	if got.UpdatedAt.Equal(got.CreatedAt) {
		t.Error("UpdatedAt field not updated")
	}
}

// TestUserUpdateTags 验证 User Update Tags 相关行为。
func TestUserUpdateTags(t *testing.T) {
	addTags := testData.Tags[0]
	removeTags := testData.Tags[1]
	resetTags := testData.Tags[2]
	got, err := adp.UserUpdateTags(types.ParseUserId("usr"+testData.Users[0].Id), addTags, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alice", "tag1"}
	if !reflect.DeepEqual(got, want) {
		t.Error(mismatchErrorString("Tags", got, want))

	}
	got, _ = adp.UserUpdateTags(types.ParseUserId("usr"+testData.Users[0].Id), nil, removeTags, nil)
	want = nil
	if !reflect.DeepEqual(got, want) {
		t.Error(mismatchErrorString("Tags", got, want))

	}
	got, _ = adp.UserUpdateTags(types.ParseUserId("usr"+testData.Users[0].Id), nil, nil, resetTags)
	want = []string{"alice", "tag111", "tag333"}
	if !reflect.DeepEqual(got, want) {
		t.Error(mismatchErrorString("Tags", got, want))

	}
	got, _ = adp.UserUpdateTags(types.ParseUserId("usr"+testData.Users[0].Id), addTags, removeTags, nil)
	want = []string{"tag111", "tag333"}
	if !reflect.DeepEqual(got, want) {
		t.Error(mismatchErrorString("Tags", got, want))

	}
	got, _ = adp.UserUpdateTags(types.ParseUserId("usr"+testData.Users[0].Id), addTags, removeTags, nil)
	want = []string{"tag111", "tag333"}
	if !reflect.DeepEqual(got, want) {
		t.Error(mismatchErrorString("Tags", got, want))
	}
}

// TestCredFail 验证 Cred Fail 相关行为。
func TestCredFail(t *testing.T) {
	err := adp.CredFail(types.ParseUserId("usr"+testData.Creds[3].User), "tel")
	if err != nil {
		t.Error(err)
	}

	// 检查是否 fields updated
	var got types.Credential
	_ = db.Collection("credentials").FindOne(ctx, b.M{
		"user":   testData.Creds[3].User,
		"method": "tel",
		"value":  testData.Creds[3].Value}).Decode(&got)
	if got.Retries != 1 {
		t.Error(mismatchErrorString("Retries count", got.Retries, 1))
	}
	if got.UpdatedAt.Equal(got.CreatedAt) {
		t.Error("UpdatedAt field not updated")
	}
}

// TestCredConfirm 验证 Cred Confirm 相关行为。
func TestCredConfirm(t *testing.T) {
	err := adp.CredConfirm(types.ParseUserId("usr"+testData.Creds[3].User), "tel")
	if err != nil {
		t.Fatal(err)
	}

	// Test fields are updated
	var got types.Credential
	err = db.Collection("credentials").FindOne(ctx, b.M{
		"user":   testData.Creds[3].User,
		"method": "tel",
		"value":  testData.Creds[3].Value}).Decode(&got)
	if err != nil {
		t.Fatal(err)
	}
	if got.UpdatedAt.Equal(got.CreatedAt) {
		t.Error("Credential not updated correctly")
	}
	// and uncomfirmed credential deleted
	err = db.Collection("credentials").FindOne(ctx, b.M{"_id": testData.Creds[3].User + ":" + got.Method + ":" + got.Value}).Decode(&got)
	if err != mdb.ErrNoDocuments {
		t.Error("Uncomfirmed credential not deleted")
	}
}

// TestAuthUpdRecord 验证 Auth Upd Record 相关行为。
func TestAuthUpdRecord(t *testing.T) {
	rec := testData.Recs[1]
	newSecret := []byte{'s', 'e', 'c', 'r', 'e', 't'}
	err := adp.AuthUpdRecord(types.ParseUserId("usr"+rec.UserId), rec.Scheme, rec.Unique,
		rec.AuthLvl, newSecret, rec.Expires)
	if err != nil {
		t.Fatal(err)
	}
	var got common.AuthRecord
	err = db.Collection("auth").FindOne(ctx, b.M{"_id": rec.Unique}).Decode(&got)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(got.Secret, rec.Secret) {
		t.Error(mismatchErrorString("Secret", got.Secret, rec.Secret))
	}

	// Test with auth ID (unique) change
	newId := "basic:bob12345"
	err = adp.AuthUpdRecord(types.ParseUserId("usr"+rec.UserId), rec.Scheme, newId,
		rec.AuthLvl, newSecret, rec.Expires)
	if err != nil {
		t.Fatal(err)
	}
	// Test if old ID deleted
	err = db.Collection("auth").FindOne(ctx, b.M{"_id": rec.Unique}).Decode(&got)
	if err == nil || err != mdb.ErrNoDocuments {
		t.Errorf("Unique not changed. Got error: %v; ID: %v", err, got.Unique)
	}
	if bytes.Equal(got.Secret, rec.Secret) {
		t.Error(mismatchErrorString("Secret", got.Secret, rec.Secret))
	}
	if bytes.Equal(got.Secret, rec.Secret) {
		t.Error(mismatchErrorString("Secret", got.Secret, rec.Secret))
	}
}

// TestTopicUpdateOnMessage 验证 Topic Update On Message 相关行为。
func TestTopicUpdateOnMessage(t *testing.T) {
	msg := types.Message{
		ObjHeader: types.ObjHeader{
			CreatedAt: testData.Now.Add(33 * time.Minute),
		},
		SeqId: 66,
	}
	err := adp.TopicUpdateOnMessage(testData.Topics[2].Id, &msg)
	if err != nil {
		t.Fatal(err)
	}
	var got types.Topic
	err = db.Collection("topics").FindOne(ctx, b.M{"_id": testData.Topics[2].Id}).Decode(&got)
	if err != nil {
		t.Fatal(err)
	}
	if got.TouchedAt != msg.CreatedAt || got.SeqId != msg.SeqId {
		t.Error(mismatchErrorString("TouchedAt", got.TouchedAt, msg.CreatedAt))
		t.Error(mismatchErrorString("SeqId", got.SeqId, msg.SeqId))
	}
}

// TestTopicUpdate 验证 Topic Update 相关行为。
func TestTopicUpdate(t *testing.T) {
	update := map[string]any{
		"UpdatedAt": testData.Now.Add(55 * time.Minute),
	}
	err := adp.TopicUpdate(testData.Topics[0].Id, update)
	if err != nil {
		t.Fatal(err)
	}
	var got types.Topic
	_ = db.Collection("topics").FindOne(ctx, b.M{"_id": testData.Topics[0].Id}).Decode(&got)
	if got.UpdatedAt != update["UpdatedAt"] {
		t.Error(mismatchErrorString("UpdatedAt", got.UpdatedAt, update["UpdatedAt"]))
	}
}

// TestTopicOwnerChange 验证 Topic Owner Change 相关行为。
func TestTopicOwnerChange(t *testing.T) {
	err := adp.TopicOwnerChange(testData.Topics[0].Id, types.ParseUserId("usr"+testData.Users[1].Id))
	if err != nil {
		t.Fatal(err)
	}
	var got types.Topic
	_ = db.Collection("topics").FindOne(ctx, b.M{"_id": testData.Topics[0].Id}).Decode(&got)
	if got.Owner != testData.Users[1].Id {
		t.Error(mismatchErrorString("Owner", got.Owner, testData.Users[1].Id))
	}
}

// TestSubsUpdate 验证 Subs Update 相关行为。
func TestSubsUpdate(t *testing.T) {
	update := map[string]any{
		"UpdatedAt": testData.Now.Add(22 * time.Minute),
	}
	err := adp.SubsUpdate(testData.Topics[0].Id, types.ParseUserId("usr"+testData.Users[0].Id), update)
	if err != nil {
		t.Fatal(err)
	}
	var got types.Subscription
	_ = db.Collection("subscriptions").FindOne(ctx, b.M{"_id": testData.Topics[0].Id + ":" + testData.Users[0].Id}).Decode(&got)
	if got.UpdatedAt != update["UpdatedAt"] {
		t.Error(mismatchErrorString("UpdatedAt", got.UpdatedAt, update["UpdatedAt"]))
	}

	err = adp.SubsUpdate(testData.Topics[1].Id, types.ZeroUid, update)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Collection("subscriptions").FindOne(ctx, b.M{"topic": testData.Topics[1].Id}).Decode(&got)
	if got.UpdatedAt != update["UpdatedAt"] {
		t.Error(mismatchErrorString("UpdatedAt", got.UpdatedAt, update["UpdatedAt"]))
	}
}

// TestSubsDelete 验证 Subs Delete 相关行为。
func TestSubsDelete(t *testing.T) {
	err := adp.SubsDelete(testData.Topics[1].Id, types.ParseUserId("usr"+testData.Users[0].Id))
	if err != nil {
		t.Fatal(err)
	}
	var got types.Subscription
	_ = db.Collection("subscriptions").FindOne(ctx, b.M{"_id": testData.Topics[1].Id + ":" + testData.Users[0].Id}).Decode(&got)
	if got.DeletedAt == nil {
		t.Error(mismatchErrorString("DeletedAt", got.DeletedAt, nil))
	}
}

// TestDeviceUpsert 验证 Device Upsert 相关行为。
func TestDeviceUpsert(t *testing.T) {
	err := adp.DeviceUpsert(types.ParseUserId("usr"+testData.Users[0].Id), testData.Devs[0])
	if err != nil {
		t.Fatal(err)
	}
	var got types.User
	err = db.Collection("users").FindOne(ctx, b.M{"_id": testData.Users[0].Id}).Decode(&got)
	if err != nil {
		t.Error(err)
	}
	if !reflect.DeepEqual(got.DeviceArray[0], testData.Devs[0]) {
		t.Error(mismatchErrorString("Device", got.DeviceArray[0], testData.Devs[0]))
	}
	// Test update
	testData.Devs[0].Platform = "Web"
	err = adp.DeviceUpsert(types.ParseUserId("usr"+testData.Users[0].Id), testData.Devs[0])
	if err != nil {
		t.Fatal(err)
	}
	err = db.Collection("users").FindOne(ctx, b.M{"_id": testData.Users[0].Id}).Decode(&got)
	if err != nil {
		t.Error(err)
	}
	if got.DeviceArray[0].Platform != "Web" {
		t.Error("Device not updated.", got.DeviceArray[0])
	}
	// Test add same device to another 用户
	err = adp.DeviceUpsert(types.ParseUserId("usr"+testData.Users[1].Id), testData.Devs[0])
	if err != nil {
		t.Fatal(err)
	}
	err = db.Collection("users").FindOne(ctx, b.M{"_id": testData.Users[1].Id}).Decode(&got)
	if err != nil {
		t.Error(err)
	}
	if got.DeviceArray[0].Platform != "Web" {
		t.Error("Device not updated.", got.DeviceArray[0])
	}

	err = adp.DeviceUpsert(types.ParseUserId("usr"+testData.Users[2].Id), testData.Devs[1])
	if err != nil {
		t.Error(err)
	}
}

// TestMessageAttachments 验证 Message Attachments 相关行为。
func TestMessageAttachments(t *testing.T) {
	fids := []string{testData.Files[0].Id, testData.Files[1].Id}
	err := adp.FileLinkAttachments("", types.ZeroUid, types.ParseUid(testData.Msgs[1].Id), fids)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string][]string
	findOpts := mdbopts.FindOne().SetProjection(b.M{"attachments": 1, "_id": 0})
	err = db.Collection("messages").FindOne(ctx, b.M{"_id": testData.Msgs[1].Id}, findOpts).Decode(&got)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["attachments"], fids) {
		t.Error(mismatchErrorString("Attachments", got["attachments"], fids))
	}
	var got2 map[string]int
	findOpts = mdbopts.FindOne().SetProjection(b.M{"usecount": 1, "_id": 0})
	err = db.Collection("fileuploads").FindOne(ctx, b.M{"_id": testData.Files[0].Id}, findOpts).Decode(&got2)
	if err != nil {
		t.Fatal(err)
	}
	if got2["usecount"] != 1 {
		t.Error(mismatchErrorString("UseCount", got2["usecount"], 1))
	}
}

// TestFileFinishUpload 验证 File Finish Upload 相关行为。
func TestFileFinishUpload(t *testing.T) {
	got, err := adp.FileFinishUpload(testData.Files[0], true, 22222)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != types.UploadCompleted {
		t.Error(mismatchErrorString("Status", got.Status, types.UploadCompleted))
	}
	if got.Size != 22222 {
		t.Error(mismatchErrorString("Size", got.Size, 22222))
	}
}

// ================== Other tests =================================
func TestDeviceGetAll(t *testing.T) {
	uid0 := types.ParseUserId("usr" + testData.Users[0].Id)
	uid1 := types.ParseUserId("usr" + testData.Users[1].Id)
	uid2 := types.ParseUserId("usr" + testData.Users[2].Id)
	gotDevs, count, err := adp.DeviceGetAll(uid0, uid1, uid2)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatal(mismatchErrorString("count", count, 2))
	}
	if !reflect.DeepEqual(gotDevs[uid1][0], *testData.Devs[0]) {
		t.Error(mismatchErrorString("Device", gotDevs[uid1][0], *testData.Devs[0]))
	}
	if !reflect.DeepEqual(gotDevs[uid2][0], *testData.Devs[1]) {
		t.Error(mismatchErrorString("Device", gotDevs[uid2][0], *testData.Devs[1]))
	}
}

// TestDeviceDelete 验证 Device Delete 相关行为。
func TestDeviceDelete(t *testing.T) {
	err := adp.DeviceDelete(types.ParseUserId("usr"+testData.Users[1].Id), testData.Devs[0].DeviceId)
	if err != nil {
		t.Fatal(err)
	}
	var got types.User
	err = db.Collection("users").FindOne(ctx, b.M{"_id": testData.Users[1].Id}).Decode(&got)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.DeviceArray) != 0 {
		t.Error("Device not deleted:", got.DeviceArray)
	}

	err = adp.DeviceDelete(types.ParseUserId("usr"+testData.Users[2].Id), "")
	if err != nil {
		t.Fatal(err)
	}
	err = db.Collection("users").FindOne(ctx, b.M{"_id": testData.Users[2].Id}).Decode(&got)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.DeviceArray) != 0 {
		t.Error("Device not deleted:", got.DeviceArray)
	}
}

// ================== Persistent Cache tests ======================
func TestPCacheUpsert(t *testing.T) {
	err := adp.PCacheUpsert("test_key", "test_value", false)
	if err != nil {
		t.Fatal(err)
	}

	// Test duplicate with failOnDuplicate = true
	err = adp.PCacheUpsert("test_key2", "test_value2", true)
	if err != nil {
		t.Fatal(err)
	}

	err = adp.PCacheUpsert("test_key2", "new_value", true)
	if err != types.ErrDuplicate {
		t.Error("Expected duplicate error")
	}
}

// TestPCacheGet 验证 P Cache Get 相关行为。
func TestPCacheGet(t *testing.T) {
	value, err := adp.PCacheGet("test_key")
	if err != nil {
		t.Fatal(err)
	}
	if value != "test_value" {
		t.Error(mismatchErrorString("Cache value", value, "test_value"))
	}

	// Test not found
	_, err = adp.PCacheGet("nonexistent")
	if err != types.ErrNotFound {
		t.Error("Expected not found error")
	}
}

// TestPCacheDelete 验证 P Cache Delete 相关行为。
func TestPCacheDelete(t *testing.T) {
	err := adp.PCacheDelete("test_key")
	if err != nil {
		t.Fatal(err)
	}

	// Verify deleted
	_, err = adp.PCacheGet("test_key")
	if err != types.ErrNotFound {
		t.Error("Key should be deleted")
	}
}

// TestPCacheExpire 验证 P Cache Expire 相关行为。
func TestPCacheExpire(t *testing.T) {
	// Insert some test keys with prefix
	adp.PCacheUpsert("prefix_key1", "value1", false)
	adp.PCacheUpsert("prefix_key2", "value2", false)

	// Expire keys older than now (should delete all test keys)
	err := adp.PCacheExpire("prefix_", time.Now().Add(1*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
}

// ================== Delete tests ================================
func TestCredDel(t *testing.T) {
	err := adp.CredDel(types.ParseUserId("usr"+testData.Users[0].Id), "email", "alice@test.example.com")
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	cur, err := db.Collection("credentials").Find(ctx, b.M{"method": "email", "value": "alice@test.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err = cur.All(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Error("Got result but shouldn't", got)
	}

	err = adp.CredDel(types.ParseUserId("usr"+testData.Users[1].Id), "", "")
	if err != nil {
		t.Fatal(err)
	}
	cur, err = db.Collection("credentials").Find(ctx, b.M{"user": testData.Users[1].Id})
	if err != nil {
		t.Fatal(err)
	}
	if err = cur.All(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Error("Got result but shouldn't", got)
	}
}

// TestAuthDelScheme 验证 Auth Del Scheme 相关行为。
func TestAuthDelScheme(t *testing.T) {
	// tested during TestAuthUpdRecord
}

// TestAuthDelAllRecords 验证 Auth Del All Records 相关行为。
func TestAuthDelAllRecords(t *testing.T) {
	delCount, err := adp.AuthDelAllRecords(types.ParseUserId("usr" + testData.Recs[0].UserId))
	if err != nil {
		t.Fatal(err)
	}
	if delCount != 1 {
		t.Error(mismatchErrorString("delCount", delCount, 1))
	}

	// With dummy 用户
	delCount, _ = adp.AuthDelAllRecords(types.ParseUserId("dummyuserid"))
	if delCount != 0 {
		t.Error(mismatchErrorString("delCount", delCount, 0))
	}
}

// TestSubsDelForUser 验证 Subs Del For User 相关行为。
func TestSubsDelForUser(t *testing.T) {
	// Tested during TestUserDelete (both hard and soft deletions)
}

// TestMessageDeleteList 验证 Message Delete List 相关行为。
func TestMessageDeleteList(t *testing.T) {
	toDel := types.DelMessage{
		ObjHeader: types.ObjHeader{
			Id:        testData.UGen.GetStr(),
			CreatedAt: testData.Now,
			UpdatedAt: testData.Now,
		},
		Topic:       testData.Topics[1].Id,
		DeletedFor:  testData.Users[2].Id,
		DelId:       1,
		SeqIdRanges: []types.Range{{Low: 9}, {Low: 3, Hi: 7}},
	}
	err := adp.MessageDeleteList(toDel.Topic, &toDel)
	if err != nil {
		t.Fatal(err)
	}

	var got []types.Message
	cur, err := db.Collection("messages").Find(ctx, b.M{"topic": toDel.Topic})
	if err != nil {
		t.Fatal(err)
	}
	if err = cur.All(ctx, &got); err != nil {
		t.Fatal(err)
	}
	for _, msg := range got {
		if msg.SeqId == 1 && msg.DeletedFor != nil {
			t.Error("Message with SeqID=1 should not be deleted")
		}
		if msg.SeqId == 5 && msg.DeletedFor == nil {
			t.Error("Message with SeqID=5 should be deleted")
		}
		if msg.SeqId == 11 && msg.DeletedFor != nil {
			t.Error("Message with SeqID=11 should not be deleted")
		}
	}
	//
	toDel = types.DelMessage{
		ObjHeader: types.ObjHeader{
			Id:        testData.UGen.GetStr(),
			CreatedAt: testData.Now,
			UpdatedAt: testData.Now,
		},
		Topic:       testData.Topics[0].Id,
		DelId:       3,
		SeqIdRanges: []types.Range{{Low: 1, Hi: 3}},
	}
	err = adp.MessageDeleteList(toDel.Topic, &toDel)
	if err != nil {
		t.Fatal(err)
	}
	cur, err = db.Collection("messages").Find(ctx, b.M{"topic": toDel.Topic})
	if err != nil {
		t.Fatal(err)
	}
	if err = cur.All(ctx, &got); err != nil {
		t.Fatal(err)
	}
	for _, msg := range got {
		if msg.Content != nil && msg.SeqId != 3 {
			t.Error("Message not deleted:", msg.SeqId)
		}
	}

	err = adp.MessageDeleteList(testData.Topics[0].Id, nil)
	if err != nil {
		t.Fatal(err)
	}
	cur, err = db.Collection("messages").Find(ctx, b.M{"topic": testData.Topics[0].Id})
	if err != nil {
		t.Fatal(err)
	}
	if err = cur.All(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Error("Result should be empty:", got)
	}
}

// TestTopicDelete 验证 Topic Delete 相关行为。
func TestTopicDelete(t *testing.T) {
	err := adp.TopicDelete(testData.Topics[1].Id, false, false)
	if err != nil {
		t.Fatal()
	}
	var got types.Topic
	cur, err := db.Collection("topics").Find(ctx, b.M{"topic": testData.Topics[1].Id})
	if err != nil {
		t.Fatal(err)
	}
	for cur.Next(ctx) {
		if err = cur.Decode(&got); err != nil {
			t.Error(err)
		}
		if got.State != types.StateDeleted {
			t.Error("Soft delete failed:", got)
		}
	}

	err = adp.TopicDelete(testData.Topics[0].Id, true, true)
	if err != nil {
		t.Fatal()
	}

	var got2 []types.Topic
	cur, err = db.Collection("topics").Find(ctx, b.M{"topic": testData.Topics[0].Id})
	if err != nil {
		t.Fatal(err)
	}
	if err = cur.All(ctx, &got2); err != nil {
		t.Fatal(err)
	}
	if len(got2) != 0 {
		t.Error("Hard delete failed:", got2)
	}
}

// TestFileDeleteUnused 验证 File Delete Unused 相关行为。
func TestFileDeleteUnused(t *testing.T) {
	// time.Now() is correct (as opposite to testData.Now):
	// the FileFinishUpload uses time.Now() as a timestamp.
	locs, err := adp.FileDeleteUnused(time.Now().Add(1*time.Minute), 999)
	if err != nil {
		t.Fatal(err)
	}
	if len(locs) != 2 {
		t.Error(mismatchErrorString("Locations length", len(locs), 2))
	}
}

// TestUserDelete 验证 User Delete 相关行为。
func TestUserDelete(t *testing.T) {
	err := adp.UserDelete(types.ParseUserId("usr"+testData.Users[0].Id), false)
	if err != nil {
		t.Fatal(err)
	}
	var got types.User
	err = db.Collection("users").FindOne(ctx, b.M{"_id": testData.Users[0].Id}).Decode(&got)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != types.StateDeleted {
		t.Error("User soft delete failed", got)
	}

	err = adp.UserDelete(types.ParseUserId("usr"+testData.Users[1].Id), true)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Collection("users").FindOne(ctx, b.M{"_id": testData.Users[1].Id}).Decode(&got)
	if err != mdb.ErrNoDocuments {
		t.Error("User hard delete failed", err)
	}
}

// TestUserUnreadCount 验证 User Unread Count 相关行为。
func TestUserUnreadCount(t *testing.T) {
	uids := []types.Uid{
		types.ParseUserId("usr" + testData.Users[1].Id),
		types.ParseUserId("usr" + testData.Users[2].Id),
	}
	expected := map[types.Uid]int{uids[0]: 0, uids[1]: 166}
	counts, err := adp.UserUnreadCount(uids...)
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 2 {
		t.Error(mismatchErrorString("UnreadCount length", len(counts), 2))
	}

	for uid, unread := range counts {
		if expected[uid] != unread {
			t.Error(mismatchErrorString("UnreadCount", unread, expected[uid]))
		}
	}

	// Test not found (even if the account is not found, the call must return one record).
	uid := types.ParseUserId("dummyuserid")
	counts, err = adp.UserUnreadCount(uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 1 {
		t.Error(mismatchErrorString("UnreadCount length (dummy)", len(counts), 1))
	}
	if counts[uid] != 0 {
		t.Error(mismatchErrorString("Non-zero UnreadCount (dummy)", counts[uid], 0))
	}
}

// ================== Other tests =================================
func TestMessageGetDeleted(t *testing.T) {
	qOpts := types.QueryOpt{
		Since:  1,
		Before: 10,
		Limit:  999,
	}
	got, err := adp.MessageGetDeleted(testData.Topics[1].Id, types.ParseUserId("usr"+testData.Users[2].Id), &qOpts)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Error(mismatchErrorString("result length", len(got), 1))
	}
}

// ================================================================
func mismatchErrorString(key string, got, want any) string {
	return fmt.Sprintf("%s mismatch:\nGot  = %+v\nWant = %+v", key, got, want)
}

// init 注册当前包提供的实现并初始化包级状态。
func init() {
	logs.Init(os.Stderr, "stdFlags")
	adp = backend.GetTestAdapter()
	conffile := flag.String("config", "./test.yaml", "数据库连接 YAML 配置文件")

	if err := configutil.DecodeFile(*conffile, &config); err != nil {
		log.Fatal(err)
	}

	if adp == nil {
		log.Fatal("Database adapter is missing")
	}
	if adp.IsOpen() {
		log.Print("Connection is already opened")
	}

	err := adp.Open(config.Adapters[adp.GetName()])
	if err != nil {
		log.Fatal(err)
	}

	db = adp.GetTestDB().(*mdb.Database)
	testData = test_data.InitTestData()
	if testData == nil {
		log.Fatal("Failed to initialize test data")
	}
}
