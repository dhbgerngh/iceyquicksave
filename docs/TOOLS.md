# 保留的本地工具

本项目下载的反编译器与编译工具均放在 `G:\iceyquicksave\tools`，工作结束后保留；Git 忽略二进制目录。

| 工具 | 来源 | 本地位置 |
| --- | --- | --- |
| ILSpyCmd 9.1.0.7988 | https://api.nuget.org/v3-flatcontainer/ilspycmd/9.1.0.7988/ilspycmd.9.1.0.7988.nupkg | tools/ilspycmd.9.1.0.7988.zip 与 tools/ilspycmd |
| WinLibs GCC 16.2.0 x64 UCRT POSIX | https://github.com/brechtsanders/winlibs_mingw/releases/tag/16.2.0posix-14.0.0-ucrt-r2 | tools/winlibs-gcc.zip 与 tools/winlibs/mingw64 |

下载包 SHA-256：

- ILSpyCmd：2b5058f5ccc164c33b7aabf1a5eb0cf3d3a6af6c145aaf58efd3ed891443af7c
- WinLibs：d5dbafc4a170e762ca6143151ec918fb9e2c72736fb14cd704abebc6bdd5276a

ILSpyCmd 使用电脑已有的 .NET 8 运行时，仅供开发分析。最终工具没有 C# 代码或托管插件；Go c-shared 构建所需的 ABI/运行时胶水由 Go 工具链生成，GCC 只用于链接该 DLL。

Go 依赖为 golang.org/x/arch 的 x86 指令解码器；用于复制完整指令与修正跳板中的 RIP 相对寻址，不搜索游戏内存中的类。
