package hostimage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func linuxTemplateData(plan Plan) ([]byte, error) {
	// These supported cloud images need no distribution-specific template
	// settings. Keep one pinned local image, with no network image fallback.
	value := map[string]any{
		"minimumLimaVersion": "2.0.0",
		"images":             []map[string]string{{"location": plan.Path, "arch": map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[plan.Host.Arch], "digest": plan.Digest}},
	}
	return json.Marshal(value)
}

// ClearLinuxTemplate releases an unstarted choice while retaining its media.
func ClearLinuxTemplate(state string, plan Plan) error {
	dir := filepath.Join(state, "guest-media")
	path := filepath.Join(dir, "host-system.yaml")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	expected, err := linuxTemplateData(plan)
	if err != nil || string(expected) != string(data) {
		return errors.New("本机系统模板归属已变化")
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, sourceRecord)); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
