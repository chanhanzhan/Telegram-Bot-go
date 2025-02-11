# Telegram-Bot-go

## 项目简介
Telegram-Bot-go 是一个基于 Go 语言开发的 Telegram 机器人，提供用户签到、积分管理、卡密兑换和文件美化等功能。

## 功能列表
- 用户签到获取积分
- 查看用户信息
- 管理员命令（添加积分、扣除积分、生成卡密、封禁/解禁用户）
- 卡密兑换积分
- 文件美化（支持 .zip、.dat、.txt 文件）

## 安装步骤
1. 克隆项目到本地：
    ```sh
    git clone https://github.com/yourusername/Telegram-Bot-go.git
    cd Telegram-Bot-go
    ```

2. 安装依赖：
    ```sh
    go mod tidy
    ```

3. 配置机器人 Token：
    编辑 `main.go` 文件，替换 `botToken` 变量的值为你的 Telegram 机器人 Token。
    替换 adminID 为你的管理员 ID(可以填写数组)。   

4. 运行项目：
    ```sh
    go run main.go
    ```

## 使用方法
1. 启动机器人后，用户可以通过 `/start` 命令开始使用机器人。
2. 用户可以通过点击内嵌按钮进行签到、查看信息和文件美化操作。
3. 管理员可以使用特定命令进行积分管理和用户管理。

## 管理员命令
- 添加积分：`/addpoints <用户ID> <积分>`
- 扣除积分：`/deductpoints <用户ID> <积分>`
- 生成卡密：`/gencode <积分> [有效期天数]`
- 列出所有卡密：`/listcodes`
- 封禁用户：`/ban <用户ID>`
- 解禁用户：`/unban <用户ID>`

## 文件美化
用户可以通过发送代码对和文件进行美化操作，支持 .zip、.dat、.txt 文件类型。

## 项目文件目录
```
Telegram-Bot-go/
├── main.go          # 主程序文件
├── data.json        # 用户数据文件
├── codes.json       # 卡密数据文件
├── README.md        # 项目说明文件
└── go.mod           # Go 模块文件
```

## 编译运行
1. 编译项目：
    ```sh
    go build -o telegram-bot-go
    ```

2. 运行编译后的二进制文件：
    ```sh
    ./telegram-bot-go
    ```

## 运行信息
- 日志：运行时会在控制台输出日志信息，包含用户操作记录和错误信息。
- 数据保存：用户数据和卡密数据会自动保存到 `data.json` 和 `codes.json` 文件中。
- 信号处理：支持 SIGINT 和 SIGTERM 信号，接收到信号后会自动保存数据并安全退出。

## 贡献
欢迎提交 Issue 和 Pull Request 来帮助改进本项目。

## 许可证
本项目使用 MIT 许可证，详情请参阅 LICENSE 文件。