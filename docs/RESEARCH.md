# ICEY 运行时调查

- 游戏：Steam 553640，build 2971687，Windows x64。
- Unity：5.4.0.10934487，内置 Mono，非 IL2CPP。
- ICEY.exe SHA-256：222991c803a16ac95716694133dd78eba116b8907245f79867147a512ef3ef53。
- Assembly-CSharp.dll SHA-256：3969f0d5339cef74a12e7c7b11932db727ef1cea5fd83d112dd1acd718a998d7。
- 工具及反编译结果分别保留在项目 `tools/`、`analysis/`，不提交游戏反编译源码。

## 已确认的接口

原始存档位于 Steam RemoteStorage 的 `save_data.bin`。`GameData.Save` 只保存玩家位置、关卡名、部分属性及进度，不能恢复任意战斗。

`R.Player`、`R.Enemy`、`R.SceneData` 与各个 MonoBehaviour 共同持有战斗数据。`BattleCheckPoint` 存有当前波次、战斗边界、剩余敌人配置；`EnemyQueue` 负责延时生成。

部分游戏逻辑使用 `unscaledDeltaTime`，因此只将 `Time.timeScale` 设为零不足以冻结。当前原型在 Unity 窗口的主线程消息边界，通过 Mono API 调用游戏方法，并在 `mono_runtime_invoke` 层拦截 Update、FixedUpdate、LateUpdate、协程步进等；不调用 R.PauseGame 或游戏暂停菜单。

Unity 5.4 自带 Mono 并不导出较新版本的 `mono_string_length`、`mono_array_length` 和 `mono_free`。使用 `mono_string_to_utf8` / `g_free`、托管 `Array.Length` 等实际存在的接口。

Mono 的 `mono_field_set_value` 对引用字段接收对象指针本身，不是对象指针的地址。Nullable<T> 的装箱结果是 T 或 null，必须通过反射转换，不能按 Nullable<T> 的布局盲目 unbox。

## 用户验收标准

任意位置存档，读取任意地图保存的存档；完整恢复战斗状态。允许怪物 AI 重置，但不能因此省略敌人、血量、护盾、位置、子弹、玩家动作、波次和关卡进度。不能将同场景/同会话原型视作最终完成。

## 验证边界

2026-10-03 当前实现已改为游戏 API 存读档。已实际验证 Steam 自动加载代理、Mono API 初始化、F5 游戏数据及截图保存、独立原生读取窗口、取消恢复、同场景及 `C1L2S1 → C1L2S2` 跨场景加载。

关卡中读取窗口打开时，两次探测的 `Time.time` 均为 96.61233，`frameCount` 从 76376 增至 78769，确认逻辑暂停而渲染继续。确认后在同一次窗口主线程回调内恢复时间、动画及音频，再立即启动 `LevelManager.LoadLevelByPosition`，加载协程不会被暂停钩子拦截。

实测发现 `STARTUPINFO` 的隐藏参数会让第一次 `ShowWindow` 无效，产生“游戏暂停但没有窗口”。已移除选择器的隐藏启动标志，并增加第二次显式显示以兼容仍在运行的旧 DLL；原生窗口回归测试覆盖该条件。

原型中 `GetObject`、`GetBuffer`、`R.set_GameData`、`LoadLevelByPosition` 的引用参数误传为指针地址，已改为 Mono 要求的对象指针。真正的 `GameData.LoadPlayerAttribute(ref PlayerAttribute, ...)` 仍传引用地址。`ChangeState` 按完整签名选择 string 重载。

完整战斗恢复的历史验收标准尚未达成。测试中战斗存档有两只敌人未能在新场景唯一匹配，日志明确记录；波次、协程、子弹及掉落物不属于当前已验证恢复范围。钩子可行性及当前取舍见 README.md。测试证据保留在忽略的 `analysis/validation-20261003/`。
