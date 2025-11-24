package parser

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hx-w/minidemo-encoder/internal/encoder"
	ilog "github.com/hx-w/minidemo-encoder/internal/logger"
	dem "github.com/markus-wa/demoinfocs-golang/v2/pkg/demoinfocs"
	common "github.com/markus-wa/demoinfocs-golang/v2/pkg/demoinfocs/common"
	events "github.com/markus-wa/demoinfocs-golang/v2/pkg/demoinfocs/events"
)

type TickPlayer struct {
	tick    int
	steamid uint64
}

type RoundInfo struct {
	roundNum        int
	freezetimeStart int
	freezetimeEnd   int
	roundEnd        int
	inFreezeTime    bool
	isHalftime      bool
	started         bool
}

var allRoundsFreezeInfo []string
var allFreezeDurations []float64

var allPurchaseData AllRoundsPurchaseData
var weaponTracker *WeaponTracker
var currentRoundPurchases *RoundPurchaseData

var outputBaseDir string

// 帧率检测相关变量
var (
	detectedTickRate  float64 = 128.0
	detectedFrameRate float64 = 128.0
	timeScaleFactor   float64 = 1.0
	frameRateDetected bool    = false
	shouldInterpolate bool    = false
)

// C4持有者记录
type C4HolderInfo struct {
	RoundNum   int    `json:"round"`
	PlayerName string `json:"player_name"`
}

var allC4Holders []C4HolderInfo

// 聊天消息记录
type ChatMessage struct {
	Round      int     `json:"round"`
	Time       float64 `json:"time"`
	PlayerName string  `json:"player_name"`
	Team       string  `json:"team"`
	Message    string  `json:"message"`
	IsTeamChat bool    `json:"is_team_chat"`
}

var allChatMessages []ChatMessage

// 玩家信息记录
type PlayerInfo struct {
	SteamID       uint64 `json:"steamid"`
	CrosshairCode string `json:"crosshair_code"`
}

var allPlayersInfo map[string]*PlayerInfo

// 初始化玩家到正确的队伍，避免重复
func initializePlayerInRound(player *common.Player, roundPurchases *RoundPurchaseData) {
	if player == nil || roundPurchases == nil {
		return
	}

	playerName := player.Name

	// 先从两个队伍中都移除该玩家（避免重复）
	delete(roundPurchases.T, playerName)
	delete(roundPurchases.CT, playerName)

	// 然后根据当前队伍添加到正确的位置
	var teamMap map[string]*PlayerPurchaseData
	if player.Team == common.TeamTerrorists {
		teamMap = roundPurchases.T
	} else if player.Team == common.TeamCounterTerrorists {
		teamMap = roundPurchases.CT
	}

	if teamMap != nil {
		teamMap[playerName] = &PlayerPurchaseData{
			Purchases:      []PurchaseRecord{},
			FinalInventory: []string{},
		}
	}
}

// 武器变化检测函数
func detectWeaponChanges(player *common.Player, currentTick int, buttonTickMap map[TickPlayer]int32, playerLastWeapons map[uint64][]string) {
	if player == nil {
		return
	}

	steamID := player.SteamID64

	currentWeapons := []string{}
	for _, weapon := range player.Weapons() {
		if weapon != nil {
			weaponName := getEquipmentName(weapon)
			if weaponName != "" && !shouldFilterWeapon(weaponName) {
				currentWeapons = append(currentWeapons, weaponName)
			}
		}
	}

	lastWeapons, exists := playerLastWeapons[steamID]

	if exists {
		for _, lastWeapon := range lastWeapons {
			found := false
			for _, currentWeapon := range currentWeapons {
				if currentWeapon == lastWeapon {
					found = true
					break
				}
			}

			if !found {
				// 武器消失
			}
		}

		for _, currentWeapon := range currentWeapons {
			found := false
			for _, lastWeapon := range lastWeapons {
				if currentWeapon == lastWeapon {
					found = true
					break
				}
			}

			if !found {
				// 新武器出现
			}
		}
	}

	playerLastWeapons[steamID] = currentWeapons
}

