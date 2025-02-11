package main

import (
	"archive/zip"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	// 导入Telegram Bot API SDK

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// 用户结构体
type User struct {
	ID          int64     `json:"id"`
	Username    string    `json:"username"`
	FirstName   string    `json:"firstname"`
	LastName    string    `json:"lastname"`
	Points      float64   `json:"points"`
	LastCheckIn time.Time `json:"last_check_in"`
	IsBanned    bool      `json:"is_banned"`
}

// 卡密结构体
type RedeemCode struct {
	Code      string    `json:"code"`
	Points    float64   `json:"points"`
	ExpiresAt time.Time `json:"expires_at"`
	Used      bool      `json:"used"`
	UsedBy    int64     `json:"used_by"`
}

var (
	users           = map[int64]*User{}
	dataFile        = "data.json"
	botToken        = "xxx:xxxxx"
	processingUsers = sync.Map{}
	mu              sync.Mutex
	maxFileSize     = 50 * 1024 * 1024             // 50MB
	adminIDs        = []int64{123456789, 1234567}  // 管理员ID
	codes           = make(map[string]*RedeemCode) // 卡密存储
	codesFile       = "codes.json"
)

const maxIdleTime = 10 * time.Minute // 最大空闲时间

/******************* 初始化并运行 Telegram Bot *******************/
func main() {
	rand.Seed(time.Now().UnixNano())
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatalf("创建 Bot 失败: %v", err)
	}

	bot.Debug = true
	log.Printf("已登录为: %s", bot.Self.UserName)

	loadData()
	loadCodes()

	// 捕获 SIGINT 信号 : Ctrl+C
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)

	// 启动一个 goroutine 监听信号
	go func() {
		sig := <-signalChan
		log.Printf("收到信号: %v，正在保存数据并退出...", sig)
		saveData()
		saveCodes()
		os.Exit(0)
	}()

	// 启动一个 goroutine 清理超时的处理会话
	go func() {
		for {
			time.Sleep(1 * time.Minute)
			now := time.Now()
			processingUsers.Range(func(key, value interface{}) bool {
				processData := value.(map[string]interface{})
				lastActivity, ok := processData["last_activity"].(time.Time)
				if ok && now.Sub(lastActivity) > maxIdleTime {
					processingUsers.Delete(key)
					log.Printf("处理会话超时，已清理: 用户ID=%d", key)
					chatID := processData["chat_id"].(int64)
					bot := processData["bot"].(*tgbotapi.BotAPI)
					bot.Send(tgbotapi.NewMessage(chatID, "❌ 处理会话超时，已结束本次修改任务。请重新开始。"))
				}
				return true
			})
		}
	}()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	log.Println("开始监听更新...")

	// 处理每条更新
	for update := range updates {
		if update.Message != nil {
			if update.Message.Document != nil {
				handleFileMessage(bot, update.Message)
			} else if update.Message.Text != "" {
				handleTextInput(bot, update.Message)
			}
			handleMessage(bot, update.Message)
		} else if update.CallbackQuery != nil {
			handleCallback(bot, update.CallbackQuery)
		}
	}
}

