# NovaEmby 插件

NovaEmby 的**官方插件仓库**，同时也是**写插件的模板**：插件源码、Go 开发包、打包与发布工具、教程都在这里。

## 安装插件（NovaEmby 管理员）

官方仓库已经内置在 NovaEmby 里，插件商店里直接安装即可。仓库地址：

```
https://raw.githubusercontent.com/Mzxya/novaemby-plugins/main/index.json
```

`index.json` 是插件清单，`index.json.sig` 是官方签名，由 GitHub Actions 自动维护，不要手改。

## 写插件（插件作者）

不需要改 NovaEmby 的代码，也不需要在电脑上装 Go（GitHub Actions 负责编译和发布）。

- **自己发布**：点右上角 **Use this template** 建自己的插件仓库，删掉 `index.json`、`index.json.sig` 和示例插件，写好插件推送即可；在 NovaEmby 里添加你仓库的 `index.json` 地址就能安装。
- **投稿官方**：Fork 本仓库，在 `plugins/<插件ID>/` 下写插件，提 Pull Request，审核通过后成为官方插件。

完整步骤见 **[插件开发教程](docs/插件开发教程.md)**。需要 NovaEmby 增加新能力（插件现在做不到的事），请提 Issue。

## 目录

|路径|内容|
|--|--|
|`plugins/<插件ID>/`|一个插件：`manifest.json`、`main.go`，可选 `assets/`、`CHANGELOG/<版本>.md`。目录名必须等于插件 ID|
|`plugins/hello/`|示例插件，演示全部能力，写新插件时复制它|
|`sdk/`|Go 开发包（`github.com/Mzxya/novaemby-plugins/sdk`）|
|`tools/mkrepo/`|打包、维护 index.json、签名的工具|
|`.github/workflows/release.yml`|检查、测试、发布（推送到 main 时运行；Pull Request 只检查不发布）|
|`docs/插件开发教程.md`|完整教程|

修改插件后**把 manifest 的 version 加一**再推送；版本号没变时不会重新发布。

## 安全

插件是在 NovaEmby 容器里运行的程序，能联网、能读容器里的文件，**不是沙箱**。只安装信任的插件。官方仓库的插件经过审核并签名；其他仓库的插件安装时 NovaEmby 会要求确认风险。
