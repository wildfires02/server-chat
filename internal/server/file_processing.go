package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

type fileProcessingJob struct {
	File *types.FileDef
	URL  string
}

var dialClamAV = net.DialTimeout

type fileProcessingRuntime struct {
	config             mediaProcessingConfig
	owner              string
	wake               chan struct{}
	stop               chan struct{}
	wg                 sync.WaitGroup
	once               sync.Once
	scannerVersionMu   sync.Mutex
	scannerVersion     string
	scannerVersionNext time.Time
}

const fileContentIndexPrefix = "filecontent:v1:"

type fileContentIndex struct {
	FileID    string            `json:"file_id"`
	Owner     string            `json:"owner"`
	MimeType  string            `json:"mime_type"`
	SHA256    string            `json:"sha256"`
	Preview   map[string]string `json:"preview,omitempty"`
	UpdatedAt time.Time         `json:"updated_at"`
}

func startFileProcessing(config *mediaProcessingConfig) func() {
	if config == nil || !config.Enabled {
		globals.fileProcessor = nil
		return func() {}
	}
	if config.Workers <= 0 {
		config.Workers = 2
	}
	if config.QueueSize <= 0 {
		config.QueueSize = 256
	}
	if config.Timeout <= 0 {
		config.Timeout = 120
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 2
	}
	if config.MaxAttempts <= 0 {
		config.MaxAttempts = 5
	}
	if config.RetryBase <= 0 {
		config.RetryBase = 5
	}
	if config.LeaseSeconds <= config.Timeout {
		config.LeaseSeconds = config.Timeout + 60
	}
	runtime := &fileProcessingRuntime{
		config: *config,
		owner:  newResumableLeaseOwner(),
		wake:   make(chan struct{}, 1),
		stop:   make(chan struct{}),
	}
	for i := 0; i < config.Workers; i++ {
		runtime.wg.Add(1)
		go runtime.worker()
	}
	globals.fileProcessor = runtime
	runtime.signal()
	logs.Info.Printf("Reliable file processing enabled with %d workers", config.Workers)
	return func() {
		runtime.once.Do(func() { close(runtime.stop) })
		runtime.wg.Wait()
		if globals.fileProcessor == runtime {
			globals.fileProcessor = nil
		}
	}
}

func queueFileProcessing(definition *types.FileDef, rawURL string) {
	runtime := globals.fileProcessor
	if runtime == nil || definition == nil || definition.Id == "" {
		return
	}
	state, _ := store.GetFileProcessingState(definition.Id)
	if state == nil {
		state = &store.FileProcessingState{}
	}
	state.ScanStatus = "pending"
	state.ProcessStatus = "queued"
	state.Error = ""
	state.Attempts = 0
	state.NextRetryAt = nil
	if err := enqueuePersistentFileProcessingJob(definition, rawURL); err != nil {
		state.ProcessStatus = "failed"
		state.Error = "unable to persist processing job"
		_ = store.SetFileProcessingState(definition.Id, *state)
		logs.Warn.Printf("File processing enqueue failed, fid=%s: %v", definition.Id, err)
		return
	}
	_ = store.SetFileProcessingState(definition.Id, *state)
	runtime.signal()
}