// 检测demo的实际帧率
func detectActualFrameRate(parser dem.Parser) {
	var (
		frameSamples  []float64
		lastFrameTick int
		sampleCount   int
		maxSamples    = 500
	)

	parser.RegisterEventHandler(func(e events.FrameDone) {
		gs := parser.GameState()

		if gs.IsWarmupPeriod() || frameRateDetected {
			return
		}

		currentTick := gs.IngameTick()

		if lastFrameTick > 0 {
			tickDiff := currentTick - lastFrameTick
			if tickDiff > 0 && tickDiff < 10 {
				frameSamples = append(frameSamples, float64(tickDiff))
				sampleCount++
			}
		}

		lastFrameTick = currentTick

		if sampleCount >= maxSamples {
			frameRateDetected = true

			var sum float64
			for _, sample := range frameSamples {
				sum += sample
			}
			avgTicksPerFrame := sum / float64(len(frameSamples))

			detectedTickRate = parser.TickRate()
			detectedFrameRate = detectedTickRate / avgTicksPerFrame
			timeScaleFactor = detectedFrameRate / detectedTickRate

			ilog.InfoLogger.Printf("========== 帧率检测结果 ==========")
			ilog.InfoLogger.Printf("Demo Tick Rate: %.2f", detectedTickRate)
			ilog.InfoLogger.Printf("实际帧率: %.2f fps", detectedFrameRate)
			ilog.InfoLogger.Printf("平均每帧间隔: %.2f ticks", avgTicksPerFrame)
			ilog.InfoLogger.Printf("时间缩放因子: %.4f", timeScaleFactor)

			// 智能判断是否需要插帧
			const MIN_ACCEPTABLE_FPS = 90.0 // 最低可接受帧率
			const MAX_NORMAL_FPS = 120.0    // 正常帧率上限

			if detectedFrameRate < MIN_ACCEPTABLE_FPS {
				shouldInterpolate = true
				targetFPS := 128.0
				interpolationRatio := targetFPS / detectedFrameRate
				ilog.InfoLogger.Printf("  检测到低帧率 (%.1f fps)，将进行插帧优化", detectedFrameRate)
				ilog.InfoLogger.Printf("  目标帧率: %.0f fps (插值比例: %.2fx)", targetFPS, interpolationRatio)
			} else if detectedFrameRate > MAX_NORMAL_FPS {
				shouldInterpolate = false
				ilog.InfoLogger.Printf("  高帧率demo (%.1f fps)，无需处理", detectedFrameRate)
			} else {
				shouldInterpolate = false
				ilog.InfoLogger.Printf("  帧率正常 (%.1f fps)，无需处理", detectedFrameRate)
			}

			ilog.InfoLogger.Printf("==================================")
			saveDemoInfo()
		}
	})
}

func getAdjustedTime(tickDiff int, tickRate float64) float64 {
	return float64(tickDiff) / tickRate
}

// 保存demo信息到文件
func saveDemoInfo() {
	infoFile := filepath.Join(outputBaseDir, "demo_info.json")

	processingInfo := "无需处理"
	if shouldInterpolate {
		targetFPS := 128.0
		interpolationRatio := targetFPS / detectedFrameRate
		processingInfo = fmt.Sprintf("已进行插帧优化 (%.1f fps → %.0f fps, %.2fx)",
			detectedFrameRate, targetFPS, interpolationRatio)
	}

	info := map[string]interface{}{
		"tick_rate":           detectedTickRate,
		"original_frame_rate": detectedFrameRate,
		"target_frame_rate":   128.0,
		"interpolated":        shouldInterpolate,
		"interpolation_ratio": 128.0 / detectedFrameRate,
		"note":                processingInfo,
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		ilog.ErrorLogger.Printf("保存demo信息失败: %s\n", err.Error())
		return
	}

	os.WriteFile(infoFile, data, 0644)
	ilog.InfoLogger.Printf("Demo信息已保存到: %s", infoFile)
}

