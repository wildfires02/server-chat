package store

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"chat/server/media"
	"chat/server/store/types"
)

type fileSecurityMediaHandler struct{}

func (fileSecurityMediaHandler) Init(string) error { return nil }
func (fileSecurityMediaHandler) Headers(string, *url.URL, http.Header, bool) (http.Header, int, error) {
	return nil, 0, nil
}
func (fileSecurityMediaHandler) Upload(*types.FileDef, io.Reader) (string, int64, error) {
	return "", 0, nil
}
func (fileSecurityMediaHandler) Download(string) (*types.FileDef, media.ReadSeekCloser, error) {
	return nil, nil, types.ErrNotFound
}
func (fileSecurityMediaHandler) Delete([]string) error { return nil }
func (fileSecurityMediaHandler) GetIdFromUrl(rawURL string) types.Uid {
	return types.ParseUid(strings.TrimPrefix(rawURL, "/file/"))
}

type fileSecurityFiles struct {
	definition *types.FileDef
}

func (files *fileSecurityFiles) StartUpload(*types.FileDef) error { return nil }
func (files *fileSecurityFiles) FinishUpload(def *types.FileDef, _ bool, _ int64) (*types.FileDef, error) {
	return def, nil
}
func (files *fileSecurityFiles) Get(fid string) (*types.FileDef, error) {
	if files.definition != nil && files.definition.Id == fid {
		return files.definition, nil
	}
	return nil, types.ErrNotFound
}
func (files *fileSecurityFiles) DeleteUnused(time.Time, int) error { return nil }
func (files *fileSecurityFiles) LinkAttachments(string, types.Uid, []string) error {
	return nil
}

type fileSecuritySubs struct {
	topic string
	user  types.Uid
}

func (*fileSecuritySubs) Create(...*types.Subscription) error { return nil }
func (subs *fileSecuritySubs) Get(topic string, user types.Uid, _ bool) (*types.Subscription, error) {
	if topic == subs.topic && user == subs.user {
		return &types.Subscription{
			ModeWant:  types.ModeCReadOnly,
			ModeGiven: types.ModeCReadOnly,
		}, nil
	}
	return nil, types.ErrNotFound
}
func (*fileSecuritySubs) Update(string, types.Uid, map[string]any) error { return nil }
func (*fileSecuritySubs) Delete(string, types.Uid) error                 { return nil }

func TestFileDownloadACL(t *testing.T) {
	useMemoryPersistentCache(t)
	oldFiles, oldSubs, oldMedia := Files, Subs, mediaHandler
	t.Cleanup(func() {
		Files, Subs, mediaHandler = oldFiles, oldSubs, oldMedia
	})

	fid := types.Uid(800)
	owner := types.Uid(10)
	member := types.Uid(20)
	stranger := types.Uid(30)
	rawURL := "/file/" + fid.String()
	Files = &fileSecurityFiles{definition: &types.FileDef{
		ObjHeader: types.ObjHeader{Id: fid.String()},
		User:      owner.String(),
	}}
	// 频道读者订阅 chn...，而消息与附件绑定在对应的 grp...。
	Subs = &fileSecuritySubs{topic: "chn-secure", user: member}
	mediaHandler = fileSecurityMediaHandler{}

	if _, err := AuthorizeFileDownload(owner, rawURL); err != nil {
		t.Fatalf("owner denied: %v", err)
	}
	if err := GrantFileAccess("grp-secure", types.ZeroUid, []string{rawURL}); err != nil {
		t.Fatal(err)
	}
	if _, err := AuthorizeFileDownload(member, rawURL); err != nil {
		t.Fatalf("topic reader denied: %v", err)
	}
	if _, topic, err := AuthorizeFileDownloadContext(member, rawURL); err != nil || topic != "grp-secure" {
		t.Fatalf("topic context: want grp-secure, got %q, %v", topic, err)
	}
	if _, err := AuthorizeFileDownload(stranger, rawURL); err != types.ErrPermissionDenied {
		t.Fatalf("stranger: want denied, got %v", err)
	}
	if err := SetFilePublicAccess(rawURL, true); err != nil {
		t.Fatal(err)
	}
	if _, err := AuthorizeFileDownload(stranger, rawURL); err != nil {
		t.Fatalf("published asset denied: %v", err)
	}
	if !isFileGCProtected(fid.String()) {
		t.Fatal("published asset was not protected from file GC")
	}
	if err := ValidateFileAttachments(stranger, []string{rawURL}); err != types.ErrPermissionDenied {
		t.Fatalf("non-owner attachment: want denied, got %v", err)
	}
}

func TestQuarantinedFileCannotBeAttachedOrDownloadedByRecipient(t *testing.T) {
	useMemoryPersistentCache(t)
	oldFiles, oldSubs, oldMedia := Files, Subs, mediaHandler
	t.Cleanup(func() {
		Files, Subs, mediaHandler = oldFiles, oldSubs, oldMedia
	})
	fid := types.Uid(801)
	owner := types.Uid(10)
	member := types.Uid(20)
	rawURL := "/file/" + fid.String()
	Files = &fileSecurityFiles{definition: &types.FileDef{
		ObjHeader: types.ObjHeader{Id: fid.String()},
		User:      owner.String(),
	}}
	Subs = &fileSecuritySubs{topic: "grp-secure", user: member}
	mediaHandler = fileSecurityMediaHandler{}
	if err := GrantFileAccess("grp-secure", types.ZeroUid, []string{rawURL}); err != nil {
		t.Fatal(err)
	}
	if err := SetFileProcessingState(fid.String(), FileProcessingState{ScanStatus: "quarantined"}); err != nil {
		t.Fatal(err)
	}
	if _, err := AuthorizeFileDownload(member, rawURL); err != types.ErrPermissionDenied {
		t.Fatalf("quarantined recipient download: want denied, got %v", err)
	}
	if err := ValidateFileAttachments(owner, []string{rawURL}); err != types.ErrPermissionDenied {
		t.Fatalf("quarantined attachment: want denied, got %v", err)
	}
}

func TestFileURLsWithPreviewsIncludesDerivedFiles(t *testing.T) {
	useMemoryPersistentCache(t)
	oldMedia := mediaHandler
	t.Cleanup(func() { mediaHandler = oldMedia })
	mediaHandler = fileSecurityMediaHandler{}
	source := types.Uid(901)
	preview := types.Uid(902)
	sourceURL := "/file/" + source.String()
	previewURL := "/file/" + preview.String()
	if err := SetFileProcessingState(source.String(), FileProcessingState{
		ScanStatus: "clean",
		Preview:    map[string]string{"poster": previewURL, "original": sourceURL},
	}); err != nil {
		t.Fatal(err)
	}
	urls := FileURLsWithPreviews([]string{sourceURL})
	if len(urls) != 2 || urls[0] != sourceURL || urls[1] != previewURL {
		t.Fatalf("unexpected expanded URLs: %#v", urls)
	}
}