func (runtime *fileProcessingRuntime) worker() {
	defer runtime.wg.Done()
	ticker := time.NewTicker(time.Duration(runtime.config.PollInterval) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-runtime.stop:
			return
		default:
		}
		claimed, err := claimPersistentFileProcessingJob(runtime.owner, types.TimeNow(),
			time.Duration(runtime.config.LeaseSeconds)*time.Second, runtime.config.QueueSize)
		if err != nil {
			logs.Warn.Printf("File processing job scan failed: %v", err)
		} else if claimed != nil {
			statsInc("FileProcessingClaims", 1)
			if claimed.job.Attempts > 1 {
				statsInc("FileProcessingLeaseRecoveries", 1)
			}
			job := fileProcessingJob{File: claimed.job.File, URL: claimed.job.URL}
			processErr := runtime.process(job, claimed.job.Attempts)
			if processErr == nil {
				swapped, completeErr := completePersistentFileProcessingJob(claimed)
				if completeErr != nil {
					logs.Warn.Printf("File processing completion save failed, fid=%s: %v",
						job.File.Id, completeErr)
				} else if !swapped {
					logs.Warn.Printf("File processing lease lost before completion, fid=%s", job.File.Id)
				} else {
					statsInc("FileProcessingCompleted", 1)
				}
			} else {
				swapped, retry, retryErr := retryPersistentFileProcessingJob(claimed,
					runtime.config.MaxAttempts, time.Duration(runtime.config.RetryBase)*time.Second,
					processErr)
				if retryErr != nil {
					logs.Warn.Printf("File processing retry save failed, fid=%s: %v",
						job.File.Id, retryErr)
				} else if swapped {
					runtime.recordRetryState(job.File.Id, retry)
					if retry.Status == "dead" {
						statsInc("FileProcessingDeadLetters", 1)
					} else {
						statsInc("FileProcessingRetries", 1)
					}
				}
				logs.Warn.Printf("File processing attempt failed, fid=%s attempt=%d: %v",
					job.File.Id, claimed.job.Attempts, processErr)
			}
			continue
		}
		select {
		case <-runtime.wake:
		case <-ticker.C:
		case <-runtime.stop:
			return
		}
	}
}

func (runtime *fileProcessingRuntime) signal() {
	select {
	case runtime.wake <- struct{}{}:
	default:
	}
}

func (runtime *fileProcessingRuntime) recordRetryState(fid string, job *persistentFileProcessingJob) {
	if job == nil {
		return
	}
	state, _ := store.GetFileProcessingState(job.File.Id)
	if state == nil {
		state = &store.FileProcessingState{}
	}
	state.Attempts = job.Attempts
	state.Error = job.LastError
	if job.Status == "dead" {
		state.ProcessStatus = "dead"
		state.NextRetryAt = nil
	} else {
		state.ProcessStatus = "retrying"
		next := job.NextAttemptAt
		state.NextRetryAt = &next
	}
	_ = store.SetFileProcessingState(fid, *state)
}

