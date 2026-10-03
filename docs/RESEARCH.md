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

已实际验证 Steam 自动加载代理、Mono API 初始化、独立原生窗口、冻结时 Time.time 不变且 frameCount 继续增加、游戏截图、关卡中对象图采集与压缩。

跨场景绑定、动态对象重建、战斗中的协程与原生动画状态仍需逐项实测。源码中的实验实现不代表这些验收项已全部通过。
