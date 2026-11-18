//go:build darwin && cgo

package macnative

/*
#cgo CFLAGS: -fobjc-arc -mmacosx-version-min=14.0
#cgo LDFLAGS: -framework Cocoa -framework LocalAuthentication -framework Security
#include <stdlib.h>
#include "native_darwin.h"
*/
import "C"
import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unsafe"
)

const (
	accountDeviceService     = "io.github.lee-xeo.claude-cage.account-device"
	trialGuestKeychainSvc    = "io.github.lee-xeo.claude-cage.macos-trial"
	trialGuestKeychainAcct   = "guest-password"
)

type storedDeviceCredential struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
}

func accountKeychainName(kind, binding string) string {
	return kind + ":" + binding
}

// BiometricAvailable reports whether macOS Touch ID / biometry can evaluate now.
func BiometricAvailable() bool {
	return C.cage_biometric_available() != 0
}

// AccountDeviceStatus inspects macOS Keychain enrollment for both automatic and biometric credentials.
func AccountDeviceStatus(binding string, devices map[string]string) map[string]any {
	biometricAvail := BiometricAvailable()
	states := map[string]string{}
	if binding == "" {
		states["automatic"] = "disabled"
		states["biometric"] = "disabled"
		return map[string]any{
			"supported":           true,
			"biometric_available": biometricAvail,
			"states":              states,
		}
	}
	cSvc := C.CString(accountDeviceService)
	defer C.free(unsafe.Pointer(cSvc))

	for _, kind := range []string{"automatic", "biometric"} {
		expectedID := ""
		if devices != nil {
			expectedID = devices[kind]
		}
		if kind == "biometric" && expectedID == "" && !biometricAvail {
			states[kind] = "unavailable"
			continue
		}
		acct := accountKeychainName(kind, binding)
		cAcct := C.CString(acct)
		cID := C.CString(expectedID)
		isBio := C.int(0)
		if kind == "biometric" {
			isBio = 1
		}
		res := C.GoString(C.cage_keychain_status(cSvc, cAcct, cID, isBio))
		C.free(unsafe.Pointer(cAcct))
		C.free(unsafe.Pointer(cID))
		if kind == "biometric" && !biometricAvail && expectedID != "" {
			states[kind] = "unavailable"
		} else {
			states[kind] = res
		}
	}
	return map[string]any{
		"supported":           true,
		"biometric_available": biometricAvail,
		"states":              states,
	}
}

// StoreAccountDevice saves an account device secret into macOS Keychain.
func StoreAccountDevice(kind, binding, id, secret string) error {
	if kind != "automatic" && kind != "biometric" {
		return fmt.Errorf("不支持的快捷登录类型：%s", kind)
	}
	if strings.TrimSpace(binding) == "" || strings.TrimSpace(id) == "" || secret == "" {
		return errors.New("快捷登录凭据不完整")
	}
	payload, err := json.Marshal(storedDeviceCredential{ID: id, Secret: secret})
	if err != nil {
		return err
	}
	defer clear(payload)

	cSvc := C.CString(accountDeviceService)
	cAcct := C.CString(accountKeychainName(kind, binding))
	label := "Airlock 记住本机登录"
	isBio := C.int(0)
	if kind == "biometric" {
		label = "Airlock Touch ID 快捷解锁"
		isBio = 1
	}
	cLabel := C.CString(label)
	cID := C.CString(id)
	defer C.free(unsafe.Pointer(cSvc))
	defer C.free(unsafe.Pointer(cAcct))
	defer C.free(unsafe.Pointer(cLabel))
	defer C.free(unsafe.Pointer(cID))

	var cErr *C.char
	rc := C.cage_keychain_store(cSvc, cAcct, cLabel, unsafe.Pointer(&payload[0]), C.size_t(len(payload)), cID, isBio, &cErr)
	if cErr != nil {
		msg := C.GoString(cErr)
		C.free(unsafe.Pointer(cErr))
		if rc != 0 {
			return errors.New(msg)
		}
	}
	if rc != 0 {
		return errors.New("保存快捷登录凭据失败")
	}
	return nil
}