func Start(filePath string) {
	iFile, err := os.Open(filePath)
	checkError(err)

	iParser := dem.NewParser(iFile)
	defer iParser.Close()

	demoFileName := filepath.Base(filePath)
	demoName := strings.TrimSuffix(demoFileName, filepath.Ext(demoFileName))

	outputBaseDir = filepath.Join("output", demoName)

	err = os.MkdirAll(outputBaseDir, os.ModePerm)
	if err != nil {
		ilog.ErrorLogger.Printf("创建输出目录失败: %s\n", err.Error())
		return
	}

	encoder.SetSaveDir(outputBaseDir)

	ilog.InfoLogger.Printf("输出目录: %s", outputBaseDir)

	allRoundsFreezeInfo = make([]string, 0)
	allFreezeDurations = make([]float64, 0)

	allPurchaseData = make(AllRoundsPurchaseData)
	weaponTracker = NewWeaponTracker()

	allC4Holders = make([]C4HolderInfo, 0)
	allPlayersInfo = make(map[string]*PlayerInfo)
	allChatMessages = make([]ChatMessage, 0)

	var buttonTickMap map[TickPlayer]int32 = make(map[TickPlayer]int32)
	detectActualFrameRate(iParser)
	var playerLastScopedState map[uint64]bool = make(map[uint64]bool)
	var playerLastWeapons map[uint64][]string = make(map[uint64][]string)
	var (
		roundNum           = 0
		currentRound       *RoundInfo
		firstRoundDetected = false
		gameStarted        = false
	)

	iParser.RegisterEventHandler(func(e events.FrameDone) {
		gs := iParser.GameState()

		// 检查是否在热身
		if gs.IsWarmupPeriod() {
			return
		}

		// 检查游戏是否已开始
		if !gameStarted || currentRound == nil || !currentRound.started {
			return
		}

		currentTick := gs.IngameTick()

		tPlayers := gs.TeamTerrorists().Members()
		ctPlayers := gs.TeamCounterTerrorists().Members()
		Players := append(tPlayers, ctPlayers...)
		for _, player := range Players {
			if player != nil {
				var addonButton int32 = 0
				key := TickPlayer{currentTick, player.SteamID64}
				if val, ok := buttonTickMap[key]; ok {
					addonButton = val
					delete(buttonTickMap, key)
				}

				steamID := player.SteamID64
				currentScoped := player.IsScoped()
				lastScoped, exists := playerLastScopedState[steamID]

				if !exists {
					playerLastScopedState[steamID] = currentScoped
				} else if currentScoped != lastScoped {
					addonButton |= IN_ATTACK2
					playerLastScopedState[steamID] = currentScoped
				}

				if player.IsDefusing {
					addonButton |= IN_USE
				}
				if player.IsPlanting {
					addonButton |= IN_USE
				}

				detectWeaponChanges(player, currentTick, buttonTickMap, playerLastWeapons)

				parsePlayerFrame(player, addonButton, iParser.TickRate(), currentRound.inFreezeTime)
			}
		}
	})

	iParser.RegisterEventHandler(func(e events.ItemDrop) {
		gs := iParser.GameState()

		// 检查是否在热身
		if gs.IsWarmupPeriod() {
			return
		}

		currentTick := gs.IngameTick()

		if e.Player != nil {
			key := TickPlayer{currentTick, e.Player.SteamID64}

			if e.Weapon != nil {
				weaponType := e.Weapon.Type

				isGrenade := false
				switch weaponType {
				case common.EqFlash, common.EqSmoke, common.EqHE,
					common.EqMolotov, common.EqIncendiary, common.EqDecoy:
					isGrenade = true
				}

				if isGrenade {
					if _, ok := buttonTickMap[key]; ok {
						buttonTickMap[key] |= IN_ATTACK
					} else {
						buttonTickMap[key] = IN_ATTACK
					}
				} else {
				}
			}
		}

		if currentRound == nil || currentRoundPurchases == nil || !currentRound.inFreezeTime {
			return
		}

		if e.Player == nil || e.Weapon == nil {
			return
		}

		weaponName := getEquipmentName(e.Weapon)
		if shouldFilterWeapon(weaponName) {
			return
		}

		weaponTracker.RegisterDrop(e.Weapon, e.Player.SteamID64)

		dropTime := getAdjustedTime(currentTick-currentRound.freezetimeStart, iParser.TickRate())

		var teamMap map[string]*PlayerPurchaseData
		if e.Player.Team == common.TeamTerrorists {
			teamMap = currentRoundPurchases.T
		} else if e.Player.Team == common.TeamCounterTerrorists {
			teamMap = currentRoundPurchases.CT
		} else {
			return
		}

		playerName := e.Player.Name
		if _, exists := teamMap[playerName]; !exists {
			// 使用新的初始化函数确保玩家只在一个队伍
			initializePlayerInRound(e.Player, currentRoundPurchases)
			// 重新获取 teamMap
			if e.Player.Team == common.TeamTerrorists {
				teamMap = currentRoundPurchases.T
			} else {
				teamMap = currentRoundPurchases.CT
			}
		}

		dropRecord := PurchaseRecord{
			Time:   dropTime,
			Item:   weaponName,
			Slot:   getEquipmentSlot(e.Weapon.Type),
			Action: ActionDrop,
		}
		teamMap[playerName].Purchases = append(teamMap[playerName].Purchases, dropRecord)
	})

	iParser.RegisterEventHandler(func(e events.ItemPickup) {
		gs := iParser.GameState()

		// 检查是否在热身
		if gs.IsWarmupPeriod() {
			return
		}

		currentTick := gs.IngameTick()

		if e.Player != nil && e.Weapon != nil {
			weaponName := getEquipmentName(e.Weapon)

			if !shouldFilterWeapon(weaponName) {
				key := TickPlayer{currentTick, e.Player.SteamID64}

				if _, ok := buttonTickMap[key]; ok {
					buttonTickMap[key] |= IN_USE
				} else {
					buttonTickMap[key] = IN_USE
				}

				if currentRound != nil && !currentRound.inFreezeTime {
				}
			}
		}

		if currentRound == nil || currentRoundPurchases == nil {
			return
		}

		if e.Player == nil || e.Weapon == nil {
			return
		}

		if !currentRound.inFreezeTime {
			return
		}

		weaponName := getEquipmentName(e.Weapon)

		if shouldFilterWeapon(weaponName) {
			return
		}

		actionTime := getAdjustedTime(currentTick-currentRound.freezetimeStart, iParser.TickRate())

		var teamMap map[string]*PlayerPurchaseData
		if e.Player.Team == common.TeamTerrorists {
			teamMap = currentRoundPurchases.T
		} else if e.Player.Team == common.TeamCounterTerrorists {
			teamMap = currentRoundPurchases.CT
		} else {
			return
		}

		playerName := e.Player.Name
		if _, exists := teamMap[playerName]; !exists {
			// 使用新的初始化函数确保玩家只在一个队伍
			initializePlayerInRound(e.Player, currentRoundPurchases)
			// 重新获取 teamMap
			if e.Player.Team == common.TeamTerrorists {
				teamMap = currentRoundPurchases.T
			} else {
				teamMap = currentRoundPurchases.CT
			}
		}

		var action ItemAction

		isPurchase := weaponTracker.IsPurchase(e.Weapon, e.Player.SteamID64)
		isPickup := false

		if !isPurchase {
			isPickup = weaponTracker.IsPickup(e.Weapon, e.Player.SteamID64)
		}

		if isPurchase {
			action = ActionPurchase
		} else if isPickup {
			action = ActionPickup
		} else {
			return
		}

		purchase := PurchaseRecord{
			Time:   actionTime,
			Item:   weaponName,
			Slot:   getEquipmentSlot(e.Weapon.Type),
			Action: action,
		}
		teamMap[playerName].Purchases = append(teamMap[playerName].Purchases, purchase)
	})

	iParser.RegisterEventHandler(func(e events.WeaponFire) {
		gs := iParser.GameState()

		// 检查是否在热身
		if gs.IsWarmupPeriod() {
			return
		}

		currentTick := gs.IngameTick()
		key := TickPlayer{currentTick, e.Shooter.SteamID64}
		if _, ok := buttonTickMap[key]; ok {
			buttonTickMap[key] |= IN_ATTACK
		} else {
			buttonTickMap[key] = IN_ATTACK
		}
	})

	iParser.RegisterEventHandler(func(e events.PlayerJump) {
		gs := iParser.GameState()

		// 检查是否在热身
		if gs.IsWarmupPeriod() {
			return
		}

		currentTick := gs.IngameTick()
		key := TickPlayer{currentTick, e.Player.SteamID64}
		if _, ok := buttonTickMap[key]; ok {
			buttonTickMap[key] |= IN_JUMP
		} else {
			buttonTickMap[key] = IN_JUMP
		}
	})

	iParser.RegisterEventHandler(func(e events.BombDefuseStart) {
		if e.Player == nil {
			return
		}

		gs := iParser.GameState()

		// 检查是否在热身
		if gs.IsWarmupPeriod() {
			return
		}

		currentTick := gs.IngameTick()
		key := TickPlayer{currentTick, e.Player.SteamID64}

		if _, ok := buttonTickMap[key]; ok {
			buttonTickMap[key] |= IN_USE
		} else {
			buttonTickMap[key] = IN_USE
		}

		ilog.InfoLogger.Printf("  [拆弹开始] %s 开始拆弹 (Tick: %d)",
			e.Player.Name, currentTick)
	})

	iParser.RegisterEventHandler(func(e events.BombDefuseAborted) {
		if e.Player == nil {
			return
		}

		gs := iParser.GameState()

		// 检查是否在热身
		if gs.IsWarmupPeriod() {
			return
		}

		currentTick := gs.IngameTick()

		ilog.InfoLogger.Printf("  [拆弹中止] %s (Tick: %d)",
			e.Player.Name, currentTick)
	})

	iParser.RegisterEventHandler(func(e events.BombDefused) {
		if e.Player == nil {
			return
		}

		gs := iParser.GameState()

		// 检查是否在热身
		if gs.IsWarmupPeriod() {
			return
		}

		currentTick := gs.IngameTick()

		ilog.InfoLogger.Printf("  [拆弹完成] %s 成功拆除炸弹 (Tick: %d)",
			e.Player.Name, currentTick)
	})

	iParser.RegisterEventHandler(func(e events.BombPlantBegin) {
		if e.Player == nil {
			return
		}

		gs := iParser.GameState()

		// 检查是否在热身
		if gs.IsWarmupPeriod() {
			return
		}

		currentTick := gs.IngameTick()
		key := TickPlayer{currentTick, e.Player.SteamID64}

		if _, ok := buttonTickMap[key]; ok {
			buttonTickMap[key] |= IN_USE
		} else {
			buttonTickMap[key] = IN_USE
		}

		ilog.InfoLogger.Printf("  [埋弹开始] %s 开始埋弹 (Tick: %d)",
			e.Player.Name, currentTick)
	})

	iParser.RegisterEventHandler(func(e events.BombPlantAborted) {
		if e.Player == nil {
			return
		}

		gs := iParser.GameState()

		// 检查是否在热身
		if gs.IsWarmupPeriod() {
			return
		}

		currentTick := gs.IngameTick()

		ilog.InfoLogger.Printf("  [埋弹中止] %s (Tick: %d)",
			e.Player.Name, currentTick)
	})

	iParser.RegisterEventHandler(func(e events.BombPlanted) {
		if e.Player == nil {
			return
		}

		gs := iParser.GameState()

		// 检查是否在热身
		if gs.IsWarmupPeriod() {
			return
		}

		currentTick := gs.IngameTick()

		siteName := "未知"
		switch e.Site {
		case events.BombsiteA:
			siteName = "A点"
		case events.BombsiteB:
			siteName = "B点"
		}

		ilog.InfoLogger.Printf("  [埋弹完成] %s 成功放置炸弹于 %s (Tick: %d)",
			e.Player.Name, siteName, currentTick)
	})

	iParser.RegisterEventHandler(func(e events.GameHalfEnded) {
		gs := iParser.GameState()

		// 检查是否在热身
		if gs.IsWarmupPeriod() {
			return
		}

		if currentRound != nil {
			currentRound.isHalftime = true
			ilog.InfoLogger.Printf("半场结束,回合 %d 包含换边时间", currentRound.roundNum)
		}
	})

	// 聊天消息处理
	iParser.RegisterEventHandler(func(e events.ChatMessage) {
		gs := iParser.GameState()

		// 检查是否在热身
		if gs.IsWarmupPeriod() {
			return
		}

		if currentRound == nil || !currentRound.started {
			return
		}

		currentTick := gs.IngameTick()
		chatTime := getAdjustedTime(currentTick-currentRound.freezetimeStart, iParser.TickRate())

		teamName := "Unknown"
		var sender *common.Player

		allPlayers := append(gs.TeamTerrorists().Members(), gs.TeamCounterTerrorists().Members()...)
		for _, player := range allPlayers {
			if player != nil && player.Name == e.Sender.Name {
				sender = player
				break
			}
		}

		if sender != nil {
			switch sender.Team {
			case common.TeamTerrorists:
				teamName = "T"
			case common.TeamCounterTerrorists:
				teamName = "CT"
			case common.TeamSpectators:
				teamName = "Spectator"
			default:
				teamName = "Unknown"
			}
		}

		chatMsg := ChatMessage{
			Round:      currentRound.roundNum,
			Time:       chatTime,
			PlayerName: e.Sender.Name,
			Team:       teamName,
			Message:    e.Text,
			IsTeamChat: !e.IsChatAll,
		}

		allChatMessages = append(allChatMessages, chatMsg)

		chatType := "全体"
		if chatMsg.IsTeamChat {
			chatType = "队伍"
		}

		ilog.InfoLogger.Printf("  [聊天-%s] 回合%d %.2f秒 - %s (%s): %s",
			chatType, currentRound.roundNum, chatTime, e.Sender.Name, teamName, e.Text)
	})

	iParser.RegisterEventHandler(func(e events.RoundStart) {
		gs := iParser.GameState()
		currentTick := gs.IngameTick()

		// 检查热身状态
		if gs.IsWarmupPeriod() {
			ilog.InfoLogger.Printf("跳过热身回合 (Tick: %d)", currentTick)
			if gameStarted {
				ilog.InfoLogger.Printf("⚠ 检测到重新进入热身，重置游戏状态")
				gameStarted = false
				firstRoundDetected = false
				roundNum = 0
				currentRound = nil
			}
			return
		}

		// 如果当前回合还存在且已开始，不处理新的 RoundStart
		if currentRound != nil && currentRound.started {
			ilog.InfoLogger.Printf("⚠ 检测到重复的 RoundStart 事件 (Tick: %d)，当前回合 %d 还未结束，跳过", currentTick, currentRound.roundNum)
			return
		}

		if !firstRoundDetected {
			firstRoundDetected = true
			gameStarted = true
			roundNum = 1
			ilog.InfoLogger.Printf("检测到第一个正式回合开始")
		} else {
			roundNum++
		}

		if currentRound != nil && currentRound.roundNum == roundNum {
			ilog.InfoLogger.Printf("回合 %d 已初始化,跳过重复初始化", roundNum)
			return
		}

		ilog.InfoLogger.Printf("====================================")
		ilog.InfoLogger.Printf("回合 %d 开始 (Tick: %d)", roundNum, currentTick)

		currentRound = &RoundInfo{
			roundNum:        roundNum,
			freezetimeStart: currentTick,
			freezetimeEnd:   currentTick,
			inFreezeTime:    true,
			isHalftime:      false,
			started:         false,
		}

		currentRoundPurchases = &RoundPurchaseData{
			T:  make(map[string]*PlayerPurchaseData),
			CT: make(map[string]*PlayerPurchaseData),
		}
		allPurchaseData[fmt.Sprintf("round%d", roundNum)] = currentRoundPurchases

		weaponTracker = NewWeaponTracker()

		playerLastScopedState = make(map[uint64]bool)
		playerLastWeapons = make(map[uint64][]string)
		tPlayers := gs.TeamTerrorists().Members()
		ctPlayers := gs.TeamCounterTerrorists().Members()
		Players := append(tPlayers, ctPlayers...)

		for _, player := range Players {
			if player != nil {
				parsePlayerInitFrame(player)
				recordPlayerStartMoney(player, roundNum)
				recordPlayerInfo(player)

				// 使用新的初始化函数
				initializePlayerInRound(player, currentRoundPurchases)
			}
		}

		currentRound.started = true
	})

	iParser.RegisterEventHandler(func(e events.RoundFreezetimeEnd) {
		gs := iParser.GameState()

		// 检查是否在热身
		if gs.IsWarmupPeriod() {
			return
		}

		if currentRound != nil {
			currentTick := gs.IngameTick()

			currentRound.freezetimeEnd = currentTick
			currentRound.inFreezeTime = false

			freezeDuration := getAdjustedTime(currentTick-currentRound.freezetimeStart, iParser.TickRate())
			ilog.InfoLogger.Printf("回合 %d 冻结时间结束 (Tick: %d, 持续: %.2f秒)",
				currentRound.roundNum, currentTick, freezeDuration)

			recordAllPlayersInventory(&gs, currentRoundPurchases)
			detectC4Holder(&gs, currentRound.roundNum)
		}
	})

	iParser.RegisterEventHandler(func(e events.RoundEnd) {
		gs := iParser.GameState()

		if gs.IsWarmupPeriod() {
			ilog.InfoLogger.Printf("⚠  回合结束时检测到热身状态，跳过保存")
			currentRound = nil
			return
		}

		if !gameStarted {
			ilog.InfoLogger.Printf("⚠  游戏未开始，跳过回合结束处理")
			currentRound = nil
			return
		}

		if currentRound != nil {
			currentTick := gs.IngameTick()
			currentRound.roundEnd = currentTick

			freezeDuration := getAdjustedTime(currentRound.freezetimeEnd-currentRound.freezetimeStart, iParser.TickRate())

			ilog.InfoLogger.Printf("回合 %d 结束 (Tick: %d)", currentRound.roundNum, currentTick)

			if currentRound.isHalftime {
				ilog.InfoLogger.Printf("  ⚠  半场换边回合,冻结时间将使用最常见值")
				freezeInfo := fmt.Sprintf("HALFTIME:%d", currentRound.roundNum)
				allRoundsFreezeInfo = append(allRoundsFreezeInfo, freezeInfo)
			} else {
				ilog.InfoLogger.Printf("  冻结时间: %.2f秒", freezeDuration)
				freezeInfo := fmt.Sprintf("round%d: %.2f秒", currentRound.roundNum, freezeDuration)
				allRoundsFreezeInfo = append(allRoundsFreezeInfo, freezeInfo)
				allFreezeDurations = append(allFreezeDurations, freezeDuration)
			}

			printPurchaseStats(currentRoundPurchases, currentRound.roundNum)
			adjustMoneyForFinalInventory(currentRoundPurchases, currentRound.roundNum)

			tPlayers := gs.TeamTerrorists().Members()
			ctPlayers := gs.TeamCounterTerrorists().Members()
			Players := append(tPlayers, ctPlayers...)

			ilog.InfoLogger.Printf("  正在保存录像文件...")
			savedCount := 0

			for _, player := range Players {
				if player != nil {
					// 如果需要插帧，先进行插帧
					if shouldInterpolate {
						interpolatePlayerFrames(player.Name)
					}
					saveToRecFile(player, int32(currentRound.roundNum))
					savedCount++
				}
			}

			if shouldInterpolate {
				ilog.InfoLogger.Printf("  ✓ 已对所有玩家进行插帧优化")
			}

			ilog.InfoLogger.Printf("  已保存 %d 个玩家录像到: %s/round%d/", savedCount, outputBaseDir, currentRound.roundNum)
			ilog.InfoLogger.Printf("====================================\n")

			currentRound = nil
		}
	})

	err = iParser.ParseToEnd()
	checkError(err)

	saveFreezeTimeInfo()

	ilog.InfoLogger.Println("\n开始保存购买数据...")

	for roundKey, roundData := range allPurchaseData {
		tTotal := 0
		ctTotal := 0
		for _, pdata := range roundData.T {
			tTotal += len(pdata.Purchases)
		}
		for _, pdata := range roundData.CT {
			ctTotal += len(pdata.Purchases)
		}
		ilog.InfoLogger.Printf("  %s: T方 %d 条记录, CT方 %d 条记录", roundKey, tTotal, ctTotal)
	}

	err = savePurchaseData(allPurchaseData)
	if err != nil {
		ilog.ErrorLogger.Printf("保存购买数据失败: %s\n", err.Error())
	} else {
		ilog.InfoLogger.Printf("购买数据已保存到: %s/purchases.json", outputBaseDir)
	}

	ilog.InfoLogger.Println("\n开始保存金钱数据...")
	err = saveMoneyData()
	if err != nil {
		ilog.ErrorLogger.Printf("保存金钱数据失败: %s\n", err.Error())
	} else {
		ilog.InfoLogger.Printf("金钱数据已保存到: %s/money.json", outputBaseDir)
	}

	ilog.InfoLogger.Println("\n开始保存C4持有者数据...")
	err = saveC4HolderData()
	if err != nil {
		ilog.ErrorLogger.Printf("保存C4数据失败: %s\n", err.Error())
	} else {
		ilog.InfoLogger.Printf("C4数据已保存到: %s/c4_holders.json", outputBaseDir)
	}

	ilog.InfoLogger.Println("\n开始保存玩家信息数据...")
	err = savePlayersInfo()
	if err != nil {
		ilog.ErrorLogger.Printf("保存玩家信息失败: %s\n", err.Error())
	} else {
		ilog.InfoLogger.Printf("玩家信息已保存到: %s/players_info.json", outputBaseDir)
	}

	ilog.InfoLogger.Println("\n开始保存聊天数据...")
	err = saveChatData()
	if err != nil {
		ilog.ErrorLogger.Printf("保存聊天数据失败: %s\n", err.Error())
	} else {
		ilog.InfoLogger.Printf("聊天数据已保存到: %s/chat.json", outputBaseDir)
		ilog.InfoLogger.Printf("共记录 %d 条聊天消息", len(allChatMessages))
	}

	ilog.InfoLogger.Printf("\n解析完成!所有回合录像已保存到 %s/ 目录", outputBaseDir)
	ilog.InfoLogger.Printf("共解析 %d 个回合\n", roundNum)
}

