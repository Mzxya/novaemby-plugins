# 历史播放记录 / 播放观测台

NovaEmby 播放历史插件，当前版本 0.5.2。接收宿主事件并在 `/history` 提供手机优先的播放看板与历史查询页。

## 功能

- 按用户记录电影、剧集和其他视频的播放会话，合并开始、暂停和停止事件。
- 看板支持 7/30/90 天、关键指标、观看趋势、用户排行、热门内容、类型分布和未结束会话。
- 手机使用单列卡片、固定导航、折叠筛选和触摸日期详情；桌面保留大屏排布。
- 全新亮色界面：中性白、深色文字和蓝色重点，手机底部导航，默认亮色并保留暗色记忆。
- 最近播放可以直接点开对应内容记录；默认每 60 秒自动更新。
- 独立明细支持关键词、类型、状态、日期、排序、分页和按内容汇总。
- 媒体库补全片名、类型、剧集与海报；存储支持保留期限、数量限制和定时清理。
- 数据采用插件数据目录中的 `history.jsonl`，升级沿用原格式。

## 使用

1. 在 NovaEmby 系统设置中开启播放开始 / 停止通知。
2. 安装并启用插件，点击插件详情页的「主页」进入 `/history`。
3. 播放视频后等待自动更新。插件只采集运行期间收到的事件，不能补录启用前的历史。

明细中的删除功能默认隐藏，勾选「管理记录」后才显示；清空需要确认，范围为所选用户全部历史，不受列表筛选限制。

## 统计与访问边界

所有查询和管理路由均为 `auth:none`，适用于可信内网。会话经过时长包含暂停，跨日会话归属开始日；未结束会话仅表示尚未收到停止事件，不等于真实在线人数。用户名相同的事件按同一用户归组。

## 核心结构

| 部分 | 文件 | 职责 |
|---|---|---|
| 事件处理 | main.go、eventmap.go、eventshape.go、eventlog.go | 生命周期、事件解析、媒体补全及可选详细日志 |
| 数据存储 | model.go、store.go、dashboard.go | 记录模型、JSONL、会话合并、查询与统一看板快照 |
| 页面展示 | http.go、assets/ | 路由、嵌入式资源、主题和手机/桌面交互 |

## 验证与构建

在仓库根目录执行：

```bash
go test -count=1 ./...
go vet ./...
node --check plugins/playback-history/assets/theme.js
node --check plugins/playback-history/assets/app.js
node plugins/playback-history/web_test.mjs
go run ./tools/mkrepo check -plugin plugins/playback-history
go run ./tools/mkrepo build -plugin plugins/playback-history -out plugins/插件安装包
```

安装包输出至 `plugins/插件安装包/`，包含 Linux amd64 / arm64；资源通过 go:embed 编入程序，修改页面后必须重新构建。具备 C 编译器和 CGO 的环境可运行 `go test -race -count=1 ./...`。