func (runtime *fileProcessingRuntime) process(job fileProcessingJob, attempt int) error {
	state, _ := store.GetFileProcessingState(job.File.Id)
	if state == nil {
		state = &store.FileProcessingState{}
	}
	state.ProcessStatus = "processing"
	state.Error = ""
	state.Attempts = attempt
	state.NextRetryAt = nil
	_ = store.SetFileProcessingState(job.File.Id, *state)

	workDir, err := os.MkdirTemp("", "chat-file-processing-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)

	source, err := runtime.downloadToTemp(job, workDir)
	if err != nil {
		return err
	}
	if digest, digestErr := fileSHA256(source); digestErr != nil {
		return digestErr
	} else {
		state.SHA256 = digest
		_ = store.SetFileProcessingState(job.File.Id, *state)
	}
	if runtime.config.ClamAVAddr != "" {
		state.ScannerVersion = runtime.loadClamAVVersion()
		state.ScanStatus = "scanning"
		_ = store.SetFileProcessingState(job.File.Id, *state)
		infected, scanErr := scanFileWithClamAV(source, runtime.config.ClamAVAddr,
			time.Duration(runtime.config.Timeout)*time.Second)
		if scanErr != nil {
			state.ScanStatus = "error"
			_ = store.SetFileProcessingState(job.File.Id, *state)
			return scanErr
		}
		if infected {
			state.ScanStatus = "quarantined"
			state.ProcessStatus = "blocked"
			state.Error = "malware detected"
			quarantineLocation, quarantineErr := store.QuarantineFile(job.File)
			if quarantineErr != nil {
				state.QuarantineStatus = "isolation_failed"
				state.Error = "malware detected; physical isolation failed: " + quarantineErr.Error()
				_ = store.SetFileProcessingState(job.File.Id, *state)
				return quarantineErr
			}
			state.QuarantineStatus = "isolated"
			state.QuarantineLocation = quarantineLocation
			_ = store.SetFileProcessingState(job.File.Id, *state)
			return nil
		}
		state.ScanStatus = "clean"
	} else {
		state.ScanStatus = "skipped"
	}
	if duplicate, duplicateErr := findReadyFileContent(job.File, state.SHA256); duplicateErr != nil {
		return duplicateErr
	} else if duplicate != nil {
		previews := reusedFilePreviews(duplicate.Preview, job.URL)
		for previewType, previewURL := range previews {
			if previewType == "original" {
				continue
			}
			if previewURL != "" {
				if err = store.CopyFileAccess(job.File.Id, previewURL); err != nil {
					return err
				}
			}
		}
		state.DuplicateOf = duplicate.FileID
		state.Preview = previews
		state.ProcessStatus = "ready"
		state.Error = ""
		return store.SetFileProcessingState(job.File.Id, *state)
	}
	previews, err := runtime.generatePreviews(job, source, workDir)
	if err != nil {
		return err
	}
	state.Preview = previews
	state.ProcessStatus = "ready"
	state.Error = ""
	if err = store.SetFileProcessingState(job.File.Id, *state); err != nil {
		return err
	}
	return saveReadyFileContent(job.File, state.SHA256, previews)
}

func copyStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func reusedFilePreviews(source map[string]string, originalURL string) map[string]string {
	previews := copyStringMap(source)
	if previews == nil {
		previews = make(map[string]string)
	}
	// 去重只复用同一上传者的派生资源。original 必须始终指向本次上传，
	// 否则删除旧文件会让新消息失效，也会泄漏旧文件标识。
	previews["original"] = originalURL
	return previews
}

func fileContentIndexKey(definition *types.FileDef, digest string) string {
	ownerDigest := sha256.Sum256([]byte(definition.User))
	return fileContentIndexPrefix + hex.EncodeToString(ownerDigest[:8]) + ":" + digest
}

func findReadyFileContent(definition *types.FileDef, digest string) (*fileContentIndex, error) {
	if definition == nil || digest == "" {
		return nil, nil
	}
	raw, err := store.PCache.Get(fileContentIndexKey(definition, digest))
	if errors.Is(err, types.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var index fileContentIndex
	if json.Unmarshal([]byte(raw), &index) != nil || index.Owner != definition.User ||
		index.MimeType != definition.MimeType || index.SHA256 != digest || index.FileID == definition.Id {
		return nil, nil
	}
	state, stateErr := store.GetFileProcessingState(index.FileID)
	if stateErr != nil || state == nil || state.ProcessStatus != "ready" ||
		(state.ScanStatus != "clean" && state.ScanStatus != "skipped") {
		return nil, nil
	}
	index.Preview = copyStringMap(state.Preview)
	return &index, nil
}

func saveReadyFileContent(definition *types.FileDef, digest string, preview map[string]string) error {
	if definition == nil || digest == "" {
		return nil
	}
	index := fileContentIndex{
		FileID: definition.Id, Owner: definition.User, MimeType: definition.MimeType,
		SHA256: digest, Preview: copyStringMap(preview), UpdatedAt: types.TimeNow(),
	}
	raw, err := json.Marshal(index)
	if err != nil {
		return err
	}
	return store.PCache.Upsert(fileContentIndexKey(definition, digest), string(raw), false)
}

func (runtime *fileProcessingRuntime) loadClamAVVersion() string {
	runtime.scannerVersionMu.Lock()
	defer runtime.scannerVersionMu.Unlock()
	if runtime.scannerVersion != "" || time.Now().Before(runtime.scannerVersionNext) {
		return runtime.scannerVersion
	}
	// ClamAV 在进程启动时可能尚未就绪。失败后五分钟重试，避免一次瞬时
	// 故障导致整个进程生命周期都缺少扫描器版本信息。
	runtime.scannerVersionNext = time.Now().Add(5 * time.Minute)
	connection, err := dialClamAV("tcp", runtime.config.ClamAVAddr,
		time.Duration(runtime.config.Timeout)*time.Second)
	if err != nil {
		return runtime.scannerVersion
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err = connection.Write([]byte("zVERSION\x00")); err != nil {
		return runtime.scannerVersion
	}
	reply, readErr := bufio.NewReader(connection).ReadString(0)
	if readErr == nil || errors.Is(readErr, io.EOF) {
		runtime.scannerVersion = strings.TrimSpace(strings.TrimSuffix(reply, "\x00"))
	}
	return runtime.scannerVersion
}

func fileSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err = io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func (runtime *fileProcessingRuntime) downloadToTemp(job fileProcessingJob, workDir string) (string, error) {
	handler := store.Store.GetMediaHandler()
	extension := ""
	if extensions, _ := mime.ExtensionsByType(job.File.MimeType); len(extensions) > 0 {
		extension = extensions[0]
	}
	target := filepath.Join(workDir, "source"+extension)
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	defer out.Close()

	_, err = copyStoredMediaURL(handler, job.URL, out,
		time.Duration(runtime.config.Timeout)*time.Second)
	return target, err
}

func scanFileWithClamAV(path, address string, timeout time.Duration) (bool, error) {
	connection, err := dialClamAV("tcp", address, timeout)
	if err != nil {
		return false, err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(timeout))
	if _, err = connection.Write([]byte("zINSTREAM\x00")); err != nil {
		return false, err
	}
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	buffer := make([]byte, 64*1024)
	var size [4]byte
	for {
		count, readErr := file.Read(buffer)
		if count > 0 {
			binary.BigEndian.PutUint32(size[:], uint32(count))
			if _, err = connection.Write(size[:]); err != nil {
				return false, err
			}
			if _, err = connection.Write(buffer[:count]); err != nil {
				return false, err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return false, readErr
		}
	}
	if _, err = connection.Write([]byte{0, 0, 0, 0}); err != nil {
		return false, err
	}
	reply, err := bufio.NewReader(connection).ReadString(0)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	if strings.Contains(reply, "FOUND") {
		return true, nil
	}
	if !strings.Contains(reply, "OK") {
		return false, fmt.Errorf("clamav scan failed: %s", strings.TrimSpace(reply))
	}
	return false, nil
}

func (runtime *fileProcessingRuntime) generatePreviews(
	job fileProcessingJob,
	source, workDir string,
) (map[string]string, error) {
	previews := map[string]string{"original": job.URL}
	mimeType := strings.ToLower(job.File.MimeType)
	if mimeType == "application/pdf" {
		separator := "?"
		if strings.Contains(job.URL, "?") {
			separator = "&"
		}
		previews["document"] = job.URL + separator + "preview=true"
	}
	type generatedOutput struct {
		label string
		path  string
		mime  string
	}
	outputs := make([]generatedOutput, 0, 6)
	switch {
	case strings.HasPrefix(mimeType, "image/") && runtime.config.FFmpeg != "":
		for _, variant := range []struct {
			label string
			width int
		}{{"image_small", 320}, {"image", 1024}, {"image_large", 2048}} {
			output := filepath.Join(workDir, variant.label+".webp")
			filter := fmt.Sprintf("scale='min(%d,iw)':-2:flags=lanczos", variant.width)
			if err := runtime.run(runtime.config.FFmpeg, "-y", "-i", source,
				"-vf", filter, "-quality", "75", output); err != nil {
				return nil, err
			}
			outputs = append(outputs, generatedOutput{variant.label, output, "image/webp"})
		}
	case strings.HasPrefix(mimeType, "video/") && runtime.config.FFmpeg != "":
		poster := filepath.Join(workDir, "poster.jpg")
		if err := runtime.run(runtime.config.FFmpeg, "-y", "-ss", "00:00:01", "-i", source,
			"-frames:v", "1", "-vf", "scale='min(1280,iw)':-2", poster); err != nil {
			return nil, err
		}
		outputs = append(outputs, generatedOutput{"poster", poster, "image/jpeg"})
		for _, variant := range []struct {
			label, width, videoRate, audioRate string
		}{
			{"video_360p", "640", "700k", "64k"},
			{"video_720p", "1280", "1800k", "96k"},
			{"video_1080p", "1920", "3500k", "128k"},
		} {
			output := filepath.Join(workDir, variant.label+".mp4")
			filter := "scale='min(" + variant.width + ",iw)':-2:flags=lanczos"
			if err := runtime.run(runtime.config.FFmpeg, "-y", "-i", source,
				"-c:v", "libx264", "-preset", "medium", "-crf", "24",
				"-maxrate", variant.videoRate, "-bufsize", variant.videoRate,
				"-vf", filter, "-c:a", "aac", "-b:a", variant.audioRate,
				"-movflags", "+faststart", output); err != nil {
				return nil, err
			}
			outputs = append(outputs, generatedOutput{variant.label, output, "video/mp4"})
		}
	case strings.HasPrefix(mimeType, "audio/") && runtime.config.FFmpeg != "":
		output := filepath.Join(workDir, "preview.opus")
		if err := runtime.run(runtime.config.FFmpeg, "-y", "-i", source,
			"-c:a", "libopus", "-b:a", "64k", output); err != nil {
			return nil, err
		}
		outputs = append(outputs, generatedOutput{"audio", output, "audio/ogg"})
	case isOfficeDocument(mimeType) && runtime.config.LibreOffice != "":
		if err := runtime.run(runtime.config.LibreOffice, "--headless", "--convert-to", "pdf",
			"--outdir", workDir, source); err != nil {
			return nil, err
		}
		matches, _ := filepath.Glob(filepath.Join(workDir, "*.pdf"))
		if len(matches) == 0 {
			return nil, errors.New("libreoffice produced no PDF")
		}
		outputs = append(outputs, generatedOutput{"document", matches[0], "application/pdf"})
	}
	if len(outputs) == 0 {
		return previews, nil
	}
	for _, output := range outputs {
		generatedURL, err := uploadGeneratedPreview(job.File, output.path, output.mime)
		if err != nil {
			return nil, err
		}
		if err = store.CopyFileAccess(job.File.Id, generatedURL); err != nil {
			return nil, err
		}
		if output.mime == "application/pdf" {
			separator := "?"
			if strings.Contains(generatedURL, "?") {
				separator = "&"
			}
			generatedURL += separator + "preview=true"
		}
		previews[output.label] = generatedURL
	}
	if previews["video_720p"] != "" {
		previews["video"] = previews["video_720p"]
	}
	return previews, nil
}

func (runtime *fileProcessingRuntime) run(command string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(runtime.config.Timeout)*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("%s failed: %w: %s", filepath.Base(command), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func isOfficeDocument(mimeType string) bool {
	return mimeType == "application/msword" ||
		mimeType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" ||
		mimeType == "application/vnd.ms-excel" ||
		mimeType == "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" ||
		mimeType == "application/vnd.ms-powerpoint" ||
		mimeType == "application/vnd.openxmlformats-officedocument.presentationml.presentation" ||
		mimeType == "application/vnd.oasis.opendocument.text" ||
		mimeType == "application/vnd.oasis.opendocument.spreadsheet" ||
		mimeType == "application/vnd.oasis.opendocument.presentation"
}

func uploadGeneratedPreview(parent *types.FileDef, path, mimeType string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	definition := &types.FileDef{
		ObjHeader: types.ObjHeader{Id: store.Store.GetUidString()},
		User:      parent.User,
		MimeType:  mimeType,
	}
	definition.InitTimes()
	handler := store.Store.GetMediaHandler()
	rawURL, _, _, err := uploadAndFinalizeFile(handler, store.Files, definition, file, 0)
	if err != nil {
		return "", err
	}
	state, _ := store.GetFileProcessingState(definition.Id)
	if state == nil {
		state = &store.FileProcessingState{}
	}
	state.ScanStatus = "clean"
	state.ProcessStatus = "ready"
	_ = store.SetFileProcessingState(definition.Id, *state)
	return rawURL, nil
}
