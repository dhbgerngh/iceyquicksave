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
		winapi.Message("操作未完成，已解除工具的逻辑拦截。\n\n" + *errorText)
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
		if *request != "save" && *request != "picker" && *request != "cancel" && *request != "probe" {
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
	winapi.Message("ICEY Quick Save\n\n从 Steam 正常启动 ICEY。\nF5：保存游戏进度、角色状态和截图\nF9：先暂停游戏逻辑，再打开读取界面\n确认读取：恢复游戏并立即调用游戏读档接口\n取消或关闭窗口：恢复游戏\n\n支持同地图及跨地图加载，战斗由游戏重新初始化；波次、协程、子弹及掉落物不能完整回滚。旧版对象快照需重新保存。\n本工具不直接写入 Steam 存档；游戏仍会正常自动保存。\n详见项目 README.md 和 savedata 中的日志。")
}
