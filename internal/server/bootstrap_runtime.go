package server

import (
	"encoding/json"
	"time"

	admincontrol "chat/server/admin"
	"chat/server/logs"
	"chat/server/push"
	"chat/server/store"
	"chat/server/store/types"
)

func applyServerRuntimeConfig(config configType) {
	globals.maxMessageSize = int64(config.MaxMessageSize)
	if globals.maxMessageSize <= 0 {
		globals.maxMessageSize = defaultMaxMessageSize
	}
	globals.maxSubscriberCount = config.MaxSubscriberCount
	if globals.maxSubscriberCount <= 1 {
		globals.maxSubscriberCount = defaultMaxSubscriberCount
	}
	globals.maxTagCount = config.MaxTagCount
	if globals.maxTagCount <= 0 {
		globals.maxTagCount = defaultMaxTagCount
	}
	globals.permanentAccounts = config.PermanentAccounts
	globals.useXForwardedFor = config.UseXForwardedFor
	globals.defaultCountryCode = config.DefaultCountryCode
	if globals.defaultCountryCode == "" {
		globals.defaultCountryCode = defaultCountryCode
	}

	globals.typesModeCP2P = types.ModeCP2P
	if config.P2PDeleteEnabled {
		globals.typesModeCP2P = types.ModeCP2PD
	}
	if config.MsgDeleteAge > 0 {
		globals.msgDeleteAge = time.Duration(config.MsgDeleteAge) * time.Second
	}

	globals.xFrameOptions = config.XFrameOptions
	if globals.xFrameOptions == "" {
		globals.xFrameOptions = "SAMEORIGIN"
	}
	if globals.xFrameOptions != "SAMEORIGIN" &&
		globals.xFrameOptions != "DENY" &&
		globals.xFrameOptions != "-" {
		logs.Warn.Println("Ignored invalid x_frame_options", config.XFrameOptions)
		globals.xFrameOptions = "SAMEORIGIN"
	}
	globals.wsCompression = !config.WSCompressionDisabled
	globals.translation = nil
	if config.Translation != nil && config.Translation.Enabled {
		refresh := time.Duration(config.Translation.RefreshInterval) * time.Second
		globals.translation = newTranslationRuntime(newPersistentTranslationSettingsSource(refresh))
	}
	globals.businessPolicy = nil
	if config.BusinessPolicy != nil {
		client, err := newBusinessPolicyClient(*config.BusinessPolicy)
		if err != nil {
			logs.Err.Fatalf("Failed to initialize business policy: %v", err)
		}
		globals.businessPolicy = client
		client.startAuditWorker()
	}
}

func initializeServerControlPlane() {
	control, err := admincontrol.NewControlPlane(persistentAdminRepository{})
	if err != nil {
		logs.Err.Fatalf("Failed to initialize server policy control plane: %v", err)
	}
	globals.adminControl = control
}

func startServerMedia(config *configType) func() {
	if config.Media == nil {
		return func() {}
	}
	if config.Media.UseHandler == "" {
		config.Media = nil
		return func() {}
	}

	globals.maxFileUploadSize = config.Media.MaxFileUploadSize
	if config.Media.Handlers != nil {
		var handlerConfig string
		if params := config.Media.Handlers[config.Media.UseHandler]; params != nil {
			handlerConfig = string(params)
		}
		if err := store.Store.UseMediaHandler(config.Media.UseHandler, handlerConfig); err != nil {
			logs.Err.Fatalf("Failed to init media handler '%s': %s", config.Media.UseHandler, err)
		}
	}
	stopProcessing := startFileProcessing(config.Media.Processing)
	var stopGC chan<- bool
	if config.Media.GcPeriod > 0 && config.Media.GcBlockSize > 0 {
		globals.mediaGcPeriod = time.Second * time.Duration(config.Media.GcPeriod)
		stopGC = largeFileRunGarbageCollection(globals.mediaGcPeriod, config.Media.GcBlockSize)
	}
	return func() {
		stopProcessing()
		if stopGC != nil {
			stopGC <- true
			logs.Info.Println("Stopped files garbage collector")
		}
	}
}

func startAccountGarbageCollector(config *accountGcConfig) func() {
	if config == nil || !config.Enabled {
		return func() {}
	}
	if config.GcPeriod <= 0 || config.GcBlockSize <= 0 || config.GcMinAccountAge <= 0 {
		logs.Err.Fatalln("Invalid account GC config")
	}
	period := time.Second * time.Duration(config.GcPeriod)
	stop := garbageCollectUsers(period, config.GcBlockSize, config.GcMinAccountAge)
	return func() {
		stop <- true
		logs.Info.Println("Stopped account garbage collector")
	}
}

func startServerPush(firebase firebaseConfig, alertConfig *push.DLQAlertConfig) func() {
	config, err := json.Marshal(map[string]any{
		"enabled":          firebase.Enabled,
		"credentials_file": firebase.CredentialFile,
		"time_to_live":     firebase.TimeToLive,
	})
	if err != nil {
		logs.Err.Fatal("Failed to encode Firebase configuration:", err)
	}
	enabled, err := push.Init("fcm", config)
	if err != nil {
		logs.Err.Fatal("Failed to initialize push notifications:", err)
	}
	stopAlerts, err := push.StartDLQAlerts(alertConfig)
	if err != nil {
		logs.Err.Fatal("Failed to initialize push DLQ alerts:", err)
	}
	logs.Info.Println("Firebase push enabled:", enabled)
	return func() {
		stopAlerts()
		push.Stop()
		logs.Info.Println("Stopped push notifications")
	}
}

func startCoreRuntime() func() {
	globals.sessionStore = NewSessionStore(idleSessionTimeout + 15*time.Second)
	globals.hub = newHub()
	stopScheduledMessages := scheduledMessagesRun()

	if globals.cluster != nil {
		if err := globals.cluster.start(); err != nil {
			logs.Err.Fatal(err)
		}
	}

	return func() {
		stopScheduledMessages <- true
		logs.Info.Println("Stopped scheduled messages dispatcher")
	}
}