func recordAllPlayersInventory(gs *dem.GameState, roundPurchases *RoundPurchaseData) {
	tPlayers := (*gs).TeamTerrorists().Members()
	ctPlayers := (*gs).TeamCounterTerrorists().Members()
	allPlayers := append(tPlayers, ctPlayers...)

	for _, player := range allPlayers {
		if player == nil {
			continue
		}

		var teamMap map[string]*PlayerPurchaseData
		if player.Team == common.TeamTerrorists {
			teamMap = roundPurchases.T
		} else if player.Team == common.TeamCounterTerrorists {
			teamMap = roundPurchases.CT
		} else {
			continue
		}

		playerName := player.Name
		if _, exists := teamMap[playerName]; !exists {
			teamMap[playerName] = &PlayerPurchaseData{
				Purchases:      []PurchaseRecord{},
				FinalInventory: []string{},
			}
		}

		teamMap[playerName].FinalInventory = getFinalInventory(player)
	}
}

func printPurchaseStats(roundPurchases *RoundPurchaseData, roundNum int) {
	ilog.InfoLogger.Printf("  [回合 %d 装备变动统计]", roundNum)

	tCount := 0
	for playerName, data := range roundPurchases.T {
		if len(data.Purchases) > 0 || len(data.FinalInventory) > 0 {
			tCount++
			ilog.InfoLogger.Printf("    T - %s: %d 次操作, 最终装备 %v",
				playerName, len(data.Purchases), data.FinalInventory)
		}
	}

	ctCount := 0
	for playerName, data := range roundPurchases.CT {
		if len(data.Purchases) > 0 || len(data.FinalInventory) > 0 {
			ctCount++
			ilog.InfoLogger.Printf("    CT - %s: %d 次操作, 最终装备 %v",
				playerName, len(data.Purchases), data.FinalInventory)
		}
	}

	ilog.InfoLogger.Printf("  T方: %d 名玩家, CT方: %d 名玩家", tCount, ctCount)
}

