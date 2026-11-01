package caddymgr

import (
	"fmt"
	"os"
	"os/user"
	"sync"
)

// Caddy 的系统服务通常以非 root 用户运行（Debian/Ubuntu 官方包的 unit 是
// User=caddy，xcaddy 官方 unit 亦如此）。站点片段里含有面板反代共享密钥与
// basic_auth 哈希，既不能设为 0600（caddy 用户读不到，服务直接起不来），
// 也不宜设为 0644（同机其他用户可读）。因此统一写成 root:caddy 0640；
// 系统上不存在 caddy 组时退化为 0644，保证任何运行用户都能读。
const snippetFileMode os.FileMode = 0o640
const snippetFallbackMode os.FileMode = 0o644

var (
	caddyGIDOnce sync.Once
	caddyGID     int
	caddyGIDOK   bool
)

func lookupCaddyGID() (int, bool) {
	caddyGIDOnce.Do(func() {
		if g, err := user.LookupGroup("caddy"); err == nil {
			var gid int
			if _, err := fmt.Sscanf(g.Gid, "%d", &gid); err == nil && gid > 0 {
				caddyGID, caddyGIDOK = gid, true
			}
		}
	})
	return caddyGID, caddyGIDOK
}

// writeSnippet 写入站点片段并调整权限，使 caddy 服务进程可读。
func writeSnippet(path string, data []byte) error {
	if err := os.WriteFile(path, data, snippetFileMode); err != nil {
		return err
	}
	return fixSnippetPerm(path)
}

// fixSnippetPerm 幂等地修正已有片段的属组与权限（用于历史/外来文件）。
func fixSnippetPerm(path string) error {
	if gid, ok := lookupCaddyGID(); ok {
		// 属主保持 root，仅换属组；chown 失败（如 NFS/只读）时退化为 0644
		if err := os.Chown(path, 0, gid); err == nil {
			return os.Chmod(path, snippetFileMode)
		}
	}
	return os.Chmod(path, snippetFallbackMode)
}
