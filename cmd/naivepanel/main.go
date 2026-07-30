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

	srv, err := web.New(cfg)
	if err != nil {
		log.Fatalf("初始化 Web 服务失败: %v", err)
	}

	if cfg.Geo.AutoUpdateWeekly {
		go geoAutoUpdate(cfg)
	}

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
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
		if err := geo.Update(cfg.Geo.Dir, cfg.Geo.Mirror); err != nil {
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
	conf := fs.String("config", config.DefaultConfigPath, "面板配置文件路径（必须已存在）")
	bin := fs.String("bin", "", "二进制安装目标路径（默认 systemd/macOS 为 /usr/local/bin/naivepanel，OpenWrt 为 /usr/bin/naivepanel）")
	start := fs.Bool("start", true, "安装后立即启动服务")
	dryRun := fs.Bool("dry-run", false, "只打印将执行的操作，不修改系统")
	_ = fs.Parse(os.Args[2:])

	if err := service.Install(service.InstallOptions{
		ConfigPath: *conf,
		BinPath:    *bin,
		Start:      *start,
		DryRun:     *dryRun,
	}); err != nil {
		log.Fatalf("安装服务失败: %v", err)
	}
}

// runServiceUninstall implements `naivepanel uninstall`.
func runServiceUninstall() {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	purge := fs.Bool("purge", false, "同时删除二进制与 /etc/naivepanel 配置目录")
	dryRun := fs.Bool("dry-run", false, "只打印将执行的操作，不修改系统")
	_ = fs.Parse(os.Args[2:])

	if err := service.Uninstall(*purge, *dryRun); err != nil {
		log.Fatalf("卸载服务失败: %v", err)
	}
}
