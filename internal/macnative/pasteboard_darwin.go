//go:build darwin && cgo

package macnative

/*
#cgo CFLAGS: -fobjc-arc -mmacosx-version-min=14.0
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#include "native_darwin.h"
*/
import "C"
import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"github.com/google/uuid"
)

const (
	MaxFileSelectionCount = 100
	MaxFileSelectionBytes = 2 * 1024 * 1024 * 1024
	fileSelectionTTL      = 10 * time.Minute
	maxFileSelections     = 16
)

// PasteboardChangeCount returns NSPasteboard.generalPasteboard.changeCount.
func PasteboardChangeCount() int {
	return int(C.cage_pasteboard_change_count())
}

// PasteboardReadText reads plain UTF-8 text from the general pasteboard.
func PasteboardReadText() string {
	ptr := C.cage_pasteboard_read_text()
	if ptr == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(ptr))
	return C.GoString(ptr)
}

// PasteboardWriteText writes plain UTF-8 text to the general pasteboard.
func PasteboardWriteText(text string) bool {
	c := C.CString(text)
	defer C.free(unsafe.Pointer(c))
	return C.cage_pasteboard_write_text(c) == 0
}

// PasteboardWriteConcealedText writes text marked ConcealedType & TransientType, clearing after d if unchanged.
func PasteboardWriteConcealedText(text string, clearSeconds int) bool {
	c := C.CString(text)
	defer C.free(unsafe.Pointer(c))
	return C.cage_pasteboard_write_concealed_text(c, C.int(clearSeconds)) == 0
}

// PasteboardReadFileURLs reads local file paths currently held on the general pasteboard.
func PasteboardReadFileURLs() []string {
	var out *C.char
	if C.cage_pasteboard_read_files_json(&out) != 0 || out == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(out))
	var paths []string
	if json.Unmarshal([]byte(C.GoString(out)), &paths) != nil {
		return nil
	}
	return paths
}

// PasteboardWriteFileURLs writes file URLs onto the general pasteboard.
func PasteboardWriteFileURLs(paths []string) bool {
	encoded, err := json.Marshal(paths)
	if err != nil {
		return false
	}
	c := C.CString(string(encoded))
	defer C.free(unsafe.Pointer(c))
	return C.cage_pasteboard_write_files_json(c) == 0
}

// PasteboardReadPNG reads an image from the general pasteboard encoded as PNG (<= 20 MB, <= 40 MP).
func PasteboardReadPNG() ([]byte, bool, error) {
	var outData unsafe.Pointer
	var outLen C.size_t
	var outErr *C.char
	rc := C.cage_pasteboard_read_png(&outData, &outLen, &outErr)
	if outErr != nil {
		defer C.free(unsafe.Pointer(outErr))
	}
	if outData != nil {
		defer C.free(outData)
	}
	if rc == 1 {
		return nil, false, nil
	}
	if rc != 0 {
		if outErr != nil {
			return nil, false, errors.New(C.GoString(outErr))
		}
		return nil, false, errors.New("剪贴板图片读取失败")
	}
	return C.GoBytes(outData, C.int(outLen)), true, nil
}

// PasteboardWritePNG writes PNG bytes to the general pasteboard as an NSImage.
func PasteboardWritePNG(png []byte) error {
	if len(png) == 0 {
		return errors.New("剪贴板内容格式或大小无效")
	}
	var outErr *C.char
	rc := C.cage_pasteboard_write_png(unsafe.Pointer(&png[0]), C.size_t(len(png)), &outErr)
	if outErr != nil {
		defer C.free(unsafe.Pointer(outErr))
	}
	if rc != 0 {
		if outErr != nil {
			return errors.New(C.GoString(outErr))
		}
		return errors.New("剪贴板写入失败")
	}
	return nil
}

type rememberedFiles struct {
	paths   []string
	expires time.Time
}

// FileSelectionStore remembers validated local file selections for uploadHostFiles.
type FileSelectionStore struct {
	mu    sync.Mutex
	items map[string]rememberedFiles
}

// NewFileSelectionStore creates an empty FileSelectionStore.
func NewFileSelectionStore() *FileSelectionStore {
	return &FileSelectionStore{items: make(map[string]rememberedFiles)}
}

// RememberFiles validates local regular files (1..100, non-symlink, <= 2 GiB) and returns a handle.
func (s *FileSelectionStore) RememberFiles(paths []string) (string, int, error) {
	if len(paths) == 0 || len(paths) > MaxFileSelectionCount {
		return "", 0, errors.New("请选择 1 至 100 个文件")
	}
	cleaned := make([]string, 0, len(paths))
	for _, raw := range paths {
		if raw == "" || !filepath.IsAbs(raw) {
			return "", 0, errors.New("请选择本机文件")
		}
		info, err := os.Lstat(raw)
		if err != nil {
			return "", 0, errors.New("请选择普通文件，暂不支持文件夹或链接")
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", 0, errors.New("请选择普通文件，暂不支持文件夹或链接")
		}
		if info.Size() > MaxFileSelectionBytes {
			return "", 0, errors.New("每个文件不能超过 2 GB")
		}
		cleaned = append(cleaned, filepath.Clean(raw))
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, entry := range s.items {
		if !entry.expires.After(now) {
			delete(s.items, id)
		}
	}
	if len(s.items) >= maxFileSelections {
		var oldestID string
		var oldestTime time.Time
		for id, entry := range s.items {
			if oldestID == "" || entry.expires.Before(oldestTime) {
				oldestID = id
				oldestTime = entry.expires
			}
		}
		if oldestID != "" {
			delete(s.items, oldestID)
		}
	}
	id := uuid.NewString()
	s.items[id] = rememberedFiles{paths: cleaned, expires: now.Add(fileSelectionTTL)}
	return id, len(cleaned), nil
}

// Take returns and removes the paths for selection ID if still valid.
func (s *FileSelectionStore) Take(id string) ([]string, bool) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.items[id]
	if !ok {
		return nil, false
	}
	delete(s.items, id)
	if !entry.expires.After(now) {
		return nil, false
	}
	return append([]string(nil), entry.paths...), true
}

