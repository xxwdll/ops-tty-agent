//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"os/user"
	"syscall"
)

// fillOwnerGroup 在 Unix 系统上填充文件的 owner 和 group
func fillOwnerGroup(st os.FileInfo, res *StatResponse) {
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		if u, err := user.LookupId(fmt.Sprintf("%d", sys.Uid)); err == nil {
			res.Owner = u.Username
		}
		if g, err := user.LookupGroupId(fmt.Sprintf("%d", sys.Gid)); err == nil {
			res.Group = g.Name
		}
	}
}
