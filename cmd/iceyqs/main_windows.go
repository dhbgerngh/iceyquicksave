package main

import (
	"flag"
	"fmt"
	"iceyquicksave/internal/winapi"
	"iceyquicksave/internal/winui"
	"os"
	"path/filepath"
)

func main() {
	picker := flag.Bool("picker", false, "open native snapshot chooser")
	errorText := flag.String("error", "", "display operation error")
	root := flag.String("root", "", "game root")
	session := flag.String("session", "", "active game session")
	request := flag.String("request", "", "save, picker, cancel, or probe")
	flag.Parse()
	if *errorText != "" {
		winapi.Message("操作未完成，已恢复游戏逻辑。\n\n" + *errorText)
		return
	}
	if *root == "" {
		exe, _ := os.Executable()
		*root = filepath.Dir(exe)
	}
	if *picker {
		selected, e := winui.Picker(*root, *session)
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		fmt.Print(selected)
		return
	}
	if *request != "" {
		if *request != "save" && *request != "picker" && *request != "cancel" && *request != "probe" && *request != "debug-screenshot" && *request != "debug-continue" {
			fmt.Fprintln(os.Stderr, "unknown request")
			os.Exit(2)
		}
		dir := filepath.Join(*root, "savedata")
		if e := os.MkdirAll(dir, 0755); e != nil {
			panic(e)
		}
		f, e := os.CreateTemp(dir, ".request-")
		if e != nil {
			panic(e)
		}
		tmp := f.Name()
		f.WriteString(*request)
		f.Close()
		if e = os.Rename(tmp, filepath.Join(dir, "request.txt")); e != nil {
			os.Remove(tmp)
			panic(e)
		}
		return
	}
	winapi.Message("ICEY Quick Save（实验版本）\n\n安装代理 DLL 后，从 Steam 正常启动 ICEY。\nF5：保存同会话快照和截图\nF9：冻结逻辑并打开独立快照选择窗口\n\n当前版本不支持完整的任意时刻恢复：协程调度、动画过渡、物理接触缓存以及跨场景 / 跨会话恢复尚未实现。\n原版 Steam 存档不会被本工具主动覆盖。\n详见项目 README.md 和 savedata 中的日志。")
}
