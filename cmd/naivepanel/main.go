// naivepanel is the NaivePanel server binary.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kinmeic/NaivePanel/internal/auth"
	"github.com/kinmeic/NaivePanel/internal/bypasscore"
	"github.com/kinmeic/NaivePanel/internal/config"
	"github.com/kinmeic/NaivePanel/internal/geo"
	"github.com/kinmeic/NaivePanel/internal/selfupdate"
	"github.com/kinmeic/NaivePanel/internal/service"
	"github.com/kinmeic/NaivePanel/internal/web"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "hash-password":
			runHashPassword()
			return
		case "gen-path":
			fmt.Println(config.RandomPath())
			return
		case "install":
			runServiceInstall()
			return
		case "uninstall":
			runServiceUninstall()
			return
		case "version", "--version", "-V":
			fmt.Println("naivepanel " + version)
			return
		}
	}

	cfgPath := flag.String("config", config.DefaultConfigPath, "面板配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		log.Fatalf("创建目录失败: %v", err)
	}

	srv, err := web.New(cfg, version)
	if err != nil {
		log.Fatalf("初始化 Web 服务失败: %v", err)
	}

	if cfg.Geo.AutoUpdateWeekly {
		go geoAutoUpdate(cfg)
	}
	if cfg.SelfUpdateEnabled() {
		go selfAutoUpdate(cfg)
	}

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Printf("NaivePanel %s 监听 %s，面板路径 %s（经 Caddy 反代 https://%s%s/）",
		version, cfg.Listen, cfg.BasePath, cfg.HostSite, cfg.BasePath)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("HTTP 服务退出: %v", err)
	}
}

// geoAutoUpdate refreshes geodata weekly and reloads BypassCore.
func geoAutoUpdate(cfg *config.Config) {
	ticker := time.NewTicker(7 * 24 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		g := cfg.GeoSnapshot()
		if err := geo.Update(g.Dir, g.Mirror); err != nil {
			log.Printf("geo 自动更新失败: %v", err)
			continue
		}
		bc := bypasscore.New(cfg)
		if bc.Installed() {
			if cur, err := bc.ReadConfig(); err == nil && len(cur) > 0 {
				_ = bc.ApplyConfig(cur)
			}
		}
		log.Printf("geo 数据已自动更新")
	}
}

// selfAutoUpdate checks for a newer NaivePanel release once a day; when one
// exists it is downloaded, verified, installed and the panel restarts itself.
func selfAutoUpdate(cfg *config.Config) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		rel, err := selfupdate.Latest(cfg.GeoSnapshot().Mirror)
		if err != nil {
			log.Printf("面板自动更新：检查失败: %v", err)
			continue
		}
		if !selfupdate.Newer(version, rel.TagName) {
			continue
		}
		tag, err := selfupdate.Apply(cfg.GeoSnapshot().Mirror)
		if err != nil {
			log.Printf("面板自动更新：安装 %s 失败: %v", rel.TagName, err)
			continue
		}
		log.Printf("面板已自动更新到 %s，正在重启", tag)
		selfupdate.RestartSelf()
	}
}

// runHashPassword implements `naivepanel hash-password` for install.sh.
func runHashPassword() {
	var pass string
	if len(os.Args) > 2 {
		pass = os.Args[2]
	} else {
		fmt.Fprint(os.Stderr, "请输入密码: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		pass = strings.TrimRight(line, "\r\n")
	}
	if len(pass) < 10 {
		log.Fatal("密码长度至少 10 位")
	}
	h, err := auth.HashPassword(pass)
	if err != nil {
		log.Fatalf("哈希失败: %v", err)
	}
	fmt.Println(h)
}

// runServiceInstall implements `naivepanel install` — registers naivepanel as
// a systemd / procd (OpenWrt) / launchd (macOS) service.
func runServiceInstall() {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	conf := fs.String("config", config.DefaultConfigPath, "path to the panel config file (must already exist)")
	bin := fs.String("bin", "", "target binary path (default: /usr/local/bin/naivepanel, /usr/bin/naivepanel on OpenWrt)")
	start := fs.Bool("start", true, "start the service right after enabling it")
	dryRun := fs.Bool("dry-run", false, "print the actions without modifying the system")
	_ = fs.Parse(os.Args[2:])

	if err := service.Install(service.InstallOptions{
		ConfigPath: *conf,
		BinPath:    *bin,
		Start:      *start,
		DryRun:     *dryRun,
	}); err != nil {
		log.Fatalf("service install failed: %v", err)
	}
}

// runServiceUninstall implements `naivepanel uninstall`.
func runServiceUninstall() {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	purge := fs.Bool("purge", false, "also remove the binary and the /etc/naivepanel config directory")
	dryRun := fs.Bool("dry-run", false, "print the actions without modifying the system")
	_ = fs.Parse(os.Args[2:])

	if err := service.Uninstall(*purge, *dryRun); err != nil {
		log.Fatalf("service uninstall failed: %v", err)
	}
}