/******************* 处理用户消息 *******************/
func handleMessage(bot *tgbotapi.BotAPI, message *tgbotapi.Message) {
	userID := message.From.ID
	chatID := message.Chat.ID

	// 初始化用户
	user, exists := users[userID]
	if !exists {
		user = &User{
			ID:          userID,
			Username:    message.From.UserName,
			FirstName:   message.From.FirstName,
			LastName:    message.From.LastName,
			Points:      0,
			LastCheckIn: time.Time{},
			IsBanned:    false,
		}
		users[userID] = user
		log.Printf("新用户注册: ID=%d, 用户名=%s", userID, user.Username)
	}

	// 封禁检查
	if user.IsBanned {
		msg := tgbotapi.NewMessage(chatID, "您已被封禁，无法使用机器人功能。")
		bot.Send(msg)
		log.Printf("被封禁用户尝试访问: ID=%d", userID)
		return
	}

	// 内嵌按钮菜单
	buttons := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("签到", "sign"),
			tgbotapi.NewInlineKeyboardButtonData("查看信息", "info"),
			tgbotapi.NewInlineKeyboardButtonData("自动美化", "auto_biuf"),
		),
	)

	/***** 个人信息 ****/
	if message.IsCommand() && message.Command() == "info" {
		info(bot, chatID, user)
		return
	}

	/***** 菜单 ****/
	if message.IsCommand() && message.Command() == "start" {
		msg := tgbotapi.NewMessage(chatID,
			message.From.FirstName+" "+message.From.LastName+"你好，我是 tainshi_bot！👋\n使用  /redeem 卡密 来兑换积分 \n admin: @tszj666 ,卡密购买请联系天使,官方频道: @tszjnb666 \n· 请点击下面的按钮进行操作：")
		msg.ReplyMarkup = buttons
		bot.Send(msg)
		return
	}

	/***** 管理员命令 ****/
	if message.IsCommand() && message.Command() == "root" {
		msg := tgbotapi.NewMessage(chatID, `管理员命令
	· 添加积分 (/addpoints):
		/addpoints <用户ID> <积分>
	
	· 扣除积分 (/deductpoints):
		/deductpoints <用户ID> <积分>
	
	· 生成卡密 (/gencode):
		/gencode <积分> [有效期天数] （默认7天）
	
	· 列出所有卡密 (/listcodes)
	
	· 封禁用户 (/ban)
		/ban <用户ID>
	
	· 解禁用户 (/unban)
		/unban <用户ID>
	`)
		msg.ReplyMarkup = buttons
		bot.Send(msg)
		return
	}

	/***** 用户兑换命令处理 ****/
	if strings.HasPrefix(message.Text, "/redeem ") {
		code := strings.TrimSpace(strings.TrimPrefix(message.Text, "/redeem "))
		handleRedeemCode(bot, message, user, code)
		return
	}

	/***** 管理员命令处理 ****/
	if isAdmin(userID) && message.IsCommand() {
		handleAdminCommand(bot, message)
		return
	}
}

func isAdmin(userID int64) bool {
	for _, id := range adminIDs {
		if userID == id {
			return true
		}
	}
	return false
}

/******************* 文本输入处理 *******************/
func handleTextInput(bot *tgbotapi.BotAPI, message *tgbotapi.Message) {
	userID := message.From.ID
	chatID := message.Chat.ID

	user, exists := users[userID]
	if !exists || user.IsBanned {
		return
	}

	data, ok := processingUsers.Load(userID)
	if !ok {
		return
	}

	processData := data.(map[string]interface{})
	processData["last_activity"] = time.Now() // 更新最后活动时间
	currentStep, ok := processData["step"].(string)
	if !ok || currentStep != "waiting_codes" {
		return
	}

	lines := strings.Split(message.Text, "\n")
	validPairs := make([][2]int, 0)

	for i, line := range lines {
		parts := strings.Fields(line)
		if len(parts) != 2 {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("第%d行格式错误，已跳过", i+1)))
			continue
		}

		original, err1 := strconv.Atoi(parts[0])
		newCode, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("第%d行包含无效数字，已跳过", i+1)))
			continue
		}

		validPairs = append(validPairs, [2]int{original, newCode})
	}

	if len(validPairs) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 未找到有效的代码对，请重新输入"))
		return
	}

	existingCodes := processData["codes"].([][2]int)
	existingCodes = append(existingCodes, validPairs...)
	processData["codes"] = existingCodes
	processingUsers.Store(userID, processData)

	bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ 已添加%d个代码对，当前共%d对。请继续输入或发送文件。", len(validPairs), len(existingCodes))))
}

/******************* 文件消息处理 *******************/
func handleFileMessage(bot *tgbotapi.BotAPI, message *tgbotapi.Message) {
	userID := message.From.ID
	chatID := message.Chat.ID

	user, exists := users[userID]
	if !exists || user.IsBanned || message.Document == nil {
		return
	}

	// 检查文件大小
	if message.Document.FileSize > maxFileSize {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 文件大小超过50MB限制"))
		return
	}

	// 下载文件到临时文件
	tempFile, err := ioutil.TempFile("", "download_*.zip")
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 创建临时文件失败"))
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	fileURL, _ := bot.GetFileDirectURL(message.Document.FileID)
	resp, err := http.Get(fileURL)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 文件下载失败"))
		return
	}
	defer resp.Body.Close()

	if _, err = io.Copy(tempFile, resp.Body); err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 文件保存失败"))
		return
	}

	switch {
	case strings.HasSuffix(message.Document.FileName, ".zip"):
		processZipFile(bot, user, chatID, tempFile.Name(), message)
	case strings.HasSuffix(message.Document.FileName, ".dat"):
		processSingleFile(bot, user, chatID, tempFile.Name(), message)
	case strings.HasSuffix(message.Document.FileName, ".txt"):
		processBatchFile(bot, user, chatID, tempFile.Name(), message)
	default:
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 不支持的文件类型"))
	}

	data, ok := processingUsers.Load(userID)
	if ok {
		processData := data.(map[string]interface{})
		processData["last_activity"] = time.Now() // 更新最后活动时间
	}
}

