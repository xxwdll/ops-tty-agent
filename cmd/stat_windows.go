//go:build windows

package cmd

import "os"

// fillOwnerGroup 在 Windows 上为空实现（无 uid/gid 概念）
func fillOwnerGroup(st os.FileInfo, res *StatResponse) {
	// Windows 不填充 owner/group
}
