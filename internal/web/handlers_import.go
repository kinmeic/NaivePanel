package web

import (
	"fmt"

	"github.com/kinmeic/NaivePanel/internal/sites"
)

// importSiteFromDisk parses an on-disk snippet into a site model. Snippets
// that don't match the panel's rendered shape are imported in raw mode so
// their content is preserved verbatim; note then carries the mismatch reason
// for display. Returns the site, the import mode label ("结构化" /
// "高级模式"), the note, and a fatal error only when no domain can be found.
func (s *Server) importSiteFromDisk(content string) (*sites.Site, string, string, error) {
	if st, err := sites.Parse(content, s.Cfg.BasePath, s.Cfg.BypassCore.SocksPort); err == nil {
		return st, "结构化", "", nil
	} else {
		domain := sites.DomainFromHeader(content)
		if domain == "" {
			return nil, "", "", fmt.Errorf("无法从配置中识别站点域名")
		}
		raw := &sites.Site{Domain: domain, RawMode: true, Raw: content}
		return raw, "高级模式", err.Error(), nil
	}
}
