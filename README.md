# ICEY Quick Save

为 Steam Windows 版《ICEY》提供快捷存档和存档选择界面的实验性工具，使用 Go 编写，支持保存截图、游戏进度和角色状态，以及同地图和跨地图读取。

**AI 编写声明：本项目的代码与文档由 AI（OpenAI Codex）在开发者提出需求、反馈和指导下编写。** 第三方依赖及游戏原有代码不属于本项目的 AI 编写内容。

## 项目是什么

工具通过代理 DLL 随游戏启动，在游戏运行时调用已有的存档和场景加载方法，并提供独立的 Windows 存档选择窗口。

- **快捷保存**：保存游戏进度、角色属性、位置、朝向、速度、动作名称及截图。
- **选择读取**：浏览本地存档和截图，选择要恢复的存档。
- **暂停选择**：先暂停游戏逻辑，再打开读取界面，选择期间游戏仍可渲染。
- **立即加载**：确认读取后，在同一次游戏主线程回调中恢复游戏并立即调用加载方法。

### 当前限制

**当前版本不能完整回滚战斗状态。** 读取会重新加载地图，战斗由游戏重新初始化。工具仅恢复能够唯一匹配的活动敌人的位置、血量、护盾和速度；无法匹配的敌人会记录在日志中。

战斗波次、协程等待、子弹、掉落物、Boss 内部阶段、动画进度和动作内部计时尚不能完整恢复。角色动作名称的恢复也不代表恢复到原动作的同一帧。

存档格式目前为版本 4，兼容包含有效游戏数据的版本 3。旧版对象图快照不兼容，需要重新保存；游戏程序集版本不一致的存档会被拒绝读取。

## 如何使用

### 运行环境

- Windows 64 位系统。
- 已安装的 Steam 版《ICEY》。当前实现针对 Unity 5.4、Mono 运行时的 Windows x64 版本；其他版本未验证。
- 按下文完成构建和安装。运行已构建的工具无需安装 Go 或 GCC。

### 安装

完成构建后，关闭 ICEY、存档选择窗口和工具错误窗口，在**项目根目录**打开 PowerShell，执行：

```powershell
go run ./cmd/build -action install -game 'G:\SteamLibrary\steamapps\common\ICEY'
go run ./cmd/build -action verify -game 'G:\SteamLibrary\steamapps\common\ICEY'
```

将示例路径替换为自己的游戏目录，即包含 `ICEY.exe` 的文件夹。安装程序会从 `dist/` 复制三个文件，并生成安装清单：

| 文件 | 用途 |
| --- | --- |
| `version.dll` | 随游戏加载的代理 DLL |
| `iceyqs_system_version.dll` | 转发原系统 DLL 的调用 |
| `ICEYQuickSave.exe` | 存档选择界面和命令入口 |
| `iceyqs-install.json` | 记录安装文件的哈希，用于校验、更新和卸载 |

安装不修改 `ICEY.exe` 或游戏程序集。若目标目录存在不属于本工具的同名文件，安装程序会停止并提示冲突。

### 保存与读取

安装完成后，从 Steam 正常启动 ICEY，进入游戏关卡并让游戏窗口获得焦点。

| 操作 | 功能 |
| --- | --- |
| **F5** | 短暂暂停，保存存档和截图，完成后恢复游戏 |
| **F9** | 暂停游戏逻辑，打开读取界面 |
| **读取所选存档 / 双击存档 / 在列表中按 Enter** | 读取选中的存档 |
| **取消 / Esc / 关闭读取窗口** | 取消选择并恢复游戏 |

确认读取时，工具先在暂停期间读取、校验和解码文件，然后恢复游戏并立即发起加载。加载期间游戏协程正常运行，工具不接受重复存读档；已开始的场景加载不能通过“取消”中止。

工具不直接写入 Steam 的 `save_data.bin`。游戏自身的自动保存仍会运行，读取后的进度可能在后续自动保存时写入原版存档。

