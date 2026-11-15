package vnc

// keysymToKey maps common X11 keysyms (sent by VNC clients) to the key names
// understood by the control package (browser KeyboardEvent.code style).
func keysymToKey(ks uint32) (string, bool) {
	switch {
	case ks >= 0x61 && ks <= 0x7a: // a-z
		return "Key" + string(rune('A'+ks-0x61)), true
	case ks >= 0x41 && ks <= 0x5a: // A-Z
		return "Key" + string(rune(ks)), true
	case ks >= 0x30 && ks <= 0x39: // 0-9
		return "Digit" + string(rune(ks)), true
	}

	switch ks {
	case 0x20:
		return "Space", true
	case 0x0D, 0xFF0D:
		return "Enter", true
	case 0x08, 0xFF08:
		return "Backspace", true
	case 0x09, 0xFF09:
		return "Tab", true
	case 0x1B, 0xFF1B:
		return "Escape", true
	case 0xFF51:
		return "ArrowLeft", true
	case 0xFF52:
		return "ArrowUp", true
	case 0xFF53:
		return "ArrowRight", true
	case 0xFF54:
		return "ArrowDown", true
	case 0xFF50:
		return "Home", true
	case 0xFF57:
		return "End", true
	case 0xFF55:
		return "PageUp", true
	case 0xFF56:
		return "PageDown", true
	case 0xFF63:
		return "Insert", true
	case 0xFFFF:
		return "Delete", true
	case 0xFFE1:
		return "ShiftLeft", true
	case 0xFFE2:
		return "ShiftRight", true
	case 0xFFE3:
		return "ControlLeft", true
	case 0xFFE4:
		return "ControlRight", true
	case 0xFFE9:
		return "AltLeft", true
	case 0xFFEA:
		return "AltRight", true
	case 0xFFEB:
		return "MetaLeft", true
	case 0xFFEC:
		return "MetaRight", true
	case 0xFFE5:
		return "CapsLock", true
	case 0x2D:
		return "Minus", true
	case 0x3D:
		return "Equal", true
	case 0x2C:
		return "Comma", true
	case 0x2E:
		return "Period", true
	case 0x2F:
		return "Slash", true
	case 0x3B:
		return "Semicolon", true
	case 0x27:
		return "Quote", true
	case 0x5B:
		return "BracketLeft", true
	case 0x5D:
		return "BracketRight", true
	case 0x5C:
		return "Backslash", true
	case 0x60:
		return "Backquote", true
	}

	if ks >= 0xFFBE && ks <= 0xFFC9 { // F1..F12
		return "F" + string(rune('1'+ks-0xFFBE)), true
	}
	return "", false
}