/******************* 处理管理员命令 *******************/
func handleAdminCommand(bot *tgbotapi.BotAPI, message *tgbotapi.Message) {
	command := message.Command()
	args := strings.Fields(message.CommandArguments())
	chatID := message.Chat.ID

	switch command {
	case "addpoints", "deductpoints":
		if len(args) < 2 {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ 参数不足。用法：/"+command+" <用户ID> <积分>"))
			return
		}

		targetID, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ 无效的用户ID"))
			return
		}

		points, err := strconv.ParseFloat(args[1], 64)
		if err != nil || points <= 0 {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ 无效的积分值"))
			return
		}

		mu.Lock()
		defer mu.Unlock()
		targetUser, exists := users[targetID]
		if !exists {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ 用户不存在"))
			return
		}

		if command == "addpoints" {
			targetUser.Points += points
		} else {
			targetUser.Points -= points
			if targetUser.Points < 0 {
				targetUser.Points = 0
			}
		}

		bot.Send(tgbotapi.NewMessage(chatID,
			fmt.Sprintf("✅ 用户 %d 积分已更新\n当前积分：%.2f", targetID, targetUser.Points)))

	case "gencode":
		if len(args) < 1 {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ 参数不足。用法：/gencode <积分> [有效期天数]"))
			return
		}

		points, err := strconv.ParseFloat(args[0], 64)
		if err != nil || points <= 0 {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ 无效的积分值"))
			return
		}

		expiryDays := 7
		if len(args) >= 2 {
			expiryDays, err = strconv.Atoi(args[1])
			if err != nil || expiryDays <= 0 {
				bot.Send(tgbotapi.NewMessage(chatID, "❌ 无效的有效期天数"))
				return
			}
		}

		code := generateCode(16)
		expiresAt := time.Now().AddDate(0, 0, expiryDays)

		mu.Lock()
		codes[code] = &RedeemCode{
			Code:      code,
			Points:    points,
			ExpiresAt: expiresAt,
			Used:      false,
		}
		saveCodes()
		mu.Unlock()

		msg := fmt.Sprintf("✅ 卡密生成成功！\n卡密: %s\n积分: %.2f\n有效期至: %s",
			code, points, expiresAt.Format("2006-01-02"))
		bot.Send(tgbotapi.NewMessage(chatID, msg))

	case "listcodes":
		var sb strings.Builder
		sb.WriteString("📜 卡密列表：\n")
		for code, rc := range codes {
			status := "未使用"
			if rc.Used {
				status = fmt.Sprintf("已使用（用户 %d）", rc.UsedBy)
			}
			sb.WriteString(fmt.Sprintf("▫️ %s - %.2f 积分\n   有效期至 %s\n   状态：%s\n\n",
				code, rc.Points, rc.ExpiresAt.Format("2006-01-02"), status))
		}
		bot.Send(tgbotapi.NewMessage(chatID, sb.String()))

	case "ban":
		if len(args) < 1 {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ 参数不足。用法：/ban <用户ID>"))
			return
		}
		targetID, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ 无效的用户ID"))
			return
		}
		mu.Lock()
		targetUser, exists := users[targetID]
		if !exists {
			mu.Unlock()
			bot.Send(tgbotapi.NewMessage(chatID, "❌ 用户不存在"))
			return
		}

		// 封禁用户
		targetUser.IsBanned = true
		mu.Unlock()
		saveData()
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ 用户 %d 已被封禁", targetID)))

	case "unban":
		if len(args) < 1 {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ 参数不足。用法：/unban <用户ID>"))
			return
		}
		targetID, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ 无效的用户ID"))
			return
		}
		mu.Lock()
		targetUser, exists := users[targetID]
		if !exists {
			mu.Unlock()
			bot.Send(tgbotapi.NewMessage(chatID, "❌ 用户不存在"))
			return
		}

		// 解禁用户
		targetUser.IsBanned = false
		mu.Unlock()
		saveData()
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ 用户 %d 已被解禁", targetID)))

	default:
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 未知的管理员命令"))
	}
}