### 存档与日志位置

所有快捷存档位于**游戏目录**的 `savedata/` 文件夹：

```text
savedata/
├── <存档编号>/
│   ├── state.iceyqs    # 存档数据
│   ├── slot.json       # 列表信息
│   └── screen.png      # 保存时的截图
├── iceyqs.log          # 操作与错误日志
└── status.json         # 最近一次状态记录
```

备份快捷存档时，复制整个存档编号文件夹，或复制整个 `savedata/`。

### 更新与卸载

更新时，关闭游戏及工具窗口，重新构建并执行安装命令，然后重新启动游戏。

卸载时，在项目根目录执行以下命令。卸载会保留 `savedata/`：

```powershell
go run ./cmd/build -action uninstall -game 'G:\SteamLibrary\steamapps\common\ICEY'
```

## 如何构建

### 准备工具链

1. 安装 [Go 1.24 或更新版本](https://go.dev/dl/)，确保 PowerShell 中可以运行 `go version`。
2. 下载 [WinLibs 的 MinGW-w64 GCC 工具链](https://github.com/brechtsanders/winlibs_mingw/releases)，选择 **x86_64 / UCRT / POSIX** 版本。
3. 将工具链解压到项目的 `tools/winlibs/`，确保 GCC 位于下面的路径：

```text
<项目根目录>/tools/winlibs/mingw64/bin/gcc.exe
```

保留完整的 `mingw64` 目录，不能只复制 `gcc.exe`。构建程序按上述固定路径查找编译器，并自行设置 CGO、Windows amd64 目标和编译器环境。

`tools/` 和 `dist/` 不包含在 Git 仓库中，从 GitHub 获取源码后需要自行准备工具链。构建无需反编译器或 .NET；首次获取 Go 依赖需要网络连接。

### 执行构建

在项目根目录打开 PowerShell：

```powershell
go mod download
go run ./cmd/build -action build
```

构建成功后，`dist/` 中会生成安装所需的三个文件：

```text
dist/
├── version.dll
├── iceyqs_system_version.dll
└── ICEYQuickSave.exe
```

代理 DLL 使用 Go 的 `c-shared` 模式构建，GCC 负责链接；构建程序会复制当前系统的 `version.dll`，并生成代理的导出转发表。完成后按上文“安装”步骤操作。

需要运行项目测试时执行：

```powershell
go test ./...
```

测试包含存档校验、加载顺序、原生窗口可见性及代理转发等；窗口测试会短暂打开一个读取窗口。自动测试通过不代表完整战斗回滚已经实现。

## 实现概览

| 目录 | 内容 |
| --- | --- |
| `cmd/build` | 构建、安装、校验与卸载 |
| `cmd/payload` | 游戏内 DLL 入口 |
| `cmd/iceyqs` | 原生读取窗口与命令入口 |
| `internal/agent` | 暂停、快捷键、存读档流程 |
| `internal/gameapi` | 游戏现有方法调用及存档格式校验 |
| `internal/mono` | Mono 运行时接口 |
| `internal/hook` | 暂停所需的原生钩子 |
| `internal/archive` | 存档压缩、校验与文件写入 |
| `internal/peproxy` | 代理 DLL 的导出转发 |
| `internal/winui`、`internal/winapi` | Windows 界面与系统调用 |

读档主要调用游戏现有的 `SaveData.GetObject`、`R.GameData`、`GameData.LoadPlayerAttribute` 和 `LevelManager.LoadLevelByPosition`。暂停使用 `mono_runtime_invoke` 钩子，配合时间缩放、音频和动画暂停，处理不受 `timeScale=0` 控制的逻辑。

完整战斗恢复还需要额外的数据采集及恢复协议。仅新增方法钩子无法补齐存档未保存的波次、协程或子弹状态，因此当前实现未启用未经验证的完整战斗恢复钩子。
