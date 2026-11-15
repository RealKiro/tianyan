//go:build windows

package block

import (
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func (e *Enforcer) enforceAppsOnce() {
	e.mu.Lock()
	policy := e.policy
	cb := e.OnViolation
	doUSB := policy.BlockUSBGames && time.Since(e.lastUSBCheck) >= 10*time.Second
	if doUSB {
		e.lastUSBCheck = time.Now()
	}
	e.mu.Unlock()

	if policy.Mode == "black" && len(policy.Apps) > 0 {
		e.killBlacklisted(policy.Apps, cb)
	}
	if doUSB {
		e.checkUSBGames(policy, cb)
	}
}

// killBlacklisted terminates blacklisted running processes by image name.
func (e *Enforcer) killBlacklisted(apps []string, cb func(kind, target string)) {
	out, err := exec.Command("tasklist", "/FO", "CSV", "/NH").Output()
	if err != nil {
		return
	}
	running := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		i := strings.IndexByte(line, ',')
		if i <= 0 {
			continue
		}
		name := strings.Trim(line[:i], `"`)
		running[strings.ToLower(name)] = true
	}
	for _, app := range apps {
		if running[app] {
			_ = exec.Command("taskkill", "/F", "/IM", app).Run()
			if cb != nil {
				cb("app", app)
			}
		}
	}
}

const usbQuery = `$ErrorActionPreference='SilentlyContinue';
$drives=@(Get-CimInstance Win32_LogicalDisk -Filter 'DriveType=2' | Select-Object -ExpandProperty DeviceID);
$procs=@(Get-CimInstance Win32_Process | Select-Object ProcessId,Name,ExecutablePath);
[pscustomobject]@{drives=$drives;procs=$procs} | ConvertTo-Json -Depth 3 -Compress`

type usbSnapshot struct {
	Drives []string `json:"drives"`
	Procs  []struct {
		ProcessID      int     `json:"ProcessId"`
		Name           string  `json:"Name"`
		ExecutablePath *string `json:"ExecutablePath"`
	} `json:"procs"`
}

// usbGameKeywords are name heuristics for portable games launched from
// removable media. Combined with the removable-drive location this avoids
// false positives on ordinary tools.
var usbGameKeywords = []string{
	"game", "minecraft", "pokemon", "宝可梦", "口袋妖怪",
	"genshin", "原神", "crossfire", "pubg", "dota", "lol", "gta",
	"roblox", "steam", "simulator", "emulator", "模拟器", "游戏", "网游", "单机",
}

func (e *Enforcer) checkUSBGames(policy Policy, cb func(kind, target string)) {
	out, err := exec.Command("powershell", "-NoProfile", "-Command", usbQuery).Output()
	if err != nil {
		return
	}
	var snap usbSnapshot
	if json.Unmarshal(out, &snap) != nil || len(snap.Drives) == 0 {
		return
	}

	appSet := map[string]bool{}
	for _, a := range policy.Apps {
		appSet[a] = true
	}

	for _, p := range snap.Procs {
		if p.ExecutablePath == nil || *p.ExecutablePath == "" {
			continue
		}
		path := strings.ToLower(*p.ExecutablePath)
		name := strings.ToLower(p.Name)
		if !onRemovable(path, snap.Drives) {
			continue
		}
		if appSet[name] || usbGameName(name) {
			_ = exec.Command("taskkill", "/F", "/PID", strconv.Itoa(p.ProcessID)).Run()
			if cb != nil {
				cb("usb_game", p.Name)
			}
		}
	}
}

func onRemovable(path string, drives []string) bool {
	for _, d := range drives {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if !strings.HasSuffix(d, ":") {
			d += ":"
		}
		if strings.HasPrefix(path, d+`\`) {
			return true
		}
	}
	return false
}

func usbGameName(name string) bool {
	for _, k := range usbGameKeywords {
		if strings.Contains(name, k) {
			return true
		}
	}
	return false
}