/******************* 卡密兑换处理 *******************/
func handleRedeemCode(bot *tgbotapi.BotAPI, message *tgbotapi.Message, user *User, code string) {
	chatID := message.Chat.ID

	mu.Lock()
	defer mu.Unlock()

	rc, exists := codes[code]
	if !exists {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 无效的卡密"))
		return
	}

	if rc.Used {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 该卡密已被使用"))
		return
	}

	if time.Now().After(rc.ExpiresAt) {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 卡密已过期"))
		return
	}

	user.Points += rc.Points
	rc.Used = true
	rc.UsedBy = user.ID

	saveData()
	saveCodes()

	msg := fmt.Sprintf("🎉 卡密兑换成功！\n获得 %.2f 积分\n当前积分：%.2f", rc.Points, user.Points)
	bot.Send(tgbotapi.NewMessage(chatID, msg))
}

/******************* 卡密生成函数 *******************/

func generateCode(length int) string {
	const charset = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // 排除易混淆字符
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}

/******************* 修改文件函数 *******************/
func modifyFileHex(fileContent []byte, A, B string) ([]byte, error) {
	searchSeq1, _ := hex.DecodeString(A)
	searchSeq2, _ := hex.DecodeString(B)

	index1 := bytes.LastIndex(fileContent, searchSeq1)
	index2 := bytes.LastIndex(fileContent, searchSeq2)

	if index1 == -1 || index2 == -1 {
		return nil, errors.New("未找到指定的搜索序列")
	}

	newContent := make([]byte, len(fileContent))
	copy(newContent, fileContent)

	copy(newContent[index2:index2+4], fileContent[index1:index1+4])
	copy(newContent[index1:index1+4], fileContent[index2:index2+4])

	return newContent, nil
}

/******************* 十进制转十六进制函数 *******************/
func decToHex(decimal int) string {
	hexStr := fmt.Sprintf("%08X", decimal)
	parts := make([]string, 0, 4)
	for i := 0; i < len(hexStr); i += 2 {
		parts = append(parts, hexStr[i:i+2])
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, "")
}

/******************* 美化操作处理 *******************/
func handleAutoBeautify(bot *tgbotapi.BotAPI, user *User, chatID int64, message *tgbotapi.Message) {
	if user.Points < 1 {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 积分不足，请先签到获取积分！"))
		return
	}

	processingUsers.Store(user.ID, map[string]interface{}{
		"step":          "waiting_codes",
		"codes":         make([][2]int, 0),
		"last_activity": time.Now(), // 初始化最后活动时间
		"chat_id":       chatID,
		"bot":           bot,
	})

	msg := tgbotapi.NewMessage(chatID, `🛠 请按以下格式发送代码对（每行两个十进制数字，用空格分隔），发送完成后请发送要处理的文件：
例如：
1234 5678
8765 4321`)
	bot.Send(msg)
}