func getTeamName(team common.Team) string {
	switch team {
	case common.TeamTerrorists:
		return "T"
	case common.TeamCounterTerrorists:
		return "CT"
	default:
		return "Unknown"
	}
}

func saveFreezeTimeInfo() {
	mostCommonFreeze := getMostCommonFreezeDuration()

	for i, info := range allRoundsFreezeInfo {
		if len(info) >= 9 && info[:9] == "HALFTIME:" {
			var roundNum int
			fmt.Sscanf(info, "HALFTIME:%d", &roundNum)
			allRoundsFreezeInfo[i] = fmt.Sprintf("round%d: %.2f秒", roundNum, mostCommonFreeze)
		}
	}

	freezeFile := filepath.Join(outputBaseDir, "freeze.txt")
	file, err := os.Create(freezeFile)
	if err != nil {
		ilog.ErrorLogger.Printf("创建 freeze.txt 失败: %s\n", err.Error())
		return
	}
	defer file.Close()

	for _, info := range allRoundsFreezeInfo {
		var roundNum int
		var duration float64
		if _, err := fmt.Sscanf(info, "回合 %d: %f秒", &roundNum, &duration); err == nil {
			file.WriteString(fmt.Sprintf("round%d: %.2f秒\n", roundNum, duration))
		} else {
			file.WriteString(info + "\n")
		}
	}

	file.WriteString(fmt.Sprintf("冻结时间:%.2f秒", mostCommonFreeze))

	ilog.InfoLogger.Printf("\n冻结时间信息已保存到: %s", freezeFile)
	ilog.InfoLogger.Printf("(半场回合使用最常见冻结时间: %.2f秒)\n", mostCommonFreeze)
}