// ReadAccountDevice reads an account device credential from macOS Keychain, prompting Touch ID for biometric items.
func ReadAccountDevice(kind, binding, reason string) (string, string, error) {
	if kind != "automatic" && kind != "biometric" {
		return "", "", fmt.Errorf("不支持的快捷登录类型：%s", kind)
	}
	if strings.TrimSpace(binding) == "" {
		return "", "", errors.New("缺少账户设备绑定标识")
	}
	if reason == "" {
		reason = "验证本机身份以解锁 Airlock"
	}
	cSvc := C.CString(accountDeviceService)
	cAcct := C.CString(accountKeychainName(kind, binding))
	cReason := C.CString(reason)
	defer C.free(unsafe.Pointer(cSvc))
	defer C.free(unsafe.Pointer(cAcct))
	defer C.free(unsafe.Pointer(cReason))

	interactive := C.int(0)
	if kind == "biometric" {
		interactive = 1
	}
	var outData unsafe.Pointer
	var outLen C.size_t
	var cErr *C.char
	rc := C.cage_keychain_read(cSvc, cAcct, cReason, interactive, &outData, &outLen, &cErr)
	if outData != nil {
		defer C.free(outData)
	}
	if cErr != nil {
		msg := C.GoString(cErr)
		C.free(unsafe.Pointer(cErr))
		if rc != 0 {
			return "", "", errors.New(msg)
		}
	}
	if rc != 0 || outData == nil {
		return "", "", errors.New("快捷登录不可用，请使用密码解锁后重新开启")
	}
	raw := C.GoBytes(outData, C.int(outLen))
	defer clear(raw)
	var cred storedDeviceCredential
	if err := json.Unmarshal(raw, &cred); err != nil || cred.ID == "" || cred.Secret == "" {
		return "", "", errors.New("本机快捷登录凭据已损坏，请使用密码解锁后重新开启")
	}
	return cred.ID, cred.Secret, nil
}

// RemoveAccountDevice deletes a stored account device credential from macOS Keychain.
func RemoveAccountDevice(kind, binding string) error {
	if kind == "" || strings.TrimSpace(binding) == "" {
		return nil
	}
	cSvc := C.CString(accountDeviceService)
	cAcct := C.CString(accountKeychainName(kind, binding))
	defer C.free(unsafe.Pointer(cSvc))
	defer C.free(unsafe.Pointer(cAcct))

	var cErr *C.char
	rc := C.cage_keychain_remove(cSvc, cAcct, &cErr)
	if cErr != nil {
		msg := C.GoString(cErr)
		C.free(unsafe.Pointer(cErr))
		if rc != 0 {
			return errors.New(msg)
		}
	}
	if rc != 0 {
		return errors.New("移除本机快捷登录凭据失败")
	}
	return nil
}

// StoreTrialGuestPassword saves a one-shot trial macOS guest login password into Keychain.
func StoreTrialGuestPassword(password string) error {
	if password == "" {
		return errors.New("试用密码不能为空")
	}
	raw := []byte(password)
	defer clear(raw)

	cSvc := C.CString(trialGuestKeychainSvc)
	cAcct := C.CString(trialGuestKeychainAcct)
	cLabel := C.CString("Airlock macOS 试用客机密码")
	defer C.free(unsafe.Pointer(cSvc))
	defer C.free(unsafe.Pointer(cAcct))
	defer C.free(unsafe.Pointer(cLabel))

	var cErr *C.char
	rc := C.cage_keychain_store(cSvc, cAcct, cLabel, unsafe.Pointer(&raw[0]), C.size_t(len(raw)), nil, 0, &cErr)
	if cErr != nil {
		msg := C.GoString(cErr)
		C.free(unsafe.Pointer(cErr))
		if rc != 0 {
			return errors.New(msg)
		}
	}
	if rc != 0 {
		return errors.New("无法保存试用客机密码到钥匙串")
	}
	return nil
}

// ConsumeTrialGuestPassword reads and deletes the trial macOS guest password from Keychain.
func ConsumeTrialGuestPassword() (string, error) {
	cSvc := C.CString(trialGuestKeychainSvc)
	cAcct := C.CString(trialGuestKeychainAcct)
	cReason := C.CString("读取 Airlock 试用客机自动登录密码")
	defer C.free(unsafe.Pointer(cSvc))
	defer C.free(unsafe.Pointer(cAcct))
	defer C.free(unsafe.Pointer(cReason))

	var outData unsafe.Pointer
	var outLen C.size_t
	var cErr *C.char
	rc := C.cage_keychain_read(cSvc, cAcct, cReason, 0, &outData, &outLen, &cErr)
	if outData != nil {
		defer C.free(outData)
	}
	if cErr != nil {
		msg := C.GoString(cErr)
		C.free(unsafe.Pointer(cErr))
		if rc != 0 {
			return "", errors.New(msg)
		}
	}
	if rc != 0 || outData == nil {
		return "", errors.New("未找到保存的试用客机密码")
	}
	pw := string(C.GoBytes(outData, C.int(outLen)))
	_ = C.cage_keychain_remove(cSvc, cAcct, nil)
	return pw, nil
}