/******************* zip文件处理 *******************/
func processZipFile(bot *tgbotapi.BotAPI, user *User, chatID int64, zipPath string, message *tgbotapi.Message) {
	// 从处理会话获取代码对
	data, ok := processingUsers.Load(user.ID)
	if !ok {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 处理会话已过期"))
		return
	}

	processData := data.(map[string]interface{})
	codePairs, ok := processData["codes"].([][2]int)
	if !ok || len(codePairs) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 无效的代码对"))
		processingUsers.Delete(user.ID)
		return
	}

	// 创建临时工作目录
	workDir, err := ioutil.TempDir("", "zip_process_*")
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 创建临时目录失败"))
		processingUsers.Delete(user.ID)
		return
	}
	defer os.RemoveAll(workDir) // 确保清理

	// 解压原始ZIP
	if err = unzip(zipPath, workDir); err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 解压文件失败: "+err.Error()))
		processingUsers.Delete(user.ID)
		return
	}

	// 处理目录中的.dat文件
	if err = processDirectory(workDir, codePairs); err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 文件处理失败: "+err.Error()))
		processingUsers.Delete(user.ID)
		return
	}

	// 创建内存缓冲区存放新ZIP
	var zipBuffer bytes.Buffer
	zipWriter := zip.NewWriter(&zipBuffer)

	// 遍历文件树写入ZIP
	err = filepath.Walk(workDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}

		relPath, _ := filepath.Rel(workDir, path)
		zipEntry, err := zipWriter.Create(relPath)
		if err != nil {
			return err
		}

		fileContent, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		_, err = zipEntry.Write(fileContent)
		return err
	})

	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 创建压缩文件失败: "+err.Error()))
		processingUsers.Delete(user.ID)
		return
	}

	// 必须显式关闭zipWriter以确保数据写入
	if err = zipWriter.Close(); err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 压缩文件关闭失败: "+err.Error()))
		processingUsers.Delete(user.ID)
		return
	}

	// 扣除积分
	mu.Lock()
	user.Points -= 1
	mu.Unlock()
	saveData()

	// 构造友好文件名
	originalName := filepath.Base(message.Document.FileName)
	newName := "modified_" + strings.TrimSuffix(originalName, filepath.Ext(originalName)) + ".zip"

	// 发送ZIP文件
	file := tgbotapi.FileBytes{
		Name:  newName,
		Bytes: zipBuffer.Bytes(),
	}
	msg := tgbotapi.NewDocument(chatID, file)
	msg.Caption = fmt.Sprintf("✅ 美化完成！消耗1积分，剩余积分: %.2f", user.Points)
	if _, err := bot.Send(msg); err != nil {
		log.Printf("发送文件失败: %v", err)
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 发送文件失败，请联系管理员"))
	}

	// 清理处理会话
	processingUsers.Delete(user.ID)
}

/******************* .zip压缩包处理 *******************/
func processZipArchive(inputPath string, codes [][2]int) (string, error) {
	// 创建临时工作目录
	workDir, err := ioutil.TempDir("", "bot_processing_*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(workDir)

	// 解压zip文件
	if err := unzip(inputPath, workDir); err != nil {
		return "", fmt.Errorf("解压zip文件失败: %w", err)
	}

	// 处理目录中的.dat文件
	if err := processDirectory(workDir, codes); err != nil {
		return "", err
	}

	// 创建新的zip文件
	outputPath := filepath.Join(os.TempDir(), "processed_"+filepath.Base(inputPath))
	if err := createZipArchive(outputPath, workDir); err != nil {
		return "", err
	}

	return outputPath, nil
}

/******************* 解压 *******************/
func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		filePath := filepath.Join(dest, f.Name)

		if f.FileInfo().IsDir() {
			os.MkdirAll(filePath, os.ModePerm)
			continue
		}

		if err = os.MkdirAll(filepath.Dir(filePath), os.ModePerm); err != nil {
			return err
		}

		outFile, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}
		defer outFile.Close()

		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()

		if _, err := io.Copy(outFile, rc); err != nil {
			return err
		}
	}

	return nil
}

/******************* 创建zip压缩包 *******************/
func createZipArchive(outputPath string, sourceDir string) error {
	zipFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("创建zip文件失败: %w", err)
	}
	defer zipFile.Close()

	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()

	return filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		zipEntry, err := zipWriter.Create(relPath)
		if err != nil {
			return err
		}

		if _, err := io.Copy(zipEntry, file); err != nil {
			return err
		}

		return nil
	})
}

/******************* 递归处理目录 *******************/
func processDirectory(dir string, codes [][2]int) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(info.Name(), ".dat") {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			for _, codePair := range codes {
				modified, err := modifyFileHex(content, decToHex(codePair[0]), decToHex(codePair[1]))
				if err != nil {
					return err
				}
				content = modified
			}

			if err := os.WriteFile(path, content, 0644); err != nil {
				return err
			}
		}
		return nil
	})
}

