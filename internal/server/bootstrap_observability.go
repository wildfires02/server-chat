package server

import (
	"encoding/json"
	"net/http"
	"os"
	"runtime/pprof"

	"chat/server/logs"
	"chat/server/store"
)

func initServerStats(mux *http.ServeMux, config configType, overridePath string) {
	path := overridePath
	if path == "" {
		path = config.ExpvarPath
	}
	statsInit(mux, path)
	statsRegisterInt("Version")
	version := base10Version(parseVersion(buildstamp))
	if version <= 0 {
		version = base10Version(parseVersion(currentVersion))
	}
	statsSet("Version", version)
	statsRegisterString("RuntimeEnvironment", config.Runtime.Environment)
	statsRegisterString("DeploymentMode", config.Runtime.DeploymentMode)
}

func startServerProfiler(curwd, path string) func() {
	if path == "" {
		return func() {}
	}
	path = toAbsolutePath(curwd, path)
	cpuFile, err := os.Create(path + ".cpu")
	if err != nil {
		logs.Err.Fatal("Failed to create CPU pprof file: ", err)
	}
	memoryFile, err := os.Create(path + ".mem")
	if err != nil {
		_ = cpuFile.Close()
		logs.Err.Fatal("Failed to create Mem pprof file: ", err)
	}
	_ = pprof.StartCPUProfile(cpuFile)
	logs.Info.Printf("Profiling info saved to '%s.(cpu|mem)'", path)

	return func() {
		_ = pprof.WriteHeapProfile(memoryFile)
		pprof.StopCPUProfile()
		_ = memoryFile.Close()
		_ = cpuFile.Close()
	}
}

func openServerStore(workerID int, config json.RawMessage) func() {
	err := store.Store.Open(workerID, config)
	logs.Info.Println("DB adapter", store.Store.GetAdapterName(), store.Store.GetAdapterVersion())
	if err != nil {
		logs.Err.Fatal("Failed to connect to DB: ", err)
	}
	statsRegisterDbStats()

	return func() {
		store.Store.Close()
		logs.Info.Println("Closed database connection(s)")
		logs.Info.Println("All done, good bye")
	}
}

func startServerHealth(mux *http.ServeMux, config healthConfig) func() {
	runtimeConfig, err := normalizeHealthConfig(config)
	if err != nil {
		logs.Err.Fatal(err)
	}
	globals.health = newServiceHealth(runtimeConfig, defaultDatabaseHealthCheck)
	globals.health.Start()
	registerServiceHealthHandlers(mux, globals.health)
	logs.Info.Printf(
		"Health endpoints: live=%s ready=%s drain=%s topology=%s",
		runtimeConfig.LivePath,
		runtimeConfig.ReadyPath,
		runtimeConfig.DrainPath,
		runtimeConfig.TopologyPath,
	)

	return func() {
		globals.health.Stop()
		globals.health = nil
	}
}