// ClipboardCacheTracker mirrors Cage.swift's clipboard file cache garbage collection.
type ClipboardCacheTracker struct {
	mu        sync.Mutex
	loaded    bool
	copies    map[string][]string
	readers   map[string][]string
	pending   map[string]bool
	releasing map[string]bool
	started   bool
	stopCh    chan struct{}
}

// NewClipboardCacheTracker constructs a ClipboardCacheTracker.
func NewClipboardCacheTracker() *ClipboardCacheTracker {
	return &ClipboardCacheTracker{
		copies:    make(map[string][]string),
		readers:   make(map[string][]string),
		pending:   make(map[string]bool),
		releasing: make(map[string]bool),
	}
}

// Start begins periodic cache inspection (1s cadence) and restores existing caches once.
func (t *ClipboardCacheTracker) Start() {
	t.mu.Lock()
	if t.started {
		t.mu.Unlock()
		return
	}
	t.started = true
	t.stopCh = make(chan struct{})
	stopCh := t.stopCh
	t.mu.Unlock()

	go t.restore()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				t.collect()
			}
		}
	}()
}

// Stop terminates background cache cleanup.
func (t *ClipboardCacheTracker) Stop() {
	t.mu.Lock()
	if t.started && t.stopCh != nil {
		close(t.stopCh)
		t.started = false
	}
	t.mu.Unlock()
}

func (t *ClipboardCacheTracker) AddReader(id string, paths []string) {
	t.mu.Lock()
	t.readers[id] = append([]string(nil), paths...)
	t.mu.Unlock()
}

func (t *ClipboardCacheTracker) RemoveReader(id string) {
	t.mu.Lock()
	delete(t.readers, id)
	t.mu.Unlock()
	t.collect()
}

func (t *ClipboardCacheTracker) BeginPending(cacheID string, paths []string) {
	if cacheID == "" {
		return
	}
	t.mu.Lock()
	t.copies[cacheID] = append([]string(nil), paths...)
	t.pending[cacheID] = true
	t.mu.Unlock()
}

func (t *ClipboardCacheTracker) EndPending(cacheID string) {
	if cacheID == "" {
		return
	}
	t.mu.Lock()
	delete(t.pending, cacheID)
	t.mu.Unlock()
	t.collect()
}

func (t *ClipboardCacheTracker) Publish(cacheID string) {
	if cacheID == "" {
		return
	}
	go func() {
		_, _ = RunClipboardCacheCommand(context.Background(), "publish", cacheID)
	}()
}

func (t *ClipboardCacheTracker) Release(cacheID string) {
	if cacheID == "" {
		return
	}
	t.mu.Lock()
	t.releasing[cacheID] = true
	t.mu.Unlock()

	go func() {
		_, err := RunClipboardCacheCommand(context.Background(), "release", cacheID)
		t.mu.Lock()
		delete(t.releasing, cacheID)
		if err == nil {
			delete(t.copies, cacheID)
		}
		t.mu.Unlock()
	}()
}

func (t *ClipboardCacheTracker) restore() {
	res, err := RunClipboardCacheCommand(context.Background(), "list", "")
	t.mu.Lock()
	t.loaded = true
	if err == nil {
		if selections, ok := res["selections"].([]any); ok {
			for _, raw := range selections {
				sel, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				id, _ := sel["cache_id"].(string)
				rawPaths, _ := sel["paths"].([]any)
				var paths []string
				for _, p := range rawPaths {
					if s, ok := p.(string); ok && s != "" {
						paths = append(paths, s)
					}
				}
				if id != "" && len(paths) > 0 {
					t.copies[id] = paths
				}
			}
		}
	}
	t.mu.Unlock()
	t.collect()
}

func (t *ClipboardCacheTracker) collect() {
	t.mu.Lock()
	if !t.loaded || len(t.copies) == 0 {
		t.mu.Unlock()
		return
	}
	readers := make([]string, 0)
	for _, list := range t.readers {
		readers = append(readers, list...)
	}
	type candidate struct {
		id   string
		root string
	}
	var candidates []candidate
	for id, paths := range t.copies {
		if t.pending[id] || t.releasing[id] || len(paths) == 0 {
			continue
		}
		candidates = append(candidates, candidate{id: id, root: filepath.Clean(filepath.Dir(paths[0]))})
	}
	t.mu.Unlock()

	if len(candidates) == 0 {
		return
	}
	selected := PasteboardReadFileURLs()
	all := append(selected, readers...)
	for _, c := range candidates {
		referenced := false
		for _, p := range all {
			cp := filepath.Clean(p)
			if cp == c.root || hasPathPrefix(cp, c.root) || hasPathPrefix(c.root, cp) {
				referenced = true
				break
			}
		}
		if !referenced {
			t.Release(c.id)
		}
	}
}

func hasPathPrefix(path, prefix string) bool {
	if prefix == "/" {
		return len(path) > 1 && path[0] == '/'
	}
	return len(path) > len(prefix) && path[:len(prefix)] == prefix && path[len(prefix)] == '/'
}