/******************* 单个文件处理 *******************/
func processSingleFile(bot *tgbotapi.BotAPI, user *User, chatID int64, filePath string, message *tgbotapi.Message) {
	// 检查处理会话
	data, ok := processingUsers.Load(user.ID)
	if !ok {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 处理会话已过期"))
		return
	}

	processData := data.(map[string]interface{})
	codes, ok := processData["codes"].([][2]int)
	if !ok || len(codes) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 未找到有效的代码对"))
		processingUsers.Delete(user.ID)
		return
	}

	// 读取文件内容
	content, err := os.ReadFile(filePath)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 读取文件失败"))
		processingUsers.Delete(user.ID)
		return
	}

	// 应用所有代码对
	for _, pair := range codes {
		modified, err := modifyFileHex(content, decToHex(pair[0]), decToHex(pair[1]))
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ 处理失败: "+err.Error()))
			processingUsers.Delete(user.ID)
			return
		}
		content = modified
	}

	// 扣除积分
	mu.Lock()
	user.Points -= 1
	mu.Unlock()

	// 发送结果
	sendModifiedFile(bot, chatID, content, message.Document.FileName)

	// 清理会话
	processingUsers.Delete(user.ID)
}

/******************* 批量文件处理 *******************/
func processBatchFile(bot *tgbotapi.BotAPI, user *User, chatID int64, filePath string, message *tgbotapi.Message) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 读取文件失败"))
		return
	}

	lines := strings.Split(string(content), "\n")
	codeList := make([][2]int, 0)

	for _, line := range lines {
		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue
		}
		original, err1 := strconv.Atoi(parts[0])
		newCode, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			continue
		}
		codeList = append(codeList, [2]int{original, newCode})
	}

	if len(codeList) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ 未找到有效的代码对"))
		return
	}

	// 存储代码对用于后续处理
	processingUsers.Store(user.ID, map[string]interface{}{
		"codes": codeList,
		"file":  filePath,
	})

	bot.Send(tgbotapi.NewMessage(chatID, "✅ 代码对已接收，请发送要处理的文件或压缩包"))
}

/******************* 发送修改后的文件 *******************/
func sendModifiedFile(bot *tgbotapi.BotAPI, chatID int64, content []byte, originalFileName string) {
	// 保留原始文件后缀
	ext := filepath.Ext(originalFileName)
	if ext == "" {
		ext = ".dat"
	}

	// 构造友好文件名
	baseName := strings.TrimSuffix(filepath.Base(originalFileName), ext)
	newFileName := "modified_" + baseName + ext

	// 使用 FileBytes 并指定文件名
	file := tgbotapi.FileBytes{
		Name:  newFileName,
		Bytes: content,
	}

	msg := tgbotapi.NewDocument(chatID, file)
	msg.Caption = "✅ 文件美化完成！消耗1积分"
	bot.Send(msg)
}

/******************* 内嵌按钮回调处理 *******************/
func handleCallback(bot *tgbotapi.BotAPI, callback *tgbotapi.CallbackQuery) {
	userID := callback.From.ID
	chatID := callback.Message.Chat.ID

	// 初始化用户
	user, exists := users[userID]
	if !exists {
		user = &User{
			ID:          userID,
			Username:    callback.From.UserName,
			Points:      0,
			LastCheckIn: time.Time{},
			IsBanned:    false,
		}
		users[userID] = user
		log.Printf("新用户注册: ID=%d, 用户名=%s", userID, user.Username)
	}

	// 封禁检查
	if user.IsBanned {
		bot.Send(tgbotapi.NewMessage(chatID, "您已被封禁，无法使用机器人功能。"))
		log.Printf("被封禁用户尝试访问: ID=%d", userID)
		return
	}

	// 按钮功能逻辑
	switch callback.Data {
	case "sign":
		sign(bot, chatID, user)
	case "info":
		info(bot, chatID, user)
	case "auto_biuf":
		handleAutoBeautify(bot, user, chatID, callback.Message)
	}
}

/******************* 签到功能 *******************/
func sign(bot *tgbotapi.BotAPI, chatID int64, user *User) {
	// 检查是否已经签到过
	if time.Since(user.LastCheckIn).Hours() < 24 {
		msg := tgbotapi.NewMessage(chatID, "您今日已签到，请明天再来！")
		bot.Send(msg)
		return
	}

	// 判断是否是第一次签到
	if user.LastCheckIn.IsZero() {
		user.Points += 1
	} else {
		user.Points += 0.5
	}

	// 更新最后签到时间
	user.LastCheckIn = time.Now()

	// 发送签到成功消息
	msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("签到成功！当前积分: *%.2f*", user.Points))
	msg.ParseMode = tgbotapi.ModeMarkdownV2 // 启用 MarkdownV2 解析模式
	bot.Send(msg)

	// 发送签到成功提示
	successMsg := tgbotapi.NewMessage(chatID, "🎉 恭喜您签到成功，积分已增加！")
	bot.Send(successMsg)

	log.Printf("用户签到成功: ID=%d, 用户名=%s, 积分=%.2f", user.ID, user.Username, user.Points)
}

