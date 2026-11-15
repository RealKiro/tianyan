package block

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

const (
	blockStart = "# ==== TianYan managed block (do not edit) ===="
	blockEnd   = "# ==== end TianYan block ===="
)

func hostsPath() string {
	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		return root + `\System32\drivers\etc\hosts`
	}
	return "/etc/hosts"
}

// applyHosts writes the given sites into the managed hosts block. A nil or
// empty slice removes the managed block. Requires administrator/root.
func applyHosts(sites []string) error {
	path := hostsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(data)
	if !strings.Contains(content, blockStart) && len(sites) > 0 {
		// Keep a one-time backup of the original file.
		_ = os.WriteFile(path+".tianyan.bak", data, 0644)
	}
	content = stripManagedBlock(content)

	if len(sites) > 0 {
		var b strings.Builder
		b.WriteString(blockStart)
		b.WriteString("\n")
		for _, s := range sites {
			b.WriteString("127.0.0.1 " + s + "\n")
			b.WriteString("127.0.0.1 www." + s + "\n")
		}
		b.WriteString(blockEnd)
		b.WriteString("\n")
		content = strings.TrimRight(content, "\n") + "\n\n" + b.String()
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("block: cannot update hosts (run as admin/root): %w", err)
	}
	return nil
}

// RestoreHosts removes the TianYan-managed block from the hosts file.
func RestoreHosts() error { return applyHosts(nil) }

func stripManagedBlock(content string) string {
	for {
		i := strings.Index(content, blockStart)
		if i < 0 {
			return content
		}
		j := strings.Index(content, blockEnd)
		if j < 0 {
			return content[:i]
		}
		content = content[:i] + content[j+len(blockEnd):]
	}
}
