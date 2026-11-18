package macnative

import "strings"

// KeyCodes maps macOS virtual key codes to DOM KeyboardEvent.code names.
// Keep in sync with scripts/guest-os/MacKeyboard.swift.
var KeyCodes = map[uint16]string{
	0: "KeyA", 1: "KeyS", 2: "KeyD", 3: "KeyF", 4: "KeyH", 5: "KeyG", 6: "KeyZ", 7: "KeyX", 8: "KeyC", 9: "KeyV",
	10: "IntlBackslash", 11: "KeyB", 12: "KeyQ", 13: "KeyW", 14: "KeyE", 15: "KeyR", 16: "KeyY", 17: "KeyT",
	18: "Digit1", 19: "Digit2", 20: "Digit3", 21: "Digit4", 22: "Digit6", 23: "Digit5", 24: "Equal", 25: "Digit9",
	26: "Digit7", 27: "Minus", 28: "Digit8", 29: "Digit0", 30: "BracketRight", 31: "KeyO", 32: "KeyU", 33: "BracketLeft",
	34: "KeyI", 35: "KeyP", 36: "Enter", 37: "KeyL", 38: "KeyJ", 39: "Quote", 40: "KeyK", 41: "Semicolon",
	42: "Backslash", 43: "Comma", 44: "Slash", 45: "KeyN", 46: "KeyM", 47: "Period", 48: "Tab", 49: "Space",
	50: "Backquote", 51: "Backspace", 53: "Escape", 54: "MetaRight", 55: "MetaLeft", 56: "ShiftLeft", 57: "CapsLock",
	58: "AltLeft", 59: "ControlLeft", 60: "ShiftRight", 61: "AltRight", 62: "ControlRight",
	64: "F17", 65: "NumpadDecimal", 67: "NumpadMultiply", 69: "NumpadAdd", 71: "NumLock", 75: "NumpadDivide",
	76: "NumpadEnter", 78: "NumpadSubtract", 79: "F18", 80: "F19", 81: "NumpadEqual", 82: "Numpad0", 83: "Numpad1",
	84: "Numpad2", 85: "Numpad3", 86: "Numpad4", 87: "Numpad5", 88: "Numpad6", 89: "Numpad7", 90: "F20",
	91: "Numpad8", 92: "Numpad9", 95: "NumpadComma", 96: "F5", 97: "F6", 98: "F7", 99: "F3", 100: "F8", 101: "F9", 103: "F11",
	105: "F13", 106: "F16", 107: "F14", 109: "F10", 111: "F12", 113: "F15", 114: "Insert", 115: "Home",
	116: "PageUp", 117: "Delete", 118: "F4", 119: "End", 120: "F2", 121: "PageDown", 122: "F1",
	123: "ArrowLeft", 124: "ArrowRight", 125: "ArrowDown", 126: "ArrowUp",
}

// CodeToKeyCode maps DOM code names back to macOS virtual key codes.
var CodeToKeyCode = func() map[string]uint16 {
	m := make(map[string]uint16, len(KeyCodes))
	for k, v := range KeyCodes {
		m[v] = k
	}
	return m
}()

var keyNames = map[string]string{
	"MetaLeft": "Meta", "MetaRight": "Meta", "ControlLeft": "Control", "ControlRight": "Control",
	"ShiftLeft": "Shift", "ShiftRight": "Shift", "AltLeft": "Alt", "AltRight": "Alt", "Space": " ",
	"NumpadEnter": "Enter", "NumpadDecimal": ".", "NumpadMultiply": "*", "NumpadAdd": "+",
	"NumpadDivide": "/", "NumpadSubtract": "-", "NumpadEqual": "=", "NumpadComma": ",",
}

var symbolCodes = map[string]string{
	" ": "Space", "=": "Equal", "+": "Equal", "-": "Minus", "_": "Minus", "[": "BracketLeft", "{": "BracketLeft",
	"]": "BracketRight", "}": "BracketRight", "'": "Quote", "\"": "Quote", ";": "Semicolon", ":": "Semicolon",
	"\\": "Backslash", "|": "Backslash", ",": "Comma", "<": "Comma", ".": "Period", ">": "Period",
	"/": "Slash", "?": "Slash", "`": "Backquote", "~": "Backquote",
}

var punctuationCodes = map[string]bool{
	"Equal": true, "Minus": true, "BracketLeft": true, "BracketRight": true, "Quote": true,
	"Semicolon": true, "Backslash": true, "IntlBackslash": true, "Comma": true, "Period": true,
	"Slash": true, "Backquote": true,
}

// KeyIdentity returns the DOM key and code strings for a macOS virtual key code and characters.
func KeyIdentity(keyCode uint16, characters string) (key, code string) {
	c, ok := KeyCodes[keyCode]
	if !ok {
		if characters == "" {
			return "Unidentified", "Unidentified"
		}
		return characters, "Unidentified"
	}
	if k, found := keyNames[c]; found {
		return k, c
	}
	if strings.HasPrefix(c, "Key") || strings.HasPrefix(c, "Digit") || strings.HasPrefix(c, "Numpad") || punctuationCodes[c] {
		if characters == "" {
			return "Unidentified", c
		}
		return characters, c
	}
	return c, c
}

// VirtualKey resolves a macOS virtual key code from DOM code/key values.
func VirtualKey(code, key string, shortcut bool) (uint16, bool) {
	if shortcut && len(key) == 1 {
		upper := strings.ToUpper(key)
		b := upper[0]
		if b >= 'A' && b <= 'Z' {
			if kc, ok := CodeToKeyCode["Key"+upper]; ok {
				return kc, true
			}
		}
		if b >= '0' && b <= '9' {
			if kc, ok := CodeToKeyCode["Digit"+key]; ok {
				return kc, true
			}
		}
		if sym, ok := symbolCodes[key]; ok {
			if kc, ok := CodeToKeyCode[sym]; ok {
				return kc, true
			}
		}
	}
	if kc, ok := CodeToKeyCode[code]; ok {
		return kc, true
	}
	if kc, ok := CodeToKeyCode[key]; ok {
		return kc, true
	}
	if sym, ok := symbolCodes[key]; ok {
		if kc, ok := CodeToKeyCode[sym]; ok {
			return kc, true
		}
	}
	return 0, false
}