/******************* 查看信息功能 *******************/
func info(bot *tgbotapi.BotAPI, chatID int64, user *User) {
	lastCheckIn := "从未签到"
	if !user.LastCheckIn.IsZero() {
		lastCheckIn = user.LastCheckIn.Format("2006-01-02 15:04:05")
	}

	// 对 MarkdownV2 保留字符进行转义
	escapedUsername := escapeMarkdownV2(user.Username)
	escapedFirstname := escapeMarkdownV2(user.FirstName)
	escapedLastname := escapeMarkdownV2(user.LastName)
	escapedLastCheckIn := escapeMarkdownV2(lastCheckIn)

	// 构建用户显示名称
	var displayName string
	if escapedFirstname != "" && escapedLastname != "" {
		displayName = fmt.Sprintf("%s %s", escapedFirstname, escapedLastname)
	} else if escapedFirstname != "" {
		displayName = escapedFirstname
	} else if escapedLastname != "" {
		displayName = escapedLastname
	} else {
		displayName = "Unknown User"
	}

	// 使用 MarkdownV2 格式
	msg := tgbotapi.NewMessage(chatID, fmt.Sprintf(
		"%s \\(@%s\\) 的信息\n"+
			"  \\- *用户ID*: `%d`\n"+
			"  \\- *积分*: `%.2f`\n"+
			"  \\- *最后签到时间*: `%s`",
		displayName, escapedUsername, user.ID, user.Points, escapedLastCheckIn,
	))
	msg.ParseMode = tgbotapi.ModeMarkdownV2 // 启用 MarkdownV2 解析模式
	_, err := bot.Send(msg)
	if err != nil {
		log.Printf("发送消息失败: %v", err)
	}
}

/******************* 转义 MarkdownV2 保留字符 *******************/
func escapeMarkdownV2(text string) string {
	// 需要转义的字符
	specialChars := []string{"_", "*", "[", "]", "(", ")", "~", "`", ">", "#", "+", "-", "=", "|", "{", "}", ".", "!"}
	for _, char := range specialChars {
		text = strings.ReplaceAll(text, char, "\\"+char)
	}
	return text
}

/******************* 加载/保存 卡密 *******************/
func loadCodes() {
	file, err := ioutil.ReadFile(codesFile)
	if err != nil {
		if os.IsNotExist(err) {
			log.Println("卡密文件不存在，将初始化为空数据。")
		} else {
			log.Printf("读取卡密文件失败: %v", err)
		}
		return
	}
	if err := json.Unmarshal(file, &codes); err != nil {
		log.Printf("解析卡密文件失败: %v", err)
	}
}

func saveCodes() {
	data, err := json.MarshalIndent(codes, "", "  ")
	if err != nil {
		log.Printf("序列化卡密数据失败: %v", err)
		return
	}
	if err := ioutil.WriteFile(codesFile, data, 0644); err != nil {
		log.Printf("保存卡密数据失败: %v", err)
	}
}

/******************* 加载/保存 用户数据 *******************/
func loadData() {
	file, err := ioutil.ReadFile(dataFile)
	if err != nil {
		if os.IsNotExist(err) {
			log.Println("数据文件不存在，将初始化为空数据。")
		} else {
			log.Fatalf("读取数据文件失败: %v", err)
		}
		return
	}
	if err := json.Unmarshal(file, &users); err != nil {
		log.Fatalf("解析数据文件失败: %v", err)
	}
	log.Println("用户数据加载成功。")
}

func saveData() {
	data, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		log.Printf("序列化用户数据失败: %v", err)
		return
	}
	if err := ioutil.WriteFile(dataFile, data, 0644); err != nil {
		log.Printf("保存用户数据失败: %v", err)
	} else {
		log.Println("用户数据保存成功。")
	}
}