func getMostCommonFreezeDuration() float64 {
	if len(allFreezeDurations) == 0 {
		return 15.0
	}

	countMap := make(map[int]int)
	for _, duration := range allFreezeDurations {
		rounded := int(duration*10 + 0.5)
		countMap[rounded]++
	}

	maxCount := 0
	mostCommon := 150
	for duration, count := range countMap {
		if count > maxCount {
			maxCount = count
			mostCommon = duration
		}
	}

	return float64(mostCommon) / 10.0
}

func detectC4Holder(gs *dem.GameState, roundNum int) {
	tPlayers := (*gs).TeamTerrorists().Members()

	for _, player := range tPlayers {
		if player == nil {
			continue
		}

		for _, weapon := range player.Weapons() {
			if weapon != nil && weapon.Type == common.EqBomb {
				c4Info := C4HolderInfo{
					RoundNum:   roundNum,
					PlayerName: player.Name,
				}
				allC4Holders = append(allC4Holders, c4Info)

				ilog.InfoLogger.Printf("  [C4检测] 回合 %d: %s 持有C4",
					roundNum, player.Name)
				return
			}
		}
	}

	ilog.InfoLogger.Printf("  [C4检测] ⚠ 回合 %d: 未检测到C4持有者", roundNum)
}

func recordPlayerInfo(player *common.Player) {
	if player == nil {
		return
	}

	playerName := player.Name

	if _, exists := allPlayersInfo[playerName]; exists {
		return
	}

	crosshairCode := player.CrosshairCode()
	if crosshairCode == "" {
		crosshairCode = "N/A"
	}

	allPlayersInfo[playerName] = &PlayerInfo{
		SteamID:       player.SteamID64,
		CrosshairCode: crosshairCode,
	}

	ilog.InfoLogger.Printf("  [玩家信息] %s - SteamID: %d, 准星: %s",
		playerName, player.SteamID64, crosshairCode)
}

func saveC4HolderData() error {
	c4File := filepath.Join(outputBaseDir, "c4_holders.json")

	data, err := json.MarshalIndent(allC4Holders, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(c4File, data, 0644)
}

func savePlayersInfo() error {
	playersFile := filepath.Join(outputBaseDir, "players_info.json")

	data, err := json.MarshalIndent(allPlayersInfo, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(playersFile, data, 0644)
}

func saveChatData() error {
	chatFile := filepath.Join(outputBaseDir, "chat.json")

	data, err := json.MarshalIndent(allChatMessages, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(chatFile, data, 0644)
}
